package httpapi

import (
	"context"
	"net/url"
	"strings"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
)

var errBridgeProposes = fail.New(fail.Forbidden, "bridge_only", "Only a bridge's token posts proposals.")

func (s *server) PostProposal(ctx context.Context, req PostProposalRequestObject) (PostProposalResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if c.BridgeID == 0 {
		return nil, errBridgeProposes
	}
	p := grouping.Proposal{Anchor: req.Body.Anchor}
	if req.Body.Title != nil {
		p.Title = *req.Body.Title
	}
	if req.Body.Evidence != nil {
		p.Evidence = *req.Body.Evidence
	}
	for _, g := range req.Body.Groups {
		var pg grouping.ProposedGroup
		if g.Name != nil {
			pg.Name = *g.Name
		}
		for _, r := range g.PullRequests {
			pg.PRs = append(pg.PRs, grouping.PRRef{Repo: r.Repo, Number: r.Number})
		}
		p.Groups = append(p.Groups, pg)
	}
	res, err := s.grouping.Propose(ctx, c.BridgeID, c.TokenID, p)
	if err != nil {
		return nil, err
	}
	return PostProposal201JSONResponse{Id: res.ID, State: ProposalResultState(res.State)}, nil
}

func (s *server) RetractProposal(ctx context.Context, req RetractProposalRequestObject) (RetractProposalResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if c.BridgeID == 0 {
		return nil, errBridgeProposes
	}
	if err := s.grouping.RetractProposal(ctx, c.BridgeID, req.Params.Anchor); err != nil {
		return nil, err
	}
	return RetractProposal204Response{}, nil
}

func (s *server) ListProposals(ctx context.Context, _ ListProposalsRequestObject) (ListProposalsResponseObject, error) {
	if _, err := requireCaller(ctx); err != nil {
		return nil, err
	}
	ps, err := s.grouping.Proposals(ctx)
	if err != nil {
		return nil, err
	}
	out := ListProposals200JSONResponse{Proposals: []Proposal{}}
	for _, v := range ps {
		p := Proposal{Id: v.ID, Bridge: v.Bridge, CreatedAt: v.CreatedAt, Groups: []ProposedGroup{}, Link: bridgeLink(v.LinkTemplate, v.Anchor)}
		if v.Title != "" {
			title := v.Title
			p.Title = &title
		}
		for _, g := range v.Groups {
			pg := ProposedGroup{PullRequests: g.PRs}
			if g.Name != "" {
				name := g.Name
				pg.Name = &name
			}
			p.Groups = append(p.Groups, pg)
		}
		out.Proposals = append(out.Proposals, p)
	}
	return out, nil
}

// bridgeLink renders a bridge's link to one of its IDs from its link template; nil when it has none.
func bridgeLink(template, id string) *string {
	if template == "" {
		return nil
	}
	link := strings.ReplaceAll(template, "{id}", url.PathEscape(id))
	return &link
}

func (s *server) AcceptProposal(ctx context.Context, req AcceptProposalRequestObject) (AcceptProposalResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	var edited []grouping.EditedGroup
	if req.Body != nil && req.Body.Groups != nil {
		edited = []grouping.EditedGroup{}
		for _, g := range *req.Body.Groups {
			edited = append(edited, grouping.EditedGroup{Name: g.Name, PRs: g.PullRequests})
		}
	}
	made, err := s.grouping.AcceptProposal(ctx, actor, req.Id, edited)
	if err != nil {
		return nil, err
	}
	return AcceptProposal200JSONResponse{Groups: made}, nil
}

func (s *server) DismissProposal(ctx context.Context, req DismissProposalRequestObject) (DismissProposalResponseObject, error) {
	actor, err := requireMember(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.grouping.DismissProposal(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	return DismissProposal204Response{}, nil
}
