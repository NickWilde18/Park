// Package tasks leases the work verdicts call for to executors: docs/design/06-tasks.md.
//
// A task is one member's work on one unit, made of items: one action on one pull request, with its targets. An
// executor claims a task under a lease, works, and reports; the task is done only when forge facts satisfy every
// item's post-condition (T1). Claims lock every pull request of every group and chain a task's items touch, so that
// no two executors work on the same code at once (T4). A failed verification counts against an attempt budget, and
// what cannot be fixed is handed to a person.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/work"
)

// The work of tasks.
const (
	KindVerify    = "tasks.verify"    // a task: verify its attempt, or see whether facts settle it
	KindLease     = "tasks.lease"     // a task: end its lease if it has expired
	KindUnclaimed = "tasks.unclaimed" // a task: hand it to a person if no executor took it in time
)

// The states of a task.
const (
	Queued      = "queued"
	Leased      = "leased"
	Verifying   = "verifying"
	Done        = "done"
	NeedsPerson = "needs_person"
	Cancelled   = "cancelled"
	Dismissed   = "dismissed"
)

// Settings are the tasks' configuration: docs/design/06-tasks.md#defaults.
type Settings struct {
	Lease          time.Duration
	Heartbeat      time.Duration
	Backoff        time.Duration
	Poll           time.Duration
	ChecksDeadline time.Duration
	Deadline       time.Duration
	Unclaimed      time.Duration
	LongestWait    time.Duration
	Attempts       int
}

// DefaultSettings are the documented defaults.
var DefaultSettings = Settings{Lease: 15 * time.Minute, Heartbeat: 5 * time.Minute, Backoff: 10 * time.Minute, Poll: 30 * time.Second,
	ChecksDeadline: 60 * time.Minute, Deadline: 10 * time.Minute, Unclaimed: 24 * time.Hour, LongestWait: 30 * time.Second, Attempts: 3}

// Service is the task stage.
type Service struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	groups   *grouping.Service
	now      func() time.Time
	settings Settings
}

// New returns the task stage, which reads chains to know what a task locks.
func New(pool *pgxpool.Pool, log *slog.Logger, groups *grouping.Service) *Service {
	return &Service{pool: pool, log: log, groups: groups, now: time.Now, settings: DefaultSettings}
}

// Settings returns the tasks' settings.
func (s *Service) Settings() Settings { return s.settings }

// Register gives the runner the tasks' work.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindVerify, func(ctx context.Context, it work.Item) error { return s.verify(ctx, key(it)) })
	r.Handle(KindLease, func(ctx context.Context, it work.Item) error { return s.leaseEnds(ctx, key(it)) })
	r.Handle(KindUnclaimed, func(ctx context.Context, it work.Item) error { return s.unclaimed(ctx, key(it)) })
}

func key(it work.Item) int64 {
	id, _ := strconv.ParseInt(it.Key, 10, 64)
	return id
}

func later(ctx context.Context, tx pgx.Tx, kind string, id int64, at time.Time) error {
	return work.Enqueue(ctx, tx, kind, strconv.FormatInt(id, 10), at)
}

// Task is a task, with its items and, when asked for, its events.
type Task struct {
	CreatedAt      time.Time
	UpdatedAt      time.Time
	NextAttemptAt  time.Time
	QueuedAt       time.Time
	LeaseExpiresAt *time.Time
	StartedAt      *time.Time
	Deadline       *time.Time
	Unit           string
	State          string
	Priority       string
	LeaseID        string
	LeaseToken     string
	Executor       string
	Note           string
	Reason         string
	Items          []Item
	Events         []Event
	ID             int64
	Member         int64
	Attempt        int
	CancelMarked   bool
	Waiting        bool
}

// Item is one action on one pull request of a task.
type Item struct {
	Start    *Start
	Report   json.RawMessage
	Action   string
	State    string
	Priority string
	Targets  Targets
	Failures []string
	ID       int64
	PR       int64
}

// Event is one change to a task, with its actor: "token:<id>", "member:<id>", or "park".
type Event struct {
	At     time.Time      `json:"at"`
	Detail map[string]any `json:"detail"`
	Kind   string         `json:"kind"`
	Actor  string         `json:"actor"`
	State  string         `json:"state"`
	ID     int64          `json:"id"`
}

// The actors of events.
const park = "park"

func tokenActor(token string) string  { return "token:" + token }
func memberActor(member int64) string { return "member:" + strconv.FormatInt(member, 10) }

// record appends an event to a task (T2).
func record(ctx context.Context, tx pgx.Tx, task int64, kind, actor, state string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO task_event (task_id, kind, actor, state, detail) VALUES ($1, $2, $3, $4, $5)",
		task, kind, actor, state, data)
	return err
}

var errNoTask = fail.New(fail.NotFound, "not_found", "There is no such task.")

const taskColumns = `id, member_id, unit, state, priority, attempt, COALESCE(lease_id, ''), COALESCE(lease_token, ''),
	COALESCE(executor, ''), lease_expires_at, cancel_marked, next_attempt_at, started_at, deadline, note, waiting, reason,
	queued_at, created_at, updated_at`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Member, &t.Unit, &t.State, &t.Priority, &t.Attempt, &t.LeaseID, &t.LeaseToken, &t.Executor,
		&t.LeaseExpiresAt, &t.CancelMarked, &t.NextAttemptAt, &t.StartedAt, &t.Deadline, &t.Note, &t.Waiting, &t.Reason,
		&t.QueuedAt, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// load reads a task and its items, locking the task's row when asked.
func load(ctx context.Context, tx pgx.Tx, id int64, lock bool) (Task, error) {
	q := "SELECT " + taskColumns + " FROM task WHERE id = $1"
	if lock {
		q += " FOR UPDATE"
	}
	t, err := scanTask(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, errNoTask
	}
	if err != nil {
		return Task{}, err
	}
	t.Items, err = items(ctx, tx, id)
	return t, err
}

func items(ctx context.Context, tx pgx.Tx, task int64) ([]Item, error) {
	rows, err := tx.Query(ctx, `SELECT id, pr_id, action, targets, priority, start, state, failures, report FROM task_item
		WHERE task_id = $1 ORDER BY id`, task)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Item, error) {
		var it Item
		var targets, start, report []byte
		if err := row.Scan(&it.ID, &it.PR, &it.Action, &targets, &it.Priority, &start, &it.State, &it.Failures, &report); err != nil {
			return it, err
		}
		if report != nil {
			it.Report = report
		}
		if start != nil {
			it.Start = &Start{}
			if err := json.Unmarshal(start, it.Start); err != nil {
				return it, err
			}
		}
		return it, json.Unmarshal(targets, &it.Targets)
	})
}

// open are a task's items still to be satisfied.
func (t Task) open() []Item {
	var out []Item
	for _, it := range t.Items {
		if it.State == "open" {
			out = append(out, it)
		}
	}
	return out
}

// stored is a pull request as the facts have it, with its repository.
type stored struct {
	fetchedAt time.Time
	facts     facts.PR
	owner     string
	name      string
	id        int64
	repo      int64
	forge     int64
	open      bool
	stale     bool
}

func prs(ctx context.Context, tx pgx.Tx, ids []int64) (map[int64]stored, error) {
	rows, err := tx.Query(ctx, `SELECT p.id, p.state, p.open, p.stale, p.fetched_at, r.id, r.forge_id, r.owner, r.name
		FROM pr_fact p JOIN repo r ON r.id = p.repo_id WHERE p.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]stored{}
	var p stored
	var state []byte
	_, err = pgx.ForEachRow(rows, []any{&p.id, &state, &p.open, &p.stale, &p.fetchedAt, &p.repo, &p.forge, &p.owner, &p.name}, func() error {
		p.facts = facts.PR{}
		if err := json.Unmarshal(state, &p.facts); err != nil {
			return err
		}
		out[p.id] = p
		return nil
	})
	return out, err
}

// accounts are a member's linked forge accounts, by forge.
func accounts(ctx context.Context, tx pgx.Tx, member int64) (map[int64]map[int64]bool, error) {
	rows, err := tx.Query(ctx, "SELECT forge_id, subject FROM member_identity WHERE member_id = $1", member)
	if err != nil {
		return nil, err
	}
	out := map[int64]map[int64]bool{}
	var forge, subject int64
	_, err = pgx.ForEachRow(rows, []any{&forge, &subject}, func() error {
		if out[forge] == nil {
			out[forge] = map[int64]bool{}
		}
		out[forge][subject] = true
		return nil
	})
	return out, err
}

// logins are accounts' logins on a forge; an account Park has not seen is shown by its ID.
func logins(ctx context.Context, tx pgx.Tx, forge int64) (func(int64) string, error) {
	rows, err := tx.Query(ctx, "SELECT account_id, login FROM account WHERE forge_id = $1", forge)
	if err != nil {
		return nil, err
	}
	m := map[int64]string{}
	var id int64
	var login string
	if _, err := pgx.ForEachRow(rows, []any{&id, &login}, func() error {
		m[id] = login
		return nil
	}); err != nil {
		return nil, err
	}
	return func(id int64) string {
		if l, ok := m[id]; ok {
			return l
		}
		return strconv.FormatInt(id, 10)
	}, nil
}

// lockSet is every pull request a task on these pull requests locks: theirs, and every one of their groups and
// chains (T4), in order of ID.
func lockSet(ctx context.Context, tx pgx.Tx, ids []int64, chains map[int64]grouping.Chain) ([]int64, error) {
	set := map[int64]bool{}
	for _, id := range ids {
		set[id] = true
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT m2.pr_id FROM membership m1 JOIN membership m2 ON m2.group_id = m1.group_id
		WHERE m1.pr_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	grouped, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range grouped {
		set[id] = true
	}
	chained := map[int64]bool{}
	for id := range set {
		if c := chains[id].ID; c != 0 {
			chained[c] = true
		}
	}
	for id, c := range chains {
		if chained[c.ID] {
			set[id] = true
		}
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
