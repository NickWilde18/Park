package rules

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/interpreter"
)

// Input is what a person's list is evaluated against.
type Input struct {
	// Fields are every field of the catalog but judgements, by name: booleans, int64 numbers, strings, and []string
	// lists. An optional number is an int64, or nil when it is not set.
	Fields map[string]any
	// Judgements are the recorded answers, by question. A question with no answer here is unknown.
	Judgements map[string]string
}

// Outcome is what came of trying one rule.
type Outcome string

// The outcomes of a rule tried.
const (
	Matched    Outcome = "matched"
	NotMatched Outcome = "not_matched"
	// Unknown is a condition whose value depends on a judgement not known: it does not match.
	Unknown Outcome = "unknown"
	// Failed is a condition that failed at run time or exceeded its cost limit: it does not match.
	Failed Outcome = "error"
)

// Step is one rule tried, in a person's path.
type Step struct {
	Name       string  `json:"name"`
	PassedOver string  `json:"passed_over,omitempty"`
	Outcome    Outcome `json:"outcome"`
	Error      string  `json:"error,omitempty"`
	// Judgements are the questions an unknown outcome depends on.
	Judgements []string `json:"judgements,omitempty"`
	Rule       int      `json:"rule"`
}

// Part is one person's part on one pull request, with the path that led to it.
type Part struct {
	Kind     string `json:"kind"`
	Action   string `json:"action,omitempty"`
	Priority string `json:"priority"`
	Reason   string `json:"reason"`
	// ReasonError says why the reason could not be shown; the reason is then the rule's name.
	ReasonError string   `json:"reason_error,omitempty"`
	Targets     []string `json:"targets,omitempty"`
	Path        []Step   `json:"path"`
	// Rule is the number of the rule that decided, from 1; 0 when none matched, which a published set rules out.
	Rule   int  `json:"rule"`
	Remind bool `json:"remind,omitempty"`
	Task   bool `json:"task,omitempty"`
}

// Evaluate evaluates a role's list for one person. The first rule whose condition matches decides (R1). Evaluation is
// total (R5): a condition that errs, exceeds its cost limit, or depends on an unknown judgement does not match, and
// the path records it.
func (c *Compiled) Evaluate(role Role, in Input) Part {
	var part Part
	act, actErr := in.activation()
	for i, r := range c.lists[role] {
		step := Step{Rule: i + 1, Name: r.rule.Name, PassedOver: r.rule.PassedOver, Outcome: NotMatched}
		var out ref.Val
		err := actErr
		if err == nil {
			out, _, err = r.when.Eval(act)
		}
		switch {
		case err != nil:
			step.Outcome, step.Error = Failed, err.Error()
		case types.IsUnknown(out):
			step.Outcome, step.Judgements = Unknown, unknownJudgements(out)
		case out == types.True:
			step.Outcome = Matched
		}
		part.Path = append(part.Path, step)
		if step.Outcome != Matched {
			continue
		}
		t := r.rule.Then
		part.Rule, part.Kind, part.Action, part.Remind, part.Task = i+1, t.Kind, t.Action, t.Remind, t.Task
		part.Priority = t.Priority
		if part.Priority == "" {
			part.Priority = PriorityNormal
		}
		if t.Targets != "" {
			part.Targets, _ = in.Fields[t.Targets].([]string)
		}
		if part.Reason, err = render(r.reason, act); err != nil {
			part.Reason, part.ReasonError = r.rule.Name, err.Error()
		}
		return part
	}
	return part
}

// activation binds the input's fields and recorded judgements, and marks every other judgement unknown.
func (in Input) activation() (interpreter.PartialActivation, error) {
	vars := make(map[string]any, len(in.Fields)+len(Questions))
	for name, v := range in.Fields {
		f, ok := catalog[name]
		if !ok || f.Type == Judgement {
			continue
		}
		if f.Type == OptionalNumber {
			switch n := v.(type) {
			case nil:
				v = types.OptionalNone
			case int64:
				v = types.OptionalOf(types.Int(n))
			case int:
				v = types.OptionalOf(types.Int(n))
			}
		}
		vars[name] = v
	}
	var unknown []*interpreter.AttributePattern
	for _, q := range Questions {
		if answer, ok := in.Judgements[q.Name]; ok {
			vars[judgementPrefix+q.Name] = answer
		} else {
			unknown = append(unknown, cel.AttributePattern(judgementPrefix+q.Name))
		}
	}
	return cel.PartialVars(vars, unknown...)
}

// unknownJudgements names the questions an unknown value depends on.
func unknownJudgements(v ref.Val) []string {
	u, _ := v.(*types.Unknown)
	var out []string
	for _, id := range u.IDs() {
		trails, _ := u.GetAttributeTrails(id)
		for _, t := range trails {
			if q, ok := strings.CutPrefix(t.Variable(), judgementPrefix); ok && !slices.Contains(out, q) {
				out = append(out, q)
			}
		}
	}
	slices.Sort(out)
	return out
}

// render fills a reason's expressions in.
func render(segs []segment, act interpreter.PartialActivation) (string, error) {
	var b strings.Builder
	for _, s := range segs {
		if s.prg == nil {
			b.WriteString(s.text)
			continue
		}
		out, _, err := s.prg.Eval(act)
		if err != nil {
			return "", err
		}
		if types.IsUnknown(out) {
			return "", errors.New("the reason shows a judgement that is not known")
		}
		text, err := show(out)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// show writes a value for a reason: lists as "a, b and c", and an optional that is not set as nothing.
func show(v ref.Val) (string, error) {
	switch v := v.(type) {
	case types.String:
		return string(v), nil
	case types.Int:
		return strconv.FormatInt(int64(v), 10), nil
	case types.Uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case types.Double:
		return strconv.FormatFloat(float64(v), 'f', -1, 64), nil
	case types.Bool:
		return strconv.FormatBool(bool(v)), nil
	case *types.Optional:
		if !v.HasValue() {
			return "", nil
		}
		return show(v.GetValue())
	case traits.Lister:
		n, _ := v.Size().(types.Int)
		items := make([]string, 0, n)
		for i := range n {
			item, err := show(v.Get(i))
			if err != nil {
				return "", err
			}
			items = append(items, item)
		}
		if len(items) < 2 {
			return strings.Join(items, ""), nil
		}
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1], nil
	}
	return "", errors.New("a reason shows text, numbers, true or false, and lists of them")
}
