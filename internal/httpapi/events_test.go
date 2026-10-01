package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/events"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

// sse is one event read from a stream.
type sse struct{ id, event, data string }

// bearer is a request's API token; session, its browser session.
func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func session(c *http.Cookie) func(*http.Request) { return func(r *http.Request) { r.AddCookie(c) } }

// stream opens an event stream with a credential and a Last-Event-ID, and delivers its events until it is stopped,
// or the test ends.
func stream(t *testing.T, url string, credential func(*http.Request), last string) (<-chan sse, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", url+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	credential(req)
	if last != "" {
		req.Header.Set("Last-Event-ID", last)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		_ = resp.Body.Close()
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sse, 16)
	go read(resp.Body, out)
	return out, cancel
}

// read delivers the events of a stream's body, until the body ends.
func read(body io.ReadCloser, out chan<- sse) {
	defer close(out)
	defer func() { _ = body.Close() }()
	lines := bufio.NewScanner(body)
	var e sse
	for lines.Scan() {
		line := lines.Text()
		switch {
		case line == "":
			if e.event != "" {
				out <- e
			}
			e = sse{}
		case strings.HasPrefix(line, "id: "):
			e.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			e.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			e.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func next(t *testing.T, events <-chan sse) sse {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("the stream ended")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event in five seconds")
	}
	return sse{}
}

// ended waits for a stream to end, and fails if it sends anything first.
func ended(t *testing.T, events <-chan sse) {
	t.Helper()
	select {
	case e, ok := <-events:
		if ok {
			t.Fatalf("the stream sent %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end in five seconds")
	}
}

func notice(t *testing.T, pool *pgxpool.Pool, kind, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "INSERT INTO event_notice (kind, subject) VALUES ($1, $2)", kind, subject); err != nil {
		t.Fatal(err)
	}
}

// eventServer serves the API over HTTP with live updates, and gives a signed-in admin's browser, and a read token of
// theirs.
func eventServer(t *testing.T) (*pgxpool.Pool, string, NewToken, *browser) {
	t.Helper()
	pool := storetest.MigratedPool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Events: events.New(pool, log), Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: &syncBuffer{}}
	admin := a.admin(t)
	var token NewToken
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return pool, srv.URL, token, admin
}

// A stream sends the notices after its Last-Event-ID, then each one after as it is written. A fresh stream begins
// with what changes next. A stream further behind than notices are kept is told to reload. Without a caller, there
// is no stream.
func TestEventStream(t *testing.T) {
	pool, url, token, _ := eventServer(t)

	notice(t, pool, "verdict", "pr:1")
	notice(t, pool, "task", "7")
	notice(t, pool, "group", "3")
	resumed, stop := stream(t, url, bearer(token.Token), "1")
	for _, want := range []sse{{"2", "task", `{"subject":"7"}`}, {"3", "group", `{"subject":"3"}`}} {
		if got := next(t, resumed); got != want {
			t.Fatalf("resumed: %+v, want %+v", got, want)
		}
	}
	notice(t, pool, "pr", "12")
	if got := next(t, resumed); got != (sse{"4", "pr", `{"subject":"12"}`}) {
		t.Fatalf("live: %+v", got)
	}
	stop()

	fresh, stop := stream(t, url, bearer(token.Token), "")
	notice(t, pool, "verdict", "group:3")
	if got := next(t, fresh); got.id != "5" {
		t.Fatalf("a fresh stream began with %+v", got)
	}
	stop()

	if _, err := pool.Exec(context.Background(), "DELETE FROM event_notice WHERE id < 5"); err != nil {
		t.Fatal(err)
	}
	behind, stop := stream(t, url, bearer(token.Token), "2")
	if got := next(t, behind); got.event != "reload" {
		t.Fatalf("a stream too far behind: %+v", got)
	}
	stop()
	req, err := http.NewRequestWithContext(context.Background(), "GET", url+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a caller: %d", resp.StatusCode)
	}
}

// A stream misses no change, whatever order changes are made and committed in: a change made before another and
// committed after it is streamed once it commits, and so is one whose notices are written as another commits, or as
// the stream opens.
func TestEventStreamInCommitOrder(t *testing.T) {
	pool, url, token, _ := eventServer(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	group := func(name string) string {
		t.Helper()
		var id int64
		must(pool.QueryRow(ctx, "INSERT INTO pr_group (name, name_source) VALUES ($1, 'manual') RETURNING id", name).Scan(&id))
		return itoa(id)
	}
	rename := func(db interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}, group string) error {
		_, err := db.Exec(ctx, "UPDATE pr_group SET name = name || ' too' WHERE id = $1", group)
		return err
	}
	expect := func(events <-chan sse, group string) {
		t.Helper()
		if got := next(t, events); got.event != "group" || got.data != `{"subject":"`+group+`"}` {
			t.Fatalf("got %+v, want group %s", got, group)
		}
	}
	first, second, third, fourth := group("Billing"), group("Invoices"), group("Payments"), group("Refunds")
	events, stop := stream(t, url, bearer(token.Token), "")
	defer stop()

	early, err := pool.Begin(ctx)
	must(err)
	defer func() { _ = early.Rollback(ctx) }()
	must(rename(early, first))
	must(rename(pool, second))
	expect(events, second)
	must(early.Commit(ctx))
	expect(events, first)

	// SET CONSTRAINTS ALL IMMEDIATE writes a transaction's notices at once, as committing it would.
	committing, err := pool.Begin(ctx)
	must(err)
	defer func() { _ = committing.Rollback(ctx) }()
	must(rename(committing, third))
	_, err = committing.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	must(err)
	done := make(chan error, 1)
	go func() { done <- rename(pool, fourth) }()
	time.Sleep(200 * time.Millisecond) // time for the other change to commit, were it not made to wait
	select {
	case err := <-done:
		t.Fatalf("a change committed while notices numbered before its own were not yet committed (%v)", err)
	default:
	}
	must(committing.Commit(ctx))
	must(<-done)
	expect(events, third)
	expect(events, fourth)

	// A fresh stream begins after the last notice committed, not after the last one numbered.
	late, err := pool.Begin(ctx)
	must(err)
	defer func() { _ = late.Rollback(ctx) }()
	must(rename(late, first))
	_, err = late.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	must(err)
	fresh, stopFresh := stream(t, url, bearer(token.Token), "")
	defer stopFresh()
	must(late.Commit(ctx))
	expect(fresh, first)
}

// A stream ends, sending nothing more, once the credential it was opened with stops working: the token revoked, or
// the session signed out. Until then it goes on.
func TestEventStreamEndsWithItsCredential(t *testing.T) {
	pool, url, token, admin := eventServer(t)
	byToken, stop := stream(t, url, bearer(token.Token), "")
	defer stop()
	bySession, stop := stream(t, url, session(admin.cookies[sessionCookie]), "")
	defer stop()
	notice(t, pool, "group", "1")
	for _, s := range []<-chan sse{byToken, bySession} {
		if got := next(t, s); got.data != `{"subject":"1"}` {
			t.Fatalf("before: %+v", got)
		}
	}

	admin.call(t, "DELETE", "/api/v1/me/tokens/"+token.Record.Id, nil, nil)
	notice(t, pool, "group", "2")
	ended(t, byToken)
	if got := next(t, bySession); got.data != `{"subject":"2"}` {
		t.Fatalf("the session's stream, with the token revoked: %+v", got)
	}
	admin.call(t, "POST", "/api/v1/auth/logout", nil, nil)
	notice(t, pool, "group", "3")
	ended(t, bySession)
}

// Changes write their own notices: a new verdict, and one retired; a task's state and priority, its items, and its
// events; a group's members and name; a pull request's facts; and a proposal opened, retitled, decided, and
// retracted. A refresh that changed nothing, a heartbeat that only extends a lease, and a proposal's new evidence
// write none.
func TestNoticesFromChanges(t *testing.T) {
	pool := storetest.MigratedPool(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var forge, inst, repo, pr, member, group, task int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id").Scan(&forge))
	must(pool.QueryRow(ctx, "INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id", forge).Scan(&inst))
	must(pool.QueryRow(ctx, "INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name) VALUES ($1, 1, $2, 'acme', 'api') RETURNING id", forge, inst).Scan(&repo))
	must(pool.QueryRow(ctx, `INSERT INTO pr_fact (repo_id, number, state, open, head_sha, head_branch, base_branch, forge_updated_at, fetched_at,
		refreshed_at) VALUES ($1, 12, '{}', true, 'h', 'x', 'main', now(), now(), now()) RETURNING id`, repo).Scan(&pr))
	exec("UPDATE pr_fact SET fetched_at = now(), stale = false WHERE id = $1", pr)
	exec("UPDATE pr_fact SET seq = seq + 1 WHERE id = $1", pr)
	exec("UPDATE pr_fact SET stale = true WHERE id = $1", pr)
	must(pool.QueryRow(ctx, "INSERT INTO member (display_name) VALUES ('Ana') RETURNING id").Scan(&member))
	must(pool.QueryRow(ctx, "INSERT INTO task (member_id, unit) VALUES ($1, 'pr:1') RETURNING id", member).Scan(&task))
	exec("UPDATE task SET lease_expires_at = now() WHERE id = $1", task)
	var item int64
	must(pool.QueryRow(ctx, "INSERT INTO task_item (task_id, pr_id, action, targets) VALUES ($1, $2, 'fix_checks', '{}') RETURNING id", task, pr).Scan(&item))
	exec("UPDATE task_item SET state = 'satisfied' WHERE id = $1", item)
	exec("UPDATE task SET priority = 'low' WHERE id = $1", task)
	exec("INSERT INTO task_event (task_id, kind, actor, state) VALUES ($1, 'item_added', 'park', 'queued')", task)
	exec("UPDATE task SET state = 'dismissed' WHERE id = $1", task)
	must(pool.QueryRow(ctx, "INSERT INTO pr_group (name, name_source) VALUES ('Billing', 'manual') RETURNING id").Scan(&group))
	var assertion int64
	must(pool.QueryRow(ctx, `INSERT INTO assertion (source, authority, pr_id, group_id, negative, actor_member, seen_at) VALUES ('manual', 3, $1, $2, false, $3, now())
		RETURNING id`, pr, group, member).Scan(&assertion))
	exec("INSERT INTO membership (pr_id, group_id, assertion_id) VALUES ($1, $2, $3)", pr, group, assertion)
	exec("UPDATE pr_group SET name = 'Invoices' WHERE id = $1", group)
	exec("INSERT INTO rule_version (version, rules) VALUES (1, '{}')")
	exec(`INSERT INTO verdict (unit, side, holders, parts, reminders, inputs, seqs, rule_version, evaluated_at)
		VALUES ('group:1', 'author', '[]', '[]', '[]', '[]', '{}', 1, now())`)
	exec("UPDATE verdict SET current = false WHERE unit = 'group:1'")
	var proposal int64
	must(pool.QueryRow(ctx, "INSERT INTO proposal (source, anchor, fingerprint) VALUES ('bridge:1', 't', 'f') RETURNING id").Scan(&proposal))
	exec("UPDATE proposal SET evidence = '{m-1}' WHERE id = $1", proposal)
	exec("UPDATE proposal SET title = 'Two features' WHERE id = $1", proposal)
	exec("UPDATE proposal SET state = 'dismissed' WHERE id = $1", proposal)
	exec("UPDATE proposal SET retracted_at = now() WHERE id = $1", proposal)
	rows, err := pool.Query(ctx, "SELECT kind || ' ' || subject FROM event_notice ORDER BY id")
	must(err)
	var got []string
	for rows.Next() {
		var s string
		must(rows.Scan(&s))
		got = append(got, s)
	}
	pr1, task1, group1 := itoa(pr), itoa(task), itoa(group)
	want := []string{"pr " + pr1, "pr " + pr1, "pr " + pr1}
	for range 6 { // made; an item added, then satisfied; its priority; an event; dismissed
		want = append(want, "task "+task1)
	}
	want = append(want, "group "+group1, "group "+group1, "verdict group:1", "verdict group:1")
	for range 4 { // opened; retitled; dismissed; retracted
		want = append(want, "proposal "+itoa(proposal))
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("notices %v, want %v", got, want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
