package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/work"
)

func prKey(repoID int64, number int) string {
	return strconv.FormatInt(repoID, 10) + "/" + strconv.Itoa(number)
}

func parsePRKey(key string) (int64, int, error) {
	r, n, ok := strings.Cut(key, "/")
	repo, err1 := strconv.ParseInt(r, 10, 64)
	number, err2 := strconv.Atoi(n)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("a malformed pull request key %q", key)
	}
	return repo, number, nil
}

// wait turns a rate limit into a wait for its reset; other errors pass through.
func wait(err error) error {
	var rl *RateLimited
	if errors.As(err, &rl) {
		return work.RetryAt(rl.Until, err)
	}
	return err
}

// refreshPR is the refresh flow: fetch the full state, normalize it, compare it with the stored one, and store the
// changes with their event, in one transaction. A failed fetch leaves the stored state as it was, marked stale.
func (s *Service) refreshPR(ctx context.Context, it work.Item) error {
	repoID, number, err := parsePRKey(it.Key)
	if err != nil {
		return err
	}
	r, err := s.loadRepo(ctx, repoID)
	if errors.Is(err, errNoRepo) || (err == nil && !r.Watched) {
		return nil
	}
	if err != nil {
		return err
	}
	col, _, err := s.collector(ctx, r.InstallationID)
	if err != nil {
		return err
	}
	fetched, err := col.FetchPR(ctx, r.facts(), number, r.protection)
	if errors.Is(err, ErrNotFound) {
		s.log.WarnContext(ctx, "the forge has no such pull request", "repo", repoID, "number", number)
		return s.markStale(ctx, repoID, number)
	}
	if err != nil {
		if serr := s.markStale(ctx, repoID, number); serr != nil {
			s.log.WarnContext(ctx, "mark a pull request stale", "repo", repoID, "number", number, "error", serr)
		}
		return wait(err)
	}
	watched, err := s.watched(ctx, r.ForgeID)
	if err != nil {
		return err
	}
	p := &fetched.PR
	p.References = ParseRefs(fetched.RefText, WatchedRepo{ID: r.ID, Owner: r.Owner, Name: r.Name}, number, watched)
	p.AuthorCanDismiss = s.canDismiss(ctx, &r, col, &fetched)
	fetchedAt := s.now()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.store(ctx, tx, &r, &fetched, fetchedAt) })
}

// store records a fetched state, inside tx.
func (s *Service) store(ctx context.Context, tx pgx.Tx, r *repoState, f *Fetched, fetchedAt time.Time) error {
	p := &f.PR
	// F4: one writer per pull request, whichever worker holds its refresh.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('park.pr_fact:' || $1::bigint::text || '/' || $2::int::text, 0))",
		r.ID, p.Number); err != nil {
		return err
	}
	var id, seq int64
	var raw []byte
	var storedAt time.Time
	err := tx.QueryRow(ctx, "SELECT id, state, seq, fetched_at FROM pr_fact WHERE repo_id = $1 AND number = $2 FOR UPDATE",
		r.ID, p.Number).Scan(&id, &raw, &seq, &storedAt)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if exists && storedAt.After(fetchedAt) {
		return nil // a newer fetch was stored already
	}
	var old *PR
	if exists {
		old = &PR{}
		if err := json.Unmarshal(raw, old); err != nil {
			return err
		}
	}
	changes, err := Diff(old, p)
	if err != nil {
		return err
	}
	if err := s.noteAccounts(ctx, tx, r.ForgeID, f); err != nil {
		return err
	}
	state, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var headRepo *int64
	if p.HeadRepo != 0 {
		headRepo = &p.HeadRepo
	}
	switch {
	case len(changes) == 0:
		_, err = tx.Exec(ctx, `UPDATE pr_fact SET fetched_at = $2, refreshed_at = $2, stale = false WHERE id = $1`, id, fetchedAt)
	case !exists:
		err = tx.QueryRow(ctx, `INSERT INTO pr_fact (repo_id, number, state, seq, open, head_sha, head_branch, head_repo_id,
				base_branch, forge_updated_at, fetched_at, refreshed_at)
			VALUES ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9, $10, $10) RETURNING id`,
			r.ID, p.Number, state, p.Open(), p.HeadSHA, p.HeadBranch, headRepo, p.BaseBranch, p.UpdatedAt, fetchedAt).Scan(&id)
		seq = 0
	default:
		_, err = tx.Exec(ctx, `UPDATE pr_fact SET state = $2, seq = seq + 1, open = $3, head_sha = $4, head_branch = $5,
				head_repo_id = $6, base_branch = $7, forge_updated_at = $8, fetched_at = $9, refreshed_at = $9, stale = false
			WHERE id = $1`, id, state, p.Open(), p.HeadSHA, p.HeadBranch, headRepo, p.BaseBranch, p.UpdatedAt, fetchedAt)
	}
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		data, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO fact_event (pr_id, seq, changes, source, observed_at) VALUES ($1, $2, $3, 'forge', $4)",
			id, seq+1, data, fetchedAt); err != nil {
			return err
		}
		for _, follow := range s.followers {
			if err := follow(ctx, tx, id, Changed(changes)); err != nil {
				return err
			}
		}
	}
	if _, known := r.protection[p.BaseBranch]; !known && p.Open() {
		if err := work.Enqueue(ctx, tx, KindRefreshRepo, strconv.FormatInt(r.ID, 10), time.Time{}); err != nil {
			return err
		}
	}
	if needsShortPoll(p) {
		return work.Enqueue(ctx, tx, KindCheckPR, prKey(r.ID, p.Number), fetchedAt.Add(s.timing.ShortPoll))
	}
	return nil
}

// needsShortPoll reports a pull request about to change without moving its update time: its required checks are
// pending, or its mergeability is not computed yet.
func needsShortPoll(p *PR) bool {
	if !p.Open() {
		return false
	}
	if p.Mergeable == MergeableUnknown || len(p.MissingChecks) > 0 {
		return true
	}
	for _, c := range p.Checks {
		if c.State == CheckPending {
			return true
		}
	}
	return false
}

// noteAccounts records the display data of the accounts a fetch saw, and the teams it was asked for, queuing a
// refresh of a team's members when they are not known.
func (s *Service) noteAccounts(ctx context.Context, tx pgx.Tx, forgeID int64, f *Fetched) error {
	for _, a := range f.Accounts {
		if _, err := tx.Exec(ctx, `INSERT INTO account (forge_id, account_id, login, name) VALUES ($1, $2, $3, $4)
			ON CONFLICT (forge_id, account_id) DO UPDATE SET login = EXCLUDED.login,
				name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE account.name END, updated_at = now()`,
			forgeID, a.ID, a.Login, a.Name); err != nil {
			return err
		}
	}
	for _, t := range f.Teams {
		var refreshed *time.Time
		err := tx.QueryRow(ctx, `INSERT INTO team (forge_id, team_id, org, slug, name) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (forge_id, team_id) DO UPDATE SET org = EXCLUDED.org, slug = EXCLUDED.slug, name = EXCLUDED.name
			RETURNING refreshed_at`, forgeID, t.ID, t.Org, t.Slug, t.Name).Scan(&refreshed)
		if err != nil {
			return err
		}
		if refreshed == nil {
			if err := work.Enqueue(ctx, tx, KindRefreshTeam, strconv.FormatInt(forgeID, 10)+"/"+strconv.FormatInt(t.ID, 10), time.Time{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// canDismiss works out whether the author may dismiss reviews: write access or more, as the forge reports it and
// Park keeps for an hour. When the forge will not say, the author's standing in the repository decides.
func (s *Service) canDismiss(ctx context.Context, r *repoState, col Collector, f *Fetched) bool {
	author := f.PR.Author
	if author == 0 {
		return false
	}
	var level string
	var refreshed time.Time
	err := s.pool.QueryRow(ctx, "SELECT level, refreshed_at FROM repo_access WHERE repo_id = $1 AND account_id = $2", r.ID, author).
		Scan(&level, &refreshed)
	if err != nil || s.now().Sub(refreshed) > s.timing.AccessLife {
		level = ""
		if f.AuthorLogin != "" {
			got, err := col.Access(ctx, r.facts(), f.AuthorLogin)
			if err == nil {
				level = got
				if _, err := s.pool.Exec(ctx, `INSERT INTO repo_access (repo_id, account_id, level, refreshed_at) VALUES ($1, $2, $3, $4)
					ON CONFLICT (repo_id, account_id) DO UPDATE SET level = EXCLUDED.level, refreshed_at = EXCLUDED.refreshed_at`,
					r.ID, author, level, s.now()); err != nil {
					s.log.WarnContext(ctx, "keep an access level", "repo", r.ID, "error", err)
				}
			}
		}
	}
	switch level {
	case "admin", "maintain", "write":
		return true
	case "":
		switch f.Association {
		case "OWNER", "MEMBER", "COLLABORATOR":
			return true
		}
	}
	return false
}

// markStale notes that a pull request's stored state could not be refreshed; its last refresh time stays.
func (s *Service) markStale(ctx context.Context, repoID int64, number int) error {
	_, err := s.pool.Exec(ctx, "UPDATE pr_fact SET stale = true WHERE repo_id = $1 AND number = $2", repoID, number)
	return err
}

// checkPR is short polling: a cheap look at what changes without moving the update time. A difference from the
// stored state calls for a full refresh; otherwise, while something is still pending, it looks again later.
func (s *Service) checkPR(ctx context.Context, it work.Item) error {
	repoID, number, err := parsePRKey(it.Key)
	if err != nil {
		return err
	}
	r, err := s.loadRepo(ctx, repoID)
	if errors.Is(err, errNoRepo) || (err == nil && !r.Watched) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, "SELECT state FROM pr_fact WHERE repo_id = $1 AND number = $2 AND open", repoID, number).
		Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var stored PR
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	col, _, err := s.collector(ctx, r.InstallationID)
	if err != nil {
		return err
	}
	light, err := col.Light(ctx, r.facts(), number)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return wait(err)
	}
	if lightDiffers(&stored, &light) {
		return work.Enqueue(ctx, s.pool, KindRefreshPR, it.Key, time.Time{})
	}
	if needsShortPoll(&stored) {
		return work.Enqueue(ctx, s.pool, KindCheckPR, it.Key, s.now().Add(s.timing.ShortPoll))
	}
	return nil
}

// lightDiffers compares a cheap look with the stored state: the head, mergeability, and each check's state.
func lightDiffers(stored *PR, l *Light) bool {
	if stored.HeadSHA != l.HeadSHA || stored.Mergeable != l.Mergeable || len(stored.Checks) != len(l.Checks) {
		return true
	}
	states := map[string]string{}
	for _, c := range stored.Checks {
		states[c.Name] = c.State
	}
	for _, c := range l.Checks {
		if states[c.Name] != c.State {
			return true
		}
	}
	return false
}
