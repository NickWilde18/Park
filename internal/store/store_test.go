package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/store"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

// migrations builds a migration set from file names and their SQL.
func migrations(t *testing.T, files map[string]string) []store.Migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, sql := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(sql)}
	}
	ms, err := store.LoadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// versions reads the versions recorded in schema_migrations, in order.
func versions(t *testing.T, pool *pgxpool.Pool) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

// Migrations load in version order — numerically, not by name — and files that are not .sql are ignored.
func TestLoadMigrations(t *testing.T) {
	ms := migrations(t, map[string]string{
		"0010_later.sql": "SELECT 1", "0002_earlier.sql": "SELECT 2", "README.md": "not a migration",
	})
	got := []int64{}
	for _, m := range ms {
		got = append(got, m.Version)
	}
	if !slices.Equal(got, []int64{2, 10}) || ms[0].Name != "earlier" || ms[0].SQL != "SELECT 2" {
		t.Fatalf("got %+v", ms)
	}
}

// Two migrations with the same version — as when two pull requests pick the same number — are an error.
func TestLoadMigrationsRejectsDuplicateVersions(t *testing.T) {
	fsys := fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1")}, "1_b.sql": {Data: []byte("SELECT 1")}}
	_, err := store.LoadMigrations(fsys)
	if err == nil || !strings.Contains(err.Error(), "have the same version") {
		t.Fatalf("got %v", err)
	}
}

// A .sql file that is not named NNNN_name.sql is an error, not a migration silently skipped.
func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	for _, name := range []string{"0003-add-x.sql", "0003_Add.sql", "add_x.sql"} {
		_, err := store.LoadMigrations(fstest.MapFS{name: {Data: []byte("SELECT 1")}})
		if err == nil || !strings.Contains(err.Error(), "is not named NNNN_name.sql") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// The migrations built into the binary load: their names are valid and their versions unique.
func TestEmbeddedMigrations(t *testing.T) {
	if _, err := store.Migrations(); err != nil {
		t.Fatal(err)
	}
}

// Migrate applies each migration once: a second run applies nothing. A migration may hold several statements.
func TestMigrateAppliesEachOnce(t *testing.T) {
	pool := storetest.Pool(t)
	ms := migrations(t, map[string]string{
		"0001_one.sql": "CREATE TABLE one (id int)",
		"0002_two.sql": "CREATE TABLE two (id int); INSERT INTO two VALUES (1), (2)",
	})
	ctx := context.Background()
	if n, err := store.Migrate(ctx, pool, ms); err != nil || n != 2 {
		t.Fatalf("first run: applied %d, %v", n, err)
	}
	if n, err := store.Migrate(ctx, pool, ms); err != nil || n != 0 {
		t.Fatalf("second run: applied %d, %v", n, err)
	}
	if got := versions(t, pool); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("recorded %v", got)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM two").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("two has %d rows, %v", rows, err)
	}
}

// Processes that start at once apply every migration exactly once between them. Each pool is a separate set of
// sessions, like a separate process; the migrations fail if applied twice.
func TestMigrateConcurrently(t *testing.T) {
	url := storetest.Database(t)
	ms := migrations(t, map[string]string{
		"0001_counter.sql": "CREATE TABLE counter (n int)",
		"0002_count.sql":   "INSERT INTO counter VALUES (1)",
	})
	ctx := context.Background()
	const processes = 8
	var wg sync.WaitGroup
	applied := make([]int, processes)
	errs := make([]error, processes)
	for i := range processes {
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		wg.Go(func() { applied[i], errs[i] = store.Migrate(ctx, pool, ms) })
	}
	wg.Wait()
	total := 0
	for i := range processes {
		if errs[i] != nil {
			t.Errorf("process %d: %v", i, errs[i])
		}
		total += applied[i]
	}
	if total != 2 {
		t.Fatalf("applied %d migrations in all, want 2", total)
	}
	check, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var rows int
	if err := check.QueryRow(ctx, "SELECT count(*) FROM counter").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("counter has %d rows, %v", rows, err)
	}
}

// A failing migration stops the run: the ones before it stay applied, it leaves nothing behind, and the ones after
// it are not applied. The error names it.
func TestMigrateStopsAtFailure(t *testing.T) {
	pool := storetest.Pool(t)
	ms := migrations(t, map[string]string{
		"0001_good.sql":   "CREATE TABLE good (id int)",
		"0002_broken.sql": "CREATE TABLE half (id int); SELECT no_such_column FROM good",
		"0003_after.sql":  "CREATE TABLE after (id int)",
	})
	ctx := context.Background()
	n, err := store.Migrate(ctx, pool, ms)
	if err == nil || !strings.Contains(err.Error(), "0002_broken") || n != 1 {
		t.Fatalf("applied %d, %v", n, err)
	}
	if got := versions(t, pool); !slices.Equal(got, []int64{1}) {
		t.Fatalf("recorded %v", got)
	}
	for _, table := range []string{"half", "after"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || exists {
			t.Errorf("table %s exists: %v, %v", table, exists, err)
		}
	}
}

// A migration and its record commit together: when recording fails — here because the migration drops the record
// table — the migration's own changes are rolled back too, and the record table survives.
func TestMigrateRecordsAtomically(t *testing.T) {
	pool := storetest.Pool(t)
	ms := migrations(t, map[string]string{
		"0001_good.sql":  "CREATE TABLE good (id int)",
		"0002_drops.sql": "CREATE TABLE made (id int); DROP TABLE schema_migrations",
	})
	ctx := context.Background()
	if _, err := store.Migrate(ctx, pool, ms); err == nil || !strings.Contains(err.Error(), "record migration 0002_drops") {
		t.Fatalf("got %v", err)
	}
	var made bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('made') IS NOT NULL").Scan(&made); err != nil || made {
		t.Fatalf("table made exists: %v, %v", made, err)
	}
	if got := versions(t, pool); !slices.Equal(got, []int64{1}) {
		t.Fatalf("recorded %v", got)
	}
}

// A database with a migration this binary does not know was migrated by a newer Park: Migrate and Pending refuse
// it, and Migrate applies nothing.
func TestRefuseNewerDatabase(t *testing.T) {
	pool := storetest.Pool(t)
	newer := migrations(t, map[string]string{"0001_a.sql": "CREATE TABLE a (id int)", "0002_b.sql": "CREATE TABLE b (id int)"})
	older := newer[:1]
	ctx := context.Background()
	if _, err := store.Migrate(ctx, pool, newer); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Migrate(ctx, pool, older); !errors.Is(err, store.ErrNewerSchema) || n != 0 {
		t.Fatalf("Migrate: applied %d, %v", n, err)
	}
	if _, err := store.Pending(ctx, pool, older); !errors.Is(err, store.ErrNewerSchema) {
		t.Fatalf("Pending: %v", err)
	}
}

// Pending lists what Migrate would apply: everything on an empty database, nothing after it, and only what is new
// when the binary brings another migration.
func TestPending(t *testing.T) {
	pool := storetest.Pool(t)
	ms := migrations(t, map[string]string{"0001_a.sql": "CREATE TABLE a (id int)", "0002_b.sql": "CREATE TABLE b (id int)"})
	ctx := context.Background()
	pending, err := store.Pending(ctx, pool, ms[:1])
	if err != nil || len(pending) != 1 {
		t.Fatalf("empty database: %v, %v", pending, err)
	}
	if _, err := store.Migrate(ctx, pool, ms[:1]); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(ctx, pool, ms[:1]); err != nil || len(pending) != 0 {
		t.Fatalf("after migrating: %v, %v", pending, err)
	}
	if pending, err := store.Pending(ctx, pool, ms); err != nil || len(pending) != 1 || pending[0].Version != 2 {
		t.Fatalf("with a new migration: %v, %v", pending, err)
	}
}

// Once Migrate returns — after success or failure — no session holds an advisory lock any more.
func TestMigrateReleasesLock(t *testing.T) {
	url := storetest.Database(t)
	ctx := context.Background()
	for _, files := range []map[string]string{
		{"0001_a.sql": "CREATE TABLE a (id int)"},
		{"0001_a.sql": "CREATE TABLE a (id int)", "0002_bad.sql": "SELECT no_such_column FROM a"},
	} {
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = store.Migrate(ctx, pool, migrations(t, files))
		var held int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'").Scan(&held); err != nil || held != 0 {
			t.Fatalf("%d advisory locks held, %v", held, err)
		}
		pool.Close()
	}
}
