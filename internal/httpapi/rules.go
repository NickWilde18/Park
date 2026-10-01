package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/verdicts"
)

// requireRules returns the calling member's ID if they hold the rules permission. A token gets here only to read
// drafts: tokenMay lets none draft, preview, or publish.
func requireRules(ctx context.Context) (int64, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return 0, err
	}
	if !c.Member.Rules {
		return 0, fail.New(fail.Forbidden, "forbidden", "Only a member with the rules permission may do this.")
	}
	return c.Member.ID, nil
}

// ruleSetIn reads a rule-set document strictly, so that a mistyped key is refused rather than dropped.
func ruleSetIn(in RuleSetDocument) (rules.RuleSet, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return rules.RuleSet{}, err
	}
	return rules.Parse(data)
}

// ruleSetOut writes the rules package's rule set as the contract's: the two describe one document.
func ruleSetOut(in rules.RuleSet) (RuleSet, error) {
	var out RuleSet
	data, err := json.Marshal(in)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	return out, err
}

func (s *server) ListFields(ctx context.Context, _ ListFieldsRequestObject) (ListFieldsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	out := ListFields200JSONResponse{Fields: []Field{}}
	for _, f := range rules.Catalog() {
		field := Field{Name: f.Name, Type: FieldType(f.Type), Meaning: f.Meaning, Operators: f.Type.Operators()}
		if f.Choices != nil {
			choices := append([]string(nil), f.Choices...)
			field.Choices = &choices
		}
		out.Fields = append(out.Fields, field)
	}
	return out, nil
}

func (s *server) ListQuestions(ctx context.Context, _ ListQuestionsRequestObject) (ListQuestionsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	out := ListQuestions200JSONResponse{Questions: []Question{}}
	for _, q := range rules.Questions {
		out.Questions = append(out.Questions, Question{Name: q.Name, Role: QuestionRole(q.Role), Asks: q.Asks,
			Answers: append([]string(nil), rules.Answers...)})
	}
	return out, nil
}

func (s *server) ListActions(ctx context.Context, _ ListActionsRequestObject) (ListActionsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	out := ListActions200JSONResponse{Actions: []Action{}}
	for _, a := range rules.Actions {
		out.Actions = append(out.Actions, Action{Name: a.Name, Role: ActionRole(a.Role), Task: a.Task})
	}
	return out, nil
}

func (s *server) ListRuleSets(ctx context.Context, _ ListRuleSetsRequestObject) (ListRuleSetsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	vs, err := s.rules.Versions(ctx)
	if err != nil {
		return nil, err
	}
	out := ListRuleSets200JSONResponse{Versions: []RuleSetVersionSummary{}}
	for _, v := range vs {
		out.Versions = append(out.Versions, RuleSetVersionSummary{Version: v.Number, BasedOn: v.BasedOn, PublishedBy: v.PublishedBy,
			PublishedAt: v.PublishedAt})
	}
	return out, nil
}

func versionOut(v rules.Version) (RuleSetVersion, error) {
	rs, err := ruleSetOut(v.Rules)
	return RuleSetVersion{Version: v.Number, BasedOn: v.BasedOn, PublishedBy: v.PublishedBy, PublishedAt: v.PublishedAt, Rules: rs}, err
}

func (s *server) GetRuleSet(ctx context.Context, req GetRuleSetRequestObject) (GetRuleSetResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	v, err := s.rules.Version(ctx, req.Version)
	if err != nil {
		return nil, err
	}
	out, err := versionOut(v)
	return GetRuleSet200JSONResponse(out), err
}

func draftOut(d rules.Draft) (RuleSetDraft, error) {
	rs, err := ruleSetOut(d.Rules)
	out := RuleSetDraft{Id: d.ID, BaseVersion: d.BaseVersion, Rules: rs, Problems: []Problem{}, CreatedBy: d.CreatedBy,
		CreatedByName: d.CreatorName, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, PublishedVersion: d.PublishedVersion}
	for _, p := range d.Problems {
		problem := Problem{Part: ProblemPart(p.Part), Code: p.Code, Message: p.Message}
		if p.Role != "" {
			role := ProblemRole(p.Role)
			problem.Role = &role
		}
		if p.Rule > 0 {
			n := p.Rule
			problem.Rule = &n
		}
		out.Problems = append(out.Problems, problem)
	}
	return out, err
}

func (s *server) ListRuleSetDrafts(ctx context.Context, _ ListRuleSetDraftsRequestObject) (ListRuleSetDraftsResponseObject, error) {
	if _, err := requireRules(ctx); err != nil {
		return nil, err
	}
	ds, err := s.rules.Drafts(ctx)
	if err != nil {
		return nil, err
	}
	out := ListRuleSetDrafts200JSONResponse{Drafts: []RuleSetDraft{}}
	for _, d := range ds {
		draft, err := draftOut(d)
		if err != nil {
			return nil, err
		}
		out.Drafts = append(out.Drafts, draft)
	}
	return out, nil
}

func (s *server) GetRuleSetDraft(ctx context.Context, req GetRuleSetDraftRequestObject) (GetRuleSetDraftResponseObject, error) {
	if _, err := requireRules(ctx); err != nil {
		return nil, err
	}
	d, err := s.rules.Draft(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := draftOut(d)
	return GetRuleSetDraft200JSONResponse(out), err
}

func (s *server) PreviewRuleSetDraft(ctx context.Context, req PreviewRuleSetDraftRequestObject) (PreviewRuleSetDraftResponseObject, error) {
	if _, err := requireRules(ctx); err != nil {
		return nil, err
	}
	d, err := s.rules.Draft(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	compiled, problems := rules.Compile(d.Rules)
	if len(problems) > 0 {
		return nil, fail.New(fail.Invalid, "draft_has_problems", fmt.Sprintf(
			"Fix the draft's problems to preview it. It has %d, the first: %s", len(problems), problems[0].Message))
	}
	p, err := s.verdicts.Preview(ctx, compiled)
	if err != nil {
		return nil, err
	}
	names, err := s.unitNames(ctx)
	if err != nil {
		return nil, err
	}
	out := PreviewRuleSetDraft200JSONResponse{Version: p.Version, EvaluatedAt: p.EvaluatedAt, EvaluatedUnits: p.Units,
		Changes: []PreviewChange{}, Fallback: []PreviewUnit{}}
	for _, c := range p.Changes {
		out.Changes = append(out.Changes, PreviewChange{Unit: unitOut(c.After.Unit), Name: names[c.After.Unit],
			Before: previewOut(c.Before), After: previewOut(c.After)})
	}
	for _, v := range p.Fallback {
		out.Fallback = append(out.Fallback, PreviewUnit{Unit: unitOut(v.Unit), Name: names[v.Unit], Verdict: previewOut(v)})
	}
	return out, nil
}

// previewOut writes a verdict a preview computed, which is not stored.
func previewOut(v verdicts.Verdict) PreviewVerdict {
	full := verdictOut(v, false)
	return PreviewVerdict{Side: PreviewVerdictSide(v.Side), Fallback: v.Fallback, Holders: full.Holders, Parts: full.Parts,
		Reminders: full.Reminders}
}

// unitNames names the units that can be open: a group by its name, and a pull request by its title, when it is open.
func (s *server) unitNames(ctx context.Context) (map[string]string, error) {
	gs, err := s.grouping.Groups(ctx)
	if err != nil {
		return nil, err
	}
	prs, err := s.facts.PRs(ctx, 0, true)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, g := range gs {
		names["group:"+strconv.FormatInt(g.ID, 10)] = g.Name
	}
	for _, p := range prs {
		names["pr:"+strconv.FormatInt(p.ID, 10)] = p.State.Title
	}
	return names, nil
}

func (s *server) CreateRuleSetDraft(ctx context.Context, req CreateRuleSetDraftRequestObject) (CreateRuleSetDraftResponseObject, error) {
	actor, err := requireRules(ctx)
	if err != nil {
		return nil, err
	}
	var imported *rules.RuleSet
	if req.Body.Rules != nil {
		rs, err := ruleSetIn(*req.Body.Rules)
		if err != nil {
			return nil, err
		}
		imported = &rs
	}
	d, err := s.rules.CreateDraft(ctx, actor, req.Body.BaseVersion, imported)
	if err != nil {
		return nil, err
	}
	out, err := draftOut(d)
	return CreateRuleSetDraft201JSONResponse(out), err
}

func (s *server) UpdateRuleSetDraft(ctx context.Context, req UpdateRuleSetDraftRequestObject) (UpdateRuleSetDraftResponseObject, error) {
	if _, err := requireRules(ctx); err != nil {
		return nil, err
	}
	rs, err := ruleSetIn(req.Body.Rules)
	if err != nil {
		return nil, err
	}
	d, err := s.rules.UpdateDraft(ctx, req.Id, req.Body.BaseVersion, rs)
	if err != nil {
		return nil, err
	}
	out, err := draftOut(d)
	return UpdateRuleSetDraft200JSONResponse(out), err
}

func (s *server) PublishRuleSetDraft(ctx context.Context, req PublishRuleSetDraftRequestObject) (PublishRuleSetDraftResponseObject, error) {
	actor, err := requireRules(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.rules.Publish(ctx, actor, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := versionOut(v)
	return PublishRuleSetDraft201JSONResponse(out), err
}
