package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/work"
)

// current reports whether a lease is the task's current one: leased, under that lease ID, and not expired.
func (s *Service) current(t Task, lease string) bool {
	return t.State == Leased && t.LeaseID == lease && lease != "" && t.LeaseExpiresAt != nil && t.LeaseExpiresAt.After(s.now())
}

func notCurrent(t Task) error {
	return fail.New(fail.Conflict, "lease_not_current", fmt.Sprintf("The lease is no longer current: the task is %s.", t.State))
}

// mine loads a task of a member's for update; another member's task is not found (T10).
func mine(ctx context.Context, tx pgx.Tx, member, id int64) (Task, error) {
	t, err := load(ctx, tx, id, true)
	if err == nil && t.Member != member {
		return Task{}, errNoTask
	}
	return t, err
}

// Heartbeat is what a heartbeat's answer says.
type Heartbeat struct {
	ExpiresAt *time.Time
	State     string
	Current   bool
	Cancel    bool
}

// Beat extends a current lease, with an optional progress note, and says whether the task is marked to cancel. For a
// lease that is no longer current, it says so and the task's state, and the executor must stop (T13).
func (s *Service) Beat(ctx context.Context, member int64, id int64, lease, note string, waiting bool) (Heartbeat, error) {
	var out Heartbeat
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := mine(ctx, tx, member, id)
		if err != nil {
			return err
		}
		out = Heartbeat{State: t.State, Cancel: t.CancelMarked}
		if !s.current(t, lease) {
			return nil
		}
		expires := s.now().Add(s.settings.Lease)
		if _, err := tx.Exec(ctx, "UPDATE task SET lease_expires_at = $2, note = $3, waiting = $4, updated_at = now() WHERE id = $1",
			id, expires, note, waiting); err != nil {
			return err
		}
		out.Current, out.ExpiresAt = true, &expires
		if note != t.Note || waiting != t.Waiting {
			if err := record(ctx, tx, id, "heartbeat", tokenActor(t.LeaseToken), Leased, map[string]any{"note": note, "waiting": waiting}); err != nil {
				return err
			}
		}
		return later(ctx, tx, KindLease, id, expires)
	})
	return out, err
}

// ItemReport is what an executor did for one item.
type ItemReport struct {
	Note    string   `json:"note,omitempty"`
	Commits []string `json:"commits,omitempty"`
	Threads []string `json:"threads,omitempty"`
	Reviews []int64  `json:"reviews,omitempty"`
	ID      int64    `json:"id"`
}

// Report is an executor's report of an attempt: its outcome, done or failed, and what it did item by item.
type Report struct {
	Lease   string
	Outcome string
	Items   []ItemReport
}

// Report records an attempt's report and moves the task to verifying, whatever the outcome says: a report is a claim,
// not a fact (T1). Reports are idempotent per lease: reporting again under the lease already reported returns the
// task as it is.
func (s *Service) Report(ctx context.Context, member int64, id int64, r Report) (Task, error) {
	if r.Outcome != "done" && r.Outcome != "failed" {
		return Task{}, fail.New(fail.Invalid, "invalid_report", "A report's outcome is done or failed.")
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := mine(ctx, tx, member, id)
		if err != nil {
			return err
		}
		if !s.current(t, r.Lease) {
			var reported bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM task_event WHERE task_id = $1 AND kind = 'reported' AND detail->>'lease' = $2)",
				id, r.Lease).Scan(&reported); err != nil {
				return err
			}
			if reported {
				return nil
			}
			return notCurrent(t)
		}
		for _, ir := range r.Items {
			data, err := json.Marshal(ir)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE task_item SET report = $3 WHERE id = $1 AND task_id = $2", ir.ID, id, data); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, id, "reported", tokenActor(t.LeaseToken), Verifying, map[string]any{"lease": r.Lease, "outcome": r.Outcome,
			"items": r.Items}); err != nil {
			return err
		}
		return s.toVerifying(ctx, tx, t)
	})
	if err != nil {
		return Task{}, err
	}
	return s.Task(ctx, id)
}

// toVerifying ends an attempt: the task waits for the facts to confirm it, which Park refreshes at once, against a
// deadline — longer when checks must run.
func (s *Service) toVerifying(ctx context.Context, tx pgx.Tx, t Task) error {
	deadline := s.settings.Deadline
	for _, it := range t.open() {
		if it.Action == "fix_checks" {
			deadline = s.settings.ChecksDeadline
		}
	}
	now := s.now()
	if _, err := tx.Exec(ctx, "UPDATE task SET state = 'verifying', deadline = $2, verified_at = $3, updated_at = now() WHERE id = $1",
		t.ID, now.Add(deadline), now); err != nil {
		return err
	}
	if err := s.refresh(ctx, tx, t, facts.KindRefreshPR); err != nil {
		return err
	}
	return later(ctx, tx, KindVerify, t.ID, now.Add(s.settings.Poll))
}

// refresh queues a fetch of every pull request a task's open items touch.
func (s *Service) refresh(ctx context.Context, tx pgx.Tx, t Task, kind string) error {
	var ids []int64
	for _, it := range t.open() {
		ids = append(ids, it.PR)
	}
	rows, err := tx.Query(ctx, "SELECT repo_id::text || '/' || number::text FROM pr_fact WHERE id = ANY($1)", ids)
	if err != nil {
		return err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := work.Enqueue(ctx, tx, kind, k, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// Release gives a task back without a result: it is queued again, and the attempt is not counted.
func (s *Service) Release(ctx context.Context, member int64, id int64, lease string) (Task, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := mine(ctx, tx, member, id)
		if err != nil {
			return err
		}
		if !s.current(t, lease) {
			return notCurrent(t)
		}
		if err := s.requeue(ctx, tx, t, s.now(), true); err != nil {
			return err
		}
		return record(ctx, tx, id, "released", tokenActor(t.LeaseToken), Queued, map[string]any{"lease": lease})
	})
	if err != nil {
		return Task{}, err
	}
	return s.Task(ctx, id)
}

// requeue puts a task back in the queue, from the time given. A follow-up queued on the same unit meanwhile gives it
// its items and is cancelled, so that there is still one queued task per member per unit (T3). A released attempt is
// not counted.
func (s *Service) requeue(ctx context.Context, tx pgx.Tx, t Task, next time.Time, released bool) error {
	var follow int64
	err := tx.QueryRow(ctx, "SELECT id FROM task WHERE member_id = $1 AND unit = $2 AND state = 'queued' AND id <> $3 FOR UPDATE",
		t.Member, t.Unit, t.ID).Scan(&follow)
	switch {
	case err == nil:
		f, err := load(ctx, tx, follow, false)
		if err != nil {
			return err
		}
		if err := s.finish(ctx, tx, follow, Cancelled, park, fmt.Sprintf("its items joined task %d", t.ID)); err != nil {
			return err
		}
		for _, it := range f.open() {
			if err := s.absorb(ctx, tx, t.ID, it); err != nil {
				return err
			}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	attempt := "attempt"
	if released {
		attempt = "attempt - 1"
	}
	_, err = tx.Exec(ctx, `UPDATE task SET state = 'queued', attempt = `+attempt+`, lease_expires_at = NULL, locks = '{}',
		cancel_marked = false, next_attempt_at = $2, queued_at = $3, deadline = NULL, updated_at = now() WHERE id = $1`, t.ID, next, s.now())
	if err != nil {
		return err
	}
	return later(ctx, tx, KindUnclaimed, t.ID, s.now().Add(s.settings.Unclaimed))
}

// absorb moves a follow-up's item into a task, merging it with the task's item for the same pull request and action.
func (s *Service) absorb(ctx context.Context, tx pgx.Tx, task int64, it Item) error {
	var existing []byte
	var id int64
	err := tx.QueryRow(ctx, "SELECT id, targets FROM task_item WHERE task_id = $1 AND pr_id = $2 AND action = $3", task, it.PR, it.Action).
		Scan(&id, &existing)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err := tx.Exec(ctx, "UPDATE task_item SET task_id = $2 WHERE id = $1", it.ID, task)
		return err
	}
	if err != nil {
		return err
	}
	var old Targets
	if err := json.Unmarshal(existing, &old); err != nil {
		return err
	}
	data, err := json.Marshal(old.plus(it.Targets))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE task_item SET targets = $2, state = 'open' WHERE id = $1", id, data); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE task_item SET state = 'dropped' WHERE id = $1", it.ID)
	return err
}

// leaseEnds ends a lease that has expired without a report: the attempt is verified as if the executor had reported
// (T8), since it may have finished the work and died before reporting.
func (s *Service) leaseEnds(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := load(ctx, tx, id, true)
		if err != nil || t.State != Leased {
			return err
		}
		if t.LeaseExpiresAt != nil && t.LeaseExpiresAt.After(s.now()) {
			return later(ctx, tx, KindLease, id, *t.LeaseExpiresAt)
		}
		if err := record(ctx, tx, id, "lease_ended", park, Verifying, map[string]any{"reason": "expired", "lease": t.LeaseID}); err != nil {
			return err
		}
		return s.toVerifying(ctx, tx, t)
	})
}

// IdentityFollower ends at once the leases of revoked tokens, verified like expired ones; a disabled member's tasks
// are marked to cancel after their attempts, and their queued and waiting ones are cancelled.
func (s *Service) IdentityFollower() identity.Follower {
	return func(ctx context.Context, tx pgx.Tx, r identity.Revoked) error {
		rows, err := tx.Query(ctx, "SELECT id FROM task WHERE state = 'leased' AND lease_token = ANY($1) ORDER BY id FOR UPDATE", r.Tokens)
		if err != nil {
			return err
		}
		leased, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range leased {
			t, err := load(ctx, tx, id, false)
			if err != nil {
				return err
			}
			if err := record(ctx, tx, id, "lease_ended", park, Verifying, map[string]any{"reason": "token revoked", "lease": t.LeaseID}); err != nil {
				return err
			}
			if err := s.toVerifying(ctx, tx, t); err != nil {
				return err
			}
		}
		if !r.Disabled {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE task SET cancel_marked = true, updated_at = now() WHERE member_id = $1 AND state = 'verifying'", r.Member); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, "SELECT id FROM task WHERE member_id = $1 AND state IN ('queued', 'needs_person') ORDER BY id FOR UPDATE", r.Member)
		if err != nil {
			return err
		}
		waiting, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range waiting {
			if err := s.finish(ctx, tx, id, Cancelled, park, "its member was disabled"); err != nil {
				return err
			}
		}
		return nil
	}
}
