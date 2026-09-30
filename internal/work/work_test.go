package work

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/store/storetest"
)

// syncBuffer is a log buffer that runners may write to from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs r until the test ends, and prints its log if the test failed.
func start(t *testing.T, r *Runner, logs *syncBuffer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Logf("runner log:\n%s", logs.String())
		}
	})
	return stop
}

func newRunner(pool *pgxpool.Pool, opts Options) (*Runner, *syncBuffer) {
	logs := &syncBuffer{}
	return NewRunner(pool, slog.New(slog.NewTextHandler(logs, nil)), opts), logs
}

type row struct {
	state      string
	runAfter   time.Time
	rerunAfter *time.Time
	attempts   int
	run        int64
}

// read returns the item of kind and key, and whether it exists.
func read(t *testing.T, pool *pgxpool.Pool, kind, key string) (row, bool) {
	t.Helper()
	var r row
	err := pool.QueryRow(context.Background(),
		"SELECT state, run_after, rerun_after, attempts, run FROM work_item WHERE kind = $1 AND key = $2", kind, key).
		Scan(&r.state, &r.runAfter, &r.rerunAfter, &r.attempts, &r.run)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// eventually waits up to five seconds for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func near(a, b time.Time) bool {
	d := a.Sub(b)
	return d < 2*time.Second && d > -2*time.Second
}

func enqueue(t *testing.T, db DB, kind, key string, at time.Time) {
	t.Helper()
	if err := Enqueue(context.Background(), db, kind, key, at); err != nil {
		t.Fatal(err)
	}
}

// Enqueuing the same kind and key while it waits keeps one item, due at the earliest time asked for.
func TestEnqueueCoalesces(t *testing.T) {
	pool := storetest.MigratedPool(t)
	now := time.Now()
	for _, d := range []time.Duration{10 * time.Second, 5 * time.Second, 20 * time.Second} {
		enqueue(t, pool, "refresh", "pr/1", now.Add(d))
	}
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM work_item").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d items, %v", n, err)
	}
	if r, _ := read(t, pool, "refresh", "pr/1"); !near(r.runAfter, now.Add(5*time.Second)) {
		t.Fatalf("due at %v, want about %v", r.runAfter, now.Add(5*time.Second))
	}
}

// An item waiting out the backoff of a failure keeps it when enqueued again, so that a failing item that keeps
// being enqueued is not retried in a tight loop.
func TestEnqueueKeepsBackoff(t *testing.T) {
	pool := storetest.MigratedPool(t)
	later := time.Now().Add(time.Hour)
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO work_item (kind, key, run_after, attempts) VALUES ('refresh', 'pr/1', $1, 3)", later); err != nil {
		t.Fatal(err)
	}
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	if r, _ := read(t, pool, "refresh", "pr/1"); !near(r.runAfter, later) {
		t.Fatalf("due at %v, want the backoff's %v", r.runAfter, later)
	}
}

// A transaction's items exist only once it commits: rolled back, it leaves none.
func TestEnqueueIsTransactional(t *testing.T) {
	pool := storetest.MigratedPool(t)
	ctx := context.Background()
	for _, commit := range []bool{false, true} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		enqueue(t, tx, "refresh", "pr/1", time.Time{})
		if _, ok := read(t, pool, "refresh", "pr/1"); ok {
			t.Fatal("the item is visible before the transaction ends")
		}
		if commit {
			err = tx.Commit(ctx)
		} else {
			err = tx.Rollback(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := read(t, pool, "refresh", "pr/1"); ok != commit {
			t.Fatalf("commit %v: item exists %v", commit, ok)
		}
	}
}

// A runner with nothing due sleeps until notified: an item enqueued later runs within moments. Nothing else would
// wake it — no item was due, or queued at all, when it went to sleep.
func TestRunnerWakesOnNotification(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{})
	ran := make(chan Item, 1)
	r.Handle("refresh", func(_ context.Context, it Item) error { ran <- it; return nil })
	start(t, r, logs)
	time.Sleep(300 * time.Millisecond) // let it find the queue empty and sleep
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	select {
	case it := <-ran:
		if it.Key != "pr/1" || it.Attempt != 1 {
			t.Fatalf("got %+v", it)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the runner did not wake")
	}
	eventually(t, "the item to be done", func() bool { _, ok := read(t, pool, "refresh", "pr/1"); return !ok })
}

// An item enqueued for later runs when it is due, not before.
func TestRunnerRunsDelayedItemWhenDue(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{})
	ran := make(chan time.Time, 1)
	r.Handle("poll", func(context.Context, Item) error { ran <- time.Now(); return nil })
	start(t, r, logs)
	due := time.Now().Add(time.Second)
	enqueue(t, pool, "poll", "repo/1", due)
	select {
	case at := <-ran:
		if at.Before(due) {
			t.Fatalf("ran at %v, before it was due at %v", at, due)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the item did not run")
	}
}

// A change enqueued while its item runs makes the item run again once it finishes — at the time asked for, so that
// a handler that enqueues its own item for later is not rerun at once.
func TestChangeMidRunRunsAgain(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{})
	release := make(chan struct{}, 2)
	var runs atomic.Int32
	started := make(chan struct{}, 4)
	r.Handle("refresh", func(ctx context.Context, _ Item) error {
		runs.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	start(t, r, logs)

	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	<-started
	enqueue(t, pool, "refresh", "pr/1", time.Time{}) // a change arrives mid-run
	if row, _ := read(t, pool, "refresh", "pr/1"); row.state != "running" || row.rerunAfter == nil {
		t.Fatalf("mid-run: %+v", row)
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the item did not run again")
	}
	enqueue(t, pool, "refresh", "pr/1", time.Now().Add(time.Hour)) // asked for later, mid-run
	release <- struct{}{}
	eventually(t, "the second run to finish", func() bool {
		row, ok := read(t, pool, "refresh", "pr/1")
		return ok && row.state == "waiting"
	})
	if row, _ := read(t, pool, "refresh", "pr/1"); !near(row.runAfter, time.Now().Add(time.Hour)) || row.attempts != 0 {
		t.Fatalf("after the second run: %+v", row)
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("%d runs, want 2", n)
	}
}

// A failure is retried after its backoff, with the attempt counted; a success then finishes the item.
func TestFailureIsRetried(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{Backoff: func(int) time.Duration { return 200 * time.Millisecond }})
	attempts := make(chan int, 4)
	r.Handle("refresh", func(_ context.Context, it Item) error {
		attempts <- it.Attempt
		if it.Attempt == 1 {
			return errors.New("the forge is down")
		}
		return nil
	})
	start(t, r, logs)
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	for want := 1; want <= 2; want++ {
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("attempt %d, want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("attempt %d did not run", want)
		}
	}
	eventually(t, "the item to be done", func() bool { _, ok := read(t, pool, "refresh", "pr/1"); return !ok })
}

// A handler that panics fails its attempt, and the runner carries on.
func TestPanicIsAFailure(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{Backoff: func(int) time.Duration { return 100 * time.Millisecond }})
	var calls atomic.Int32
	r.Handle("refresh", func(_ context.Context, it Item) error {
		calls.Add(1)
		if it.Attempt == 1 {
			panic("a bug")
		}
		return nil
	})
	start(t, r, logs)
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	eventually(t, "the retry to succeed", func() bool { _, ok := read(t, pool, "refresh", "pr/1"); return !ok && calls.Load() == 2 })
}

// An item left running by a worker that died is taken again once its lease has run out, and counts the attempt.
func TestDeadWorkersItemIsTakenAgain(t *testing.T) {
	pool := storetest.MigratedPool(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO work_item (kind, key, state, run_after, attempts, run, lease_until)
		VALUES ('refresh', 'pr/1', 'running', now() - interval '1 minute', 1, 7, now() - interval '1 second')`); err != nil {
		t.Fatal(err)
	}
	r, logs := newRunner(pool, Options{})
	ran := make(chan Item, 1)
	r.Handle("refresh", func(_ context.Context, it Item) error { ran <- it; return nil })
	start(t, r, logs)
	select {
	case it := <-ran:
		if it.Attempt != 2 {
			t.Fatalf("attempt %d, want 2", it.Attempt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the item was not taken again")
	}
	eventually(t, "the item to be done", func() bool { _, ok := read(t, pool, "refresh", "pr/1"); return !ok })
}

// A worker whose item was taken over has its handler canceled at its next heartbeat, and changes nothing afterwards:
// the item stays with its new run.
func TestLostLeaseCancelsHandler(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{Lease: 600 * time.Millisecond})
	started := make(chan struct{})
	causes := make(chan error, 1)
	r.Handle("refresh", func(ctx context.Context, _ Item) error {
		close(started)
		<-ctx.Done()
		causes <- context.Cause(ctx)
		return nil
	})
	start(t, r, logs)
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	<-started
	// Another runner takes it over, as if this one had stopped renewing its lease.
	if _, err := pool.Exec(context.Background(),
		"UPDATE work_item SET run = run + 1, lease_until = now() + interval '1 hour' WHERE key = 'pr/1'"); err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-causes:
		if !errors.Is(cause, errLeaseLost) {
			t.Fatalf("canceled with %v", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler was not canceled")
	}
	time.Sleep(200 * time.Millisecond)
	if row, ok := read(t, pool, "refresh", "pr/1"); !ok || row.state != "running" || row.run != 2 {
		t.Fatalf("the old worker changed the item: %+v, exists %v", row, ok)
	}
}

// Shutting down stops the handlers and puts their items back, due at once, without counting the attempt.
func TestShutdownReleasesItems(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{})
	started := make(chan struct{})
	r.Handle("refresh", func(ctx context.Context, _ Item) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	stop := start(t, r, logs)
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	<-started
	stop()
	row, ok := read(t, pool, "refresh", "pr/1")
	if !ok || row.state != "waiting" || row.attempts != 0 || row.runAfter.After(time.Now()) {
		t.Fatalf("after shutdown: %+v, exists %v", row, ok)
	}
}

// Three runners, as three processes, work through changes to five keys that arrive while they run. A key never runs
// in two places at once, and the last change to every key is processed. Each change bumps the key's version and
// enqueues it in one transaction; each run reads the version it saw.
func TestConcurrentRunners(t *testing.T) {
	url := storetest.MigratedDatabase(t)
	ctx := context.Background()
	open := func() *pgxpool.Pool {
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	pool := open()
	if _, err := pool.Exec(ctx, "CREATE TABLE version (key text PRIMARY KEY, n int NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	running := map[string]bool{}
	seen := map[string]int{}
	for i := range 3 {
		p := open()
		r, logs := newRunner(p, Options{Workers: 2})
		r.Handle("evaluate", func(ctx context.Context, it Item) error {
			mu.Lock()
			if running[it.Key] {
				t.Errorf("runner %d: %s is already running", i, it.Key)
			}
			running[it.Key] = true
			mu.Unlock()
			defer func() {
				mu.Lock()
				running[it.Key] = false
				mu.Unlock()
			}()
			var n int
			if err := p.QueryRow(ctx, "SELECT n FROM version WHERE key = $1", it.Key).Scan(&n); err != nil {
				return err
			}
			time.Sleep(time.Duration(rand.IntN(5)) * time.Millisecond)
			mu.Lock()
			seen[it.Key] = max(seen[it.Key], n)
			mu.Unlock()
			return nil
		})
		start(t, r, logs)
	}

	final := map[string]int{}
	for range 300 {
		key := fmt.Sprintf("unit/%d", rand.IntN(5))
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO version VALUES ($1, 1)
				ON CONFLICT (key) DO UPDATE SET n = version.n + 1 RETURNING n`, key).Scan(new(int)); err != nil {
				return err
			}
			return Enqueue(ctx, tx, "evaluate", key, time.Time{})
		})
		if err != nil {
			t.Fatal(err)
		}
		final[key]++
		time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)
	}
	eventually(t, "the queue to drain", func() bool {
		var n int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM work_item").Scan(&n)
		return n == 0
	})
	mu.Lock()
	defer mu.Unlock()
	for key, n := range final {
		if seen[key] != n {
			t.Errorf("%s: the last run saw version %d of %d", key, seen[key], n)
		}
	}
}

// A handler that must wait returns RetryAt: the item waits until then, and its attempt is not counted.
func TestRetryAt(t *testing.T) {
	pool := storetest.MigratedPool(t)
	r, logs := newRunner(pool, Options{})
	attempts := make(chan int, 4)
	resets := time.Now().Add(time.Hour)
	r.Handle("refresh", func(_ context.Context, it Item) error {
		attempts <- it.Attempt
		return RetryAt(resets, errors.New("rate limited"))
	})
	start(t, r, logs)
	enqueue(t, pool, "refresh", "pr/1", time.Time{})
	<-attempts
	eventually(t, "the item to wait", func() bool {
		row, ok := read(t, pool, "refresh", "pr/1")
		return ok && row.state == "waiting"
	})
	if row, _ := read(t, pool, "refresh", "pr/1"); !near(row.runAfter, resets) || row.attempts != 0 {
		t.Fatalf("after RetryAt: %+v", row)
	}
}
