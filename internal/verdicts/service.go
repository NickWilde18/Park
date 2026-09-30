// Package verdicts places each unit's ball: docs/design/04-verdicts.md.
//
// A unit is a group, or a pull request in no group. The fields of every open pull request's people — its author and
// its reviewers — are computed from the facts, and each person's list is evaluated with every judgement unknown; the
// ball is then placed once for the unit, from all of those parts. A unit is evaluated under its lock. A new verdict is
// stored only when what a person sees changes, and never over one computed from newer facts.
package verdicts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/work"
)

// The work of verdicts.
const (
	KindEvaluate = "verdicts.evaluate" // a unit: evaluate it again
	KindHourly   = "verdicts.hourly"   // "all": evaluate every open unit again, for rules that read elapsed time
)

// Settings are the verdicts' configuration, from docs/design/04-verdicts.md#defaults.
type Settings struct {
	// MinimumApprovals is the team's minimum number of approvals.
	MinimumApprovals int
	// Every is how often every open unit is evaluated again.
	Every time.Duration
}

// DefaultSettings are the documented defaults.
var DefaultSettings = Settings{MinimumApprovals: 1, Every: time.Hour}

// Service is the verdict stage.
type Service struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	rules    *rules.Service
	groups   *grouping.Service
	now      func() time.Time
	settings Settings
}

// New returns the verdict stage, which reads the published rules and the chains.
func New(pool *pgxpool.Pool, log *slog.Logger, rs *rules.Service, groups *grouping.Service) *Service {
	return &Service{pool: pool, log: log, rules: rs, groups: groups, now: time.Now, settings: DefaultSettings}
}

// Register gives the runner the verdicts' work.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindEvaluate, func(ctx context.Context, it work.Item) error { return s.Evaluate(ctx, it.Key) })
	r.Handle(KindHourly, func(ctx context.Context, it work.Item) error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := enqueueOpen(ctx, tx); err != nil {
				return err
			}
			return work.Enqueue(ctx, tx, KindHourly, it.Key, s.now().Add(s.settings.Every))
		})
	})
}

// Start schedules the hourly pass, beginning now.
func (s *Service) Start(ctx context.Context) error {
	return work.Enqueue(ctx, s.pool, KindHourly, "all", time.Time{})
}

// enqueueOpen queues every open unit to be evaluated, and every unit whose current verdict is still open, so that it
// is closed out or ends.
func enqueueOpen(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT CASE WHEN m.group_id IS NULL THEN 'pr:' || p.id ELSE 'group:' || m.group_id END
		FROM pr_fact p JOIN repo r ON r.id = p.repo_id LEFT JOIN membership m ON m.pr_id = p.id WHERE p.open AND r.watched
		UNION SELECT unit FROM verdict WHERE current AND closed IS NULL`)
	if err != nil {
		return err
	}
	units, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, u := range units {
		if err := work.Enqueue(ctx, tx, KindEvaluate, u, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// unitOf is the unit a pull request is in now.
func unitOf(ctx context.Context, tx pgx.Tx, pr int64) (string, error) {
	var g int64
	err := tx.QueryRow(ctx, "SELECT group_id FROM membership WHERE pr_id = $1", pr).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return "pr:" + strconv.FormatInt(pr, 10), nil
	}
	return "group:" + strconv.FormatInt(g, 10), err
}

// FactsFollower queues a pull request's unit whenever its facts change.
func (s *Service) FactsFollower() facts.Follower {
	return func(ctx context.Context, tx pgx.Tx, pr int64, _ []string) error {
		u, err := unitOf(ctx, tx, pr)
		if err != nil {
			return err
		}
		return work.Enqueue(ctx, tx, KindEvaluate, u, time.Time{})
	}
}

// GroupingFollower queues the units pull requests moved into, and the ones they left.
func (s *Service) GroupingFollower() grouping.Follower {
	return func(ctx context.Context, tx pgx.Tx, prs []int64) error {
		for _, pr := range prs {
			u, err := unitOf(ctx, tx, pr)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "SELECT unit FROM verdict WHERE current AND seqs ? $1", strconv.FormatInt(pr, 10))
			if err != nil {
				return err
			}
			left, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, unit := range append(left, u) {
				if err := work.Enqueue(ctx, tx, KindEvaluate, unit, time.Time{}); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// RulesFollower queues every open unit when a rule set is published.
func (s *Service) RulesFollower() rules.Follower {
	return func(ctx context.Context, tx pgx.Tx, _ int) error { return enqueueOpen(ctx, tx) }
}

// stored is a pull request of a unit, as the facts have it.
type stored struct {
	facts facts.PR
	repo  string
	forge int64
	id    int64
	seq   int64
	open  bool
}

// unitPRs are the pull requests of a unit in watched repositories: a group's, or a pull request's while it is in no
// group.
func unitPRs(ctx context.Context, tx pgx.Tx, unit string) ([]stored, error) {
	kind, rawID, _ := strings.Cut(unit, ":")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || (kind != "group" && kind != "pr") {
		return nil, fail.New(fail.Invalid, "invalid_unit", "A unit is group:<id> or pr:<id>.")
	}
	where := "p.id IN (SELECT pr_id FROM membership WHERE group_id = $1)"
	if kind == "pr" {
		where = "p.id = $1 AND NOT EXISTS (SELECT 1 FROM membership m WHERE m.pr_id = p.id)"
	}
	rows, err := tx.Query(ctx, `SELECT p.id, p.state, p.seq, p.open, r.name, r.forge_id FROM pr_fact p JOIN repo r ON r.id = p.repo_id
		WHERE r.watched AND `+where+` ORDER BY p.id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
		var p stored
		var state []byte
		if err := row.Scan(&p.id, &state, &p.seq, &p.open, &p.repo, &p.forge); err != nil {
			return p, err
		}
		return p, json.Unmarshal(state, &p.facts)
	})
}

// newer reports whether a verdict was computed from newer facts of any pull request than seqs.
func newer(verdict, seqs map[int64]int64) bool {
	for pr, seq := range verdict {
		if now, ok := seqs[pr]; ok && seq > now {
			return true
		}
	}
	return false
}

// Evaluate evaluates a unit again, under its lock, and stores a new verdict if what a person sees has changed:
// docs/design/04-verdicts.md#flow-evaluate-a-unit. When every pull request of the unit is merged or closed, its
// verdict is closed out; a unit that has no pull requests left, such as a group merged into another, has none.
func (s *Service) Evaluate(ctx context.Context, unit string) error {
	version, compiled, err := s.rules.Current(ctx)
	if err != nil {
		return err
	}
	chains, err := s.groups.ChainsOf(ctx)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.unit:' || $1))", unit); err != nil {
			return err
		}
		prs, err := unitPRs(ctx, tx, unit)
		if err != nil {
			return err
		}
		previous, err := currentVerdict(ctx, tx, unit)
		if err != nil {
			return err
		}
		if len(prs) == 0 {
			if previous == nil {
				return nil
			}
			_, err := tx.Exec(ctx, "UPDATE verdict SET current = false WHERE id = $1", previous.ID)
			return err
		}
		now := s.now()
		v := &Verdict{Unit: unit, Seqs: map[int64]int64{}, RuleVersion: version, EvaluatedAt: now}
		var open []stored
		merged := true
		for _, p := range prs {
			v.Seqs[p.id] = p.seq
			switch {
			case p.open:
				open = append(open, p)
			case p.facts.State != facts.StateMerged:
				merged = false
			}
		}
		if previous != nil && newer(previous.Seqs, v.Seqs) {
			return nil
		}
		if len(open) == 0 {
			if previous == nil {
				return nil
			}
			v.Closed = "closed"
			if merged {
				v.Closed = "merged"
			}
		} else if err := s.evaluate(ctx, tx, v, open, compiled, chains, previous, strings.HasPrefix(unit, "group:")); err != nil {
			return err
		}
		if previous != nil && previous.visible() == v.visible() {
			return nil
		}
		return store(ctx, tx, previous, v)
	})
}

// evaluate fills a verdict in from its open pull requests: every person's part, the ball, and the reminders.
func (s *Service) evaluate(ctx context.Context, tx pgx.Tx, v *Verdict, open []stored, compiled *rules.Compiled,
	chains map[int64]grouping.Chain, previous *Verdict, group bool) error {
	size := 0
	if group {
		size = len(open)
	}
	for _, p := range open {
		c, names, err := s.context(ctx, tx, p, v.EvaluatedAt)
		if err != nil {
			return err
		}
		c.Chain, c.GroupSize = chains[p.id], size
		for _, person := range People(p.facts, c) {
			login, name := c.login(person.Account), names[person.Account]
			v.Parts = append(v.Parts, Part{Part: compiled.Evaluate(person.Role, person.Input), Login: login, Name: name, Role: person.Role,
				PR: p.id, Forge: p.forge, Account: person.Account})
			v.Inputs = append(v.Inputs, Input{Fields: person.Input.Fields, Login: login, Name: name, Role: person.Role, PR: p.id,
				Forge: p.forge, Account: person.Account})
		}
	}
	placed := Place(v.Parts)
	v.Side, v.Fallback = placed.Side, placed.Fallback
	v.Holders = holders(placed.Holders, previous, v.EvaluatedAt)
	for _, p := range v.Parts {
		if p.Remind {
			v.Reminders = append(v.Reminders, Reminder{Login: p.Login, Name: p.Name, Reason: p.Reason, PR: p.PR, Forge: p.Forge,
				Account: p.Account})
		}
	}
	return nil
}

// context loads what a pull request's fields need beyond its facts — logins, teams, and nudges — and the accounts'
// names.
func (s *Service) context(ctx context.Context, tx pgx.Tx, p stored, now time.Time) (Context, map[int64]string, error) {
	c := Context{Now: now, Logins: map[int64]string{}, Teams: map[int64]Team{}, Nudged: map[int64]time.Time{}, Repo: p.repo,
		MinimumApprovals: s.settings.MinimumApprovals}
	pr := p.facts
	ids := []int64{pr.Author}
	var teams []int64
	for _, r := range pr.ReviewRequests {
		if r.Account != 0 {
			ids = append(ids, r.Account)
		}
		if r.Team != 0 {
			teams = append(teams, r.Team)
		}
	}
	for _, r := range pr.Reviews {
		ids = append(ids, r.Author)
	}
	for _, th := range pr.Threads {
		for _, cm := range th.Comments {
			ids = append(ids, cm.Author)
		}
	}
	rows, err := tx.Query(ctx, "SELECT account_id, login, name FROM account WHERE forge_id = $1 AND account_id = ANY($2)", p.forge, ids)
	if err != nil {
		return c, nil, err
	}
	names := map[int64]string{}
	var id int64
	var login, name string
	if _, err := pgx.ForEachRow(rows, []any{&id, &login, &name}, func() error {
		c.Logins[id], names[id] = login, name
		return nil
	}); err != nil {
		return c, nil, err
	}
	rows, err = tx.Query(ctx, "SELECT team_id, org || '/' || slug, members FROM team WHERE forge_id = $1 AND team_id = ANY($2)",
		p.forge, teams)
	if err != nil {
		return c, nil, err
	}
	var team Team
	if _, err := pgx.ForEachRow(rows, []any{&id, &team.Slug, &team.Members}, func() error {
		c.Teams[id] = Team{Slug: team.Slug, Members: team.Members}
		return nil
	}); err != nil {
		return c, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT mi.subject, max(a.at) FROM member_action a
		JOIN member_identity mi ON mi.member_id = a.member_id AND mi.forge_id = $2
		WHERE a.pr_id = $1 AND a.kind = 'nudged' GROUP BY mi.subject`, p.id, p.forge)
	if err != nil {
		return c, nil, err
	}
	var at time.Time
	_, err = pgx.ForEachRow(rows, []any{&id, &at}, func() error {
		c.Nudged[id] = at
		return nil
	})
	return c, names, err
}

// store makes a verdict the unit's current one, superseding the previous.
func store(ctx context.Context, tx pgx.Tx, previous, v *Verdict) error {
	if previous != nil {
		if _, err := tx.Exec(ctx, "UPDATE verdict SET current = false WHERE id = $1", previous.ID); err != nil {
			return err
		}
	}
	holderKeys, partyKeys := []string{}, []string{}
	seen := map[string]bool{}
	for _, h := range v.Holders {
		holderKeys = append(holderKeys, key(h.Forge, h.Account))
	}
	for _, p := range v.Parts {
		if k := key(p.Forge, p.Account); !seen[k] {
			seen[k] = true
			partyKeys = append(partyKeys, k)
		}
	}
	var side *string
	if v.Side != "" {
		s := string(v.Side)
		side = &s
	}
	var closed *string
	if v.Closed != "" {
		closed = &v.Closed
	}
	docs := make([][]byte, 5)
	for i, doc := range []any{orEmpty(v.Holders), orEmpty(v.Parts), orEmpty(v.Reminders), orEmpty(v.Inputs), v.Seqs} {
		data, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		docs[i] = data
	}
	return tx.QueryRow(ctx, `INSERT INTO verdict (unit, side, closed, fallback, holders, parts, reminders, inputs, seqs, holder_keys,
			party_keys, rule_version, evaluated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id, created_at`,
		v.Unit, side, closed, v.Fallback, docs[0], docs[1], docs[2], docs[3], docs[4], holderKeys, partyKeys, v.RuleVersion,
		v.EvaluatedAt).Scan(&v.ID, &v.CreatedAt)
}

func orEmpty[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// MarkNudged records that a member nudged the people a pull request waits on, and queues its unit.
func (s *Service) MarkNudged(ctx context.Context, member, pr int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "INSERT INTO member_action (member_id, pr_id, kind) SELECT $1, id, 'nudged' FROM pr_fact WHERE id = $2",
			member, pr)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail.New(fail.NotFound, "not_found", "There is no such pull request.")
		}
		u, err := unitOf(ctx, tx, pr)
		if err != nil {
			return err
		}
		return work.Enqueue(ctx, tx, KindEvaluate, u, time.Time{})
	})
}
