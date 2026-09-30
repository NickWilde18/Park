// Package storetest gives each test a Postgres database of its own.
//
// The server is named by PARK_TEST_DATABASE_URL, a postgres:// URL for a role that may create databases. Without
// it, tests that need a database are skipped — except in CI, where they fail, so that they cannot be skipped
// unnoticed.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/store"
)

// Database creates an empty database, drops it when the test ends, and returns its URL.
func Database(t testing.TB) string {
	t.Helper()
	base := os.Getenv("PARK_TEST_DATABASE_URL")
	if base == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("PARK_TEST_DATABASE_URL is not set")
		}
		t.Skip("PARK_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to the test server: %v", err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "park_test_" + hex.EncodeToString(suffix)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create the test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the test database: %v", err)
		}
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("PARK_TEST_DATABASE_URL is not a URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// Pool opens a connection pool to a new database from Database, closed when the test ends.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), Database(t))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// MigratedDatabase is Database with Park's schema applied.
func MigratedDatabase(t testing.TB) string {
	t.Helper()
	url := Database(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	defer pool.Close()
	migrations, err := store.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, pool, migrations); err != nil {
		t.Fatalf("migrate the test database: %v", err)
	}
	return url
}

// MigratedPool opens a connection pool to a new database from MigratedDatabase, closed when the test ends.
func MigratedPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), MigratedDatabase(t))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
