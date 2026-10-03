package httpapi

import (
	"context"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
)

func groupOut(g grouping.GroupView) Group {
	out := Group{Id: g.ID, Name: g.Name, NameSource: GroupNameSource(g.NameSource), State: GroupState(g.State), CreatedAt: g.CreatedAt,
		Members: []GroupMember{}}
	for _, m := range g.Members {
		out.Members = append(out.Members, GroupMember{PullRequest: m.PR, Open: m.Open, Since: m.Since, Source: GroupMemberSource(m.Source),
			Reason: m.Reason, Chain: chainOut(m.Chain)})
	}
	return out
}

// chainOut is a pull request's place in its chain; one not stacked is the only one of a chain of one.
func chainOut(c grouping.Chain) ChainPosition {
	chain := ChainPosition{Position: c.Position, Length: c.Length, Below: c.Below}
	if c.ID != 0 {
		id := c.ID
		chain.Id = &id
	}
	if chain.Position == 0 {
		chain.Position, chain.Length = 1, 1
	}
	return chain
}

// requireMember returns the calling member's ID. Only a session gets here: tokenMay lets no token change groups.
func requireMember(ctx context.Context) (int64, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return 0, err
	}
	return c.Member.ID, nil
}

func (s *server) ListGroups(ctx context.Context, _ ListGroupsRequestObject) (ListGroupsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	gs, err := s.grouping.Groups(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.ID)
	}
	described, err := s.docsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := ListGroups200JSONResponse{Groups: []Group{}}
	for _, g := range gs {
		out.Groups = append(out.Groups, described.with(groupOut(g)))
	}
	return out, nil
}

// group is a group as the contract has it, with its description.
func (s *server) group(ctx context.Context, id int64) (Group, error) {
	g, err := s.grouping.Group(ctx, id)
	if err != nil {
		return Group{}, err
	}
	described, err := s.docsOf(ctx, []int64{g.ID})
	if err != nil {
		return Group{}, err
	}
	return described.with(groupOut(g)), nil
}

// GetGroup is a group, with its description and whether the caller may change it.
func (s *server) GetGroup(ctx context.Context, req GetGroupRequestObject) (GetGroupResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	g, err := s.grouping.Group(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	described, err := s.docsOf(ctx, []int64{g.ID})
	if err != nil {
		return nil, err
	}
	out := described.with(groupOut(g))
	editable, err := s.mayDescribe(ctx, c, g)
	if err != nil {
		return nil, err
	}
	out.DocEditable = &editable
	return GetGroup200JSONResponse(out), nil
}

func (s *server) CreateGroup(ctx context.Context, req CreateGroupRequestObject) (CreateGroupResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.grouping.Create(ctx, actor, req.Body.Name, req.Body.PullRequests)
	if err != nil {
		return nil, err
	}
	g, err := s.group(ctx, id)
	if err != nil {
		return nil, err
	}
	return CreateGroup201JSONResponse(g), nil
}

func (s *server) RenameGroup(ctx context.Context, req RenameGroupRequestObject) (RenameGroupResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	if err := s.grouping.Rename(ctx, req.Id, req.Body.Name); err != nil {
		return nil, err
	}
	g, err := s.group(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return RenameGroup200JSONResponse(g), nil
}

func (s *server) MoveIntoGroup(ctx context.Context, req MoveIntoGroupRequestObject) (MoveIntoGroupResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.Move(ctx, actor, req.Body.PullRequest, req.Id); err != nil {
		return nil, err
	}
	g, err := s.group(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return MoveIntoGroup200JSONResponse(g), nil
}

func (s *server) TakeOutOfGroup(ctx context.Context, req TakeOutOfGroupRequestObject) (TakeOutOfGroupResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.TakeOut(ctx, actor, req.Pr, req.Id); err != nil {
		return nil, err
	}
	return TakeOutOfGroup204Response{}, nil
}

func (s *server) MergeGroups(ctx context.Context, req MergeGroupsRequestObject) (MergeGroupsResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.Merge(ctx, actor, req.Id, req.Body.From); err != nil {
		return nil, err
	}
	g, err := s.group(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return MergeGroups200JSONResponse(g), nil
}

func (s *server) SplitGroup(ctx context.Context, req SplitGroupRequestObject) (SplitGroupResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.grouping.Split(ctx, actor, req.Id, req.Body.Name, req.Body.PullRequests)
	if err != nil {
		return nil, err
	}
	g, err := s.group(ctx, id)
	if err != nil {
		return nil, err
	}
	return SplitGroup201JSONResponse(g), nil
}

func (s *server) ListSuggestions(ctx context.Context, _ ListSuggestionsRequestObject) (ListSuggestionsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	ss, err := s.grouping.Suggestions(ctx)
	if err != nil {
		return nil, err
	}
	out := ListSuggestions200JSONResponse{Suggestions: []Suggestion{}}
	for _, v := range ss {
		out.Suggestions = append(out.Suggestions, Suggestion{Id: v.ID, PullRequest: v.PR, Group: v.Group, Reason: v.Reason, CreatedAt: v.CreatedAt})
	}
	return out, nil
}

func (s *server) AcceptSuggestion(ctx context.Context, req AcceptSuggestionRequestObject) (AcceptSuggestionResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.Accept(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	return AcceptSuggestion204Response{}, nil
}

func (s *server) DismissSuggestion(ctx context.Context, req DismissSuggestionRequestObject) (DismissSuggestionResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.Dismiss(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	return DismissSuggestion204Response{}, nil
}

var errBridgeOnly = fail.New(fail.Forbidden, "bridge_only", "Only a bridge's token posts assertions.")

func (s *server) PostAssertion(ctx context.Context, req PostAssertionRequestObject) (PostAssertionResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if c.BridgeID == 0 {
		return nil, errBridgeOnly
	}
	if err := s.bridges.Accepting(ctx, c.BridgeID); err != nil {
		return nil, err
	}
	a := grouping.Assertion{Anchor: req.Body.Anchor}
	if req.Body.Title != nil {
		a.Title = *req.Body.Title
	}
	if req.Body.Evidence != nil {
		a.Evidence = *req.Body.Evidence
	}
	for _, p := range req.Body.PullRequests {
		a.PRs = append(a.PRs, grouping.PRRef{Repo: p.Repo, Number: p.Number})
	}
	res, err := s.grouping.Assert(ctx, c.BridgeID, c.TokenID, a)
	if err != nil {
		return nil, err
	}
	return PostAssertion201JSONResponse{Id: res.ID, Applied: res.Applied}, nil
}

func (s *server) RetractAssertion(ctx context.Context, req RetractAssertionRequestObject) (RetractAssertionResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if c.BridgeID == 0 {
		return nil, errBridgeOnly
	}
	if err := s.bridges.Accepting(ctx, c.BridgeID); err != nil {
		return nil, err
	}
	if err := s.grouping.Retract(ctx, c.BridgeID, req.Params.Anchor); err != nil {
		return nil, err
	}
	return RetractAssertion204Response{}, nil
}
