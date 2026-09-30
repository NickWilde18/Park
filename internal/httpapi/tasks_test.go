package httpapi

import (
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
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/tasks"
	"github.com/NickWilde18/Park/internal/verdicts"
	"github.com/NickWilde18/Park/internal/work"
)

// claimOver sends a claim with an Idempotency-Key.
func claimOver(t *testing.T, a *harness, token, key string, body map[string]any, out any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/tasks/claim", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, req)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// The executor's side, through the real work queue: once Ana turns automation on, her collected pull request's failing
// check becomes a task. Her executor's token claims it — a claim retried with its key gets the same lease — sends a
// heartbeat, and reports; a report sent again is answered the same. A lease that is no longer current is refused. A
// read token cannot claim, and neither can a browser session: a lease is held by a token.
func TestTasksOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	ruleSets := rules.New(pool, log)
	if _, err := ruleSets.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, log)
	groups := grouping.New(pool, log)
	balls := verdicts.New(pool, log, ruleSets, groups)
	stage := tasks.New(pool, log, groups)
	collected.Follow(groups.FactsFollower())
	collected.Follow(balls.FactsFollower())
	collected.Follow(stage.FactsFollower())
	groups.Follow(balls.GroupingFollower())
	balls.Follow(stage.VerdictFollower())
	ids.Follow(stage.IdentityFollower())
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	balls.Register(runner)
	stage.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Grouping: groups, Rules: ruleSets,
		Verdicts: balls, Tasks: stage, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)
	var me Me
	admin.call(t, "GET", "/api/v1/me", nil, &me)
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/members/"+strconv.FormatInt(me.Member.Id, 10)+"/identities",
		map[string]any{"forge_id": f.Id, "account_id": 501}, nil), http.StatusCreated, "")
	var auto Automation
	wantStatus(t, admin.call(t, "PUT", "/api/v1/me/automation", map[string]any{"actions": []string{"fix_checks", "merge"}}, nil),
		http.StatusBadRequest, "invalid_automation")
	wantStatus(t, admin.call(t, "PUT", "/api/v1/me/automation", map[string]any{"actions": []string{"fix_checks"}}, &auto), http.StatusOK, "")
	var executor, reader NewToken
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "laptop", "scopes": []string{"tasks:work"}}, &executor)
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &reader)
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": "api"}, nil),
		http.StatusCreated, "")

	var list Tasks
	for deadline := time.Now().Add(10 * time.Second); len(list.Tasks) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no task was made; log:\n%s", logs.String())
		}
		wantStatus(t, admin.call(t, "GET", "/api/v1/tasks", nil, &list), http.StatusOK, "")
	}
	if list.Tasks[0].State != TaskStateQueued || len(list.Tasks[0].Items) != 1 || list.Tasks[0].Items[0].Action != "fix_checks" {
		t.Fatalf("tasks %+v", list)
	}

	claim := map[string]any{"executor": "laptop", "actions": []string{"fix_checks"}}
	wantStatus(t, claimOver(t, a, reader.Token, "k0", claim, nil), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, admin.call(t, "POST", "/api/v1/tasks/claim", claim, nil), http.StatusForbidden, "token_required")
	var leased, again LeasedTask
	wantStatus(t, claimOver(t, a, executor.Token, "k1", claim, &leased), http.StatusOK, "")
	item := leased.Items[0]
	if leased.Attempt != 1 || leased.HeartbeatSeconds != 300 || item.Action != "fix_checks" || item.PullRequest.Repo != "acme/api" ||
		item.PullRequest.Number != 12 || item.PullRequest.Head != "feat/x" || item.PullRequest.HeadCommit != "abc" ||
		item.Link != "https://hub.example.test/acme/api/pr/12" || item.Targets.Checks == nil || (*item.Targets.Checks)[0] != "build" {
		t.Fatalf("leased %+v", leased)
	}
	wantStatus(t, claimOver(t, a, executor.Token, "k1", claim, &again), http.StatusOK, "")
	if again.Id != leased.Id || again.LeaseId != leased.LeaseId {
		t.Fatalf("the retried claim: %+v", again)
	}
	if w := claimOver(t, a, executor.Token, "k2", claim, nil); w.Code != http.StatusNoContent {
		t.Fatalf("a second claim: %d %s", w.Code, w.Body)
	}

	path := "/api/v1/tasks/" + strconv.FormatInt(leased.Id, 10)
	ex := a.browser()
	ex.bearer = executor.Token
	var beat HeartbeatResult
	wantStatus(t, ex.call(t, "POST", path+"/heartbeat", map[string]any{"lease_id": leased.LeaseId, "note": "on it"}, &beat), http.StatusOK, "")
	if !beat.Current || beat.Cancel || beat.LeaseExpiresAt == nil {
		t.Fatalf("heartbeat %+v", beat)
	}
	wantStatus(t, ex.call(t, "POST", path+"/heartbeat", map[string]any{"lease_id": "lse_nope"}, &beat), http.StatusOK, "")
	if beat.Current {
		t.Fatalf("a heartbeat under another lease: %+v", beat)
	}
	report := map[string]any{"lease_id": leased.LeaseId, "outcome": "done", "items": []map[string]any{{"id": item.Id, "note": "pushed"}}}
	var reported Task
	wantStatus(t, ex.call(t, "POST", path+"/report", report, &reported), http.StatusOK, "")
	if reported.State != TaskStateVerifying {
		t.Fatalf("reported %+v", reported)
	}
	wantStatus(t, ex.call(t, "POST", path+"/report", report, nil), http.StatusOK, "")
	wantStatus(t, ex.call(t, "POST", path+"/release", map[string]any{"lease_id": leased.LeaseId}, nil), http.StatusConflict, "lease_not_current")
	var full Task
	wantStatus(t, admin.call(t, "GET", path, nil, &full), http.StatusOK, "")
	kinds := []string{}
	for _, e := range *full.Events {
		kinds = append(kinds, e.Kind)
	}
	if !strings.Contains(strings.Join(kinds, " "), "created item_added claimed heartbeat reported") {
		t.Fatalf("events %v", kinds)
	}
	wantStatus(t, admin.call(t, "GET", "/api/v1/me/automation", nil, &auto), http.StatusOK, "")
	if len(auto.Actions) != 1 || auto.Repos != nil {
		t.Fatalf("automation %+v", auto)
	}
}
