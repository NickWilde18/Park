// Package work is the queue through which Park's stages hand work to each other.
//
// An item names a kind of work and a key — refresh this pull request, evaluate this unit — and the earliest time it
// may run. Items with the same kind and key are one item. Enqueuing it again while it waits only moves its time
// earlier, unless it is waiting to retry a failure, when its backoff stands. Enqueuing it while it runs marks it to
// run again once it finishes, at the earliest time asked for meanwhile. So a change that arrives mid-run is never
// lost, and one kind and key never runs twice at once.
//
// Enqueue writes through the caller's transaction, so a stage records its output and the work that follows from
// it together: neither exists without the other. Runners are woken by a notification when that transaction
// commits, and otherwise sleep until the next item is due; they do not poll.
//
// A running item holds a lease, renewed while its handler runs. An item whose lease runs out — its worker died —
// is taken again by any runner. The worker that lost it has its handler's context canceled, and whatever it
// reports afterwards is ignored. Handlers must therefore be idempotent.
package work

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// channel is the notification channel that wakes runners.
const channel = "park_work"

// DB is what Enqueue writes through: a transaction, a connection, or a pool.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const enqueueSQL = `WITH queued AS (
	INSERT INTO work_item (kind, key, run_after)
	VALUES ($1, $2, COALESCE($3::timestamptz, now()))
	ON CONFLICT (kind, key) DO UPDATE SET
		run_after = CASE
			WHEN work_item.state = 'waiting' AND work_item.attempts = 0
				THEN LEAST(work_item.run_after, EXCLUDED.run_after)
			ELSE work_item.run_after
		END,
		rerun_after = CASE
			WHEN work_item.state = 'running'
				THEN LEAST(COALESCE(work_item.rerun_after, 'infinity'), EXCLUDED.run_after)
			ELSE work_item.rerun_after
		END
	RETURNING 1
)
SELECT pg_notify('` + channel + `', '') FROM queued`

// Enqueue asks for kind and key to run at or after at; the zero time means now. Through a transaction, the item
// is queued, and runners are woken, only when the transaction commits.
func Enqueue(ctx context.Context, db DB, kind, key string, at time.Time) error {
	var when any
	if !at.IsZero() {
		when = at
	}
	if _, err := db.Exec(ctx, enqueueSQL, kind, key, when); err != nil {
		return fmt.Errorf("enqueue %s %s: %w", kind, key, err)
	}
	return nil
}
