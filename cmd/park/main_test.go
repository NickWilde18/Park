package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

// park serve resolves the groups once as it starts, so that what a release changes in what resolution derives
// applies without waiting for a change resolution reads (#53). An earlier Park left an open suggestion naming an
// archived group, which resolution now drops; nothing changes after serve starts — its pull request is in a
// repository Park does not watch, so nothing is polled either — and the suggestion is gone within seconds.
func TestServeResolvesGroups(t *testing.T) {
	url := storetest.Database(t)
	vars := env(map[string]string{"PARK_DATABASE_URL": url, "PARK_SECRET_KEY": key, "PARK_HTTP_ADDR": "127.0.0.1:0"})
	if err := run(context.Background(), []string{"migrate"}, vars, io.Discard, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var forge, inst, repo, pr, group, suggestion int64
	for _, step := range []struct {
		sql  string
		args []any
		into *int64
	}{
		{"INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id", nil, &forge},
		{"INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id",
			[]any{&forge}, &inst},
		{`INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name, default_branch, watched)
			VALUES ($1, 101, $2, 'acme', 'api', 'main', false) RETURNING id`, []any{&forge, &inst}, &repo},
		{`INSERT INTO pr_fact (repo_id, number, state, open, head_sha, head_branch, head_repo_id, base_branch, forge_updated_at,
			fetched_at, refreshed_at) VALUES ($1, 1, '{}', true, '', 'feat/a', 101, 'main', now(), now(), now()) RETURNING id`,
			[]any{&repo}, &pr},
		{"INSERT INTO pr_group (name, name_source, state, archived_at) VALUES ('Gone', 'branch', 'archived', now()) RETURNING id", nil, &group},
		{"INSERT INTO suggestion (pr_id, group_id, reason, source, key) VALUES ($1, $2, 'left behind', 'structural', 'left') RETURNING id",
			[]any{&pr, &group}, &suggestion},
	} {
		args := make([]any, len(step.args))
		for i, a := range step.args {
			args[i] = *a.(*int64)
		}
		if err := pool.QueryRow(context.Background(), step.sql, args...).Scan(step.into); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan string, 1)
	done := make(chan error, 1)
	log := &logs{}
	go func() { done <- run(ctx, []string{"serve"}, vars, io.Discard, log, listening) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop")
		}
	}()
	select {
	case <-listening:
	case err := <-done:
		t.Fatalf("serve stopped: %v\n%s", err, log.String())
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not start listening")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var left int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM suggestion WHERE id = $1 AND state = 'open'",
			suggestion).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s after serve started, the suggestion naming the archived group %d is still open; log:\n%s", group, log.String())
		}
	}
}

// logs collects what serve logs, and can be read while serve still runs.
type logs struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// park serve stops cleanly while a dashboard's event stream is open: shutting down ends the stream at once, which
// would otherwise be waited for until the shutdown's deadline, and serve then returns without an error. The two are
// timed apart, and a failure shows serve's log, so that a slow shutdown says which part was slow.
func TestServeEndsStreams(t *testing.T) {
	vars := env(map[string]string{
		"PARK_DATABASE_URL": storetest.Database(t), "PARK_SECRET_KEY": key, "PARK_HTTP_ADDR": "127.0.0.1:0",
		"PARK_SETUP_CODE": "AAAA-BBBB-CCCC",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	done := make(chan error, 1)
	log := &logs{}
	go func() { done <- run(ctx, []string{"serve"}, vars, io.Discard, log, listening) }()
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
	ended := make(chan time.Duration, 1)
	go func() {
		_, _ = io.Copy(io.Discard, stream.Body)
		ended <- time.Since(stopping)
	}()
	cancel()
	select {
	case took := <-ended:
		if took > 3*time.Second {
			t.Fatalf("the stream ended %s after the shutdown began\n%s", took.Round(time.Millisecond), log)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the stream did not end\n%s", log)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, %s after the shutdown began\n%s", err, time.Since(stopping).Round(time.Millisecond), log)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not stop\n%s", log)
	}
}
