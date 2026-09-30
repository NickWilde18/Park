// Package store opens Park's Postgres database and keeps its schema current.
//
// The schema is built by forward-only migrations embedded in the binary. Migrate applies the pending ones, each in
// its own transaction, while holding a Postgres advisory lock, so that several processes starting at once apply
// every migration exactly once. A database that already has a migration this binary does not know was migrated by
// a newer Park; Migrate and Pending refuse it rather than run old code against a newer schema.
package store

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLock is the advisory lock held while migrating: the bytes of "park".
const migrationLock int64 = 0x7061726b

//go:embed migrations
var embedded embed.FS

// Migration is one step of the schema.
type Migration struct {
	Name    string
	SQL     string
	Version int64
}

var migrationName = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// Open connects to the database at url and checks that it answers.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("configure the database connection: %w", err)
	}
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	return pool, nil
}

// Migrations returns the migrations built into this binary, in version order.
func Migrations() ([]Migration, error) {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, err
	}
	return LoadMigrations(sub)
}

// LoadMigrations reads the migrations at the top of fsys: files named NNNN_name.sql, where NNNN is the version and
// the name is lower case. A .sql file named otherwise, or two files with the same version, are errors, so that no
// migration is skipped by accident; files that are not .sql are ignored.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var out []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration %s is not named NNNN_name.sql", e.Name())
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s have the same version", other, e.Name())
		}
		seen[version] = e.Name()
		sql, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: version, Name: m[2], SQL: string(sql)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out, nil
}

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    bigint      PRIMARY KEY,
	name       text        NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// Migrate applies the migrations the database does not have yet, in order, and returns how many it applied.
// It waits for any other process that is migrating the same database. On an error, the migrations before the
// failing one stay applied, and the failing one leaves no trace.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) (applied int, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire a connection: %w", err)
	}
	// The lock belongs to the session: a connection that may still hold it must not go back to the pool.
	discard := func() { _ = conn.Hijack().Close(context.WithoutCancel(ctx)) }
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		discard()
		return 0, fmt.Errorf("take the migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock); err != nil {
			discard()
			return
		}
		conn.Release()
	}()

	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}
	pending, err := pendingOn(ctx, conn.Conn(), migrations)
	if err != nil {
		return 0, err
	}
	for _, m := range pending {
		if err := apply(ctx, conn.Conn(), m); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func apply(ctx context.Context, conn *pgx.Conn, m Migration) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
			return fmt.Errorf("record migration %04d_%s: %w", m.Version, m.Name, err)
		}
		return nil
	})
}

// Pending returns the migrations the database does not have yet.
func Pending(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) ([]Migration, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a connection: %w", err)
	}
	defer conn.Release()
	return pendingOn(ctx, conn.Conn(), migrations)
}

// ErrNewerSchema reports a database migrated by a newer version of Park.
var ErrNewerSchema = errors.New("the database was migrated by a newer version of Park")

func pendingOn(ctx context.Context, conn *pgx.Conn, migrations []Migration) ([]Migration, error) {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("look for schema_migrations: %w", err)
	}
	if !exists {
		return migrations, nil
	}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	known := map[int64]bool{}
	for _, m := range migrations {
		known[m.Version] = true
	}
	done := map[int64]bool{}
	for _, v := range applied {
		if !known[v] {
			return nil, fmt.Errorf("%w: it has migration %d, which this binary does not know", ErrNewerSchema, v)
		}
		done[v] = true
	}
	var pending []Migration
	for _, m := range migrations {
		if !done[m.Version] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}
