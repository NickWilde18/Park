package rules

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// neutral is every field of the catalog but judgements at a neutral value, for a person of a role.
func neutral(role Role) map[string]any {
	m := map[string]any{}
	for _, f := range Catalog() {
		switch f.Type {
		case Boolean:
			m[f.Name] = false
		case Number:
			m[f.Name] = int64(0)
		case OptionalNumber:
			m[f.Name] = nil
		case Choice:
			m[f.Name] = f.Choices[len(f.Choices)-1]
		case Text, Account:
			m[f.Name] = ""
		case Judgement:
		default:
			m[f.Name] = []string{}
		}
	}
	m["me.role"] = string(role)
	return m
}

func rest() Rule {
	return Rule{Name: "Rest", When: Condition{Always: true}, Then: Conclusion{Kind: KindIdle, Reason: "rest"}}
}

// reviewerList compiles a rule set whose reviewer list is the given rules, then a catch-all.
func reviewerList(t *testing.T, rules ...Rule) *Compiled {
	t.Helper()
	c, problems := Compile(RuleSet{Author: []Rule{rest()}, Reviewer: append(rules, rest())})
	if len(problems) > 0 {
		t.Fatalf("problems: %+v", problems)
	}
	return c
}

// input is a reviewer's input: neutral fields with the given ones on top.
func input(fields map[string]any, judgements map[string]string) Input {
	in := Input{Fields: neutral(Reviewer), Judgements: judgements}
	for k, v := range fields {
		in.Fields[k] = v
	}
	return in
}

func term(field, op string, value any) Condition {
	return Condition{Field: field, Op: op, Value: value}
}

// Each structured operator means what the chapter says, and compiles to CEL that says so. A comparison on an optional
// number that is not set is false — ne included — and a condition on a judgement not known is unknown.
func TestStructuredTerms(t *testing.T) {
	five := int64(5)
	for _, c := range []struct {
		name       string
		when       Condition
		fields     map[string]any
		judgements map[string]string
		want       Outcome
	}{
		{"is_true", term("pr.draft", "is_true", nil), map[string]any{"pr.draft": true}, nil, Matched},
		{"is_true, false", term("pr.draft", "is_true", nil), nil, nil, NotMatched},
		{"is_false", term("pr.draft", "is_false", nil), nil, nil, Matched},
		{"eq", term("pr.approvals", "eq", 2.0), map[string]any{"pr.approvals": int64(2)}, nil, Matched},
		{"ne", term("pr.approvals", "ne", 2.0), map[string]any{"pr.approvals": int64(2)}, nil, NotMatched},
		{"lt", term("pr.approvals", "lt", 2.0), map[string]any{"pr.approvals": int64(1)}, nil, Matched},
		{"le", term("pr.approvals", "le", 2.0), map[string]any{"pr.approvals": int64(2)}, nil, Matched},
		{"gt", term("pr.approvals", "gt", 2.0), map[string]any{"pr.approvals": int64(2)}, nil, NotMatched},
		{"ge", term("pr.approvals", "ge", 2.0), map[string]any{"pr.approvals": int64(3)}, nil, Matched},
		{"ge another field, below", term("pr.approvals", "ge", map[string]any{"field": "pr.required_approvals"}),
			map[string]any{"pr.approvals": int64(1), "pr.required_approvals": int64(2)}, nil, NotMatched},
		{"ge another field, reached", term("pr.approvals", "ge", map[string]any{"field": "pr.required_approvals"}),
			map[string]any{"pr.approvals": int64(2), "pr.required_approvals": int64(2)}, nil, Matched},
		{"optional eq, not set", term("me.hours_since_review", "eq", 5.0), nil, nil, NotMatched},
		{"optional ne, not set", term("me.hours_since_review", "ne", 5.0), nil, nil, NotMatched},
		{"optional lt, not set", term("me.hours_since_review", "lt", 5.0), nil, nil, NotMatched},
		{"optional ge, not set", term("me.hours_since_review", "ge", 5.0), nil, nil, NotMatched},
		{"optional is_set, not set", term("me.hours_since_review", "is_set", nil), nil, nil, NotMatched},
		{"optional is_not_set, not set", term("me.hours_since_review", "is_not_set", nil), nil, nil, Matched},
		{"optional eq, set", term("me.hours_since_review", "eq", 5.0), map[string]any{"me.hours_since_review": five}, nil, Matched},
		{"optional ne, set", term("me.hours_since_review", "ne", 5.0), map[string]any{"me.hours_since_review": five}, nil, NotMatched},
		{"optional is_set, set", term("me.hours_since_review", "is_set", nil), map[string]any{"me.hours_since_review": five}, nil, Matched},
		{"optional against optional", term("me.hours_since_review", "lt", map[string]any{"field": "me.hours_since_nudge"}),
			map[string]any{"me.hours_since_review": int64(3), "me.hours_since_nudge": five}, nil, Matched},
		{"optional against one not set", term("me.hours_since_review", "lt", map[string]any{"field": "me.hours_since_nudge"}),
			map[string]any{"me.hours_since_review": int64(3)}, nil, NotMatched},
		{"choice is", term("pr.checks", "is", "failing"), map[string]any{"pr.checks": "failing"}, nil, Matched},
		{"choice is_not", term("pr.checks", "is_not", "failing"), map[string]any{"pr.checks": "failing"}, nil, NotMatched},
		{"choice is_one_of", term("pr.checks", "is_one_of", []any{"failing", "pending"}), map[string]any{"pr.checks": "pending"}, nil, Matched},
		{"text is", term("pr.base", "is", "main"), map[string]any{"pr.base": "main"}, nil, Matched},
		{"text starts_with", term("pr.base", "starts_with", "release/"), map[string]any{"pr.base": "release/2.0"}, nil, Matched},
		{"text contains", term("pr.title", "contains", "[wip]"), map[string]any{"pr.title": "Billing [wip]"}, nil, Matched},
		{"account is", term("pr.author", "is", "ana"), map[string]any{"pr.author": "ana"}, nil, Matched},
		{"list is_empty", term("pr.labels", "is_empty", nil), nil, nil, Matched},
		{"list is_not_empty", term("pr.labels", "is_not_empty", nil), map[string]any{"pr.labels": []string{"bug"}}, nil, Matched},
		{"list contains", term("pr.labels", "contains", "bug"), map[string]any{"pr.labels": []string{"docs", "bug"}}, nil, Matched},
		{"choice list contains", term("pr.merge_blocked_by", "contains", "checks"), map[string]any{"pr.merge_blocked_by": []string{"reviews"}}, nil, NotMatched},
		{"judgement not known", term("judgement.comments_blocking", "is", "yes"), nil, nil, Unknown},
		{"judgement yes", term("judgement.comments_blocking", "is", "yes"), nil, map[string]string{"comments_blocking": "yes"}, Matched},
		{"judgement no", term("judgement.comments_blocking", "is", "yes"), nil, map[string]string{"comments_blocking": "no"}, NotMatched},
		{"a judgement a false term makes moot", Condition{All: []Condition{term("pr.draft", "is_true", nil),
			term("judgement.comments_blocking", "is", "yes")}}, nil, nil, NotMatched},
		{"any of all", Condition{Any: []Condition{term("pr.draft", "is_true", nil), {All: []Condition{
			term("pr.checks", "is", "failing"), term("pr.conflicting", "is_true", nil)}}}},
			map[string]any{"pr.checks": "failing", "pr.conflicting": true}, nil, Matched},
		{"the chapter's expression", Condition{Expr: `pr.checks == 'failing' && pr.failing_checks.all(c, c.startsWith('e2e-'))`},
			map[string]any{"pr.checks": "failing", "pr.failing_checks": []string{"e2e-login", "e2e-pay"}}, nil, Matched},
		{"the chapter's expression, a unit test failing", Condition{Expr: `pr.checks == 'failing' && pr.failing_checks.all(c, c.startsWith('e2e-'))`},
			map[string]any{"pr.checks": "failing", "pr.failing_checks": []string{"e2e-login", "unit"}}, nil, NotMatched},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rules := reviewerList(t, Rule{Name: "Tried", When: c.when, Then: Conclusion{Kind: KindIdle, Reason: "tried"}})
			step := rules.Evaluate(Reviewer, input(c.fields, c.judgements)).Path[0]
			if step.Outcome != c.want || step.Error != "" {
				t.Fatalf("outcome %s %q, want %s", step.Outcome, step.Error, c.want)
			}
		})
	}
}

// The first rule whose condition matches decides, and the rules after it are never tried (R1); the path shows every
// rule tried, with its passed-over label.
func TestFirstMatchWins(t *testing.T) {
	rules := reviewerList(t,
		Rule{Name: "Draft", PassedOver: "not a draft", When: term("pr.draft", "is_true", nil), Then: Conclusion{Kind: KindIdle, Reason: "Draft"}},
		Rule{Name: "First review", When: term("me.reviewed", "is_false", nil), Then: Conclusion{Kind: KindAct, Action: "review", Task: true,
			Priority: PriorityLow, Reason: "First review"}},
		Rule{Name: "Also unreviewed", When: term("me.reviewed", "is_false", nil), Then: Conclusion{Kind: KindIdle, Reason: "never"}},
	)
	part := rules.Evaluate(Reviewer, input(nil, nil))
	if part.Rule != 2 || part.Kind != KindAct || part.Action != "review" || !part.Task || part.Priority != PriorityLow ||
		part.Reason != "First review" {
		t.Fatalf("part %+v", part)
	}
	if len(part.Path) != 2 || part.Path[0].Outcome != NotMatched || part.Path[0].PassedOver != "not a draft" ||
		part.Path[1].Outcome != Matched {
		t.Fatalf("path %+v", part.Path)
	}
}

// Evaluation is total (R5): a condition that fails at run time, exceeds its cost limit, reads a field the input lacks,
// or depends on a judgement not known does not match; the path records why, and the next rule decides.
func TestEvaluationIsTotal(t *testing.T) {
	labels := make([]string, 40)
	for i := range labels {
		labels[i] = "label"
	}
	for _, c := range []struct {
		name  string
		when  Condition
		in    Input
		want  Outcome
		error string
	}{
		{"an optional that is not set, dereferenced", Condition{Expr: "me.hours_since_review.value() > 1"}, input(nil, nil), Failed,
			"optional.none() dereference"},
		{"division by zero", Condition{Expr: "10 / me.threads_open > 1"}, input(nil, nil), Failed, "division by zero"},
		{"past the cost limit", Condition{Expr: "pr.labels.map(a, pr.labels.map(b, pr.labels.map(c, a + b + c))).size() > 0"},
			input(map[string]any{"pr.labels": labels}, nil), Failed, "cost limit exceeded"},
		{"a field the input lacks", term("pr.draft", "is_true", nil), Input{Fields: map[string]any{}}, Failed, "pr.draft"},
		{"a judgement not known", Condition{All: []Condition{term("me.requested", "is_true", nil),
			term("judgement.commits_address_my_comments", "is", "yes")}}, input(map[string]any{"me.requested": true}, nil), Unknown, ""},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rules := reviewerList(t, Rule{Name: "Tried", When: c.when, Then: Conclusion{Kind: KindAct, Action: "review", Reason: "tried"}})
			part := rules.Evaluate(Reviewer, c.in)
			step := part.Path[0]
			if step.Outcome != c.want || !strings.Contains(step.Error, c.error) {
				t.Fatalf("step %+v, want %s with %q", step, c.want, c.error)
			}
			if part.Rule != 2 || part.Kind != KindIdle || len(part.Path) != 2 {
				t.Fatalf("the next rule did not decide: %+v", part)
			}
			if c.want == Unknown && (len(step.Judgements) != 1 || step.Judgements[0] != "commits_address_my_comments") {
				t.Fatalf("the unknown judgement is not named: %+v", step)
			}
		})
	}
}

// Reasons fill their expressions in: lists as "a, b and c", optionals by their value or nothing, and {{ }} as
// braces. A reason that cannot be shown leaves the rule's name as the reason, and says why.
func TestReasons(t *testing.T) {
	for _, c := range []struct {
		reason, want string
		fields       map[string]any
		failed       bool
	}{
		{"{pr.blocked_by} requested changes", "ben and carol requested changes", map[string]any{"pr.blocked_by": []string{"ben", "carol"}}, false},
		{"{pr.blocked_by}", "ben, carol and dave", map[string]any{"pr.blocked_by": []string{"ben", "carol", "dave"}}, false},
		{"Asked {pr.blocked_by}", "Asked ben", map[string]any{"pr.blocked_by": []string{"ben"}}, false},
		{"{me.threads_on_me + pr.unanswered_reviews} items", "3 items", map[string]any{"me.threads_on_me": int64(2),
			"pr.unanswered_reviews": int64(1)}, false},
		{"running for {pr.hours_checks_pending} hours", "running for 5 hours", map[string]any{"pr.hours_checks_pending": int64(5)}, false},
		{"[{pr.hours_checks_pending}]", "[]", nil, false},
		{"{pr.draft} and {1.5}", "false and 1.5", nil, false},
		{"{{literal}} {size(pr.labels)}", "{literal} 0", nil, false},
		{"{pr.hours_requested.value()} hours", "Tried", nil, true},
		{"{judgement.comments_blocking}", "Tried", nil, true},
	} {
		rules := reviewerList(t, Rule{Name: "Tried", When: Condition{Always: true}, Then: Conclusion{Kind: KindIdle, Reason: c.reason}})
		part := rules.Evaluate(Reviewer, input(c.fields, nil))
		if part.Reason != c.want || (part.ReasonError != "") != c.failed {
			t.Errorf("%q: %q (error %q), want %q", c.reason, part.Reason, part.ReasonError, c.want)
		}
	}
}

// The default rule set gives every case of testdata/default_cases.json the rule the chapter's tables give it. The
// cases are drawn from the design checks' states by docs/design/checks/default_rules.py.
func TestDefaultCases(t *testing.T) {
	data, err := os.ReadFile("testdata/default_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Base  map[string]any `json:"base"`
		Cases []struct {
			Fields     map[string]any    `json:"fields"`
			Judgements map[string]string `json:"judgements"`
			From       string            `json:"from"`
			Role       Role              `json:"role"`
			Kind       string            `json:"kind"`
			Action     string            `json:"action"`
			Rule       int               `json:"rule"`
		} `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	c, problems := Compile(Default())
	if len(problems) > 0 {
		t.Fatalf("problems: %+v", problems)
	}
	reached := map[Role]map[int]bool{Author: {}, Reviewer: {}}
	for i, tc := range file.Cases {
		in := Input{Fields: map[string]any{"me.role": string(tc.Role)}, Judgements: tc.Judgements}
		for _, fields := range []map[string]any{file.Base, tc.Fields} {
			for k, v := range fields {
				in.Fields[k] = goValue(t, v)
			}
		}
		part := c.Evaluate(tc.Role, in)
		if part.Rule != tc.Rule || part.Kind != tc.Kind || part.Action != tc.Action {
			t.Errorf("case %d (%s, %s): rule %d %s %q, the tables give %d %s %q; path %+v", i, tc.From, tc.Role, part.Rule,
				part.Kind, part.Action, tc.Rule, tc.Kind, tc.Action, part.Path)
		}
		for _, s := range part.Path {
			if s.Outcome == Failed || part.ReasonError != "" {
				t.Errorf("case %d: %+v, reason error %q", i, s, part.ReasonError)
			}
		}
		reached[tc.Role][part.Rule] = true
	}
	for _, role := range Roles {
		if len(reached[role]) != len(Default().List(role)) {
			t.Errorf("the %s cases reach %d of %d rules", role, len(reached[role]), len(Default().List(role)))
		}
	}
}

// goValue turns a decoded JSON value into what an input holds.
func goValue(t *testing.T, v any) any {
	switch v := v.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			t.Fatal(err)
		}
		return n
	case []any:
		out := make([]string, len(v))
		for i, s := range v {
			out[i], _ = s.(string)
		}
		return out
	}
	return v
}
