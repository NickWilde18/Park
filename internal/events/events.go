// Package events keeps the notices that live updates stream: docs/design/08-dashboard-and-api.md#live-updates.
//
// Changes write their notices themselves, by triggers, as their transactions commit and one transaction at a time, so
// notices are numbered in the order their changes commit: a stream that has seen one has seen every one before it.
// This package reads them in order, so that a stream resumes after the last one it saw, and forgets them after an
// hour.
package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/work"
)

// KindCleanup is the work of forgetting old notices; there is one such item, keyed "notices".
const KindCleanup = "events.cleanup"

// Keep is how long notices are kept, and so how far behind a stream can resume.
const Keep = time.Hour

// Notice is one notice: what kind of thing changed, and which.
type Notice struct {
	At      time.Time
	Kind    string
	Subject string
	ID      int64
}

// Service reads notices.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
}

// New returns the notices' reader.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log, now: time.Now}
}

// Register gives the runner the forgetting of old notices.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindCleanup, func(ctx context.Context, it work.Item) error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "DELETE FROM event_notice WHERE at < $1", s.now().Add(-Keep)); err != nil {
				return err
			}
			return work.Enqueue(ctx, tx, KindCleanup, it.Key, s.now().Add(Keep/4))
		})
	})
}

// Start schedules the forgetting, beginning now.
func (s *Service) Start(ctx context.Context) error {
	return work.Enqueue(ctx, s.pool, KindCleanup, "notices", time.Time{})
}

// After returns the notices after an ID, oldest first, at most limit of them.
func (s *Service) After(ctx context.Context, id int64, limit int) ([]Notice, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, kind, subject, at FROM event_notice WHERE id > $1 ORDER BY id LIMIT $2", id, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Notice, error) {
		var n Notice
		err := row.Scan(&n.ID, &n.Kind, &n.Subject, &n.At)
		return n, err
	})
}

// Bounds are the oldest and the newest notice kept, and the last ever numbered, which may be one still committing or
// one rolled back: each 0 when there is none. A fresh stream begins after the newest, so that it is sent every notice
// still committing.
func (s *Service) Bounds(ctx context.Context) (oldest, newest, last int64, err error) {
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(min(id), 0), COALESCE(max(id), 0),
		COALESCE(pg_sequence_last_value(pg_get_serial_sequence('event_notice', 'id')), 0) FROM event_notice`).Scan(&oldest, &newest, &last)
	return oldest, newest, last, err
}

// Missed reports whether a stream that saw notices up to after would miss some it cannot be given any more.
func Missed(after, oldest, last int64) bool {
	return after < last && (oldest == 0 || oldest > after+1)
}
