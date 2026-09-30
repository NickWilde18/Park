package facts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/work"
)

// maxDelivery is the largest delivery body read; GitHub caps payloads at 25 MB.
const maxDelivery = 26 << 20

// Webhook takes deliveries at /hooks/{installation}: it verifies the signature with the installation's secret,
// drops a delivery it has seen, works out what the delivery concerns, and queues the refreshes — all before it
// answers, which it does at once. The refreshes run afterward.
func (s *Service) Webhook() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("installation"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxDelivery))
		if err != nil {
			http.Error(w, "unreadable delivery", http.StatusBadRequest)
			return
		}
		var sealed []byte
		err = s.pool.QueryRow(r.Context(), "SELECT webhook_secret FROM installation WHERE id = $1", id).Scan(&sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.log.ErrorContext(r.Context(), "read an installation for a delivery", "installation", id, "error", err)
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		var secret []byte
		if sealed != nil {
			if secret, err = s.box.Open(sealed, webhookContext(id)); err != nil {
				s.log.ErrorContext(r.Context(), "open a webhook secret", "installation", id, "error", err)
				http.Error(w, "try again", http.StatusServiceUnavailable)
				return
			}
		}
		col, f, err := s.collector(r.Context(), id)
		if err != nil {
			s.log.ErrorContext(r.Context(), "make a collector for a delivery", "installation", id, "error", err)
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		d, err := col.Delivery(r.Header, body, secret)
		if errors.Is(err, ErrBadSignature) {
			s.log.WarnContext(r.Context(), "a delivery with a bad signature", "installation", id)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		if err != nil || d.ID == "" {
			http.Error(w, "unreadable delivery", http.StatusBadRequest)
			return
		}
		if err := s.accept(r.Context(), id, f.ID, d); err != nil {
			s.log.ErrorContext(r.Context(), "accept a delivery", "installation", id, "delivery", d.ID, "error", err)
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

// accept records a delivery and queues what it calls for, in one transaction; a delivery already recorded is
// dropped (F3).
func (s *Service) accept(ctx context.Context, installation, forgeID int64, d Delivery) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "INSERT INTO delivery (installation_id, delivery_id, received_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
			installation, d.ID, s.now())
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.schedule(ctx, tx, forgeID, d)
	})
}

// schedule queues the refreshes a delivery calls for: its pull requests; the open pull requests whose head is a
// commit it names; for a push, those whose head is the branch, and — after a delay, since the forge computes
// mergeability afterward — those whose base it is; a repository refresh; team refreshes.
func (s *Service) schedule(ctx context.Context, tx pgx.Tx, forgeID int64, d Delivery) error {
	now := s.now()
	soon := now.Add(s.timing.Coalesce)
	var repoID int64
	var watched bool
	if d.ForgeRepo != 0 {
		err := tx.QueryRow(ctx, "SELECT id, watched FROM repo WHERE forge_id = $1 AND forge_repo_id = $2", forgeID, d.ForgeRepo).
			Scan(&repoID, &watched)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	refresh := func(repo int64, number int, at time.Time) error {
		return work.Enqueue(ctx, tx, KindRefreshPR, prKey(repo, number), at)
	}
	refreshAll := func(at time.Time, q string, args ...any) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		type pr struct {
			repo   int64
			number int
		}
		prs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pr, error) {
			var p pr
			err := row.Scan(&p.repo, &p.number)
			return p, err
		})
		if err != nil {
			return err
		}
		for _, p := range prs {
			if err := refresh(p.repo, p.number, at); err != nil {
				return err
			}
		}
		return nil
	}
	if watched {
		for _, n := range d.PRs {
			if err := refresh(repoID, n, soon); err != nil {
				return err
			}
		}
		if len(d.SHAs) > 0 {
			if err := refreshAll(soon, "SELECT repo_id, number FROM pr_fact WHERE repo_id = $1 AND head_sha = ANY($2) AND open",
				repoID, d.SHAs); err != nil {
				return err
			}
		}
		if d.Pushed != "" {
			if err := refreshAll(now.Add(s.timing.BaseMoved), "SELECT repo_id, number FROM pr_fact WHERE repo_id = $1 AND base_branch = $2 AND open",
				repoID, d.Pushed); err != nil {
				return err
			}
		}
		if d.RepoChanged {
			if err := work.Enqueue(ctx, tx, KindRefreshRepo, strconv.FormatInt(repoID, 10), time.Time{}); err != nil {
				return err
			}
		}
	}
	if d.Pushed != "" && d.ForgeRepo != 0 {
		// A push to a branch that is the head of pull requests, which may belong to other repositories when the
		// branch is in a fork.
		if err := refreshAll(soon, `SELECT p.repo_id, p.number FROM pr_fact p JOIN repo r ON r.id = p.repo_id
			WHERE r.forge_id = $1 AND r.watched AND p.head_repo_id = $2 AND p.head_branch = $3 AND p.open`,
			forgeID, d.ForgeRepo, d.Pushed); err != nil {
			return err
		}
	}
	for _, t := range d.Teams {
		var known bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM team WHERE forge_id = $1 AND team_id = $2)", forgeID, t).Scan(&known); err != nil {
			return err
		}
		if known {
			if err := work.Enqueue(ctx, tx, KindRefreshTeam, strconv.FormatInt(forgeID, 10)+"/"+strconv.FormatInt(t, 10), time.Time{}); err != nil {
				return err
			}
		}
	}
	return nil
}
