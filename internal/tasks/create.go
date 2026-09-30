package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/verdicts"
)

// want is an action a holder has, which may become an item.
type want struct {
	action    string
	priority  string
	reviewers []string
	pr        int64
	forge     int64
}

func (w want) same(it Item) bool { return w.pr == it.PR && w.action == it.Action }

// canBeTask reports whether an action can be a task: docs/design/04-verdicts.md#actions.
func canBeTask(action string) bool {
	for _, a := range rules.Actions {
		if a.Name == action {
			return a.Task
		}
	}
	return false
}

// VerdictFollower turns a unit's changed verdict into tasks: docs/design/06-tasks.md#flow-create. Each member who
// holds the ball gets their actions that may become tasks, as items of their one queued task on the unit (T3). A
// member who no longer holds it, or no longer has an action, loses the matching items of their queued task, and an
// active task of theirs is marked to cancel after its attempt (T7).
func (s *Service) VerdictFollower() verdicts.Follower {
	return func(ctx context.Context, tx pgx.Tx, previous, current *verdicts.Verdict) error {
		unit := ""
		wanted := map[int64][]want{}
		holding := map[int64]bool{}
		switch {
		case current != nil:
			unit = current.Unit
			holders := map[[2]int64]int64{}
			for _, h := range current.Holders {
				var member int64
				err := tx.QueryRow(ctx, `SELECT mi.member_id FROM member_identity mi JOIN member m ON m.id = mi.member_id
					WHERE mi.forge_id = $1 AND mi.subject = $2 AND NOT m.disabled`, h.Forge, h.Account).Scan(&member)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				holders[[2]int64{h.Forge, h.Account}] = member
				holding[member] = true
			}
			for _, p := range current.Parts {
				member, ok := holders[[2]int64{p.Forge, p.Account}]
				if !ok || !p.Task || !canBeTask(p.Action) || !slices.ContainsFunc(holdersParts(current, p), func(hp verdicts.HeldPart) bool {
					return hp.PR == p.PR && hp.Action == p.Action
				}) {
					continue
				}
				wanted[member] = append(wanted[member], want{action: p.Action, priority: p.Priority, reviewers: p.Targets, pr: p.PR, forge: p.Forge})
			}
		case previous != nil:
			unit = previous.Unit
		}
		// New work joins a queued task before what is no longer wanted leaves it, so that a task whose work only
		// changes is not cancelled.
		for member, ws := range wanted {
			for _, w := range ws {
				if err := s.add(ctx, tx, member, unit, w); err != nil {
					return err
				}
			}
		}
		return s.drop(ctx, tx, unit, wanted, holding)
	}
}

// holdersParts are the held parts of the holder whose part p is.
func holdersParts(v *verdicts.Verdict, p verdicts.Part) []verdicts.HeldPart {
	for _, h := range v.Holders {
		if h.Forge == p.Forge && h.Account == p.Account {
			return h.Parts
		}
	}
	return nil
}

// drop removes, from the queued tasks on a unit, the items their members no longer want, cancelling a task left with
// none; and marks to cancel the active tasks of members who no longer hold its ball.
func (s *Service) drop(ctx context.Context, tx pgx.Tx, unit string, wanted map[int64][]want, holding map[int64]bool) error {
	rows, err := tx.Query(ctx, "SELECT id FROM task WHERE unit = $1 AND state IN ('queued', 'leased', 'verifying') ORDER BY id FOR UPDATE", unit)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		t, err := load(ctx, tx, id, false)
		if err != nil {
			return err
		}
		ws := wanted[t.Member]
		if t.State != Queued {
			if !holding[t.Member] && !t.CancelMarked {
				if _, err := tx.Exec(ctx, "UPDATE task SET cancel_marked = true, updated_at = now() WHERE id = $1", id); err != nil {
					return err
				}
				if err := record(ctx, tx, id, "marked_to_cancel", park, t.State, map[string]any{"reason": "no longer holds the ball"}); err != nil {
					return err
				}
			}
			continue
		}
		left := 0
		for _, it := range t.open() {
			if slices.ContainsFunc(ws, func(w want) bool { return w.same(it) }) {
				left++
				continue
			}
			if _, err := tx.Exec(ctx, "UPDATE task_item SET state = 'dropped' WHERE id = $1", it.ID); err != nil {
				return err
			}
			if err := record(ctx, tx, id, "item_dropped", park, Queued, map[string]any{"item": it.ID, "reason": "no longer an action of the holder"}); err != nil {
				return err
			}
		}
		if left == 0 {
			if err := s.finish(ctx, tx, id, Cancelled, park, "nothing left to do"); err != nil {
				return err
			}
		}
	}
	return nil
}

// finish moves a task to a final state, or to needs_person, releasing its locks.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, id int64, state, actor, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE task SET state = $2, reason = $3, locks = '{}', updated_at = now() WHERE id = $1`, id, state, reason); err != nil {
		return err
	}
	return record(ctx, tx, id, state, actor, state, map[string]any{"reason": reason})
}

// add makes an item of an action a holder has, if it is new work: not satisfied already, and not held by an
// unfinished task of the member. It joins the member's queued task on the unit, or starts one.
func (s *Service) add(ctx context.Context, tx pgx.Tx, member int64, unit string, w want) error {
	var actions []string
	var repos []int64
	err := tx.QueryRow(ctx, "SELECT actions, repos FROM automation WHERE member_id = $1", member).Scan(&actions, &repos)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || !slices.Contains(actions, w.action) {
		return err
	}
	found, err := prs(ctx, tx, []int64{w.pr})
	if err != nil {
		return err
	}
	pr, ok := found[w.pr]
	if !ok || !pr.open || (repos != nil && !slices.Contains(repos, pr.repo)) {
		return nil
	}
	var reviewers []int64
	if len(w.reviewers) > 0 {
		rows, err := tx.Query(ctx, "SELECT account_id FROM account WHERE forge_id = $1 AND login = ANY($2) ORDER BY account_id", pr.forge, w.reviewers)
		if err != nil {
			return err
		}
		if reviewers, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
	}
	targets := targetsOf(w.action, pr.facts, reviewers)
	mine, err := accounts(ctx, tx, member)
	if err != nil {
		return err
	}
	login, err := logins(ctx, tx, pr.forge)
	if err != nil {
		return err
	}
	now := Start{At: s.now(), Head: pr.facts.HeadSHA}
	if check(w.action, pr.facts, targets, now, mine[pr.forge], login).satisfied {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT i.targets FROM task_item i JOIN task t ON t.id = i.task_id
		WHERE t.member_id = $1 AND t.state IN ('queued', 'leased', 'verifying', 'needs_person') AND i.pr_id = $2 AND i.action = $3
			AND i.state = 'open'`, member, w.pr, w.action)
	if err != nil {
		return err
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Targets, error) {
		var data []byte
		var t Targets
		if err := row.Scan(&data); err != nil {
			return t, err
		}
		return t, json.Unmarshal(data, &t)
	})
	if err != nil {
		return err
	}
	fresh := targets
	for _, h := range held {
		fresh = fresh.minus(h)
	}
	if len(held) > 0 && fresh.empty() {
		return nil
	}
	return s.place(ctx, tx, member, unit, w, fresh, now)
}

// place puts an item in the member's queued task on the unit, merging it with the item for the same pull request and
// action, or starts the task.
func (s *Service) place(ctx context.Context, tx pgx.Tx, member int64, unit string, w want, targets Targets, start Start) error {
	var task int64
	err := tx.QueryRow(ctx, "SELECT id FROM task WHERE member_id = $1 AND unit = $2 AND state = 'queued' FOR UPDATE", member, unit).Scan(&task)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "INSERT INTO task (member_id, unit, priority, next_attempt_at, queued_at) VALUES ($1, $2, $3, $4, $4) RETURNING id",
			member, unit, w.priority, s.now()).Scan(&task)
		if err == nil {
			err = record(ctx, tx, task, "created", park, Queued, nil)
		}
		if err == nil {
			err = later(ctx, tx, KindUnclaimed, task, s.now().Add(s.settings.Unclaimed))
		}
	}
	if err != nil {
		return err
	}
	var existing []byte
	var state string
	err = tx.QueryRow(ctx, "SELECT targets, state FROM task_item WHERE task_id = $1 AND pr_id = $2 AND action = $3", task, w.pr, w.action).
		Scan(&existing, &state)
	merged := targets
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	case state == "open":
		var old Targets
		if err := json.Unmarshal(existing, &old); err != nil {
			return err
		}
		merged = old.plus(targets)
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	startData, err := json.Marshal(start)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_item (task_id, pr_id, action, targets, priority, start) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (task_id, pr_id, action) DO UPDATE SET targets = EXCLUDED.targets, priority = EXCLUDED.priority,
			start = CASE WHEN task_item.state = 'open' THEN task_item.start ELSE EXCLUDED.start END, state = 'open'`,
		task, w.pr, w.action, data, w.priority, startData); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE task SET priority = CASE WHEN EXISTS (SELECT 1 FROM task_item WHERE task_id = $1 AND state = 'open'
		AND priority = 'normal') THEN 'normal' ELSE 'low' END, updated_at = now() WHERE id = $1`, task); err != nil {
		return err
	}
	return record(ctx, tx, task, "item_added", park, Queued, map[string]any{"pr": w.pr, "action": w.action, "targets": targets})
}
