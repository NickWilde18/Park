package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/work"
)

func init() {
	forges.Register("fake", forges.Kind{PullURL: func(base, owner, name string, n int) string {
		return base + "/" + owner + "/" + name + "/pulls/" + strconv.Itoa(n)
	}})
	Register("fake", func(forges.Forge, Credential) (Collector, error) { return nil, errors.New("tests set newCollector") })
}

// fakeForge is the forge as a collector sees it, held in memory.
type fakeForge struct {
	prs        map[int]Fetched
	fetchErr   map[int]error
	light      map[int]Light
	protection map[string]Protection
	access     map[string]string
	accessErr  error
	teams      map[string][]int64
	missed     []Delivery
	repo       RepoFacts
	fetches    int
	mu         sync.Mutex
}

func (f *fakeForge) FetchPR(_ context.Context, _ Repo, number int, _ map[string]Protection) (Fetched, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if err := f.fetchErr[number]; err != nil {
		return Fetched{}, err
	}
	p, ok := f.prs[number]
	if !ok {
		return Fetched{}, ErrNotFound
	}
	return p, nil
}

func (f *fakeForge) ListOpen(context.Context, Repo) ([]Listed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Listed
	for n, p := range f.prs {
		if p.PR.Open() {
			out = append(out, Listed{Number: n, UpdatedAt: p.PR.UpdatedAt})
		}
	}
	return out, nil
}

func (f *fakeForge) Light(_ context.Context, _ Repo, n int) (Light, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.light[n], nil
}

func (f *fakeForge) Repository(context.Context, string, string) (RepoFacts, error) {
	return f.repo, nil
}

func (f *fakeForge) Protection(context.Context, Repo, []string) (map[string]Protection, error) {
	return f.protection, nil
}

func (f *fakeForge) Access(_ context.Context, _ Repo, login string) (string, error) {
	if f.accessErr != nil {
		return "", f.accessErr
	}
	return f.access[login], nil
}

func (f *fakeForge) Team(_ context.Context, org, slug string) ([]int64, error) {
	return f.teams[org+"/"+slug], nil
}

// Delivery takes the secret in X-Test-Secret, and the delivery as its JSON body.
func (f *fakeForge) Delivery(h http.Header, body, secret []byte) (Delivery, error) {
	if len(secret) == 0 || h.Get("X-Test-Secret") != string(secret) {
		return Delivery{}, ErrBadSignature
	}
	var d Delivery
	err := json.Unmarshal(body, &d)
	return d, err
}

func (f *fakeForge) Missed(context.Context, time.Time) ([]Delivery, error) { return f.missed, nil }

type env struct {
	s     *Service
	pool  *pgxpool.Pool
	forge *fakeForge
	repo  RepoRecord
	inst  Installation
	ctx   context.Context
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	box, _ := secret.New("0123456789abcdef0123456789abcdef")
	s := New(pool, box, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	e := &env{s: s, pool: pool, ctx: context.Background(), now: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return e.now }
	e.forge = &fakeForge{prs: map[int]Fetched{}, fetchErr: map[int]error{}, light: map[int]Light{}, access: map[string]string{},
		teams: map[string][]int64{}, protection: map[string]Protection{"main": {RequiredChecks: []string{}, RequiredApprovals: 1}},
		repo: RepoFacts{Owner: "acme", Name: "api", DefaultBranch: "main", ForgeRepo: 1001}}
	s.newCollector = func(forges.Forge, Credential) (Collector, error) { return e.forge, nil }
	f, err := forges.Create(e.ctx, pool, "fake", "Forge", "https://git.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if e.inst, err = s.CreateInstallation(e.ctx, InstallationInput{ForgeID: f.ID, Name: "acme", Kind: "token", Token: "tok-secret",
		WebhookSecret: "hook-secret"}); err != nil {
		t.Fatal(err)
	}
	if e.repo, err = s.WatchRepo(e.ctx, e.inst.ID, "acme", "api"); err != nil {
		t.Fatal(err)
	}
	return e
}

// fetched is a pull request as the fake forge holds it.
func (e *env) fetched(number int, mutate func(*PR)) Fetched {
	at := time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC) // fixed: moving the clock must not change the forge's state
	p := PR{Number: number, Title: "Billing export", State: StateOpen, Author: 501, HeadSHA: "sha-" + strconv.Itoa(number),
		HeadBranch: "feat/" + strconv.Itoa(number), HeadRepo: 1001, BaseBranch: "main", CreatedAt: at, UpdatedAt: at, HeadPushedAt: at,
		Mergeable: MergeableClean, Labels: []string{}, MergeBlockers: []string{}, ReviewRequests: []Request{}, Reviews: []Review{},
		Threads: []Thread{}, Conversation: []Comment{}, Checks: []Check{{Name: "build", State: CheckPassing, Required: true}},
		MissingChecks: []string{}, References: []Ref{}, ClosesIssues: []Ref{}, RequiredApproval: 1}
	if mutate != nil {
		mutate(&p)
	}
	return Fetched{PR: p, RefText: p.Title + "\nsee https://docs.example.test/page#3", AuthorLogin: "ana", Association: "CONTRIBUTOR",
		Accounts: []forges.Account{{ID: 501, Login: "ana", Name: "Ana"}}, ForgeRepo: 1001}
}

func (e *env) item(kind, key string) work.Item { return work.Item{Kind: kind, Key: key, Attempt: 1} }

func (e *env) refresh(t *testing.T, number int) {
	t.Helper()
	if err := e.s.refreshPR(e.ctx, e.item(KindRefreshPR, prKey(e.repo.ID, number))); err != nil {
		t.Fatal(err)
	}
}

type stored struct {
	state   PR
	id, seq int64
	stale   bool
}

func (e *env) stored(t *testing.T, number int) (stored, bool) {
	t.Helper()
	var s stored
	var raw []byte
	err := e.pool.QueryRow(e.ctx, "SELECT id, state, seq, stale FROM pr_fact WHERE repo_id = $1 AND number = $2", e.repo.ID, number).
		Scan(&s.id, &raw, &s.seq, &s.stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &s.state); err != nil {
		t.Fatal(err)
	}
	return s, true
}

// queued reports whether an item of kind and key waits, and when it is due.
func (e *env) queued(t *testing.T, kind, key string) (time.Time, bool) {
	t.Helper()
	var at time.Time
	err := e.pool.QueryRow(e.ctx, "SELECT run_after FROM work_item WHERE kind = $1 AND key = $2", kind, key).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return at, true
}

func (e *env) clearQueue(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, "DELETE FROM work_item"); err != nil {
		t.Fatal(err)
	}
}

// The refresh flow: a new pull request is stored with an event holding every field; the same state again records
// nothing new (F3); a change records only what changed, as the next event in sequence (F4); replaying the events
// gives the stored state (F1); neither the state nor the events hold text or addresses.
func TestRefresh(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	first, ok := e.stored(t, 12)
	if !ok || first.seq != 1 || first.state.Title != "Billing export" || first.stale {
		t.Fatalf("stored %+v", first)
	}
	e.now = e.now.Add(time.Minute)
	e.refresh(t, 12)
	if again, _ := e.stored(t, 12); again.seq != 1 {
		t.Fatalf("the same state made event %d", again.seq)
	}
	e.forge.prs[12] = e.fetched(12, func(p *PR) { p.Title = "Billing export, take two" })
	e.now = e.now.Add(time.Minute)
	e.refresh(t, 12)
	events, err := e.s.Timeline(e.ctx, first.id)
	if err != nil || len(events) != 2 || events[1].Seq != 2 || !slices.Equal(Changed(events[1].Changes), []string{"title"}) {
		t.Fatalf("timeline %+v, %v", events, err)
	}
	var changes []map[string]Change
	for _, ev := range events {
		changes = append(changes, ev.Changes)
	}
	replayed, err := Replay(changes)
	now, _ := e.stored(t, 12)
	if err != nil || replayed.Title != now.state.Title || replayed.HeadSHA != now.state.HeadSHA {
		t.Fatalf("replayed %+v, %v", replayed, err)
	}
	var dump string
	if err := e.pool.QueryRow(e.ctx, "SELECT (SELECT string_agg(state::text, ' ') FROM pr_fact) || (SELECT string_agg(changes::text, ' ') FROM fact_event)").
		Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, "http") || strings.Contains(dump, "docs.example.test") {
		t.Fatal("the facts hold an address")
	}
	var login string
	if err := e.pool.QueryRow(e.ctx, "SELECT login FROM account WHERE account_id = 501").Scan(&login); err != nil || login != "ana" {
		t.Fatalf("account %q, %v", login, err)
	}
}

// A state fetched before the one stored never replaces it.
func TestOlderFetchLoses(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	r, _ := e.s.loadRepo(e.ctx, e.repo.ID)
	older := e.fetched(12, func(p *PR) { p.Title = "An older title" })
	err := pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error { return e.s.store(e.ctx, tx, &r, &older, e.now.Add(-time.Hour)) })
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := e.stored(t, 12); s.state.Title != "Billing export" || s.seq != 1 {
		t.Fatalf("an older fetch won: %+v", s)
	}
}

// A failed fetch marks the stored state stale and is retried; a later success clears it. A rate limit waits for its
// reset instead.
func TestStaleAndRateLimit(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	e.forge.fetchErr[12] = errors.New("the forge is down")
	if err := e.s.refreshPR(e.ctx, e.item(KindRefreshPR, prKey(e.repo.ID, 12))); err == nil {
		t.Fatal("a failed fetch succeeded")
	}
	if s, _ := e.stored(t, 12); !s.stale {
		t.Fatal("not marked stale")
	}
	resets := e.now.Add(20 * time.Minute)
	e.forge.fetchErr[12] = &RateLimited{Until: resets}
	err := e.s.refreshPR(e.ctx, e.item(KindRefreshPR, prKey(e.repo.ID, 12)))
	var rl *RateLimited
	if !errors.As(err, &rl) || !rl.Until.Equal(resets) || !strings.HasPrefix(err.Error(), "retry at") {
		t.Fatalf("got %v", err)
	}
	delete(e.forge.fetchErr, 12)
	e.now = e.now.Add(time.Minute)
	e.refresh(t, 12)
	if s, _ := e.stored(t, 12); s.stale {
		t.Fatal("still stale after a successful refresh")
	}
}

// References resolve among the watched repositories; the author's access decides whether they may dismiss, and
// their standing in the repository when the forge will not say.
func TestReferencesAndDismissal(t *testing.T) {
	e := newEnv(t)
	e.forge.repo = RepoFacts{Owner: "acme", Name: "web", DefaultBranch: "main", ForgeRepo: 1002}
	web, err := e.s.WatchRepo(e.ctx, e.inst.ID, "acme", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.forge.repo = RepoFacts{Owner: "acme", Name: "api", DefaultBranch: "main", ForgeRepo: 1001}
	f := e.fetched(12, nil)
	f.RefText = "Pairs with web#7; also #9."
	e.forge.prs[12] = f
	e.forge.access["ana"] = "write"
	e.refresh(t, 12)
	s, _ := e.stored(t, 12)
	if !slices.Equal(s.state.References, []Ref{{Repo: e.repo.ID, Number: 9}, {Repo: web.ID, Number: 7}}) || !s.state.AuthorCanDismiss {
		t.Fatalf("references %v, can dismiss %v", s.state.References, s.state.AuthorCanDismiss)
	}

	f = e.fetched(13, func(p *PR) { p.Author = 505 })
	f.AuthorLogin, f.Association = "eve", "MEMBER"
	e.forge.prs[13] = f
	e.forge.accessErr = errors.New("forbidden")
	e.refresh(t, 13)
	if s, _ := e.stored(t, 13); !s.state.AuthorCanDismiss {
		t.Fatal("a member was refused dismissal when access could not be read")
	}
	f = e.fetched(14, func(p *PR) { p.Author = 506 })
	f.AuthorLogin, f.Association = "frank", "CONTRIBUTOR"
	e.forge.prs[14] = f
	e.refresh(t, 14)
	if s, _ := e.stored(t, 14); s.state.AuthorCanDismiss {
		t.Fatal("a contributor may dismiss")
	}
}

// Short polling: a pull request with pending checks is looked at again soon; a look that finds a change calls for a
// full refresh, and one that finds nothing new looks again later.
func TestShortPolling(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, func(p *PR) { p.Checks = []Check{{Name: "build", State: CheckPending, Required: true}} })
	e.clearQueue(t)
	e.refresh(t, 12)
	key := prKey(e.repo.ID, 12)
	if at, ok := e.queued(t, KindCheckPR, key); !ok || !at.Equal(e.now.Add(30*time.Second)) {
		t.Fatalf("short poll queued %v at %v", ok, at)
	}
	e.clearQueue(t)
	e.forge.light[12] = Light{HeadSHA: "sha-12", Mergeable: MergeableClean, Checks: []Check{{Name: "build", State: CheckPending}}}
	if err := e.s.checkPR(e.ctx, e.item(KindCheckPR, key)); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.queued(t, KindRefreshPR, key); ok {
		t.Fatal("nothing changed, yet a refresh was queued")
	}
	if _, ok := e.queued(t, KindCheckPR, key); !ok {
		t.Fatal("the short poll stopped while checks were pending")
	}
	e.forge.light[12] = Light{HeadSHA: "sha-12", Mergeable: MergeableClean, Checks: []Check{{Name: "build", State: CheckPassing}}}
	if err := e.s.checkPR(e.ctx, e.item(KindCheckPR, key)); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.queued(t, KindRefreshPR, key); !ok {
		t.Fatal("a finished check did not call for a refresh")
	}
}

// Polling refreshes the new, the updated, and the vanished — and not the unchanged; then it waits for the next poll.
// A sweep refreshes them all.
func TestPolling(t *testing.T) {
	e := newEnv(t)
	for _, n := range []int{11, 13, 14} {
		e.forge.prs[n] = e.fetched(n, nil)
		e.refresh(t, n)
	}
	e.forge.prs[11] = e.fetched(11, func(p *PR) { p.State = StateMerged })
	e.forge.prs[12] = e.fetched(12, nil)
	e.forge.prs[13] = e.fetched(13, func(p *PR) { p.UpdatedAt = e.now })
	e.clearQueue(t)
	if err := e.s.pollRepo(e.ctx, e.item(KindPollRepo, strconv.FormatInt(e.repo.ID, 10))); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]bool{11: true, 12: true, 13: true, 14: false} {
		if _, ok := e.queued(t, KindRefreshPR, prKey(e.repo.ID, n)); ok != want {
			t.Errorf("#%d: refresh queued %v, want %v", n, ok, want)
		}
	}
	if at, ok := e.queued(t, KindPollRepo, strconv.FormatInt(e.repo.ID, 10)); !ok || !at.Equal(e.now.Add(10*time.Minute)) {
		t.Errorf("next poll at %v, %v", at, ok)
	}
	e.clearQueue(t)
	if err := e.s.sweepRepo(e.ctx, e.item(KindSweepRepo, strconv.FormatInt(e.repo.ID, 10))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.queued(t, KindRefreshPR, prKey(e.repo.ID, 14)); !ok {
		t.Error("the sweep skipped an unchanged pull request")
	}
}

// A repository refresh follows a rename, stores the protection of the base branches in use, and, when it changed,
// refreshes the open pull requests.
func TestRepoRefresh(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	key := strconv.FormatInt(e.repo.ID, 10)
	if err := e.s.refreshRepo(e.ctx, e.item(KindRefreshRepo, key)); err != nil {
		t.Fatal(err)
	}
	e.clearQueue(t)
	e.forge.repo.Name = "api-renamed"
	e.forge.protection = map[string]Protection{"main": {RequiredChecks: []string{"build"}, RequiredApprovals: 2}}
	if err := e.s.refreshRepo(e.ctx, e.item(KindRefreshRepo, key)); err != nil {
		t.Fatal(err)
	}
	r, _ := e.s.loadRepo(e.ctx, e.repo.ID)
	if r.Name != "api-renamed" || r.protection["main"].RequiredApprovals != 2 {
		t.Fatalf("repo %+v", r)
	}
	if _, ok := e.queued(t, KindRefreshPR, prKey(e.repo.ID, 12)); !ok {
		t.Fatal("changed protection did not refresh the open pull requests")
	}
}

// Webhook deliveries: a bad signature is refused; a good one queues its refreshes, coalesced; a repeat of it is
// dropped; commits and pushes find their pull requests, and a push to a base branch refreshes after a delay.
func TestWebhook(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	e.clearQueue(t)
	mux := http.NewServeMux()
	mux.Handle("POST /hooks/{installation}", e.s.Webhook())
	send := func(installation int64, secret string, d Delivery) int {
		body, _ := json.Marshal(d)
		req := httptest.NewRequestWithContext(e.ctx, "POST", "/hooks/"+strconv.FormatInt(installation, 10), bytes.NewReader(body))
		req.Header.Set("X-Test-Secret", secret)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	key := prKey(e.repo.ID, 12)
	if code := send(e.inst.ID, "wrong", Delivery{ID: "d-1", ForgeRepo: 1001, PRs: []int{12}}); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code := send(9999, "hook-secret", Delivery{ID: "d-1"}); code != http.StatusNotFound {
		t.Fatalf("unknown installation: %d", code)
	}
	if code := send(e.inst.ID, "hook-secret", Delivery{ID: "d-1", ForgeRepo: 1001, PRs: []int{12}}); code != http.StatusAccepted {
		t.Fatalf("good delivery: %d", code)
	}
	if at, ok := e.queued(t, KindRefreshPR, key); !ok || !at.Equal(e.now.Add(5*time.Second)) {
		t.Fatalf("refresh queued %v at %v", ok, at)
	}
	e.clearQueue(t)
	if code := send(e.inst.ID, "hook-secret", Delivery{ID: "d-1", ForgeRepo: 1001, PRs: []int{12}}); code != http.StatusAccepted {
		t.Fatalf("repeat: %d", code)
	}
	if _, ok := e.queued(t, KindRefreshPR, key); ok {
		t.Fatal("a repeated delivery queued work")
	}
	send(e.inst.ID, "hook-secret", Delivery{ID: "d-2", ForgeRepo: 1001, SHAs: []string{"sha-12"}})
	if _, ok := e.queued(t, KindRefreshPR, key); !ok {
		t.Fatal("a check on the head did not refresh the pull request")
	}
	e.clearQueue(t)
	send(e.inst.ID, "hook-secret", Delivery{ID: "d-3", ForgeRepo: 1001, Pushed: "main"})
	if at, ok := e.queued(t, KindRefreshPR, key); !ok || !at.Equal(e.now.Add(time.Minute)) {
		t.Fatalf("a push to the base: queued %v at %v", ok, at)
	}
	e.clearQueue(t)
	send(e.inst.ID, "hook-secret", Delivery{ID: "d-4", ForgeRepo: 1001, Pushed: "feat/12"})
	if at, ok := e.queued(t, KindRefreshPR, key); !ok || !at.Equal(e.now.Add(5*time.Second)) {
		t.Fatalf("a push to the head: queued %v at %v", ok, at)
	}
	send(e.inst.ID, "hook-secret", Delivery{ID: "d-5", ForgeRepo: 1001, RepoChanged: true})
	if _, ok := e.queued(t, KindRefreshRepo, strconv.FormatInt(e.repo.ID, 10)); !ok {
		t.Fatal("a settings change did not refresh the repository")
	}
}

// Missed deliveries are processed once: one Park already received is dropped.
func TestRedeliver(t *testing.T) {
	e := newEnv(t)
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	if err := e.s.accept(e.ctx, e.inst.ID, e.repo.ForgeID, Delivery{ID: "d-1", ForgeRepo: 1001}); err != nil {
		t.Fatal(err)
	}
	e.clearQueue(t)
	e.forge.missed = []Delivery{{ID: "d-1", ForgeRepo: 1001, PRs: []int{13}}, {ID: "d-2", ForgeRepo: 1001, PRs: []int{12}}}
	if err := e.s.redeliver(e.ctx, e.item(KindRedeliver, strconv.FormatInt(e.inst.ID, 10))); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.queued(t, KindRefreshPR, prKey(e.repo.ID, 12)); !ok {
		t.Fatal("a missed delivery was not processed")
	}
	if _, ok := e.queued(t, KindRefreshPR, prKey(e.repo.ID, 13)); ok {
		t.Fatal("a delivery already received was processed again")
	}
}

// Followers are told, in the storing transaction, which fields changed — and nothing when nothing did.
func TestFollowers(t *testing.T) {
	e := newEnv(t)
	var calls [][]string
	e.s.Follow(func(_ context.Context, tx pgx.Tx, prID int64, changed []string) error {
		var n int
		if err := tx.QueryRow(e.ctx, "SELECT count(*) FROM fact_event WHERE pr_id = $1", prID).Scan(&n); err != nil {
			return err
		}
		calls = append(calls, append([]string{strconv.Itoa(n)}, changed...))
		return nil
	})
	e.forge.prs[12] = e.fetched(12, nil)
	e.refresh(t, 12)
	e.refresh(t, 12)
	e.forge.prs[12] = e.fetched(12, func(p *PR) { p.HeadSHA = "sha-new" })
	e.now = e.now.Add(time.Minute)
	e.refresh(t, 12)
	if len(calls) != 2 || calls[0][0] != "1" || !slices.Equal(calls[1], []string{"2", "head_sha"}) {
		t.Fatalf("calls %v", calls)
	}
}

// An installation's credential and webhook secret are sealed at rest, and never listed.
func TestInstallations(t *testing.T) {
	e := newEnv(t)
	var cred, hook []byte
	if err := e.pool.QueryRow(e.ctx, "SELECT credential, webhook_secret FROM installation WHERE id = $1", e.inst.ID).Scan(&cred, &hook); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cred, []byte("tok-secret")) || bytes.Contains(hook, []byte("hook-secret")) {
		t.Fatal("a secret is stored in the clear")
	}
	list, err := e.s.Installations(e.ctx)
	if err != nil || len(list) != 1 || !list[0].HasWebhookSecret {
		t.Fatalf("list %+v, %v", list, err)
	}
	_, err = e.s.CreateInstallation(e.ctx, InstallationInput{ForgeID: e.repo.ForgeID, Name: "app", Kind: "app", App: &AppCredential{AppID: 1}})
	if code := failCode(err); code != "invalid_credential" {
		t.Fatalf("an incomplete app credential: %v", err)
	}
	token := "rotated"
	if _, err := e.s.UpdateInstallation(e.ctx, e.inst.ID, InstallationPatch{Token: &token}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.PullURL(e.ctx, e.repo, 12); got != "https://git.example.test/acme/api/pulls/12" {
		t.Fatalf("pull URL %q", got)
	}
}

// Team members are kept; members the forge will not tell stay unknown.
func TestTeams(t *testing.T) {
	e := newEnv(t)
	f := e.fetched(12, func(p *PR) { p.ReviewRequests = []Request{{Team: 77}} })
	f.Teams = []Team{{ID: 77, Org: "acme", Slug: "billing", Name: "Billing"}, {ID: 78, Org: "acme", Slug: "secret"}}
	e.forge.prs[12] = f
	e.forge.teams["acme/billing"] = []int64{502, 503}
	e.refresh(t, 12)
	for _, id := range []int64{77, 78} {
		k := strconv.FormatInt(e.repo.ForgeID, 10) + "/" + strconv.FormatInt(id, 10)
		if _, ok := e.queued(t, KindRefreshTeam, k); !ok {
			t.Fatalf("team %d: no refresh queued", id)
		}
		if err := e.s.refreshTeam(e.ctx, e.item(KindRefreshTeam, k)); err != nil {
			t.Fatal(err)
		}
	}
	var billing, hidden []int64
	if err := e.pool.QueryRow(e.ctx, "SELECT (SELECT members FROM team WHERE team_id = 77), (SELECT members FROM team WHERE team_id = 78)").
		Scan(&billing, &hidden); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(billing, []int64{502, 503}) || hidden != nil {
		t.Fatalf("billing %v, hidden %v", billing, hidden)
	}
}

func failCode(err error) string {
	if f, ok := fail.As(err); ok {
		return f.Code
	}
	return ""
}
