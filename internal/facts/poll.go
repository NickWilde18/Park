package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/work"
)

func repoKey(it work.Item) (int64, error) { return strconv.ParseInt(it.Key, 10, 64) }

// watchedRepo loads a repository for recurring work: nil when it is gone or no longer watched, which ends the work.
func (s *Service) watchedRepo(ctx context.Context, it work.Item) (*repoState, Collector, error) {
	id, err := repoKey(it)
	if err != nil {
		return nil, nil, err
	}
	r, err := s.loadRepo(ctx, id)
	if errors.Is(err, errNoRepo) || (err == nil && !r.Watched) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	col, _, err := s.collector(ctx, r.InstallationID)
	if err != nil {
		return nil, nil, err
	}
	return &r, col, nil
}

// storedPR is what polling compares of a stored open pull request with the listing: the forge's update time, and the
// stack, 0 for none.
type storedPR struct {
	at    time.Time
	stack int
}

// storedOpen returns the stored open pull requests of a repository, by number.
func (s *Service) storedOpen(ctx context.Context, repoID int64) (map[int]storedPR, error) {
	rows, err := s.pool.Query(ctx, `SELECT number, forge_updated_at, coalesce((state->>'stack')::int, 0)
		FROM pr_fact WHERE repo_id = $1 AND open`, repoID)
	if err != nil {
		return nil, err
	}
	out := map[int]storedPR{}
	for rows.Next() {
		var n int
		var st storedPR
		if err := rows.Scan(&n, &st.at, &st.stack); err != nil {
			return nil, err
		}
		out[n] = st
	}
	return out, rows.Err()
}

// pollRepo lists a repository's open pull requests and refreshes the new ones, the ones updated since they were
// stored or listed in another stack than the stored one, and the stored ones no longer open. It then waits for the
// next poll.
func (s *Service) pollRepo(ctx context.Context, it work.Item) error {
	return s.poll(ctx, it, false)
}

// sweepRepo refreshes every open pull request regardless of its update time — a check finishing does not move it
// — and waits for the next sweep.
func (s *Service) sweepRepo(ctx context.Context, it work.Item) error {
	return s.poll(ctx, it, true)
}

func (s *Service) poll(ctx context.Context, it work.Item, sweep bool) error {
	r, col, err := s.watchedRepo(ctx, it)
	if r == nil || err != nil {
		return err
	}
	listed, err := col.ListOpen(ctx, r.facts())
	if err != nil {
		return wait(err)
	}
	stored, err := s.storedOpen(ctx, r.ID)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		open := map[int]bool{}
		for _, l := range listed {
			open[l.Number] = true
			st, ok := stored[l.Number]
			if sweep || !ok || l.UpdatedAt.After(st.at) || l.Stack != st.stack {
				if err := work.Enqueue(ctx, tx, KindRefreshPR, prKey(r.ID, l.Number), time.Time{}); err != nil {
					return err
				}
			}
		}
		for n := range stored {
			if !open[n] {
				if err := work.Enqueue(ctx, tx, KindRefreshPR, prKey(r.ID, n), time.Time{}); err != nil {
					return err
				}
			}
		}
		kind, every := KindPollRepo, s.timing.Poll
		if sweep {
			kind, every = KindSweepRepo, s.timing.Sweep
		}
		return work.Enqueue(ctx, tx, kind, it.Key, s.now().Add(every))
	})
}

// refreshRepo refreshes a repository's settings: its owner and name, which renames change, its default branch, and
// the protection of the default branch and every base branch in use. When the protection changed, every open pull
// request is refreshed, since what they require changed with it.
func (s *Service) refreshRepo(ctx context.Context, it work.Item) error {
	r, col, err := s.watchedRepo(ctx, it)
	if r == nil || err != nil {
		return err
	}
	rf, err := col.Repository(ctx, r.Owner, r.Name)
	if err != nil {
		return wait(err)
	}
	if rf.ForgeRepo != r.ForgeRepo {
		s.log.ErrorContext(ctx, "a repository's name now belongs to another repository", "repo", r.ID)
		return nil
	}
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT base_branch FROM pr_fact WHERE repo_id = $1 AND open", r.ID)
	if err != nil {
		return err
	}
	branches, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if rf.DefaultBranch != "" && !contains(branches, rf.DefaultBranch) {
		branches = append(branches, rf.DefaultBranch)
	}
	protection, err := col.Protection(ctx, r.facts(), branches)
	if err != nil {
		return wait(err)
	}
	was, err := json.Marshal(r.protection)
	if err != nil {
		return err
	}
	now, err := json.Marshal(protection)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE repo SET owner = $2, name = $3, default_branch = $4, protection = $5, refreshed_at = $6
			WHERE id = $1`, r.ID, rf.Owner, rf.Name, rf.DefaultBranch, now, s.now()); err != nil {
			return err
		}
		if !bytes.Equal(was, now) {
			stored, err := s.storedOpen(ctx, r.ID)
			if err != nil {
				return err
			}
			for n := range stored {
				if err := work.Enqueue(ctx, tx, KindRefreshPR, prKey(r.ID, n), time.Time{}); err != nil {
					return err
				}
			}
		}
		return work.Enqueue(ctx, tx, KindRefreshRepo, it.Key, s.now().Add(s.timing.RepoRefresh))
	})
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// refreshTeam refreshes a team's members, through an installation of its forge whose repositories are watched.
// Members stay unknown when the forge will not tell.
func (s *Service) refreshTeam(ctx context.Context, it work.Item) error {
	f, t, ok := strings.Cut(it.Key, "/")
	forgeID, err1 := strconv.ParseInt(f, 10, 64)
	teamID, err2 := strconv.ParseInt(t, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return errors.New("a malformed team key " + it.Key)
	}
	var org, slug string
	if err := s.pool.QueryRow(ctx, "SELECT org, slug FROM team WHERE forge_id = $1 AND team_id = $2", forgeID, teamID).
		Scan(&org, &slug); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var installation int64
	if err := s.pool.QueryRow(ctx, "SELECT installation_id FROM repo WHERE forge_id = $1 AND watched ORDER BY id LIMIT 1", forgeID).
		Scan(&installation); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	col, _, err := s.collector(ctx, installation)
	if err != nil {
		return err
	}
	members, err := col.Team(ctx, org, slug)
	if err != nil {
		return wait(err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE team SET members = $3, refreshed_at = $4 WHERE forge_id = $1 AND team_id = $2",
			forgeID, teamID, members, s.now()); err != nil {
			return err
		}
		return work.Enqueue(ctx, tx, KindRefreshTeam, it.Key, s.now().Add(s.timing.TeamRefresh))
	})
}

// redeliver processes the deliveries an app installation missed while Park was down, from the forge's delivery
// log, since the last one Park received. A token installation has no log; polling covers it.
func (s *Service) redeliver(ctx context.Context, it work.Item) error {
	id, err := strconv.ParseInt(it.Key, 10, 64)
	if err != nil {
		return err
	}
	var since *time.Time
	if err := s.pool.QueryRow(ctx, "SELECT max(received_at) FROM delivery WHERE installation_id = $1", id).Scan(&since); err != nil {
		return err
	}
	if since == nil {
		return nil
	}
	col, f, err := s.collector(ctx, id)
	if errors.Is(err, errNoInstallation) {
		return nil
	}
	if err != nil {
		return err
	}
	missed, err := col.Missed(ctx, since.Add(-time.Minute))
	if errors.Is(err, ErrNoDeliveryLog) {
		return nil
	}
	if err != nil {
		return wait(err)
	}
	for _, d := range missed {
		if err := s.accept(ctx, id, f.ID, d); err != nil {
			return err
		}
	}
	if len(missed) > 0 {
		s.log.InfoContext(ctx, "processed missed deliveries", "installation", id, "deliveries", len(missed))
	}
	return nil
}

// cleanup forgets delivery IDs older than they are kept, and comes back a day later.
func (s *Service) cleanup(ctx context.Context, it work.Item) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM delivery WHERE received_at < $1", s.now().Add(-s.timing.DeliveryLife)); err != nil {
			return err
		}
		return work.Enqueue(ctx, tx, KindCleanup, it.Key, s.now().Add(24*time.Hour))
	})
}
