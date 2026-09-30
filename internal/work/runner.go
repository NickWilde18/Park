package work

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler does the work of one item. Returning nil finishes it; returning an error retries it after a backoff.
type Handler func(ctx context.Context, item Item) error

// Item is one piece of work, as its handler sees it.
type Item struct {
	Kind string
	Key  string
	// Attempt is 1 for a first run, and counts the failures and lost leases since the item last succeeded.
	Attempt int
}

// Options configure a Runner; zero values take the defaults.
type Options struct {
	// Workers is how many items run at once: 4 by default.
	Workers int
	// Lease is how long a running item is presumed alive without a heartbeat: 1 minute by default. Heartbeats
	// come three times a lease.
	Lease time.Duration
	// Backoff is the wait before an item's attempt runs again after a failure: 2 seconds, doubling, up to
	// 5 minutes, with a fifth of jitter, by default.
	Backoff func(attempt int) time.Duration
}

// Runner takes due items from the queue and runs their handlers.
type Runner struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	handlers map[string]Handler
	opts     Options
}

// NewRunner returns a runner with no handlers.
func NewRunner(pool *pgxpool.Pool, log *slog.Logger, opts Options) *Runner {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Lease <= 0 {
		opts.Lease = time.Minute
	}
	if opts.Backoff == nil {
		opts.Backoff = defaultBackoff
	}
	return &Runner{pool: pool, log: log, handlers: map[string]Handler{}, opts: opts}
}

// Handle registers the handler of a kind. It must be called before Run.
func (r *Runner) Handle(kind string, h Handler) {
	r.handlers[kind] = h
}

func defaultBackoff(attempt int) time.Duration {
	d := 2 * time.Second
	for i := 1; i < attempt && d < 5*time.Minute; i++ {
		d *= 2
	}
	d = min(d, 5*time.Minute)
	return d - d/10 + time.Duration(rand.Int64N(int64(d/5)+1))
}

// Run takes and runs items of the registered kinds until ctx ends, then waits for the handlers still running. A
// handler interrupted that way leaves its item to run again, without counting the attempt.
func (r *Runner) Run(ctx context.Context) error {
	if len(r.handlers) == 0 {
		<-ctx.Done()
		return nil
	}
	kinds := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		kinds = append(kinds, k)
	}
	wake := make(chan struct{}, 1)
	go r.listen(ctx, wake)
	slots := make(chan struct{}, r.opts.Workers)
	var running sync.WaitGroup
	defer running.Wait()

	for {
		for len(slots) < cap(slots) {
			item, ok, err := r.take(ctx, kinds)
			if err != nil {
				if ctx.Err() == nil {
					r.log.WarnContext(ctx, "take work", "error", err)
				}
				break
			}
			if !ok {
				break
			}
			slots <- struct{}{}
			running.Go(func() {
				defer func() {
					<-slots
					poke(wake)
				}()
				r.run(ctx, item)
			})
		}

		var timer *time.Timer
		var due <-chan time.Time
		if len(slots) < cap(slots) {
			if d, ok := r.untilDue(ctx, kinds); ok {
				timer = time.NewTimer(d)
				due = timer.C
			}
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func poke(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// taken is an item this runner holds, with the fencing token of its run.
type taken struct {
	Item
	id  int64
	run int64
}

const takeSQL = `UPDATE work_item w
SET state = 'running', run = w.run + 1, attempts = w.attempts + 1, lease_until = now() + make_interval(secs => $2)
FROM (
	SELECT id FROM work_item
	WHERE kind = ANY($1)
		AND ((state = 'waiting' AND run_after <= now()) OR (state = 'running' AND lease_until < now()))
	ORDER BY run_after, id
	LIMIT 1
	FOR UPDATE SKIP LOCKED
) due
WHERE w.id = due.id
RETURNING w.id, w.kind, w.key, w.attempts, w.run`

func (r *Runner) take(ctx context.Context, kinds []string) (taken, bool, error) {
	var t taken
	err := r.pool.QueryRow(ctx, takeSQL, kinds, r.opts.Lease.Seconds()).Scan(&t.id, &t.Kind, &t.Key, &t.Attempt, &t.run)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return t, true, nil
}

const nextDueSQL = `SELECT min(t) FROM (
	SELECT min(run_after) AS t FROM work_item WHERE state = 'waiting' AND kind = ANY($1)
	UNION ALL
	SELECT min(lease_until) FROM work_item WHERE state = 'running' AND kind = ANY($1)
) due`

// untilDue is how long until the next item is due, or a lease runs out; false when nothing is queued.
func (r *Runner) untilDue(ctx context.Context, kinds []string) (time.Duration, bool) {
	var next *time.Time
	if err := r.pool.QueryRow(ctx, nextDueSQL, kinds).Scan(&next); err != nil {
		if ctx.Err() == nil {
			r.log.WarnContext(ctx, "find the next work", "error", err)
		}
		return time.Second, true
	}
	if next == nil {
		return 0, false
	}
	// An item that is due but was not taken is being taken by another runner; look again shortly.
	return max(time.Until(*next), 50*time.Millisecond), true
}

var errLeaseLost = errors.New("the lease was lost")

func (r *Runner) run(ctx context.Context, t taken) {
	hctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	beat := make(chan struct{})
	go func() {
		defer close(beat)
		r.heartbeat(hctx, t, cancel)
	}()
	err := r.call(hctx, t)
	cancel(nil)
	<-beat
	if errors.Is(context.Cause(hctx), errLeaseLost) {
		r.log.WarnContext(ctx, "work lost its lease", "kind", t.Kind, "key", t.Key, "attempt", t.Attempt)
		return
	}
	bg := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		err = r.finish(bg, t)
	case ctx.Err() != nil:
		err = r.release(bg, t)
	default:
		r.log.WarnContext(ctx, "work failed", "kind", t.Kind, "key", t.Key, "attempt", t.Attempt, "error", err)
		err = r.retry(bg, t, r.opts.Backoff(t.Attempt))
	}
	if err != nil {
		r.log.ErrorContext(ctx, "record work", "kind", t.Kind, "key", t.Key, "error", err)
	}
}

// call runs the handler, turning a panic into an error.
func (r *Runner) call(ctx context.Context, t taken) (err error) {
	defer func() {
		if v := recover(); v != nil {
			r.log.ErrorContext(ctx, "work panicked", "kind", t.Kind, "key", t.Key, "panic", v, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return r.handlers[t.Kind](ctx, t.Item)
}

// heartbeat renews the lease until ctx ends, and cancels the handler if the lease was lost.
func (r *Runner) heartbeat(ctx context.Context, t taken, cancel context.CancelCauseFunc) {
	tick := time.NewTicker(r.opts.Lease / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		tag, err := r.pool.Exec(ctx,
			"UPDATE work_item SET lease_until = now() + make_interval(secs => $3) WHERE id = $1 AND run = $2 AND state = 'running'",
			t.id, t.run, r.opts.Lease.Seconds())
		switch {
		case err != nil:
			if ctx.Err() == nil {
				r.log.WarnContext(ctx, "renew the lease", "kind", t.Kind, "key", t.Key, "error", err)
			}
		case tag.RowsAffected() == 0:
			cancel(errLeaseLost)
			return
		}
	}
}

// finish ends a successful run: the item is done, unless it was enqueued again while it ran.
func (r *Runner) finish(ctx context.Context, t taken) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var rerun *time.Time
		err := tx.QueryRow(ctx, "SELECT rerun_after FROM work_item WHERE id = $1 AND run = $2 FOR UPDATE", t.id, t.run).Scan(&rerun)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // another runner took it over
		}
		if err != nil {
			return err
		}
		if rerun == nil {
			_, err = tx.Exec(ctx, "DELETE FROM work_item WHERE id = $1", t.id)
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE work_item
			SET state = 'waiting', run_after = rerun_after, rerun_after = NULL, attempts = 0, lease_until = NULL
			WHERE id = $1`, t.id)
		if err == nil {
			_, err = tx.Exec(ctx, "SELECT pg_notify('"+channel+"', '')")
		}
		return err
	})
}

// retry puts a failed item back to wait out its backoff. A rerun asked for meanwhile is folded into the retry.
func (r *Runner) retry(ctx context.Context, t taken, backoff time.Duration) error {
	_, err := r.pool.Exec(ctx, `UPDATE work_item
		SET state = 'waiting', run_after = now() + make_interval(secs => $3), rerun_after = NULL, lease_until = NULL
		WHERE id = $1 AND run = $2`, t.id, t.run, backoff.Seconds())
	return err
}

// release puts an item interrupted by shutdown back, due at once, and does not count the attempt.
func (r *Runner) release(ctx context.Context, t taken) error {
	_, err := r.pool.Exec(ctx, `UPDATE work_item
		SET state = 'waiting', run_after = now(), rerun_after = NULL, attempts = attempts - 1, lease_until = NULL
		WHERE id = $1 AND run = $2`, t.id, t.run)
	return err
}

// listen wakes the loop on every notification. While it is reconnecting, notifications are lost, so it wakes the
// loop once it listens again.
func (r *Runner) listen(ctx context.Context, wake chan<- struct{}) {
	wait := time.Second
	for {
		err := r.listenOnce(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		r.log.WarnContext(ctx, "work notifications lost; reconnecting", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (r *Runner) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	conn, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	poke(wake)
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		poke(wake)
	}
}
