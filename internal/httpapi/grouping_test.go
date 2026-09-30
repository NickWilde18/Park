package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/work"
)

// A whole round of grouping: two repositories' pull requests from one author's branch are grouped by the queue as
// they are collected; a bridge's thread takes the group over and names it; tokens and sessions may do only their own
// part, and no token changes a group; members rename and take out through their session.
func TestGroupingOverHTTP(t *testing.T) {
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, _ := secret.New(secretKey)
	ids, err := identity.New(pool, box, log, identity.Options{SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, log)
	groups := grouping.New(pool, log)
	collected.Follow(groups.FactsFollower())
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Grouping: groups, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &harness{h: h, ids: ids, pool: pool, logs: logs}
	admin := a.admin(t)

	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	for _, name := range []string{"api", "web"} {
		wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": name}, nil),
			http.StatusCreated, "")
	}
	var list Groups
	deadline := time.Now().Add(10 * time.Second)
	for len(list.Groups) == 0 || len(list.Groups[0].Members) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the pull requests were never grouped; log:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
		admin.call(t, "GET", "/api/v1/groups", nil, &list)
	}
	g := list.Groups[0]
	if g.Name != "feat/x" || g.NameSource != GroupNameSourceBranch || g.Members[0].Source != GroupMemberSourceStructural ||
		g.Members[0].Reason != "same branch name" {
		t.Fatalf("group %+v", g)
	}

	var bridge Bridge
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/bridges", map[string]any{"name": "Tracker", "link_template": "https://tracker.example.test/t/{id}"}, &bridge),
		http.StatusCreated, "")
	var bt NewToken
	wantStatus(t, admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "tracker", "scopes": []string{"assertions:write"}, "bridge_id": bridge.Id}, &bt),
		http.StatusCreated, "")
	var rt NewToken
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &rt)

	post := map[string]any{"anchor": "thread-1", "title": "Billing export", "pull_requests": []map[string]any{
		{"repo": "acme/api", "number": 12}, {"repo": "web", "number": 12}}}
	bridgeClient := a.browser()
	bridgeClient.bearer = bt.Token
	var res AssertionResult
	wantStatus(t, bridgeClient.call(t, "POST", "/api/v1/assertions", post, &res), http.StatusCreated, "")
	if !res.Applied {
		t.Fatalf("result %+v", res)
	}
	wantStatus(t, bridgeClient.call(t, "GET", "/api/v1/groups", nil, nil), http.StatusForbidden, "insufficient_scope")
	reader := a.browser()
	reader.bearer = rt.Token
	wantStatus(t, reader.call(t, "POST", "/api/v1/assertions", post, nil), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, admin.call(t, "POST", "/api/v1/assertions", post, nil), http.StatusForbidden, "bridge_only")
	for _, token := range []*browser{reader, bridgeClient} {
		wantStatus(t, token.call(t, "PATCH", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10), map[string]string{"name": "x"}, nil),
			http.StatusForbidden, "insufficient_scope")
	}
	wantStatus(t, bridgeClient.call(t, "POST", "/api/v1/assertions", map[string]any{"anchor": "t2", "pull_requests": []map[string]any{
		{"repo": "acme/api", "number": 12}, {"repo": "acme/private", "number": 3}}}, nil), http.StatusBadRequest, "unknown_repo")

	var got Group
	wantStatus(t, admin.call(t, "GET", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10), nil, &got), http.StatusOK, "")
	if got.Name != "Billing export" || got.NameSource != GroupNameSourceExternal || got.Members[0].Reason != "Tracker" {
		t.Fatalf("after the thread: %+v", got)
	}
	wantStatus(t, admin.call(t, "PATCH", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10), map[string]string{"name": "Billing"}, &got), http.StatusOK, "")
	if got.Name != "Billing" || got.NameSource != GroupNameSourceManual {
		t.Fatalf("renamed: %+v", got)
	}
	pr := got.Members[1].PullRequest
	wantStatus(t, admin.call(t, "DELETE", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10)+"/members/"+strconv.FormatInt(pr, 10), nil, nil),
		http.StatusNoContent, "")
	admin.call(t, "GET", "/api/v1/groups/"+strconv.FormatInt(g.Id, 10), nil, &got)
	if len(got.Members) != 1 {
		t.Fatalf("after taking one out: %+v", got)
	}
	var sugs Suggestions
	wantStatus(t, reader.call(t, "GET", "/api/v1/suggestions", nil, &sugs), http.StatusOK, "")
	wantStatus(t, bridgeClient.call(t, "DELETE", "/api/v1/assertions?anchor=thread-1", nil, nil), http.StatusNoContent, "")
}
