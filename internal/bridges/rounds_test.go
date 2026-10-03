package bridges

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	_ "github.com/NickWilde18/Park/internal/forges/github" // pull requests' addresses on github.com
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/work"
)

// watchRepos watches acme/api and acme/web, on github.com, and returns their IDs.
func (e *env) watchRepos(t *testing.T) (api, web int64) {
	t.Helper()
	var forge, inst int64
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO forge (kind, name, base_address) VALUES ('github', 'GitHub', 'https://github.com') RETURNING id").
		Scan(&forge))
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO installation (forge_id, name, credential_kind, credential) VALUES ($1, 'acme', 'token', '') RETURNING id",
		forge).Scan(&inst))
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name) VALUES ($1, 1, $2, 'acme', 'api') RETURNING id",
		forge, inst).Scan(&api))
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO repo (forge_id, forge_repo_id, installation_id, owner, name) VALUES ($1, 2, $2, 'acme', 'web') RETURNING id",
		forge, inst).Scan(&web))
	return api, web
}

// link is a pull request's address on github.com.
func link(repo string, n int) string {
	return "https://github.com/acme/" + repo + "/pull/" + strconv.Itoa(n)
}

// chatThread is a thread of the chats file, hours old, whose text is its title, with its links.
func chatThread(anchor string, hours int, title string, links ...string) map[string]any {
	return map[string]any{"anchor": anchor, "created": roundNow.Add(-time.Duration(hours) * time.Hour), "text": title, "plain": title,
		"links": links}
}

// chats writes the chats file with one chat, Reviews, of these threads.
func (e *env) chatsWith(t *testing.T, threads ...map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"chats": []map[string]any{{"id": "c-reviews", "name": "Reviews", "threads": threads}}})
	must(t, err)
	e.write(t, string(data))
}

// running makes a Park-run bridge ready to run: its plugin on, its credential set, its account connected, and the
// Reviews chat chosen.
func (e *env) running(t *testing.T) Bridge {
	t.Helper()
	b := e.ready(t)
	e.connect(t, e.ana, b.ID)
	b, err := e.s.Update(e.ctx, b.ID, Change{Chat: &Chat{ID: "c-reviews", Name: "Reviews"}})
	must(t, err)
	return b
}

// groupingAssertion is an assertion as a bridge posts it, under an anchor that is its own evidence.
func groupingAssertion(anchor, title string, prs ...string) grouping.Assertion {
	return grouping.Assertion{Anchor: anchor, Title: title, Evidence: []string{anchor}, PRs: refs(prs)}
}

// run runs a round of a bridge, as the work queue would.
func (e *env) run(t *testing.T, id int64) {
	t.Helper()
	must(t, e.s.runRound(e.ctx, work.Item{Kind: KindRound, Key: strconv.FormatInt(id, 10)}))
}

// queued says when a bridge's next round is queued for, or nil.
func (e *env) queued(t *testing.T, id int64) *time.Time {
	t.Helper()
	var at *time.Time
	must(t, e.pool.QueryRow(e.ctx, "SELECT (SELECT run_after FROM work_item WHERE kind = $1 AND key = $2)", KindRound,
		strconv.FormatInt(id, 10)).Scan(&at))
	return at
}

// clock is the database's time now: the work queue's now, which a round queued for now is queued for.
func (e *env) clock(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	must(t, e.pool.QueryRow(e.ctx, "SELECT now()").Scan(&now))
	return now
}

// live is a bridge's live assertions, by anchor, as owner/name#number... joined with their titles.
func (e *env) live(t *testing.T, id int64) map[string]string {
	t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT a.anchor, coalesce(a.title, ''), coalesce(a.token_id, 'null'),
		(SELECT string_agg(r.name || '#' || (m->>'number'), ' ' ORDER BY ord) FROM jsonb_array_elements(a.members) WITH ORDINALITY AS x(m, ord)
			JOIN repo r ON r.id = (m->>'repo')::bigint)
		FROM assertion a WHERE a.source = $1 AND a.retracted_at IS NULL`, "bridge:"+strconv.FormatInt(id, 10))
	must(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var anchor, title, token, members string
		must(t, rows.Scan(&anchor, &title, &token, &members))
		out[anchor] = fmt.Sprintf("%s: %s (token %s)", title, members, token)
	}
	must(t, rows.Err())
	return out
}

// A round reads the chat through the plugin, and posts what its threads say: a thread of two pull requests of watched
// repositories is an assertion, with its title, as no token; a thread of one is nothing. The round is recorded, what
// the bridge remembers is kept, and the next round is queued a period later. A second round over the same threads
// changes nothing; once a thread is gone from the chat, its assertion is retracted.
func TestARound(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t, chatThread("m1", 1, "Billing export", link("api", 12), link("web", 7)),
		chatThread("m2", 2, "One alone", link("api", 13)))
	b := e.running(t)
	_, err := e.pool.Exec(e.ctx, "DELETE FROM work_item")
	must(t, err)
	e.run(t, b.ID)
	if got := e.live(t, b.ID); len(got) != 1 || got["m1"] != "Billing export: api#12 web#7 (token null)" {
		t.Fatalf("live assertions %v", got)
	}
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if r := b.LastRound; r == nil || r.Outcome != "ok" || r.Read != 2 || r.Changes != 1 || r.Reason != "" {
		t.Fatalf("the round: %+v", b.LastRound)
	}
	if at := e.queued(t, b.ID); at == nil || !at.Equal(e.now.Add(10*time.Minute)) {
		t.Fatalf("the next round is queued for %v, want a period later", at)
	}
	var history int
	e.run(t, b.ID)
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM assertion WHERE source = $1", "bridge:"+strconv.FormatInt(b.ID, 10)).Scan(&history))
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if history != 1 || b.LastRound.Changes != 0 {
		t.Fatalf("a second round: %d assertions, %d changes", history, b.LastRound.Changes)
	}
	e.chatsWith(t, chatThread("m2", 2, "One alone", link("api", 13)))
	e.run(t, b.ID)
	if got := e.live(t, b.ID); len(got) != 0 {
		t.Fatalf("after the thread went: %v", got)
	}
}

// A thread that links four pull requests or more is asked about, with its text, of the bridge's model, through the
// chat completions protocol with the bridge's key, and what the model answers is proposed, not asserted.
func TestARoundAsksTheModel(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "{\"groups\": [{\"name\": \"Exports\", \"pull_requests\": ` +
			`[\"acme/api#1\", \"acme/web#3\"]}, {\"name\": \"Retries\", \"pull_requests\": [\"acme/api#2\", \"acme/web#4\"]}]}"}}]}`))
	}))
	defer srv.Close()
	e.chatsWith(t, chatThread("m1", 1, "Two features", link("api", 1), link("api", 2), link("web", 3), link("web", 4)))
	b := e.running(t)
	_, err := e.s.Update(e.ctx, b.ID, Change{Model: &ModelChange{URL: ptr(srv.URL), Name: ptr("a-model"), Key: ptr("the key")}})
	must(t, err)
	e.run(t, b.ID)
	if auth != "Bearer the key" {
		t.Fatalf("the model was asked with %q", auth)
	}
	var groups []string
	rows, err := e.pool.Query(e.ctx, `SELECT g.name FROM proposal p JOIN proposal_group g ON g.proposal_id = p.id
		WHERE p.source = $1 AND p.anchor = 'm1' AND p.retracted_at IS NULL ORDER BY g.position`, "bridge:"+strconv.FormatInt(b.ID, 10))
	must(t, err)
	for rows.Next() {
		var name string
		must(t, rows.Scan(&name))
		groups = append(groups, name)
	}
	rows.Close()
	if fmt.Sprint(groups) != "[Exports Retries]" || len(e.live(t, b.ID)) != 0 {
		t.Fatalf("proposed %v, asserted %v", groups, e.live(t, b.ID))
	}
}

// A round that fails is recorded with its reason. A lapsed sign-in waits for the account to be connected again, with
// no round queued; any other failure queues the next round as usual.
func TestFailedRounds(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t)
	b := e.running(t)
	clear := func() { _, err := e.pool.Exec(e.ctx, "DELETE FROM work_item"); must(t, err) }

	e.write(t, `{"error": {"code": "rate_limited", "message": "slow down"}}`)
	clear()
	e.run(t, b.ID)
	b, err := e.s.Get(e.ctx, b.ID)
	must(t, err)
	if r := b.LastRound; r == nil || r.Outcome != "failed" || r.Reason != "plugin_failed" || e.queued(t, b.ID) == nil {
		t.Fatalf("a failing plugin: %+v, next %v", b.LastRound, e.queued(t, b.ID))
	}

	e.write(t, `{"error": {"code": "signin_lapsed", "message": "expired"}}`)
	clear()
	e.run(t, b.ID)
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if r := b.LastRound; r == nil || r.Outcome != "failed" || r.Reason != "signin_lapsed" || e.queued(t, b.ID) != nil {
		t.Fatalf("a lapsed sign-in: %+v, next %v", b.LastRound, e.queued(t, b.ID))
	}
	wantCode(t, e.s.Sync(e.ctx, b.ID), "signin_lapsed")
}

// A bridge moved in from outside starts from what it posted: its first round in Park posts nothing that did not
// change, and remembers each thread by the thread's own time.
func TestAMovedBridgeStartsFromWhatItPosted(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	var id int64
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO bridge (name, owner_id) VALUES ('Reviews', $1) RETURNING id", e.ana).Scan(&id))
	_, err := e.s.groups.Assert(e.ctx, id, "t1", groupingAssertion("m1", "Billing export", "acme/api#12", "acme/web#7"))
	must(t, err)
	_, err = e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	_, err = e.s.Update(e.ctx, id, Change{Plugin: ptr("file"), Credentials: map[string]string{"file": e.chats}})
	must(t, err)
	e.connect(t, e.ana, id)
	_, err = e.s.Update(e.ctx, id, Change{Chat: &Chat{ID: "c-reviews", Name: "Reviews"}})
	must(t, err)
	e.chatsWith(t, chatThread("m1", 30, "Billing export", link("api", 12), link("web", 7)))
	e.run(t, id)
	var history int
	must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM assertion WHERE source = $1", "bridge:"+strconv.FormatInt(id, 10)).Scan(&history))
	b, err := e.s.Get(e.ctx, id)
	must(t, err)
	if history != 1 || b.LastRound.Changes != 0 {
		t.Fatalf("the first round in Park: %d assertions, %d changes", history, b.LastRound.Changes)
	}
	mem, err := e.s.memoryOf(e.ctx, id)
	must(t, err)
	if got := mem.Posted["m1"].Created; !mem.Seeded || !got.Equal(roundNow.Add(-30*time.Hour)) {
		t.Fatalf("remembers m1 from %v, seeded %v", got, mem.Seeded)
	}
}

// Syncing now needs a bridge that is ready: Park runs it, it is on, its account is connected, and its chat chosen;
// then a round is queued now, by the work queue's clock, which is the database's: between the database's time just
// before the sync and just after, rather than later by it or by the test's own clock. Starting queues each ready
// bridge's round a period after its last, or now.
func TestSyncAndStart(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t)
	b := e.ready(t)
	wantCode(t, e.s.Sync(e.ctx, b.ID), "no_account")
	e.connect(t, e.ana, b.ID)
	wantCode(t, e.s.Sync(e.ctx, b.ID), "no_chat")
	b, err := e.s.Update(e.ctx, b.ID, Change{Chat: &Chat{ID: "c-reviews", Name: "Reviews"}, Enabled: ptr(false)})
	must(t, err)
	wantCode(t, e.s.Sync(e.ctx, b.ID), "bridge_disabled")
	_, err = e.s.Update(e.ctx, b.ID, Change{Enabled: ptr(true)})
	must(t, err)
	_, err = e.pool.Exec(e.ctx, "DELETE FROM work_item")
	must(t, err)
	before := e.clock(t)
	must(t, e.s.Sync(e.ctx, b.ID))
	if at, after := e.queued(t, b.ID), e.clock(t); at == nil || at.Before(before) || at.After(after) {
		t.Fatalf("synced: the round is queued for %v, want the queue's now, from %v to %v", at, before, after)
	}

	e.run(t, b.ID)
	_, err = e.pool.Exec(e.ctx, "DELETE FROM work_item")
	must(t, err)
	must(t, e.s.Start(e.ctx))
	if at := e.queued(t, b.ID); at == nil || !at.Equal(e.now.Add(10*time.Minute)) {
		t.Fatalf("started: the round is queued for %v, want a period after the last", at)
	}
}

// A thread the bridge remembers posting, gone from the chat, whose assertion Park no longer has live, counts as
// retracted: the bridge forgets it, rather than asking to retract it again every round.
func TestRetractingWhatIsGoneAlready(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t, chatThread("m1", 1, "Billing export", link("api", 12), link("web", 7)))
	b := e.running(t)
	e.run(t, b.ID)
	mem, err := e.s.memoryOf(e.ctx, b.ID)
	must(t, err)
	mem.Posted["ghost"] = posted{Created: roundNow.Add(-time.Hour), Title: "Never live", PRs: []string{"acme/api#1", "acme/web#1"}}
	data, err := json.Marshal(mem)
	must(t, err)
	_, err = e.pool.Exec(e.ctx, "UPDATE bridge SET state = $2 WHERE id = $1", b.ID, data)
	must(t, err)
	e.run(t, b.ID)
	mem, err = e.s.memoryOf(e.ctx, b.ID)
	must(t, err)
	if _, ok := mem.Posted["ghost"]; ok || len(mem.Posted) != 1 {
		t.Fatalf("remembers %v", mem.Posted)
	}
}

// The work queue runs a bridge's rounds once the service registers them: syncing now has a round run, and recorded.
func TestRoundsRunOnTheQueue(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t, chatThread("m1", 1, "Billing export", link("api", 12), link("web", 7)))
	b := e.running(t)
	runner := work.NewRunner(e.pool, quiet(), work.Options{})
	e.s.Register(runner)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	must(t, e.s.Sync(e.ctx, b.ID))
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rounds int
		must(t, e.pool.QueryRow(e.ctx, "SELECT count(*) FROM bridge_round WHERE bridge_id = $1 AND outcome = 'ok'", b.ID).Scan(&rounds))
		if rounds > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no round ran within 10 seconds")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := e.live(t, b.ID); len(got) != 1 {
		t.Fatalf("live assertions %v", got)
	}
}

// The dashboard follows a bridge through its notices: a change of its settings, its account, its plugin's switch, and
// each round write one, naming the bridge, or the plugin by its kind; what the bridge remembers, and tokens a plugin
// renews, write none.
func TestBridgeNotices(t *testing.T) {
	e := newEnv(t)
	e.watchRepos(t)
	e.chatsWith(t)
	notices := func() []string {
		t.Helper()
		rows, err := e.pool.Query(e.ctx, "SELECT subject FROM event_notice WHERE kind = 'bridge' ORDER BY id")
		must(t, err)
		subjects, err := pgx.CollectRows(rows, pgx.RowTo[string])
		must(t, err)
		_, err = e.pool.Exec(e.ctx, "DELETE FROM event_notice")
		must(t, err)
		return subjects
	}
	b := e.ready(t)
	id := strconv.FormatInt(b.ID, 10)
	if got := notices(); fmt.Sprint(got) != fmt.Sprintf("[file %s %s]", id, id) {
		t.Fatalf("switching the plugin on, adding the bridge, and setting its credential: %v", got)
	}
	e.connect(t, e.ana, b.ID)
	if got := notices(); fmt.Sprint(got) != "["+id+"]" {
		t.Fatalf("connecting its account: %v", got)
	}
	_, err := e.s.Chats(e.ctx, b.ID)
	must(t, err)
	if got := notices(); len(got) != 0 {
		t.Fatalf("listing chats, which renews the tokens: %v", got)
	}
	_, err = e.s.Update(e.ctx, b.ID, Change{Chat: &Chat{ID: "c-reviews", Name: "Reviews"}})
	must(t, err)
	notices()
	e.run(t, b.ID)
	if got := notices(); fmt.Sprint(got) != "["+id+"]" {
		t.Fatalf("a round, which also keeps what the bridge remembers: %v", got)
	}
}
