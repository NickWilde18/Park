package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/store/storetest"
)

const key = "0123456789abcdef0123456789abcdef"

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// park version prints the version.
func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, env(nil), &out, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "dev") {
		t.Fatalf("got %q", out.String())
	}
}

// Without a command, or with an unknown one, park explains its usage.
func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"serv"}} {
		err := run(context.Background(), args, env(nil), io.Discard, io.Discard, nil)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%v: got %v", args, err)
		}
	}
}

// A missing required key stops serve and migrate before they touch the database.
func TestMissingConfiguration(t *testing.T) {
	for _, cmd := range []string{"serve", "migrate"} {
		err := run(context.Background(), []string{cmd}, env(nil), io.Discard, io.Discard, nil)
		if err == nil || !strings.Contains(err.Error(), "database.url is required") {
			t.Errorf("%s: got %v", cmd, err)
		}
	}
}

// park migrate prepares an empty database and exits.
func TestMigrate(t *testing.T) {
	url := storetest.Database(t)
	vars := env(map[string]string{"PARK_DATABASE_URL": url, "PARK_SECRET_KEY": key})
	if err := run(context.Background(), []string{"migrate"}, vars, io.Discard, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatalf("schema_migrations: %v, %v", exists, err)
	}
}

// park serve migrates an empty database, answers on its address — ready, with the API — and stops cleanly when
// its context ends.
func TestServe(t *testing.T) {
	vars := env(map[string]string{
		"PARK_DATABASE_URL": storetest.Database(t), "PARK_SECRET_KEY": key, "PARK_HTTP_ADDR": "127.0.0.1:0",
	})
	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan string, 1)
	done := make(chan error, 1)
	var logs bytes.Buffer
	go func() { done <- run(ctx, []string{"serve"}, vars, io.Discard, &logs, listening) }()
	var addr string
	select {
	case addr = <-listening:
	case err := <-done:
		t.Fatalf("serve stopped: %v\n%s", err, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not start listening")
	}

	get := func(path string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	if code, body := get("/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz: %d %s", code, body)
	}
	code, body := get("/api/v1/auth/providers")
	var providers struct{ Providers []any }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &providers) != nil || providers.Providers == nil {
		t.Fatalf("/api/v1/auth/providers: %d %s", code, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}

// park serve stops at once, and cleanly, while a dashboard's event stream is open: shutting down ends the stream,
// which would otherwise be waited for until the shutdown's deadline.
func TestServeEndsStreams(t *testing.T) {
	vars := env(map[string]string{
		"PARK_DATABASE_URL": storetest.Database(t), "PARK_SECRET_KEY": key, "PARK_HTTP_ADDR": "127.0.0.1:0",
		"PARK_SETUP_CODE": "AAAA-BBBB-CCCC",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}, vars, io.Discard, io.Discard, listening) }()
	var addr string
	select {
	case addr = <-listening:
	case err := <-done:
		t.Fatalf("serve stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not start listening")
	}

	// Setting Park up signs its first admin in, with a session cookie.
	setup := `{"code": "AAAA-BBBB-CCCC", "username": "admin", "password": "a long enough password", "display_name": "Admin"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/api/v1/setup", strings.NewReader(setup))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "park_session" {
			session = c
		}
	}
	if resp.StatusCode != http.StatusCreated || session == nil {
		t.Fatalf("setup: %d, session %v", resp.StatusCode, session)
	}
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(session)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", stream.StatusCode)
	}

	stopping := time.Now()
	cancel()
	select {
	case err := <-done:
		took := time.Since(stopping).Round(time.Millisecond)
		if err != nil || took > 3*time.Second {
			t.Fatalf("serve returned %v after %s", err, took)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
