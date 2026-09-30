package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

func (hub) FetchPR(_ context.Context, _ facts.Repo, n int, _ map[string]facts.Protection) (facts.Fetched, error) {
	p := facts.PR{Number: n, Title: "Billing export", State: facts.StateOpen, Author: 501, HeadSHA: "abc", HeadBranch: "feat/x",
		HeadRepo: 1001, BaseBranch: "main", CreatedAt: hubAt, UpdatedAt: hubAt, HeadPushedAt: hubAt, Mergeable: facts.MergeableClean,
		Labels: []string{"billing"}, MergeBlockers: []string{}, ReviewRequests: []facts.Request{{Account: 502, CodeOwner: false}},
		Reviews:      []facts.Review{{ID: 1, Author: 503, State: facts.ReviewChangesRequested, Commit: "abc", At: hubAt, HasBody: true, Counts: true}},
		Threads:      []facts.Thread{{ID: "T1", Comments: []facts.Comment{{ID: 7, Author: 503, At: hubAt}}}},
		Conversation: []facts.Comment{}, Checks: []facts.Check{{Name: "build", State: facts.CheckFailing, Required: true}},
		MissingChecks: []string{}, References: []facts.Ref{}, ClosesIssues: []facts.Ref{}, RequiredApproval: 1}
	return facts.Fetched{PR: p, AuthorLogin: "ana", ForgeRepo: 1001, Accounts: []forges.Account{
		{ID: 501, Login: "ana", Name: "Ana"}, {ID: 502, Login: "ben"}, {ID: 503, Login: "carol", Name: "Carol"}}}, nil
}

func (hub) ListOpen(context.Context, facts.Repo) ([]facts.Listed, error) {
	return []facts.Listed{{Number: 12, UpdatedAt: hubAt}}, nil
}

func (hub) Light(context.Context, facts.Repo, int) (facts.Light, error) { return facts.Light{}, nil }

func (hub) Repository(_ context.Context, owner, name string) (facts.RepoFacts, error) {
	return facts.RepoFacts{Owner: owner, Name: name, DefaultBranch: "main", ForgeRepo: 1001}, nil
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
