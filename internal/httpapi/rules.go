package httpapi

import (
	"context"
	"encoding/json"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/rules"
)

// requireRules returns the calling member's ID if they hold the rules permission. Only a session gets here: tokenMay
// lets no token draft or publish.
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
		field := Field{Name: f.Name, Type: FieldType(f.Type), Meaning: f.Meaning}
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
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, PublishedVersion: d.PublishedVersion}
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
