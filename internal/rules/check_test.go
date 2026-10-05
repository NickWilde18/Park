package rules

import (
	"strings"
	"testing"

	"github.com/NickWilde18/Park/internal/fail"
)

func code(err error) string {
	if f, ok := fail.As(err); ok {
		return f.Code
	}
	return ""
}

// Every default rule parses and type-checks, and the default rule set passes every publish check.
func TestDefaultRuleSet(t *testing.T) {
	rs := Default()
	if len(rs.Author) != 20 || len(rs.Reviewer) != 20 {
		t.Fatalf("%d author and %d reviewer rules", len(rs.Author), len(rs.Reviewer))
	}
	c, problems := Compile(rs)
	if c == nil || len(problems) > 0 {
		t.Fatalf("problems: %+v", problems)
	}
}

// Each publish check rejects the kind of draft it exists for, naming the list, the rule, and the part; a draft with
// any problem does not compile for evaluation.
func TestPublishChecks(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(rs *RuleSet)
		role   Role
		rule   int
		part   string
		code   string
		says   string
	}{
		{"an empty list", func(rs *RuleSet) { rs.Reviewer = nil }, Reviewer, 0, PartList, "empty_list", "no rules"},
		{"no catch-all", func(rs *RuleSet) { rs.Author = rs.Author[:19] }, Author, 19, PartWhen, "no_catch_all", "catch-all"},
		{"a catch-all that is an expression", func(rs *RuleSet) { rs.Author[19].When = Condition{Expr: "true"} },
			Author, 20, PartWhen, "no_catch_all", "catch-all"},
		{"no name", func(rs *RuleSet) { rs.Author[0].Name = " " }, Author, 1, PartName, "invalid_name", "name"},
		{"an unknown field", func(rs *RuleSet) { rs.Author[0].When = term("pr.nope", "is_true", nil) },
			Author, 1, PartWhen, "invalid_condition", `no field "pr.nope"`},
		{"an operator the type does not take", func(rs *RuleSet) { rs.Author[0].When = term("pr.draft", "gt", 1.0) },
			Author, 1, PartWhen, "invalid_condition", "pr.draft is boolean, which takes is_true, is_false"},
		{"a choice's value that is not one", func(rs *RuleSet) { rs.Author[2].When = term("pr.checks", "is", "failed") },
			Author, 3, PartWhen, "invalid_condition", "one of failing, pending, passing, none"},
		{"a number that is not whole", func(rs *RuleSet) { rs.Author[3].When.All[1].Value = 1.5 },
			Author, 4, PartWhen, "invalid_condition", "whole number"},
		{"a value where none is taken", func(rs *RuleSet) { rs.Author[0].When.Value = true },
			Author, 1, PartWhen, "invalid_condition", "takes no value"},
		{"two forms at once", func(rs *RuleSet) { rs.Author[0].When.Always = true },
			Author, 1, PartWhen, "invalid_condition", "exactly one of"},
		{"all of nothing", func(rs *RuleSet) { rs.Author[3].When = Condition{All: []Condition{}} },
			Author, 4, PartWhen, "invalid_condition", "at least one"},
		{"an expression inside a structured condition", func(rs *RuleSet) {
			rs.Author[3].When.All[1] = Condition{Expr: "true"}
		}, Author, 4, PartWhen, "invalid_condition", "whole condition"},
		{"an expression that does not parse", func(rs *RuleSet) { rs.Author[0].When = Condition{Expr: "pr.draft &&"} },
			Author, 1, PartWhen, "invalid_condition", "Syntax error"},
		{"an expression that is not true or false", func(rs *RuleSet) { rs.Author[0].When = Condition{Expr: "size(pr.labels)"} },
			Author, 1, PartWhen, "invalid_condition", "gives int"},
		{"a judgement not asked for the role", func(rs *RuleSet) {
			rs.Author[0].When = term("judgement.commits_address_my_comments", "is", "yes")
		}, Author, 1, PartWhen, "judgement_role", "not asked for the author"},
		{"a judgement no question asks", func(rs *RuleSet) { rs.Reviewer[11].When.All[3].Field = "judgement.nope" },
			Reviewer, 12, PartWhen, "invalid_condition", `no field "judgement.nope"`},
		{"a judgement in the author's reason", func(rs *RuleSet) { rs.Author[0].Then.Reason = "{judgement.comments_blocking}" },
			Author, 1, PartReason, "judgement_role", "not asked for the author"},
		{"a reason that does not compile", func(rs *RuleSet) { rs.Author[2].Then.Reason = "{size(pr.failing)} failing" },
			Author, 3, PartReason, "invalid_reason", "undeclared reference"},
		{"a reason with a brace that never closes", func(rs *RuleSet) { rs.Author[0].Then.Reason = "Draft {pr.draft" },
			Author, 1, PartReason, "invalid_reason", "never closes"},
		{"a reason with a stray brace", func(rs *RuleSet) { rs.Author[0].Then.Reason = "Draft }" },
			Author, 1, PartReason, "invalid_reason", "outside an expression"},
		{"a reason that cannot be shown", func(rs *RuleSet) { rs.Author[0].Then.Reason = `{ {"a": 1} }` },
			Author, 1, PartReason, "invalid_reason", "a reason shows"},
		{"no reason", func(rs *RuleSet) { rs.Author[0].Then.Reason = "" }, Author, 1, PartThen, "missing_reason", "reason"},
		{"an unknown kind", func(rs *RuleSet) { rs.Author[0].Then.Kind = "wait" }, Author, 1, PartThen, "invalid_kind", "kind"},
		{"a reviewer's hold", func(rs *RuleSet) { rs.Reviewer[0].Then.Kind = KindHold }, Reviewer, 1, PartThen, "reviewer_kind", "act or idle"},
		{"a reviewer's finish", func(rs *RuleSet) { rs.Reviewer[0].Then.Kind = KindFinish }, Reviewer, 1, PartThen, "reviewer_kind", "act or idle"},
		{"act without an action", func(rs *RuleSet) { rs.Author[1].Then.Action, rs.Author[1].Then.Task = "", false },
			Author, 2, PartThen, "missing_action", "action"},
		{"an action there is not", func(rs *RuleSet) { rs.Author[1].Then.Action = "rebase" }, Author, 2, PartThen, "action_not_allowed", `"rebase"`},
		{"another role's action", func(rs *RuleSet) { rs.Reviewer[3].Then.Action = "fix_checks" },
			Reviewer, 4, PartThen, "action_not_allowed", "for the author"},
		{"an action on hold", func(rs *RuleSet) { rs.Author[0].Then.Action = "fix_checks" }, Author, 1, PartThen, "action_not_allowed", "no action"},
		{"a finish that is not merging", func(rs *RuleSet) { rs.Author[14].Then.Action = "unblock" },
			Author, 15, PartThen, "action_not_allowed", "merge, or none"},
		{"a reminder not on idle", func(rs *RuleSet) { rs.Author[1].Then.Remind = true }, Author, 2, PartThen, "remind_not_idle", "idle"},
		{"a task of an action that cannot be one", func(rs *RuleSet) { rs.Author[15].Then.Task = true },
			Author, 16, PartThen, "task_not_allowed", "can become tasks"},
		{"a task with no action", func(rs *RuleSet) { rs.Author[19].Then.Task = true }, Author, 20, PartThen, "task_not_allowed", "can become tasks"},
		{"targets on an action that has none", func(rs *RuleSet) { rs.Reviewer[5].Then.Targets = "pr.blocked_by" },
			Reviewer, 6, PartThen, "invalid_targets", "Only request_review"},
		{"targets that are not accounts", func(rs *RuleSet) { rs.Author[5].Then.Targets = "pr.labels" },
			Author, 6, PartThen, "invalid_targets", "list-of-accounts"},
		{"an unknown priority", func(rs *RuleSet) { rs.Reviewer[3].Then.Priority = "urgent" }, Reviewer, 4, PartThen, "invalid_priority", "normal or low"},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rs := Default()
			c.change(&rs)
			compiled, problems := Compile(rs)
			if compiled != nil {
				t.Error("a set with problems compiled for evaluation")
			}
			for _, p := range problems {
				if p.Role == c.role && p.Rule == c.rule && p.Part == c.part && p.Code == c.code && strings.Contains(p.Message, c.says) {
					return
				}
			}
			t.Fatalf("no %s problem of %s %d %s saying %q: %+v", c.code, c.role, c.rule, c.part, c.says, problems)
		})
	}
}

// A rule-set document is read strictly: an unknown key is refused rather than dropped.
func TestParse(t *testing.T) {
	if _, err := Parse([]byte(`{"author": [], "reviewer": [], "admin": []}`)); code(err) != "invalid_rule_set" {
		t.Fatalf("an unknown list: %v", err)
	}
	if _, err := Parse([]byte(`{"author": [{"name": "x", "when": {"always": true}, "then": {"kind": "idle", "reason": "x", "later": true}}], "reviewer": []}`)); code(err) != "invalid_rule_set" {
		t.Fatalf("an unknown conclusion part: %v", err)
	}
	rs, err := Parse(defaultRules)
	if err != nil || len(rs.Author) != 20 {
		t.Fatalf("the default: %v", err)
	}
}
