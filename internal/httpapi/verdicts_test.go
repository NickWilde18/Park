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
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
	"github.com/NickWilde18/Park/internal/verdicts"
	"github.com/NickWilde18/Park/internal/work"
)

// The whole loop, through the real work queue: a collected pull request whose required check fails gets a verdict
// that puts its ball with its author, to fix the checks. The author's member sees it in their queue; the verdict
// explains itself, with every part's path and the inputs; marking it nudged is recorded, and a token may read but not
// mark.
func TestVerdictsOverHTTP(t *testing.T) {
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
	collected.Follow(groups.FactsFollower())
	collected.Follow(balls.FactsFollower())
	groups.Follow(balls.GroupingFollower())
	ruleSets.Follow(balls.RulesFollower())
	runner := work.NewRunner(pool, log, work.Options{})
	collected.Register(runner)
	groups.Register(runner)
	balls.Register(runner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Facts: collected, Grouping: groups, Rules: ruleSets,
		Verdicts: balls, Pool: pool})
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
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": "api"}, nil),
		http.StatusCreated, "")

	var queue Queue
	for deadline := time.Now().Add(10 * time.Second); len(queue.Holding) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no verdict reached the author's queue; log:\n%s", logs.String())
		}
		wantStatus(t, admin.call(t, "GET", "/api/v1/queue", nil, &queue), http.StatusOK, "")
	}
	v := queue.Holding[0]
	if v.Side == nil || *v.Side != VerdictSideAuthor || v.Unit.Kind != UnitKindPullRequest || len(v.Holders) != 1 ||
		v.Holders[0].Account.Login != "ana" || v.Holders[0].Account.Name != "Ana" || v.Holders[0].Parts[0].Action == nil ||
		*v.Holders[0].Parts[0].Action != "fix_checks" || v.Holders[0].Parts[0].Reason != "1 required checks failing" || v.Inputs != nil {
		t.Fatalf("verdict %+v", v)
	}
	var list Verdicts
	wantStatus(t, admin.call(t, "GET", "/api/v1/verdicts", nil, &list), http.StatusOK, "")
	var full Verdict
	wantStatus(t, admin.call(t, "GET", "/api/v1/verdicts/"+strconv.FormatInt(v.Id, 10), nil, &full), http.StatusOK, "")
	if len(list.Verdicts) != 1 || full.Inputs == nil || len(*full.Inputs) != 3 || (*full.Inputs)[0].Fields["pr.checks"] != "failing" ||
		len(full.Parts) != 3 || full.Parts[2].Account.Login != "carol" || full.Parts[2].Role != VerdictPartRoleReviewer || len(full.Parts[0].Path) != 3 {
		t.Fatalf("list %d; verdict %+v", len(list.Verdicts), full)
	}
	wantStatus(t, admin.call(t, "GET", "/api/v1/verdicts/999", nil, nil), http.StatusNotFound, "not_found")

	pr := strconv.FormatInt(v.Unit.Id, 10)
	var token NewToken
	admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token)
	reader := a.browser()
	reader.bearer = token.Token
	wantStatus(t, reader.call(t, "GET", "/api/v1/queue", nil, nil), http.StatusOK, "")
	wantStatus(t, reader.call(t, "POST", "/api/v1/prs/"+pr+"/nudged", nil, nil), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, admin.call(t, "POST", "/api/v1/prs/"+pr+"/nudged", nil, nil), http.StatusNoContent, "")
	wantStatus(t, admin.call(t, "POST", "/api/v1/prs/999/nudged", nil, nil), http.StatusNotFound, "not_found")
	var nudges int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM member_action WHERE kind = 'nudged'").Scan(&nudges); err != nil || nudges != 1 {
		t.Fatalf("%d nudges: %v", nudges, err)
	}
}
