package rules

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
)

// CostLimit caps what evaluating one condition, or one expression of a reason, may cost. Past it, evaluation stops
// with an error, and the condition does not match (R5).
const CostLimit = 10_000

// celEnv declares every field of the catalog, judgements included, as a variable of its type.
var celEnv = func() *cel.Env {
	opts := []cel.EnvOption{cel.OptionalTypes()}
	for _, f := range Catalog() {
		opts = append(opts, cel.Variable(f.Name, celType(f.Type)))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		panic("the rule environment: " + err.Error())
	}
	return e
}()

func celType(t Type) *cel.Type {
	switch t {
	case Boolean:
		return cel.BoolType
	case Number:
		return cel.IntType
	case OptionalNumber:
		return cel.OptionalType(cel.IntType)
	case TextList, AccountList, AccountTeamList, ChoiceList:
		return cel.ListType(cel.StringType)
	default:
		return cel.StringType
	}
}

// operators are the structured operators each field type takes: docs/design/05-rules.md#conditions.
var operators = map[Type][]string{
	Boolean:         {"is_true", "is_false"},
	Number:          {"eq", "ne", "lt", "le", "gt", "ge"},
	OptionalNumber:  {"is_set", "is_not_set", "eq", "ne", "lt", "le", "gt", "ge"},
	Choice:          {"is", "is_not", "is_one_of"},
	Text:            {"is", "is_not", "starts_with", "contains"},
	Account:         {"is", "is_not", "starts_with", "contains"},
	TextList:        {"is_empty", "is_not_empty", "contains"},
	AccountList:     {"is_empty", "is_not_empty", "contains"},
	AccountTeamList: {"is_empty", "is_not_empty", "contains"},
	ChoiceList:      {"is_empty", "is_not_empty", "contains"},
	Judgement:       {"is"},
}

var comparisons = map[string]string{"eq": "==", "ne": "!=", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}

// compileCondition compiles a condition to a program, and names the judgements it reads.
func compileCondition(c Condition) (cel.Program, []string, error) {
	src, err := conditionSource(c, true)
	if err != nil {
		return nil, nil, err
	}
	ast, iss := celEnv.Compile(src)
	if iss.Err() != nil {
		return nil, nil, iss.Err()
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, nil, fmt.Errorf("a condition is true or false; this one gives %s", ast.OutputType())
	}
	prg, err := program(ast)
	return prg, judgementsOf(ast), err
}

func program(ast *cel.Ast) (cel.Program, error) {
	return celEnv.Program(ast, cel.EvalOptions(cel.OptPartialEval), cel.CostLimit(CostLimit))
}

// judgementsOf names the questions whose judgements an expression reads.
func judgementsOf(ast *cel.Ast) []string {
	var out []string
	for _, ref := range ast.NativeRep().ReferenceMap() {
		if q, ok := strings.CutPrefix(ref.Name, judgementPrefix); ok && !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
}

// conditionSource writes a condition as CEL. An expression stands only as a whole condition, so that a rule is
// either structured or an expression.
func conditionSource(c Condition, top bool) (string, error) {
	forms := 0
	for _, set := range []bool{c.Always, c.All != nil, c.Any != nil, c.Expr != "", c.Field != "" || c.Op != "" || c.Value != nil} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return "", errors.New("a condition is exactly one of: always, all, any, expr, or a term with a field and an op")
	}
	switch {
	case c.Always:
		return "true", nil
	case c.Expr != "":
		if !top {
			return "", errors.New("an expression is a whole condition, not part of one")
		}
		return c.Expr, nil
	case c.All != nil:
		return joined(c.All, " && ")
	case c.Any != nil:
		return joined(c.Any, " || ")
	}
	return termSource(c)
}

func joined(cs []Condition, op string) (string, error) {
	if len(cs) == 0 {
		return "", errors.New("all and any take at least one condition")
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		src, err := conditionSource(c, false)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + src + ")"
	}
	return strings.Join(parts, op), nil
}

// termSource writes a term as CEL. A comparison on an optional number that is not set is false.
func termSource(t Condition) (string, error) {
	f, ok := catalog[t.Field]
	if !ok {
		return "", fmt.Errorf("there is no field %q", t.Field)
	}
	if !slices.Contains(operators[f.Type], t.Op) {
		return "", fmt.Errorf("%s is %s, which takes %s", f.Name, f.Type, strings.Join(operators[f.Type], ", "))
	}
	name := f.Name
	switch t.Op {
	case "is_true", "is_false", "is_set", "is_not_set", "is_empty", "is_not_empty":
		if t.Value != nil {
			return "", fmt.Errorf("%s %s takes no value", name, t.Op)
		}
		return map[string]string{"is_true": name, "is_false": "!" + name, "is_set": name + ".hasValue()",
			"is_not_set": "!" + name + ".hasValue()", "is_empty": "size(" + name + ") == 0",
			"is_not_empty": "size(" + name + ") > 0"}[t.Op], nil
	case "eq", "ne", "lt", "le", "gt", "ge":
		rhs, guards, err := numberOperand(name, t.Value)
		if err != nil {
			return "", err
		}
		lhs := name
		if f.Type == OptionalNumber {
			guards = append([]string{name + ".hasValue()"}, guards...)
			lhs = name + ".value()"
		}
		return strings.Join(append(guards, lhs+" "+comparisons[t.Op]+" "+rhs), " && "), nil
	case "is_one_of":
		values, ok := t.Value.([]any)
		if !ok || len(values) == 0 {
			return "", fmt.Errorf("%s is_one_of takes a list of values", name)
		}
		quoted := make([]string, len(values))
		for i, v := range values {
			s, err := textValue(f, v)
			if err != nil {
				return "", err
			}
			quoted[i] = strconv.Quote(s)
		}
		return name + " in [" + strings.Join(quoted, ", ") + "]", nil
	}
	s, err := textValue(f, t.Value)
	if err != nil {
		return "", err
	}
	q := strconv.Quote(s)
	switch {
	case t.Op == "is":
		return name + " == " + q, nil
	case t.Op == "is_not":
		return name + " != " + q, nil
	case t.Op == "starts_with":
		return name + ".startsWith(" + q + ")", nil
	case f.Type.list():
		return q + " in " + name, nil
	}
	return name + ".contains(" + q + ")", nil
}

// numberOperand is the right-hand side of a comparison: a whole number, or {"field": name} for another number field,
// with the guard an optional one needs.
func numberOperand(name string, v any) (string, []string, error) {
	switch v := v.(type) {
	case float64:
		if v != math.Trunc(v) || math.Abs(v) > 1<<53 {
			return "", nil, fmt.Errorf("%s compares with a whole number", name)
		}
		return strconv.FormatInt(int64(v), 10), nil, nil
	case map[string]any:
		other, _ := v["field"].(string)
		f, ok := catalog[other]
		if len(v) != 1 || !ok || (f.Type != Number && f.Type != OptionalNumber) {
			return "", nil, fmt.Errorf(`%s compares with a whole number, or with another number field as {"field": name}`, name)
		}
		if f.Type == OptionalNumber {
			return other + ".value()", []string{other + ".hasValue()"}, nil
		}
		return other, nil, nil
	}
	return "", nil, fmt.Errorf("%s compares with a whole number", name)
}

// textValue is a term's text value; for a choice, a list of choices, or a judgement, one of its values.
func textValue(f Field, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s takes a text value", f.Name)
	}
	if f.Choices != nil && !slices.Contains(f.Choices, s) {
		return "", fmt.Errorf("%s is one of %s, not %q", f.Name, strings.Join(f.Choices, ", "), s)
	}
	return s, nil
}

// segment is a piece of a reason: literal text, or an expression.
type segment struct {
	prg  cel.Program
	text string
}

// compileTemplate compiles a reason: text with expressions in braces, where {{ and }} are literal braces. It names
// the judgements the reason reads.
func compileTemplate(s string) ([]segment, []string, error) {
	var segs []segment
	var judgements []string
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			segs = append(segs, segment{text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			text.WriteByte('{')
			i += 2
		case strings.HasPrefix(s[i:], "}}"):
			text.WriteByte('}')
			i += 2
		case s[i] == '}':
			return nil, nil, errors.New("a closing brace outside an expression; write }} for a brace")
		case s[i] == '{':
			end := closing(s, i)
			if end < 0 {
				return nil, nil, errors.New("a brace opens an expression that never closes; write {{ for a brace")
			}
			src := s[i+1 : end]
			ast, iss := celEnv.Compile(src)
			if iss.Err() != nil {
				return nil, nil, fmt.Errorf("{%s}: %w", src, iss.Err())
			}
			if !renderable(ast.OutputType()) {
				return nil, nil, fmt.Errorf("{%s} gives %s; a reason shows text, numbers, true or false, and lists of them", src,
					ast.OutputType())
			}
			prg, err := program(ast)
			if err != nil {
				return nil, nil, err
			}
			flush()
			segs = append(segs, segment{prg: prg})
			judgements = append(judgements, judgementsOf(ast)...)
			i = end + 1
		default:
			text.WriteByte(s[i])
			i++
		}
	}
	flush()
	return segs, judgements, nil
}

// closing finds the brace that closes the expression opened at start, skipping quoted text; -1 if none does.
func closing(s string, start int) int {
	depth := 0
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j
			}
		case '\'', '"':
			quote := s[j]
			for j++; j < len(s) && s[j] != quote; j++ {
				if s[j] == '\\' {
					j++
				}
			}
		}
	}
	return -1
}

func renderable(t *cel.Type) bool {
	switch t.Kind() {
	case types.StringKind, types.IntKind, types.UintKind, types.DoubleKind, types.BoolKind, types.DynKind:
		return true
	case types.ListKind:
		return renderable(t.Parameters()[0])
	case types.OpaqueKind:
		return t.TypeName() == "optional_type" && renderable(t.Parameters()[0])
	}
	return false
}
