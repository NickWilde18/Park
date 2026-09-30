package rules

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
)

// The parts of a rule set a problem is about.
const (
	PartList   = "list"
	PartName   = "name"
	PartWhen   = "when"
	PartThen   = "then"
	PartReason = "reason"
	PartBase   = "base"
)

// Problem is a reason a rule set cannot be published: docs/design/05-rules.md#editing-and-publishing.
type Problem struct {
	Role    Role   `json:"role,omitempty"`
	Part    string `json:"part"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Rule is the rule's number in its list, from 1; 0 for a problem of the list or of the whole set.
	Rule int `json:"rule,omitempty"`
}

// compiled is a rule, ready to evaluate.
type compiled struct {
	when   cel.Program
	reason []segment
	rule   Rule
}

// Compiled is a rule set compiled for evaluation.
type Compiled struct {
	lists map[Role][]compiled
}

// Compile runs every publish check but the base version's, and compiles the rule set. It returns the problems that
// keep the set from being published; the compiled set is nil when there are any.
func Compile(rs RuleSet) (*Compiled, []Problem) {
	c := &Compiled{lists: map[Role][]compiled{}}
	var problems []Problem
	for _, role := range Roles {
		list := rs.List(role)
		if len(list) == 0 {
			problems = append(problems, Problem{Role: role, Part: PartList, Code: "empty_list",
				Message: fmt.Sprintf("The %s list has no rules.", role)})
			continue
		}
		for i, r := range list {
			add := func(part, code, message string) {
				problems = append(problems, Problem{Role: role, Rule: i + 1, Part: part, Code: code, Message: message})
			}
			if n := utf8.RuneCountInString(strings.TrimSpace(r.Name)); n < 1 || n > 100 {
				add(PartName, "invalid_name", "A rule's name is 1 to 100 characters long.")
			}
			if utf8.RuneCountInString(r.PassedOver) > 100 {
				add(PartName, "invalid_name", "A passed-over label is at most 100 characters long.")
			}
			when, judgements, err := compileCondition(r.When)
			if err != nil {
				add(PartWhen, "invalid_condition", err.Error())
			}
			for _, q := range foreign(role, judgements) {
				add(PartWhen, "judgement_role", fmt.Sprintf("judgement.%s is not asked for the %s.", q, role))
			}
			reason, judgements, err := compileTemplate(r.Then.Reason)
			if err != nil {
				add(PartReason, "invalid_reason", err.Error())
			}
			for _, q := range foreign(role, judgements) {
				add(PartReason, "judgement_role", fmt.Sprintf("judgement.%s is not asked for the %s.", q, role))
			}
			for _, p := range conclusionProblems(role, r.Then) {
				add(PartThen, p[0], p[1])
			}
			c.lists[role] = append(c.lists[role], compiled{when: when, reason: reason, rule: r})
		}
		if last := list[len(list)-1]; !last.When.Always {
			problems = append(problems, Problem{Role: role, Rule: len(list), Part: PartWhen, Code: "no_catch_all",
				Message: fmt.Sprintf(`The %s list ends with a catch-all rule: "when": {"always": true}.`, role)})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return c, nil
}

// foreign are the questions among qs that are not asked for a role.
func foreign(role Role, qs []string) []string {
	var out []string
	for _, name := range qs {
		for _, q := range Questions {
			if q.Name == name && q.Role != role {
				out = append(out, name)
			}
		}
	}
	return out
}

// conclusionProblems checks that a conclusion is complete, as codes and messages.
func conclusionProblems(role Role, t Conclusion) [][2]string {
	var out [][2]string
	add := func(code, message string) { out = append(out, [2]string{code, message}) }
	switch t.Kind {
	case KindAct, KindIdle:
	case KindHold, KindFinish:
		if role == Reviewer {
			add("reviewer_kind", "A reviewer's part is act or idle; hold and finish are the author's.")
		}
	default:
		add("invalid_kind", "A part's kind is act, hold, finish, or idle.")
	}
	a, known := actions[t.Action]
	switch {
	case t.Action == "":
		if t.Kind == KindAct {
			add("missing_action", "An act part names the action to take.")
		}
	case !known:
		add("action_not_allowed", fmt.Sprintf("There is no action %q.", t.Action))
	case a.Role != role:
		add("action_not_allowed", fmt.Sprintf("%s is an action for the %s.", t.Action, a.Role))
	case t.Kind == KindFinish && t.Action != "merge":
		add("action_not_allowed", "A finish part's action is merge, or none.")
	case t.Kind == KindHold || t.Kind == KindIdle:
		add("action_not_allowed", "A hold or idle part has no action.")
	}
	if t.Remind && t.Kind != KindIdle {
		add("remind_not_idle", "Only an idle part reminds.")
	}
	if t.Task && !a.Task {
		add("task_not_allowed", "Only resolve_conflicts, fix_checks, address_comments, request_review, and review can become tasks.")
	}
	if t.Targets != "" {
		f := catalog[t.Targets]
		switch {
		case t.Action != "request_review":
			add("invalid_targets", "Only request_review has targets.")
		case f.Type != AccountList && f.Type != AccountTeamList:
			add("invalid_targets", "Targets name a list-of-accounts field, such as pr.blocked_by.")
		}
	}
	if t.Priority != "" && t.Priority != PriorityNormal && t.Priority != PriorityLow {
		add("invalid_priority", "A part's priority is normal or low.")
	}
	if strings.TrimSpace(t.Reason) == "" {
		add("missing_reason", "A part says why: its reason is required.")
	}
	return out
}
