package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
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

// Drafts read back and previewed over HTTP, against the hub's pull request, whose ball is Ana's, to fix its checks.
// Only a member with the rules permission lists, reads, or previews drafts; a read token reads them, and does not
// preview. The list holds the drafts not yet published, the one changed last first, each with who started it. A
// draft with problems is not previewed. A draft that makes failing checks and first reviews wait leaves nobody
// anything to do: its preview shows the pull request by its title, the ball with Ana to fix the checks before, and on
// the fallback after. The preview stores no verdict; once the draft is published, it leaves the list, and a draft
// started before then is behind it, yet previewed.
func TestRuleDraftsOverHTTP(t *testing.T) {
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
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "testhub", "name": "Hub", "base_address": "https://hub.example.test"}, &f)
	var inst Installation
	admin.call(t, "POST", "/api/v1/admin/installations", map[string]any{"forge_id": f.Id, "name": "acme", "kind": "token", "token": "t"}, &inst)
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/repos", map[string]any{"installation_id": inst.Id, "owner": "acme", "name": "api"}, nil),
		http.StatusCreated, "")
	var vs Verdicts
	for deadline := time.Now().Add(10 * time.Second); len(vs.Verdicts) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the hub's pull request got no verdict; log:\n%s", logs.String())
		}
		wantStatus(t, admin.call(t, "GET", "/api/v1/verdicts", nil, &vs), http.StatusOK, "")
	}

	var inv NewInvite
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	member := a.browser()
	wantStatus(t, member.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": "ana",
		"password": "a long enough password", "display_name": "Ana"}, nil), http.StatusCreated, "")
	member.refreshCSRF(t)
	var token NewToken
	wantStatus(t, admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token),
		http.StatusCreated, "")
	reader := a.browser()
	reader.bearer = token.Token

	var first, broken RuleSetDraft
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, &first), http.StatusCreated, "")
	invalid := rules.Default()
	invalid.Author[2].When = rules.Condition{Field: "pr.checks", Op: "is", Value: "failed"}
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{"rules": invalid}, &broken), http.StatusCreated, "")
	firstPath := "/api/v1/rulesets/drafts/" + strconv.FormatInt(first.Id, 10)
	brokenPath := "/api/v1/rulesets/drafts/" + strconv.FormatInt(broken.Id, 10)
	listed := func(b *browser) []int64 {
		t.Helper()
		var ds RuleSetDrafts
		wantStatus(t, b.call(t, "GET", "/api/v1/rulesets/drafts", nil, &ds), http.StatusOK, "")
		var out []int64
		for _, d := range ds.Drafts {
			if d.CreatedByName != "Root" {
				t.Fatalf("draft %d was started by %q", d.Id, d.CreatedByName)
			}
			out = append(out, d.Id)
		}
		return out
	}
	if got := listed(admin); len(got) != 2 || got[0] != broken.Id || got[1] != first.Id {
		t.Fatalf("drafts %v, want %d then %d", got, broken.Id, first.Id)
	}
	waiting := rules.Default()
	waiting.Author[2].Then = rules.Conclusion{Kind: rules.KindIdle, Reason: "Checks can wait"}
	for i, r := range waiting.Reviewer {
		if r.Name == "First review" {
			waiting.Reviewer[i].Then = rules.Conclusion{Kind: rules.KindIdle, Reason: "First reviews wait"}
		}
	}
	wantStatus(t, admin.call(t, "PUT", firstPath, map[string]any{"rules": waiting}, nil), http.StatusOK, "")
	if got := listed(reader); len(got) != 2 || got[0] != first.Id {
		t.Fatalf("drafts after a change %v, want %d first", got, first.Id)
	}
	var read RuleSetDraft
	wantStatus(t, admin.call(t, "GET", brokenPath, nil, &read), http.StatusOK, "")
	if len(read.Problems) != 1 || read.Problems[0].Code != "invalid_condition" || read.CreatedByName != "Root" {
		t.Fatalf("the broken draft %+v", read)
	}
	wantStatus(t, admin.call(t, "GET", "/api/v1/rulesets/drafts/999", nil, nil), http.StatusNotFound, "not_found")
	wantStatus(t, admin.call(t, "POST", brokenPath+"/preview", nil, nil), http.StatusBadRequest, "draft_has_problems")
	wantStatus(t, member.call(t, "GET", "/api/v1/rulesets/drafts", nil, nil), http.StatusForbidden, "forbidden")
	wantStatus(t, member.call(t, "GET", firstPath, nil, nil), http.StatusForbidden, "forbidden")
	wantStatus(t, member.call(t, "POST", firstPath+"/preview", nil, nil), http.StatusForbidden, "forbidden")
	wantStatus(t, reader.call(t, "POST", firstPath+"/preview", nil, nil), http.StatusForbidden, "insufficient_scope")

	var stored int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM verdict").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var p RuleSetPreview
	wantStatus(t, admin.call(t, "POST", firstPath+"/preview", nil, &p), http.StatusOK, "")
	if p.Version != 1 || p.EvaluatedUnits != 1 || len(p.Changes) != 1 || len(p.Fallback) != 1 {
		t.Fatalf("preview %+v", p)
	}
	c := p.Changes[0]
	before, after := c.Before, c.After
	if c.Unit.Kind != UnitKindPullRequest || c.Name != "Billing export" || before.Side != PreviewVerdictSideAuthor || before.Fallback ||
		len(before.Holders) != 1 || before.Holders[0].Account.Login != "ana" || before.Holders[0].Parts[0].Action == nil ||
		*before.Holders[0].Parts[0].Action != "fix_checks" || len(before.Parts) == 0 {
		t.Fatalf("before %+v", c)
	}
	if after.Side != PreviewVerdictSideAuthor || !after.Fallback || len(after.Holders) != 1 || after.Holders[0].Account.Login != "ana" ||
		after.Holders[0].Parts[0].Kind != HeldPartKindIdle || after.Holders[0].Parts[0].Reason != "Checks can wait" ||
		len(after.Parts[0].Path) == 0 {
		t.Fatalf("after %+v", after)
	}
	if fb := p.Fallback[0]; fb.Unit != c.Unit || fb.Name != "Billing export" || !fb.Verdict.Fallback {
		t.Fatalf("on the fallback %+v", fb)
	}
	var counted int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM verdict").Scan(&counted); err != nil || counted != stored {
		t.Fatalf("the preview stored verdicts: %d, then %d (%v)", stored, counted, err)
	}
	var behind RuleSetDraft
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, &behind), http.StatusCreated, "")
	wantStatus(t, admin.call(t, "POST", firstPath+"/publish", nil, nil), http.StatusCreated, "")
	if got := listed(admin); len(got) != 2 || got[0] != behind.Id || got[1] != broken.Id {
		t.Fatalf("drafts after publishing %v, want %d then %d", got, behind.Id, broken.Id)
	}
	wantStatus(t, admin.call(t, "GET", firstPath, nil, &read), http.StatusOK, "")
	if read.PublishedVersion == nil || *read.PublishedVersion != 2 {
		t.Fatalf("the published draft %+v", read)
	}

	// A draft of version 1 is behind version 2, and so cannot be published; it is still previewed, against version 2,
	// whose fallback it would undo.
	behindPath := "/api/v1/rulesets/drafts/" + strconv.FormatInt(behind.Id, 10)
	wantStatus(t, admin.call(t, "GET", behindPath, nil, &read), http.StatusOK, "")
	if len(read.Problems) != 1 || read.Problems[0].Code != "stale_draft" {
		t.Fatalf("the draft behind %+v", read.Problems)
	}
	p = RuleSetPreview{}
	wantStatus(t, admin.call(t, "POST", behindPath+"/preview", nil, &p), http.StatusOK, "")
	if p.Version != 2 || len(p.Changes) != 1 || !p.Changes[0].Before.Fallback || p.Changes[0].After.Fallback || len(p.Fallback) != 0 {
		t.Fatalf("the preview of the draft behind %+v", p)
	}
}

// A round of rules over HTTP. Every member reads the catalog, the questions, and the published versions, the default
// rule set being version 1; a read token reads them too. Only a member with the rules permission drafts and publishes,
// from a session. An imported rule-set file with a problem is saved, and lists it; a mistyped key is refused; once
// fixed, the draft publishes as version 2, and a draft of version 1 left behind is stale.
func TestRulesOverHTTP(t *testing.T) {
	a := newAPI(t)
	if _, err := a.rules.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := a.admin(t)
	var inv NewInvite
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	member := a.browser()
	wantStatus(t, member.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": "ana",
		"password": "a long enough password", "display_name": "Ana"}, nil), http.StatusCreated, "")
	member.refreshCSRF(t)

	var fields Fields
	wantStatus(t, member.call(t, "GET", "/api/v1/fields", nil, &fields), http.StatusOK, "")
	last := fields.Fields[len(fields.Fields)-1]
	if len(fields.Fields) != len(rules.Catalog()) || fields.Fields[0].Name != "pr.repo" || last.Type != FieldTypeJudgement ||
		last.Choices == nil || len(*last.Choices) != 2 || !slices.Equal(last.Operators, []string{"is"}) ||
		!slices.Equal(fields.Fields[0].Operators, []string{"is", "is_not", "starts_with", "contains"}) {
		t.Fatalf("fields %+v", fields)
	}
	var questions Questions
	wantStatus(t, member.call(t, "GET", "/api/v1/questions", nil, &questions), http.StatusOK, "")
	if len(questions.Questions) != 2 || questions.Questions[0].Role != QuestionRoleReviewer {
		t.Fatalf("questions %+v", questions)
	}
	var actions Actions
	wantStatus(t, member.call(t, "GET", "/api/v1/actions", nil, &actions), http.StatusOK, "")
	if len(actions.Actions) != len(rules.Actions) || actions.Actions[0].Name != "resolve_conflicts" || !actions.Actions[0].Task ||
		actions.Actions[4].Name != "review" || actions.Actions[4].Role != ActionRoleReviewer || actions.Actions[6].Task {
		t.Fatalf("actions %+v", actions)
	}
	var versions RuleSetVersions
	wantStatus(t, member.call(t, "GET", "/api/v1/rulesets", nil, &versions), http.StatusOK, "")
	if len(versions.Versions) != 1 || versions.Versions[0].Version != 1 || versions.Versions[0].PublishedBy != nil {
		t.Fatalf("versions %+v", versions)
	}
	var v1 RuleSetVersion
	wantStatus(t, member.call(t, "GET", "/api/v1/rulesets/1", nil, &v1), http.StatusOK, "")
	if len(v1.Rules.Author) != 20 || len(v1.Rules.Reviewer) != 19 || v1.Rules.Author[0].Name != "Draft" {
		t.Fatalf("version 1: %d author, %d reviewer rules", len(v1.Rules.Author), len(v1.Rules.Reviewer))
	}
	wantStatus(t, member.call(t, "GET", "/api/v1/rulesets/9", nil, nil), http.StatusNotFound, "not_found")

	wantStatus(t, member.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, nil), http.StatusForbidden, "forbidden")
	var token NewToken
	wantStatus(t, member.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token),
		http.StatusCreated, "")
	reader := a.browser()
	reader.bearer = token.Token
	wantStatus(t, reader.call(t, "GET", "/api/v1/rulesets/1", nil, nil), http.StatusOK, "")
	wantStatus(t, reader.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, nil), http.StatusForbidden, "insufficient_scope")

	broken := rules.Default()
	broken.Author[2].When = rules.Condition{Field: "pr.checks", Op: "is", Value: "failed"}
	var draft RuleSetDraft
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{"rules": broken}, &draft), http.StatusCreated, "")
	if p := draft.Problems; draft.BaseVersion == nil || *draft.BaseVersion != 1 || len(p) != 1 || p[0].Code != "invalid_condition" ||
		p[0].Role == nil || *p[0].Role != ProblemRoleAuthor || p[0].Rule == nil || *p[0].Rule != 3 || p[0].Part != ProblemPartWhen {
		t.Fatalf("draft %+v", draft)
	}
	path := "/api/v1/rulesets/drafts/" + strconv.FormatInt(draft.Id, 10)
	wantStatus(t, admin.call(t, "POST", path+"/publish", nil, nil), http.StatusBadRequest, "draft_has_problems")
	mistyped := map[string]any{"rules": map[string]any{"author": []any{map[string]any{"name": "All", "when": map[string]any{"always": true},
		"then": map[string]any{"kind": "idle", "reason": "idle", "remnd": true}}}, "reviewer": []any{}}}
	wantStatus(t, admin.call(t, "PUT", path, mistyped, nil), http.StatusBadRequest, "invalid_rule_set")

	fixed := rules.Default()
	fixed.Author[2].Then.Reason = "{size(pr.failing_checks)} required checks fail"
	wantStatus(t, admin.call(t, "PUT", path, map[string]any{"rules": fixed}, &draft), http.StatusOK, "")
	if len(draft.Problems) != 0 {
		t.Fatalf("problems %+v", draft.Problems)
	}
	var behind RuleSetDraft
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, &behind), http.StatusCreated, "")
	var v2 RuleSetVersion
	wantStatus(t, admin.call(t, "POST", path+"/publish", nil, &v2), http.StatusCreated, "")
	if v2.Version != 2 || v2.BasedOn == nil || *v2.BasedOn != 1 || v2.PublishedBy == nil ||
		v2.Rules.Author[2].Then.Reason != "{size(pr.failing_checks)} required checks fail" {
		t.Fatalf("version 2: %+v", v2)
	}
	wantStatus(t, admin.call(t, "POST", path+"/publish", nil, nil), http.StatusConflict, "draft_published")
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts/"+strconv.FormatInt(behind.Id, 10)+"/publish", nil, nil),
		http.StatusConflict, "stale_draft")
	wantStatus(t, member.call(t, "GET", "/api/v1/rulesets", nil, &versions), http.StatusOK, "")
	if len(versions.Versions) != 2 || versions.Versions[0].Version != 2 {
		t.Fatalf("versions %+v", versions)
	}
}

// Discarding drafts over HTTP. A draft not yet published is discarded by any member with the rules permission — Ana,
// once given it, discards one Root started — and is gone: neither listed nor read. A published draft is kept, as the
// record of its version. A member without the permission, a read token, and an unknown draft are refused.
func TestDiscardRuleSetDraftOverHTTP(t *testing.T) {
	a := newAPI(t)
	if _, err := a.rules.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := a.admin(t)
	var inv NewInvite
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	ana := a.browser()
	wantStatus(t, ana.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": "ana",
		"password": "a long enough password", "display_name": "Ana"}, nil), http.StatusCreated, "")
	ana.refreshCSRF(t)
	var token NewToken
	wantStatus(t, admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "scopes": []string{"read"}}, &token),
		http.StatusCreated, "")
	reader := a.browser()
	reader.bearer = token.Token

	var kept, left RuleSetDraft
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, &kept), http.StatusCreated, "")
	keptPath := "/api/v1/rulesets/drafts/" + strconv.FormatInt(kept.Id, 10)
	wantStatus(t, admin.call(t, "POST", keptPath+"/publish", nil, nil), http.StatusCreated, "")
	wantStatus(t, admin.call(t, "POST", "/api/v1/rulesets/drafts", map[string]any{}, &left), http.StatusCreated, "")
	leftPath := "/api/v1/rulesets/drafts/" + strconv.FormatInt(left.Id, 10)

	wantStatus(t, ana.call(t, "DELETE", leftPath, nil, nil), http.StatusForbidden, "forbidden")
	wantStatus(t, reader.call(t, "DELETE", leftPath, nil, nil), http.StatusForbidden, "insufficient_scope")
	wantStatus(t, admin.call(t, "DELETE", "/api/v1/rulesets/drafts/999", nil, nil), http.StatusNotFound, "not_found")
	wantStatus(t, admin.call(t, "DELETE", keptPath, nil, nil), http.StatusConflict, "draft_published")

	var members Members
	wantStatus(t, admin.call(t, "GET", "/api/v1/admin/members", nil, &members), http.StatusOK, "")
	for _, m := range members.Members {
		if m.DisplayName == "Ana" {
			wantStatus(t, admin.call(t, "PATCH", "/api/v1/admin/members/"+strconv.FormatInt(m.Id, 10), map[string]any{"rules": true}, nil),
				http.StatusOK, "")
		}
	}
	wantStatus(t, ana.call(t, "DELETE", leftPath, nil, nil), http.StatusNoContent, "")
	wantStatus(t, admin.call(t, "GET", leftPath, nil, nil), http.StatusNotFound, "not_found")
	wantStatus(t, admin.call(t, "DELETE", leftPath, nil, nil), http.StatusNotFound, "not_found")
	var ds RuleSetDrafts
	wantStatus(t, admin.call(t, "GET", "/api/v1/rulesets/drafts", nil, &ds), http.StatusOK, "")
	if len(ds.Drafts) != 0 {
		t.Fatalf("drafts after discarding: %+v", ds.Drafts)
	}
	var read RuleSetDraft
	wantStatus(t, admin.call(t, "GET", keptPath, nil, &read), http.StatusOK, "")
	if read.PublishedVersion == nil || *read.PublishedVersion != 2 {
		t.Fatalf("the published draft %+v", read)
	}
}
