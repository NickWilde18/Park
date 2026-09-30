package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/NickWilde18/Park/internal/rules"
)

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
		last.Choices == nil || len(*last.Choices) != 2 {
		t.Fatalf("fields %+v", fields)
	}
	var questions Questions
	wantStatus(t, member.call(t, "GET", "/api/v1/questions", nil, &questions), http.StatusOK, "")
	if len(questions.Questions) != 2 || questions.Questions[0].Role != QuestionRoleReviewer {
		t.Fatalf("questions %+v", questions)
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
