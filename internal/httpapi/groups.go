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
		chain := ChainPosition{Position: m.Chain.Position, Length: m.Chain.Length, Below: m.Chain.Below}
		if m.Chain.ID != 0 {
			id := m.Chain.ID
			chain.Id = &id
		}
		if chain.Position == 0 {
			chain.Position, chain.Length = 1, 1
		}
		out.Members = append(out.Members, GroupMember{PullRequest: m.PR, Open: m.Open, Since: m.Since, Source: GroupMemberSource(m.Source),
			Reason: m.Reason, Chain: chain})
	}
	return out
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
	out := ListGroups200JSONResponse{Groups: []Group{}}
	for _, g := range gs {
		out.Groups = append(out.Groups, groupOut(g))
	}
	return out, nil
}

func (s *server) group(ctx context.Context, id int64) (Group, error) {
	g, err := s.grouping.Group(ctx, id)
	if err != nil {
		return Group{}, err
	}
	return groupOut(g), nil
}

func (s *server) GetGroup(ctx context.Context, req GetGroupRequestObject) (GetGroupResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	g, err := s.group(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return GetGroup200JSONResponse(g), nil
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
	if err := s.grouping.Retract(ctx, c.BridgeID, req.Params.Anchor); err != nil {
		return nil, err
	}
	return RetractAssertion204Response{}, nil
}

func bridgeOut(b grouping.Bridge) Bridge {
	return Bridge{Id: b.ID, Name: b.Name, OwnerId: b.OwnerID, LinkTemplate: b.LinkTemplate, CreatedAt: b.CreatedAt}
}

func (s *server) ListBridges(ctx context.Context, _ ListBridgesRequestObject) (ListBridgesResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	bs, err := s.grouping.Bridges(ctx)
	if err != nil {
		return nil, err
	}
	out := ListBridges200JSONResponse{Bridges: []Bridge{}}
	for _, b := range bs {
		out.Bridges = append(out.Bridges, bridgeOut(b))
	}
	return out, nil
}

func (s *server) CreateBridge(ctx context.Context, req CreateBridgeRequestObject) (CreateBridgeResponseObject, error) {
	c, err := requireAdminSession(ctx)
	if err != nil {
		return nil, err
	}
	owner := c.Member.ID
	if req.Body.OwnerId != nil {
		owner = *req.Body.OwnerId
		if _, err := s.id.MemberByID(ctx, owner); err != nil {
			return nil, err
		}
	}
	template := ""
	if req.Body.LinkTemplate != nil {
		template = *req.Body.LinkTemplate
	}
	b, err := s.grouping.CreateBridge(ctx, owner, req.Body.Name, template)
	if err != nil {
		return nil, err
	}
	return CreateBridge201JSONResponse(bridgeOut(b)), nil
}
