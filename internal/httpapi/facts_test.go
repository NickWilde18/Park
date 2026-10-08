package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/work"
)

// hub is a forge kind for these tests, whose collector serves one repository with one open pull request.
type hub struct{}

func init() {
	forges.Register("testhub", forges.Kind{PullURL: func(base, owner, name string, n int) string {
		return base + "/" + owner + "/" + name + "/pr/" + strconv.Itoa(n)
	}})
	facts.Register("testhub", func(forges.Forge, facts.Credential) (facts.Collector, error) { return hub{}, nil })
}

var hubAt = time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC)

// hubRepos are the forge's IDs of the repositories the hub has.
var hubRepos = map[string]int64{"api": 1001, "web": 1002}

func (hub) FetchPR(_ context.Context, r facts.Repo, n int, _ map[string]facts.Protection) (facts.Fetched, error) {
	p := facts.PR{Number: n, Title: "Billing export", State: facts.StateOpen, Author: 501, HeadSHA: "abc", HeadBranch: "feat/x",
		HeadRepo: r.ForgeRepo, BaseBranch: "main", CreatedAt: hubAt, UpdatedAt: hubAt, HeadPushedAt: hubAt, Mergeable: facts.MergeableClean,
		Labels: []string{"billing"}, MergeBlockers: []string{}, ReviewRequests: []facts.Request{{Account: 502, CodeOwner: false}},
		Reviews:      []facts.Review{{ID: 1, Author: 503, State: facts.ReviewChangesRequested, Commit: "abc", At: hubAt, HasBody: true, Counts: true}},
		Threads:      []facts.Thread{{ID: "T1", Comments: []facts.Comment{{ID: 7, Author: 503, At: hubAt}}}},
		Conversation: []facts.Comment{}, Checks: []facts.Check{{Name: "build", State: facts.CheckFailing, Required: true}},
		MissingChecks: []string{}, References: []facts.Ref{}, ClosesIssues: []facts.Ref{}, RequiredApproval: 1}
	return facts.Fetched{PR: p, AuthorLogin: "ana", ForgeRepo: r.ForgeRepo, Accounts: []forges.Account{
		{ID: 501, Login: "ana", Name: "Ana"}, {ID: 502, Login: "ben"}, {ID: 503, Login: "carol", Name: "Carol"}}}, nil
}

func (hub) ListOpen(context.Context, facts.Repo) ([]facts.Listed, error) {
	return []facts.Listed{{Number: 12, UpdatedAt: hubAt}}, nil
}

func (hub) Light(context.Context, facts.Repo, int) (facts.Light, error) { return facts.Light{}, nil }

func (hub) Repository(_ context.Context, owner, name string) (facts.RepoFacts, error) {
	return facts.RepoFacts{Owner: owner, Name: name, DefaultBranch: "main", ForgeRepo: hubRepos[name]}, nil
}

func (hub) Protection(context.Context, facts.Repo, []string) (map[string]facts.Protection, error) {
	return map[string]facts.Protection{"main": {RequiredChecks: []string{"build"}, RequiredApprovals: 1}}, nil
}

func (hub) Access(context.Context, facts.Repo, string) (string, error) { return "write", nil }

func (hub) Team(context.Context, string, string) ([]int64, error) { return nil, nil }

func (hub) Delivery(h http.Header, body, secret []byte) (facts.Delivery, error) {
	if len(secret) == 0 || h.Get("X-Test-Secret") != string(secret) {
		return facts.Delivery{}, facts.ErrBadSignature
	}
	var d facts.Delivery
	err := json.Unmarshal(body, &d)
	return d, err
}

func (hub) Missed(context.Context, time.Time) ([]facts.Delivery, error) {
	return nil, facts.ErrNoDeliveryLog
}

// A whole round: an admin connects a forge and watches a repository; the queue polls it and stores its pull
// request; members read the facts, with names for the accounts and a link built for the response; the admin sees
// the installation without its secrets; deliveries need the webhook secret.
func TestFactsOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, log)
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)

	var f Forge
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub",
		"base_address": "https://hub.example.test"}, &f), http.StatusCreated, "")
	var inst Installation
	w := admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token",
		"token": "the-token", "webhook_secret": "hook-secret"}, &inst)
	wantStatus(t, w, http.StatusCreated, "")
	if inst.WebhookPath != "/hooks/"+strconv.FormatInt(inst.Id, 10) || !inst.HasWebhookSecret || strings.Contains(w.Body.String(), "the-token") {
		t.Fatalf("installation %s", w.Body)
	}
	var repo Repo
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": "api"}, &repo),
		http.StatusCreated, "")

	var list PullRequests
	deadline := time.Now().Add(10 * time.Second)
	for len(list.PullRequests) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the pull request was never collected; log:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
		admin.call(t, "GET", "/api/v1/prs", nil, &list)
	}
	s := list.PullRequests[0]
	if s.Number != 12 || s.Author.Login != "ana" || s.Checks != PullRequestSummaryChecksFailing || s.Url != "https://hub.example.test/acme/api/pr/12" ||
		s.Repo.Name != "api" {
		t.Fatalf("summary %+v", s)
	}
	var pr PullRequest
	wantStatus(t, admin.call(t, "GET", "/api/v1/prs/"+strconv.FormatInt(s.Id, 10), nil, &pr), http.StatusOK, "")
	if pr.Facts.Reviews[0].State != ReviewFactStateChangesRequested || !pr.Facts.Reviews[0].HasBody || pr.Facts.RequiredApprovals != 1 ||
		!pr.Facts.AuthorCanDismiss || len(pr.Facts.Threads) != 1 || len(pr.Accounts) != 3 {
		t.Fatalf("facts %+v", pr)
	}
	var timeline Timeline
	wantStatus(t, admin.call(t, "GET", "/api/v1/prs/"+strconv.FormatInt(s.Id, 10)+"/timeline", nil, &timeline), http.StatusOK, "")
	if len(timeline.Events) != 1 || timeline.Events[0].Seq != 1 || timeline.Events[0].Changes["title"].New != "Billing export" {
		t.Fatalf("timeline %+v", timeline)
	}
	wantStatus(t, admin.call(t, "POST", "/api/v1/prs/"+strconv.FormatInt(s.Id, 10)+"/refresh", nil, nil), http.StatusAccepted, "")
	wantStatus(t, admin.call(t, "GET", "/api/v1/prs/999999", nil, nil), http.StatusNotFound, "not_found")

	deliver := func(secret string) int {
		body, _ := json.Marshal(facts.Delivery{ID: "d-1", ForgeRepo: 1001, PRs: []int{12}})
		req := httptest.NewRequestWithContext(context.Background(), "POST", inst.WebhookPath, bytes.NewReader(body))
		req.Header.Set("X-Test-Secret", secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := deliver("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code := deliver("hook-secret"); code != http.StatusAccepted {
		t.Fatalf("delivery: %d", code)
	}

	anonymous := a.browser()
	wantStatus(t, anonymous.call(t, "GET", "/api/v1/prs", nil, nil), http.StatusUnauthorized, "unauthenticated")
	if strings.Contains(logs.String(), "the-token") || strings.Contains(logs.String(), "hook-secret") {
		t.Fatal("the log holds an installation's secret")
	}
}

// Polling now, from a member's session. An admin watches two repositories, whose polls then wait ten minutes ahead.
// Asking for one repository makes its poll due at once, while the other's waits; asking for all makes both due. A
// poll waiting out a failure's backoff keeps it. A repository no longer watched, or never known, is not found. A read
// token may not ask, and no one signed out may.
func TestRefreshRepositoriesOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: facts.New(pool, box, log), Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	repo := map[string]string{}
	for _, name := range []string{"api", "web"} {
		var r Repo
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": name}, &r),
			http.StatusCreated, "")
		repo[name] = strconv.FormatInt(r.Id, 10)
	}
	var inv NewInvite
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	member := a.browser()
	wantStatus(t, member.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": "ana",
		"password": "a long enough password", "display_name": "Ana"}, nil), http.StatusCreated, "")
	member.refreshCSRF(t)

	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	later := func() {
		exec("UPDATE work_item SET run_after = now() + interval '10 minutes' WHERE kind = $1", facts.KindPollRepo)
	}
	due := func(name string) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx, "SELECT run_after <= now() FROM work_item WHERE kind = $1 AND key = $2", facts.KindPollRepo, repo[name]).
			Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	later()
	wantStatus(t, member.call(t, "POST", "/api/v1/prs/refresh?repo="+repo["api"], nil, nil), http.StatusAccepted, "")
	if !due("api") || due("web") {
		t.Fatalf("asking for api: api due %v, web due %v", due("api"), due("web"))
	}
	later()
	wantStatus(t, member.call(t, "POST", "/api/v1/prs/refresh", nil, nil), http.StatusAccepted, "")
	if !due("api") || !due("web") {
		t.Fatalf("asking for all: api due %v, web due %v", due("api"), due("web"))
	}
	exec("UPDATE work_item SET run_after = now() + interval '5 minutes', attempts = 1 WHERE kind = $1 AND key = $2", facts.KindPollRepo, repo["api"])
	wantStatus(t, member.call(t, "POST", "/api/v1/prs/refresh", nil, nil), http.StatusAccepted, "")
	if due("api") {
		t.Fatal("a poll waiting out a failure's backoff was moved")
	}

	wantStatus(t, admin.call(t, "PATCH", "/api/v1/admin/repos/"+repo["web"], map[string]any{"watched": false}, nil), http.StatusOK, "")
	wantStatus(t, member.call(t, "POST", "/api/v1/prs/refresh?repo="+repo["web"], nil, nil), http.StatusNotFound, "not_found")
	wantStatus(t, member.call(t, "POST", "/api/v1/prs/refresh?repo=999999", nil, nil), http.StatusNotFound, "not_found")
	var token NewToken
	wantStatus(t, member.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token),
		http.StatusCreated, "")
	reader := a.browser()
	reader.bearer = token.Token
	wantStatus(t, reader.call(t, "POST", "/api/v1/prs/refresh", nil, nil), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, a.browser().call(t, "POST", "/api/v1/prs/refresh", nil, nil), http.StatusUnauthorized, "unauthenticated")
}

// A GitHub that refuses every credential, as it answers a revoked token, or an app whose key it cannot verify: the
// admin who watches a repository through either installation is told the forge refused its credential, with a 400
// they can act on rather than a server failure, and nothing is watched. The installation is listed as refused.
func TestWatchThroughARefusedCredential(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message": "Bad credentials"}`)
	}))
	t.Cleanup(refusing.Close)
	admin := factsAdmin(t)
	var f Forge
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "GitHub",
		"base_address": refusing.URL}, &f), http.StatusCreated, "")

	for _, c := range []struct {
		credential map[string]any
		name       string
	}{
		{name: "token", credential: map[string]any{"kind": "token", "token": "a-revoked-token"}},
		{name: "app", credential: map[string]any{"kind": "app", "app": map[string]any{"app_id": 7, "installation_id": 55,
			"private_key": appKey(t)}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{"forge_id": f.Id, "name": c.name}
			maps.Copy(body, c.credential)
			var inst Installation
			wantStatus(t, admin.call(t, "POST", "/api/v1/admin/installations", body, &inst), http.StatusCreated, "")
			wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme",
				"name": "api"}, nil), http.StatusBadRequest, "credential_refused")
			var list Installations
			wantStatus(t, admin.call(t, "GET", "/api/v1/admin/installations", nil, &list), http.StatusOK, "")
			for _, i := range list.Installations {
				if i.Id == inst.Id && (i.Problem == nil || *i.Problem != CollectionProblemKindRefused || i.ProblemSince == nil || i.ProblemUntil != nil) {
					t.Fatalf("the refused installation: %+v", i)
				}
			}
		})
	}
	var repos Repos
	wantStatus(t, admin.call(t, "GET", "/api/v1/admin/repos", nil, &repos), http.StatusOK, "")
	if len(repos.Repos) != 0 {
		t.Fatalf("watching %+v", repos.Repos)
	}
}

// An app installation's private key is read when the installation is added, and when its key is replaced: a key that
// is not PEM, or not an RSA key, is refused with a 400, rather than taken and failing as a server error the first
// time Park uses it; a key that reads replaces the old one.
func TestAppKeyReadWhenAdded(t *testing.T) {
	admin := factsAdmin(t)
	var f Forge
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "GitHub",
		"base_address": "https://ghe.example.test"}, &f), http.StatusCreated, "")
	app := func(key string) map[string]any {
		return map[string]any{"app_id": 7, "installation_id": 55, "private_key": key}
	}
	var inst Installation
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "app", "kind": "app",
		"app": app(appKey(t))}, &inst), http.StatusCreated, "")
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, key string }{
		{name: "not PEM", key: "-----BEGIN RSA PRIVATE KEY-----\nnot a key\n-----END RSA PRIVATE KEY-----\n"},
		{name: "not RSA", key: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))},
	} {
		t.Run("adding a key "+c.name, func(t *testing.T) {
			wantStatus(t, admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": c.name,
				"kind": "app", "app": app(c.key)}, nil), http.StatusBadRequest, "invalid_credential")
		})
		t.Run("replacing the key with one "+c.name, func(t *testing.T) {
			wantStatus(t, admin.call(t, "PATCH", "/api/v1/admin/installations/"+strconv.FormatInt(inst.Id, 10),
				map[string]any{"app": app(c.key)}, nil), http.StatusBadRequest, "invalid_credential")
		})
	}
	wantStatus(t, admin.call(t, "PATCH", "/api/v1/admin/installations/"+strconv.FormatInt(inst.Id, 10),
		map[string]any{"app": app(appKey(t))}, nil), http.StatusOK, "")
	var list Installations
	wantStatus(t, admin.call(t, "GET", "/api/v1/admin/installations", nil, &list), http.StatusOK, "")
	if len(list.Installations) != 1 {
		t.Fatalf("installations %+v", list.Installations)
	}
}

// factsAdmin signs in the admin of Park's handler with the fact layer and no runner; a failed test shows Park's log.
func factsAdmin(t *testing.T) *browser {
	t.Helper()
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log:\n%s", logs.String())
		}
	})
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: facts.New(pool, box, log), Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return (&harness{h: h, ids: ids, pool: pool, logs: logs}).admin(t)
}

// appKey is a new RSA private key, in the PEM a GitHub App's key comes in.
func appKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
