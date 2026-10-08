package facts

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/work"
)

// watched is an installation's collector as the fact layer uses it: it records on the installation what stops
// collection through it — the forge refusing its credential, or its rate limit — and clears it when a call works
// (docs/design/02-facts.md#collection-problems). While the credential is refused, it calls the forge only once each
// RefusedProbe, to find out whether the credential works again, and fails every other call with ErrRefused at once.
type watched struct {
	Collector
	s       *Service
	problem string // the installation's problem when the collector was taken
	id      int64
}

// call runs one call to the forge, unless the refused credential was tried too recently, and records what its
// error says of the installation.
func (w *watched) call(ctx context.Context, do func() error) error {
	if w.problem == ProblemRefused && !w.s.mayProbe(w.id) {
		return ErrRefused
	}
	err := do()
	w.s.note(ctx, w.id, w.problem, err)
	return err
}

func (w *watched) FetchPR(ctx context.Context, r Repo, number int, protection map[string]Protection) (f Fetched, err error) {
	err = w.call(ctx, func() error { f, err = w.Collector.FetchPR(ctx, r, number, protection); return err })
	return f, err
}

func (w *watched) ListOpen(ctx context.Context, r Repo) (l []Listed, err error) {
	err = w.call(ctx, func() error { l, err = w.Collector.ListOpen(ctx, r); return err })
	return l, err
}

func (w *watched) Light(ctx context.Context, r Repo, number int) (l Light, err error) {
	err = w.call(ctx, func() error { l, err = w.Collector.Light(ctx, r, number); return err })
	return l, err
}

func (w *watched) Repository(ctx context.Context, owner, name string) (rf RepoFacts, err error) {
	err = w.call(ctx, func() error { rf, err = w.Collector.Repository(ctx, owner, name); return err })
	return rf, err
}

func (w *watched) Protection(ctx context.Context, r Repo, branches []string) (p map[string]Protection, err error) {
	err = w.call(ctx, func() error { p, err = w.Collector.Protection(ctx, r, branches); return err })
	return p, err
}

func (w *watched) Access(ctx context.Context, r Repo, login string) (a string, err error) {
	err = w.call(ctx, func() error { a, err = w.Collector.Access(ctx, r, login); return err })
	return a, err
}

func (w *watched) Team(ctx context.Context, org, slug string) (m []int64, err error) {
	err = w.call(ctx, func() error { m, err = w.Collector.Team(ctx, org, slug); return err })
	return m, err
}

func (w *watched) Missed(ctx context.Context, since time.Time) (d []Delivery, err error) {
	err = w.call(ctx, func() error { d, err = w.Collector.Missed(ctx, since); return err })
	return d, err
}

// Delivery checks a delivery against the secret without calling the forge, so it works whatever stops collection.
func (w *watched) Delivery(header http.Header, body, secret []byte) (Delivery, error) {
	return w.Collector.Delivery(header, body, secret)
}

// mayProbe reports whether a call may try an installation's refused credential now, and if so, counts it as the
// try: one call each RefusedProbe. An installation not tried since Park started may be tried at once.
func (s *Service) mayProbe(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if last, ok := s.probed[id]; ok && now.Before(last.Add(s.timing.RefusedProbe)) {
		return false
	}
	s.probed[id] = now
	return true
}

// note records what a call's error says of its installation, whose problem was had when the call began. An error
// that says nothing of the installation — the forge failing, the call canceled — changes nothing. A failure to
// record is logged, and the call's own outcome stands.
func (s *Service) note(ctx context.Context, id int64, had string, err error) {
	var rl *RateLimited
	var q string
	var args []any
	switch {
	case err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoDeliveryLog):
		if had == "" {
			return
		}
		q = `UPDATE installation SET problem = NULL, problem_since = NULL, problem_until = NULL
			WHERE id = $1 AND problem IS NOT NULL`
		args = []any{id}
	case errors.Is(err, ErrRefused):
		s.mu.Lock()
		s.probed[id] = s.now()
		s.mu.Unlock()
		if had == ProblemRefused {
			return
		}
		q = `UPDATE installation SET problem = 'refused', problem_since = $2, problem_until = NULL
			WHERE id = $1 AND problem IS DISTINCT FROM 'refused'`
		args = []any{id, s.now()}
	case errors.As(err, &rl):
		q = `UPDATE installation SET problem = 'rate_limited', problem_until = $3,
				problem_since = CASE WHEN problem = 'rate_limited' THEN problem_since ELSE $2 END
			WHERE id = $1 AND (problem, problem_until) IS DISTINCT FROM ('rate_limited', $3)`
		args = []any{id, s.now(), rl.Until}
	default:
		return
	}
	if _, xerr := s.pool.Exec(context.WithoutCancel(ctx), q, args...); xerr != nil {
		s.log.WarnContext(ctx, "record an installation's problem", "installation", id, "error", xerr)
	}
}

// clearProblem forgets an installation's problem as its credential is replaced, and asks the forge again now about
// its watched repositories: whether the new credential works is found out by using it.
func (s *Service) clearProblem(ctx context.Context, tx pgx.Tx, id int64) error {
	if _, err := tx.Exec(ctx, "UPDATE installation SET problem = NULL, problem_since = NULL, problem_until = NULL WHERE id = $1", id); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT id FROM repo WHERE installation_id = $1 AND watched", id)
	if err != nil {
		return err
	}
	repos, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	now := s.now()
	for _, r := range repos {
		k := strconv.FormatInt(r, 10)
		for _, kind := range []string{KindPollRepo, KindRefreshRepo} {
			if err := work.Enqueue(ctx, tx, kind, k, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Problem is what stops collection through one installation, for the board: docs/design/02-facts.md#collection-problems.
type Problem struct {
	Since time.Time
	// Until is when a rate limit resets; nil for another problem.
	Until *time.Time
	// Forge is the name of the installation's forge, as an admin gave it.
	Forge string
	// Problem is ProblemRefused or ProblemRateLimited.
	Problem      string
	Repos        []RepoRecord // the watched repositories collected through the installation
	Installation int64
}

// Problems lists what stops collection, installation by installation, in the order of their IDs.
func (s *Service) Problems(ctx context.Context) ([]Problem, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id, f.name, i.problem, i.problem_since, i.problem_until
		FROM installation i JOIN forge f ON f.id = i.forge_id WHERE i.problem IS NOT NULL ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Problem, error) {
		var p Problem
		err := row.Scan(&p.Installation, &p.Forge, &p.Problem, &p.Since, &p.Until)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		rows, err := s.pool.Query(ctx, "SELECT "+repoColumns+" FROM repo WHERE installation_id = $1 AND watched ORDER BY owner, name",
			out[i].Installation)
		if err != nil {
			return nil, err
		}
		if out[i].Repos, err = pgx.CollectRows(rows, scanRepo); err != nil {
			return nil, err
		}
	}
	return out, nil
}
