package rules

import (
	"bytes"
	_ "embed"
	"encoding/json"

	"github.com/NickWilde18/Park/internal/fail"
)

// RuleSet is a rule set as it is imported, drafted, and published: each role's ordered list of rules. It is the
// document a rule-set file holds and the API takes.
type RuleSet struct {
	Author   []Rule `json:"author"`
	Reviewer []Rule `json:"reviewer"`
}

// List returns a role's list.
func (rs RuleSet) List(r Role) []Rule {
	if r == Reviewer {
		return rs.Reviewer
	}
	return rs.Author
}

// Rule is a condition and a conclusion about one person's part.
type Rule struct {
	Name string `json:"name"`
	// PassedOver is shown in a path when the rule does not match; without one, a rule that does not match leaves no
	// trace.
	PassedOver string     `json:"passed_over,omitempty"`
	When       Condition  `json:"when"`
	Then       Conclusion `json:"then"`
}

// Condition is exactly one of: always, the catch-all; all or any of other conditions; an expression; or a term,
// (field, op, value). A term's value is a literal, or {"field": name} to compare with another field.
type Condition struct {
	Value  any         `json:"value,omitempty"`
	Expr   string      `json:"expr,omitempty"`
	Field  string      `json:"field,omitempty"`
	Op     string      `json:"op,omitempty"`
	All    []Condition `json:"all,omitempty"`
	Any    []Condition `json:"any,omitempty"`
	Always bool        `json:"always,omitempty"`
}

// Conclusion is what a rule concludes about the person's part: docs/design/05-rules.md#conclusions.
type Conclusion struct {
	Kind   string `json:"kind"`
	Action string `json:"action,omitempty"`
	// Targets names an account-list field: the reviewers a request_review hands the pull request back to.
	Targets  string `json:"targets,omitempty"`
	Priority string `json:"priority,omitempty"`
	// Reason is a template: text, with expressions in braces; {{ and }} are literal braces.
	Reason string `json:"reason"`
	Remind bool   `json:"remind,omitempty"`
	// Task says the part may become a task.
	Task bool `json:"task,omitempty"`
}

// Parse reads a rule-set document. It checks only its shape: Check says whether it can be published.
func Parse(data []byte) (RuleSet, error) {
	var rs RuleSet
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rs); err != nil {
		return RuleSet{}, fail.New(fail.Invalid, "invalid_rule_set", "A rule set is JSON with an author and a reviewer list: "+err.Error())
	}
	return rs, nil
}

//go:embed default.json
var defaultRules []byte

// Default returns the default rule set, docs/design/05-rules.md#the-default-rule-set, from default.json.
func Default() RuleSet {
	rs, err := Parse(defaultRules)
	if err != nil {
		panic("the embedded default rule set: " + err.Error())
	}
	return rs
}
