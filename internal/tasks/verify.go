package tasks

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/facts"
)

// verify judges a verifying task's attempt against the facts: docs/design/06-tasks.md#flow-report-and-verify. Only
// facts fetched since the attempt ended count, and the deadline pauses while any are stale. Every item satisfied: the
// task is done (T1). An item not satisfied once its facts settle, or at the deadline: the attempt fails. A task waiting
// for a person is done too when the facts satisfy every item, whoever did it.
func (s *Service) verify(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := load(ctx, tx, id, true)
		if err != nil || (t.State != Verifying && t.State != NeedsPerson) {
			return err
		}
		open := t.open()
		var ids []int64
		for _, it := range open {
			ids = append(ids, it.PR)
		}
		found, err := prs(ctx, tx, ids)
		if err != nil {
			return err
		}
		now := s.now()
		var ended time.Time
		if err := tx.QueryRow(ctx, "SELECT COALESCE(verified_at, updated_at) FROM task WHERE id = $1", id).Scan(&ended); err != nil {
			return err
		}
		if t.State == Verifying {
			stale, fresh := false, true
			for _, it := range open {
				p := found[it.PR]
				stale = stale || p.stale
				fresh = fresh && !p.fetchedAt.Before(ended)
			}
			if stale && t.Deadline != nil {
				// The deadline pauses while facts are stale: a forge outage must not use up attempts.
				pushed := t.Deadline.Add(now.Sub(ended))
				if _, err := tx.Exec(ctx, "UPDATE task SET deadline = $2, verified_at = $3 WHERE id = $1", id, pushed, now); err != nil {
					return err
				}
				return later(ctx, tx, KindVerify, id, now.Add(s.settings.Poll))
			}
			if !fresh && t.Deadline != nil && now.Before(*t.Deadline) {
				if err := s.refresh(ctx, tx, t, facts.KindCheckPR); err != nil {
					return err
				}
				return later(ctx, tx, KindVerify, id, now.Add(s.settings.Poll))
			}
		}
		mine, err := accounts(ctx, tx, t.Member)
		if err != nil {
			return err
		}
		var failing []Item
		var reasons []string
		pending, satisfied := false, 0
		for _, it := range open {
			p := found[it.PR]
			if !p.open {
				if _, err := tx.Exec(ctx, "UPDATE task_item SET state = 'dropped' WHERE id = $1", it.ID); err != nil {
					return err
				}
				continue
			}
			login, err := logins(ctx, tx, p.forge)
			if err != nil {
				return err
			}
			start := Start{At: t.CreatedAt}
			if it.Start != nil {
				start = *it.Start
			}
			o := check(it.Action, p.facts, it.Targets, start, mine[p.forge], login)
			switch {
			case o.satisfied:
				satisfied++
				if _, err := tx.Exec(ctx, "UPDATE task_item SET state = 'satisfied' WHERE id = $1", it.ID); err != nil {
					return err
				}
			case !o.settled:
				pending = true
				it.Failures = []string{o.missing + " (not settled by the deadline)"}
				failing = append(failing, it)
			default:
				it.Failures = []string{o.missing}
				failing = append(failing, it)
			}
		}
		for _, it := range t.Items {
			if it.State == "satisfied" {
				satisfied++
			}
		}
		switch {
		case len(failing) == 0 && satisfied > 0:
			return s.finish(ctx, tx, id, Done, park, "the facts satisfy every item")
		case len(failing) == 0:
			return s.finish(ctx, tx, id, Cancelled, park, "its pull requests merged or closed")
		case t.State == NeedsPerson:
			return nil
		case pending && t.Deadline != nil && now.Before(*t.Deadline):
			if err := s.refresh(ctx, tx, t, facts.KindCheckPR); err != nil {
				return err
			}
			return later(ctx, tx, KindVerify, id, now.Add(s.settings.Poll))
		}
		for _, it := range failing {
			if _, err := tx.Exec(ctx, "UPDATE task_item SET failures = $2 WHERE id = $1", it.ID, it.Failures); err != nil {
				return err
			}
			reasons = append(reasons, it.Action+": "+strings.Join(it.Failures, "; "))
		}
		if err := record(ctx, tx, id, "verification_failed", park, t.State, map[string]any{"reasons": reasons}); err != nil {
			return err
		}
		if t.CancelMarked {
			return s.finish(ctx, tx, id, Cancelled, park, "marked to cancel, and not satisfied")
		}
		return s.failAttempt(ctx, tx, t, failing)
	})
}

// failAttempt counts a failed attempt against the budget of its member, unit, and each failing action (T9). With
// attempts remaining, the task is queued again after a backoff; with none, it needs a person.
func (s *Service) failAttempt(ctx context.Context, tx pgx.Tx, t Task, failing []Item) error {
	exhausted := false
	for _, it := range failing {
		var used int
		if err := tx.QueryRow(ctx, `INSERT INTO attempt_budget (member_id, unit, action, used) VALUES ($1, $2, $3, 1)
			ON CONFLICT (member_id, unit, action) DO UPDATE SET used = attempt_budget.used + 1 RETURNING used`,
			t.Member, t.Unit, it.Action).Scan(&used); err != nil {
			return err
		}
		exhausted = exhausted || used >= s.settings.Attempts
	}
	if exhausted {
		return s.finish(ctx, tx, t.ID, NeedsPerson, park, "its attempts are used up")
	}
	backoff := s.settings.Backoff
	for range max(t.Attempt-1, 0) {
		backoff *= 2
	}
	if err := s.requeue(ctx, tx, t, s.now().Add(backoff), false); err != nil {
		return err
	}
	return record(ctx, tx, t.ID, "queued", park, Queued, map[string]any{"next_attempt_in": backoff.String()})
}

// FactsFollower marks a leased task to cancel after its attempt when a pull request of it merges or closes (T7), drops
// the items of queued tasks on it, and has verifying and waiting tasks on it judged again.
func (s *Service) FactsFollower() facts.Follower {
	return func(ctx context.Context, tx pgx.Tx, pr int64, _ []string) error {
		var open bool
		if err := tx.QueryRow(ctx, "SELECT open FROM pr_fact WHERE id = $1", pr).Scan(&open); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT t.id, t.state FROM task t JOIN task_item i ON i.task_id = t.id
			WHERE i.pr_id = $1 AND i.state = 'open' AND t.state IN ('queued', 'leased', 'verifying', 'needs_person') ORDER BY t.id`, pr)
		if err != nil {
			return err
		}
		type row struct {
			state string
			id    int64
		}
		var found []row
		var r row
		if _, err := pgx.ForEachRow(rows, []any{&r.id, &r.state}, func() error {
			found = append(found, r)
			return nil
		}); err != nil {
			return err
		}
		for _, r := range found {
			switch {
			case r.state == Leased && !open:
				tag, err := tx.Exec(ctx, "UPDATE task SET cancel_marked = true, updated_at = now() WHERE id = $1 AND NOT cancel_marked", r.id)
				if err != nil {
					return err
				}
				if tag.RowsAffected() > 0 {
					if err := record(ctx, tx, r.id, "marked_to_cancel", park, Leased, map[string]any{"reason": "a pull request merged or closed"}); err != nil {
						return err
					}
				}
			case r.state == Queued && !open:
				if err := s.dropPR(ctx, tx, r.id, pr); err != nil {
					return err
				}
			case r.state == Verifying || r.state == NeedsPerson:
				if err := later(ctx, tx, KindVerify, r.id, time.Time{}); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// dropPR drops a queued task's items on a pull request that merged or closed, cancelling it when none are left.
func (s *Service) dropPR(ctx context.Context, tx pgx.Tx, task, pr int64) error {
	if _, err := tx.Exec(ctx, "UPDATE task_item SET state = 'dropped' WHERE task_id = $1 AND pr_id = $2 AND state = 'open'", task, pr); err != nil {
		return err
	}
	var left bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM task_item WHERE task_id = $1 AND state = 'open')", task).Scan(&left); err != nil {
		return err
	}
	if !left {
		return s.finish(ctx, tx, task, Cancelled, park, "its pull requests merged or closed")
	}
	return nil
}
