package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// yours loads a task for a person acting on it: its member, or an admin; to anyone else it is not found.
func yours(ctx context.Context, tx pgx.Tx, actor int64, admin bool, id int64) (Task, error) {
	t, err := load(ctx, tx, id, true)
	if err == nil && t.Member != actor && !admin {
		return Task{}, errNoTask
	}
	return t, err
}

// Retry queues again a task that needs a person, with the attempts of its work reset: only a person resets them (T9).
func (s *Service) Retry(ctx context.Context, actor int64, admin bool, id int64) (Task, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := yours(ctx, tx, actor, admin, id)
		if err != nil {
			return err
		}
		if t.State != NeedsPerson {
			return fail.New(fail.Conflict, "not_waiting", "Only a task that needs a person is retried; this one is "+t.State+".")
		}
		var actions []string
		for _, it := range t.open() {
			actions = append(actions, it.Action)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM attempt_budget WHERE member_id = $1 AND unit = $2 AND action = ANY($3)", t.Member, t.Unit, actions); err != nil {
			return err
		}
		if err := s.requeue(ctx, tx, t, s.now(), false); err != nil {
			return err
		}
		return record(ctx, tx, id, "retried", memberActor(actor), Queued, nil)
	})
	if err != nil {
		return Task{}, err
	}
	return s.Task(ctx, id)
}

// Dismiss closes a queued, leased, or waiting task, with a reason. A leased task's lease ends with it.
func (s *Service) Dismiss(ctx context.Context, actor int64, admin bool, id int64, reason string) (Task, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 500 {
		return Task{}, fail.New(fail.Invalid, "invalid_reason", "Dismissing a task takes a reason, of 1 to 500 characters.")
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := yours(ctx, tx, actor, admin, id)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{Queued, Leased, NeedsPerson}, t.State) {
			return fail.New(fail.Conflict, "cannot_dismiss", "A task that is "+t.State+" cannot be dismissed.")
		}
		return s.finish(ctx, tx, id, Dismissed, memberActor(actor), reason)
	})
	if err != nil {
		return Task{}, err
	}
	return s.Task(ctx, id)
}

// unclaimed hands a queued task to a person when its member's executors have left it unclaimed for too long, while
// its lock was free, so that work no executor can do does not wait unseen.
func (s *Service) unclaimed(ctx context.Context, id int64) error {
	chains, err := s.groups.ChainsOf(ctx)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := load(ctx, tx, id, true)
		if err != nil || t.State != Queued {
			return err
		}
		now := s.now()
		if due := t.QueuedAt.Add(s.settings.Unclaimed); due.After(now) {
			return later(ctx, tx, KindUnclaimed, id, due)
		}
		var ids []int64
		for _, it := range t.open() {
			ids = append(ids, it.PR)
		}
		locks, err := lockSet(ctx, tx, ids, chains)
		if err != nil {
			return err
		}
		var busy bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM task WHERE state IN ('leased', 'verifying') AND locks && $1)", locks).Scan(&busy); err != nil {
			return err
		}
		if busy || t.NextAttemptAt.After(now) {
			return later(ctx, tx, KindUnclaimed, id, now.Add(s.settings.Unclaimed/24))
		}
		return s.finish(ctx, tx, id, NeedsPerson, park, "no executor took it in time")
	})
}

// Automation is a member's automation: the actions that may become tasks, in the given repositories, or in every one
// when Repos is nil.
type Automation struct {
	Actions []string
	Repos   []int64
}

// Automation returns a member's automation; with none set, no action becomes a task.
func (s *Service) Automation(ctx context.Context, member int64) (Automation, error) {
	a := Automation{Actions: []string{}}
	err := s.pool.QueryRow(ctx, "SELECT actions, repos FROM automation WHERE member_id = $1", member).Scan(&a.Actions, &a.Repos)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	return a, err
}

// SetAutomation sets a member's automation.
func (s *Service) SetAutomation(ctx context.Context, member int64, a Automation) (Automation, error) {
	if slices.ContainsFunc(a.Actions, func(x string) bool { return !canBeTask(x) }) {
		return Automation{}, fail.New(fail.Invalid, "invalid_automation", "Only resolve_conflicts, fix_checks, address_comments, request_review, and review can become tasks.")
	}
	if a.Actions == nil {
		a.Actions = []string{}
	}
	if a.Repos != nil {
		var n int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM repo WHERE id = ANY($1)", a.Repos).Scan(&n); err != nil {
			return Automation{}, err
		}
		if n != len(a.Repos) {
			return Automation{}, fail.New(fail.Invalid, "invalid_automation", "A repository in the list is not one Park watches.")
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO automation (member_id, actions, repos) VALUES ($1, $2, $3)
		ON CONFLICT (member_id) DO UPDATE SET actions = EXCLUDED.actions, repos = EXCLUDED.repos, updated_at = now()`, member, a.Actions, a.Repos)
	return a, err
}
