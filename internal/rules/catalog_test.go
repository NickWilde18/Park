package rules

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// chapter returns the part of a design chapter between two headings.
func chapter(t *testing.T, name, from, to string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "design", name))
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(data), from)
	body, _, ok2 := strings.Cut(after, to)
	if !ok || !ok2 {
		t.Fatalf("%s has no section from %q to %q", name, from, to)
	}
	return body
}

var backticked = regexp.MustCompile("`(\\w+)`")

// The field catalog is the chapter's, field by field: name, type, and meaning, in order, with each choice's values
// the ones its meaning names.
func TestCatalogMatchesTheChapter(t *testing.T) {
	row := regexp.MustCompile("(?m)^\\| `([\\w.]+)` \\| ([^|]+?) \\|(?: ([^|]*?) \\|)?$")
	var want []Field
	for _, m := range row.FindAllStringSubmatch(chapter(t, "05-rules.md", "## The field catalog", "## Conclusions"), -1) {
		want = append(want, Field{Name: m[1], Type: Type(m[2]), Meaning: m[3]})
	}
	got := Catalog()
	if len(got) != len(want) {
		t.Fatalf("%d fields, the chapter has %d", len(got), len(want))
	}
	for i, f := range got {
		w := want[i]
		if f.Type == Judgement {
			w.Meaning = f.Meaning // the chapter's judgement table has no meaning column; the question's text serves
		}
		if f.Name != w.Name || f.Type != w.Type || f.Meaning != w.Meaning {
			t.Errorf("field %d: %s %q %q; the chapter says %s %q %q", i, f.Name, f.Type, f.Meaning, w.Name, w.Type, w.Meaning)
		}
		if f.Type != Choice && f.Type != ChoiceList {
			continue
		}
		var named []string
		for _, m := range backticked.FindAllStringSubmatch(f.Meaning, -1) {
			if !slices.Contains(named, m[1]) && !strings.Contains(m[1], ".") {
				named = append(named, m[1])
			}
		}
		if !slices.Equal(named, f.Choices) {
			t.Errorf("%s: choices %v; its meaning names %v", f.Name, f.Choices, named)
		}
	}
}

// Each field type takes the structured operators the chapter's table lists for it.
func TestOperatorsMatchTheChapter(t *testing.T) {
	names := map[string]string{"is true": "is_true", "is false": "is_false", "=": "eq", "≠": "ne", "<": "lt", "≤": "le",
		">": "gt", "≥": "ge", "is": "is", "is not": "is_not", "is one of": "is_one_of", "starts with": "starts_with",
		"contains": "contains", "is empty": "is_empty", "is not empty": "is_not_empty", "is set": "is_set",
		"is not set": "is_not_set", "is `yes`": "is", "is `no`": "is"}
	table := map[string][]string{}
	row := regexp.MustCompile(`(?m)^\| (Boolean|Number|Choice|Text|List|Optional|Judgement) \| ([^|]+) \|$`)
	for _, m := range row.FindAllStringSubmatch(chapter(t, "05-rules.md", "## Conditions", "## The field catalog"), -1) {
		for _, op := range strings.Split(strings.Split(m[2], ". ")[0], ", ") {
			op = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(op), "and the operators of its type"))
			if op == "" {
				continue
			}
			name, ok := names[op]
			if !ok {
				t.Fatalf("%s: an operator this test does not know: %q", m[1], op)
			}
			if !slices.Contains(table[m[1]], name) {
				table[m[1]] = append(table[m[1]], name)
			}
		}
	}
	table["Optional"] = append(table["Optional"], table["Number"]...)
	for kind, types := range map[string][]Type{"Boolean": {Boolean}, "Number": {Number}, "Choice": {Choice},
		"Text": {Text, Account}, "List": {TextList, AccountList, AccountTeamList, ChoiceList},
		"Optional": {OptionalNumber}, "Judgement": {Judgement}} {
		want := append([]string(nil), table[kind]...)
		sort.Strings(want)
		for _, typ := range types {
			got := append([]string(nil), operators[typ]...)
			sort.Strings(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s takes %v; the chapter's %s row gives %v", typ, got, kind, want)
			}
		}
	}
}

// The questions and the actions are 04-verdicts.md's: names, roles, what each asks, and which actions can be tasks.
func TestQuestionsAndActionsMatchTheChapter(t *testing.T) {
	row := regexp.MustCompile("(?m)^\\| `(\\w+)` \\| (Author|Reviewer) \\| ([^|]+?) \\|")
	var questions []Question
	for _, m := range row.FindAllStringSubmatch(chapter(t, "04-verdicts.md", "### Questions", "### Flow: get a judgement"), -1) {
		questions = append(questions, Question{Name: m[1], Role: Role(strings.ToLower(m[2])), Asks: m[3]})
	}
	if !slices.Equal(questions, Questions) {
		t.Errorf("questions %+v; the chapter has %+v", Questions, questions)
	}
	row = regexp.MustCompile("(?m)^\\| `(\\w+)` \\| (Author|Reviewer) \\| [^|]+ \\| ([^|]+) \\|$")
	var actions []Action
	for _, m := range row.FindAllStringSubmatch(chapter(t, "04-verdicts.md", "## Actions", "## Flow: evaluate a unit"), -1) {
		actions = append(actions, Action{Name: m[1], Role: Role(strings.ToLower(m[2])), Task: strings.HasPrefix(m[3], "Yes")})
	}
	sortActions := func(as []Action) []Action {
		as = append([]Action(nil), as...)
		sort.Slice(as, func(i, j int) bool { return as[i].Name < as[j].Name })
		return as
	}
	if !slices.Equal(sortActions(actions), sortActions(Actions)) {
		t.Errorf("actions %+v; the chapter has %+v", Actions, actions)
	}
}
