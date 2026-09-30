package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
)

// ClaimRequest is what an executor asks for: the actions it can do, the repositories it covers ("owner/name"; every
// one when empty), and how long it will wait for a task.
type ClaimRequest struct {
	Executor string
	Key      string
	Actions  []string
	Repos    []string
	Wait     time.Duration
}

func newLeaseID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "lse_" + hex.EncodeToString(b)
}

// Claim leases the next task a member's executor may take: docs/design/06-tasks.md#flow-claim. It returns nil when
// there is none, after waiting up to the request's wait. A claim retried with the same key returns the task it took.
func (s *Service) Claim(ctx context.Context, member int64, token string, req ClaimRequest) (*Task, error) {
	if n := utf8.RuneCountInString(req.Executor); n < 1 || n > 100 {
		return nil, fail.New(fail.Invalid, "invalid_claim", "An executor names itself in 1 to 100 characters.")
	}
	if len(req.Actions) == 0 || slices.ContainsFunc(req.Actions, func(a string) bool { return !canBeTask(a) }) {
		return nil, fail.New(fail.Invalid, "invalid_claim", "A claim names the actions the executor can do, each one that can be a task.")
	}
	if req.Key != "" {
		var id int64
		err := s.pool.QueryRow(ctx, "SELECT id FROM task WHERE lease_token = $1 AND claim_key = $2", token, req.Key).Scan(&id)
		if err == nil {
			t, err := s.Task(ctx, id)
			return &t, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	deadline := time.Now().Add(min(max(req.Wait, 0), s.settings.LongestWait))
	for {
		t, err := s.claimOnce(ctx, member, token, req)
		if t != nil || err != nil || !time.Now().Before(deadline) {
			return t, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(time.Second, time.Until(deadline))):
		}
	}
}

func (s *Service) claimOnce(ctx context.Context, member int64, token string, req ClaimRequest) (*Task, error) {
	var repos []string
	if len(req.Repos) > 0 {
		repos = req.Repos
	}
	rows, err := s.pool.Query(ctx, `SELECT t.id FROM task t WHERE t.member_id = $1 AND t.state = 'queued' AND t.next_attempt_at <= $4
			AND EXISTS (SELECT 1 FROM task_item i JOIN pr_fact p ON p.id = i.pr_id JOIN repo r ON r.id = p.repo_id
				WHERE i.task_id = t.id AND i.state = 'open' AND i.action = ANY($2)
					AND ($3::text[] IS NULL OR r.owner || '/' || r.name = ANY($3)))
		ORDER BY t.priority = 'low', t.queued_at, t.id`, member, req.Actions, repos, s.now())
	if err != nil {
		return nil, err
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	chains, err := s.groups.ChainsOf(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range candidates {
		var claimed bool
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var ok bool
			var err error
			claimed, ok, err = s.claimTask(ctx, tx, id, token, req, repos, chains)
			if err != nil || !ok {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if claimed {
			t, err := s.Task(ctx, id)
			return &t, err
		}
	}
	return nil, nil
}

// claimTask tries to lease one queued task, in the claiming transaction. It reports whether it leased it; a task that
// a concurrent claim holds, whose lock another task holds, or that has nothing left to do is not leased.
func (s *Service) claimTask(ctx context.Context, tx pgx.Tx, id int64, token string, req ClaimRequest, repos []string,
	chains map[int64]grouping.Chain) (bool, bool, error) {
	var state string
	err := tx.QueryRow(ctx, "SELECT state FROM task WHERE id = $1 FOR UPDATE SKIP LOCKED", id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state != Queued) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	t, err := load(ctx, tx, id, false)
	if err != nil {
		return false, false, err
	}
	open := t.open()
	var ids []int64
	for _, it := range open {
		ids = append(ids, it.PR)
	}
	locks, err := lockSet(ctx, tx, ids, chains)
	if err != nil {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, "SELECT id FROM pr_fact WHERE id = ANY($1) ORDER BY id FOR UPDATE", locks); err != nil {
		return false, false, err
	}
	var busy bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM task WHERE state IN ('leased', 'verifying') AND locks && $1)", locks).Scan(&busy); err != nil || busy {
		return false, false, err
	}
	found, err := prs(ctx, tx, ids)
	if err != nil {
		return false, false, err
	}
	mine, err := accounts(ctx, tx, t.Member)
	if err != nil {
		return false, false, err
	}
	now := s.now()
	var take, leave []Item
	for _, it := range open {
		pr := found[it.PR]
		if !slices.Contains(req.Actions, it.Action) || (repos != nil && !slices.Contains(repos, pr.owner+"/"+pr.name)) {
			leave = append(leave, it)
			continue
		}
		login, err := logins(ctx, tx, pr.forge)
		if err != nil {
			return false, false, err
		}
		baseline := Start{At: t.CreatedAt, Head: pr.facts.HeadSHA}
		if it.Start != nil {
			baseline = *it.Start
		}
		o := check(it.Action, pr.facts, it.Targets, baseline, mine[pr.forge], login)
		if o.satisfied || !pr.open {
			if _, err := tx.Exec(ctx, "UPDATE task_item SET state = 'satisfied' WHERE id = $1", it.ID); err != nil {
				return false, false, err
			}
			continue
		}
		if it.Action == "address_comments" || it.Action == "request_review" {
			it.Targets = o.remaining
		}
		start, _ := json.Marshal(Start{At: now, Head: pr.facts.HeadSHA})
		targets, _ := json.Marshal(it.Targets)
		if _, err := tx.Exec(ctx, "UPDATE task_item SET start = $2, targets = $3 WHERE id = $1", it.ID, start, targets); err != nil {
			return false, false, err
		}
		take = append(take, it)
	}
	if len(take) == 0 {
		if len(leave) == 0 {
			return false, true, s.finish(ctx, tx, id, Done, park, "every item was already satisfied")
		}
		return false, true, nil
	}
	lease := newLeaseID()
	expires := now.Add(s.settings.Lease)
	var key *string
	if req.Key != "" {
		key = &req.Key
	}
	if _, err := tx.Exec(ctx, `UPDATE task SET state = 'leased', attempt = attempt + 1, lease_id = $2, lease_token = $3, executor = $4,
			lease_expires_at = $5, claim_key = $6, locks = $7, started_at = $8, note = '', waiting = false, updated_at = now()
		WHERE id = $1`, id, lease, token, req.Executor, expires, key, locks, now); err != nil {
		return false, false, err
	}
	if len(leave) > 0 {
		if err := s.splitOff(ctx, tx, t, leave); err != nil {
			return false, false, err
		}
	}
	names := make([]string, 0, len(take))
	for _, it := range take {
		names = append(names, it.Action)
	}
	if err := record(ctx, tx, id, "claimed", tokenActor(token), Leased, map[string]any{"executor": req.Executor, "lease": lease,
		"actions": strings.Join(names, ", ")}); err != nil {
		return false, false, err
	}
	return true, true, later(ctx, tx, KindLease, id, expires)
}

// splitOff moves the items an executor cannot do out of a task it claimed, into a queued task of their own.
func (s *Service) splitOff(ctx context.Context, tx pgx.Tx, t Task, leave []Item) error {
	var other int64
	if err := tx.QueryRow(ctx, `INSERT INTO task (member_id, unit, priority, next_attempt_at, queued_at) VALUES ($1, $2, $3, $4, $4)
		RETURNING id`, t.Member, t.Unit, t.Priority, s.now()).Scan(&other); err != nil {
		return err
	}
	for _, it := range leave {
		if _, err := tx.Exec(ctx, "UPDATE task_item SET task_id = $2 WHERE id = $1", it.ID, other); err != nil {
			return err
		}
	}
	if err := record(ctx, tx, other, "created", park, Queued, map[string]any{"split_from": t.ID}); err != nil {
		return err
	}
	return later(ctx, tx, KindUnclaimed, other, s.now().Add(s.settings.Unclaimed))
}
