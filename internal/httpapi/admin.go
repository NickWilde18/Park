package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
	"github.com/NickWilde18/Park/internal/identity"
)

func (s *server) ListMembers(ctx context.Context, _ ListMembersRequestObject) (ListMembersResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	ms, err := s.id.Members(ctx)
	if err != nil {
		return nil, err
	}
	out := ListMembers200JSONResponse{Members: []Member{}}
	for _, m := range ms {
		out.Members = append(out.Members, member(m))
	}
	return out, nil
}

func (s *server) UpdateMember(ctx context.Context, req UpdateMemberRequestObject) (UpdateMemberResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	m, err := s.id.UpdateMember(ctx, c, req.Id, identity.MemberPatch{DisplayName: b.DisplayName, Admin: b.Admin, Rules: b.Rules,
		Disabled: b.Disabled})
	if err != nil {
		return nil, err
	}
	return UpdateMember200JSONResponse(member(m)), nil
}

func (s *server) LinkMemberIdentity(ctx context.Context, req LinkMemberIdentityRequestObject) (LinkMemberIdentityResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.id.LinkByAdmin(ctx, c, req.Id, req.Body.ForgeId, req.Body.AccountId)
	if err != nil {
		return nil, err
	}
	return LinkMemberIdentity201JSONResponse(identityOut(i)), nil
}

func inviteOut(i identity.Invite) Invite {
	return Invite{Id: i.ID, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt, UsedBy: i.UsedBy, UsedAt: i.UsedAt}
}

func (s *server) ListInvites(ctx context.Context, _ ListInvitesRequestObject) (ListInvitesResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	is, err := s.id.Invites(ctx)
	if err != nil {
		return nil, err
	}
	out := ListInvites200JSONResponse{Invites: []Invite{}}
	for _, i := range is {
		out.Invites = append(out.Invites, inviteOut(i))
	}
	return out, nil
}

func (s *server) CreateInvite(ctx context.Context, req CreateInviteRequestObject) (CreateInviteResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var life time.Duration
	if req.Body.ExpiresInHours != nil {
		life = time.Duration(*req.Body.ExpiresInHours) * time.Hour
		if life <= 0 {
			return nil, fail.New(fail.Invalid, "invalid_expiry", "An invite lasts from an hour to 30 days.")
		}
	}
	code, inv, err := s.id.CreateInvite(ctx, c, life)
	if err != nil {
		return nil, err
	}
	return CreateInvite201JSONResponse{Code: code, Invite: inviteOut(inv)}, nil
}

func (s *server) DeleteInvite(ctx context.Context, req DeleteInviteRequestObject) (DeleteInviteResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.id.DeleteInvite(ctx, c, req.Id); err != nil {
		return nil, err
	}
	return DeleteInvite204Response{}, nil
}

func forgeOut(f forges.Forge) Forge {
	return Forge{Id: f.ID, Kind: ForgeKind(f.Kind), Name: f.Name, BaseAddress: f.BaseAddress, CreatedAt: f.CreatedAt}
}

// forgeError turns a refused forge record into an error the caller can act on.
func forgeError(err error) error {
	var invalid forges.ErrInvalid
	switch {
	case errors.As(err, &invalid):
		return fail.New(fail.Invalid, "invalid_forge", invalid.Reason)
	case errors.Is(err, forges.ErrNotFound):
		return fail.New(fail.NotFound, "not_found", "There is no such forge.")
	}
	return err
}

// requireAdminSession returns an admin caller in a browser session, for writes the identity service does not make.
func requireAdminSession(ctx context.Context) (*identity.Caller, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if c.TokenID != "" {
		return nil, fail.New(fail.Forbidden, "session_required", "Change settings from a browser session.")
	}
	return c, nil
}

func (s *server) ListForges(ctx context.Context, _ ListForgesRequestObject) (ListForgesResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	fs, err := forges.List(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	out := ListForges200JSONResponse{Forges: []Forge{}}
	for _, f := range fs {
		out.Forges = append(out.Forges, forgeOut(f))
	}
	return out, nil
}

func (s *server) CreateForge(ctx context.Context, req CreateForgeRequestObject) (CreateForgeResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	f, err := forges.Create(ctx, s.pool, string(req.Body.Kind), req.Body.Name, req.Body.BaseAddress)
	if err != nil {
		return nil, forgeError(err)
	}
	return CreateForge201JSONResponse(forgeOut(f)), nil
}

func (s *server) UpdateForge(ctx context.Context, req UpdateForgeRequestObject) (UpdateForgeResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	f, err := forges.Update(ctx, s.pool, req.Id, req.Body.Name, req.Body.BaseAddress)
	if err != nil {
		return nil, forgeError(err)
	}
	return UpdateForge200JSONResponse(forgeOut(f)), nil
}

func providerOut(p identity.Provider) Provider {
	return Provider{Id: p.ID, Kind: ProviderKind(p.Kind), Name: p.Name, ForgeId: p.ForgeID, ClientId: p.ClientID, CreatedAt: p.CreatedAt}
}

func (s *server) ListProviders(ctx context.Context, _ ListProvidersRequestObject) (ListProvidersResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	ps, err := s.id.Providers(ctx)
	if err != nil {
		return nil, err
	}
	out := ListProviders200JSONResponse{Providers: []Provider{}}
	for _, p := range ps {
		out.Providers = append(out.Providers, providerOut(p))
	}
	return out, nil
}

func (s *server) CreateProvider(ctx context.Context, req CreateProviderRequestObject) (CreateProviderResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	p, err := s.id.CreateProvider(ctx, c, identity.ProviderInput{ID: b.Id, Kind: string(b.Kind), Name: b.Name, ForgeID: b.ForgeId,
		ClientID: b.ClientId, ClientSecret: b.ClientSecret})
	if err != nil {
		return nil, err
	}
	return CreateProvider201JSONResponse(providerOut(p)), nil
}

func (s *server) UpdateProvider(ctx context.Context, req UpdateProviderRequestObject) (UpdateProviderResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	p, err := s.id.UpdateProvider(ctx, c, req.Provider, identity.ProviderPatch{Name: b.Name, ClientID: b.ClientId, ClientSecret: b.ClientSecret})
	if err != nil {
		return nil, err
	}
	return UpdateProvider200JSONResponse(providerOut(p)), nil
}

func (s *server) DeleteProvider(ctx context.Context, req DeleteProviderRequestObject) (DeleteProviderResponseObject, error) {
	c, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.id.DeleteProvider(ctx, c, req.Provider); err != nil {
		return nil, err
	}
	return DeleteProvider204Response{}, nil
}
