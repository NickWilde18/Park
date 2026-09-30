package httpapi

import (
	"context"

	"github.com/NickWilde18/Park/internal/identity"
)

func member(m identity.Member) Member {
	return Member{Id: m.ID, DisplayName: m.DisplayName, Admin: m.Admin, Rules: m.Rules, Disabled: m.Disabled, CreatedAt: m.CreatedAt}
}

func identityOut(i identity.Identity) Identity {
	return Identity{Id: i.ID, ForgeId: i.ForgeID, ForgeName: i.ForgeName, AccountId: i.AccountID, Login: i.Login, Name: i.Name,
		LinkedBy: i.LinkedBy, LinkedAt: i.LinkedAt}
}

func tokenOut(t identity.Token) Token {
	return Token{Id: t.ID, Name: t.Name, Scopes: t.Scopes, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
		ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt}
}

func (s *server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := GetMe200JSONResponse{Member: member(c.Member)}
	if id := sessionOf(ctx); id != "" {
		csrf := s.id.CSRF(id)
		out.CsrfToken = &csrf
	}
	if c.TokenID != "" {
		scopes := c.Scopes
		out.Scopes = &scopes
	}
	return out, nil
}

func (s *server) ListMyIdentities(ctx context.Context, _ ListMyIdentitiesRequestObject) (ListMyIdentitiesResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.id.Identities(ctx, c.Member.ID)
	if err != nil {
		return nil, err
	}
	out := ListMyIdentities200JSONResponse{Identities: []Identity{}}
	for _, i := range ids {
		out.Identities = append(out.Identities, identityOut(i))
	}
	return out, nil
}

func (s *server) DeleteMyIdentity(ctx context.Context, req DeleteMyIdentityRequestObject) (DeleteMyIdentityResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.id.Unlink(ctx, c.Member.ID, req.Id); err != nil {
		return nil, err
	}
	return DeleteMyIdentity204Response{}, nil
}

func (s *server) ListMyTokens(ctx context.Context, _ ListMyTokensRequestObject) (ListMyTokensResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := s.id.Tokens(ctx, c.Member.ID)
	if err != nil {
		return nil, err
	}
	out := ListMyTokens200JSONResponse{Tokens: []Token{}}
	for _, t := range ts {
		out.Tokens = append(out.Tokens, tokenOut(t))
	}
	return out, nil
}

func (s *server) CreateMyToken(ctx context.Context, req CreateMyTokenRequestObject) (CreateMyTokenResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	scopes := make([]string, 0, len(req.Body.Scopes))
	for _, sc := range req.Body.Scopes {
		scopes = append(scopes, string(sc))
	}
	secret, t, err := s.id.CreateToken(ctx, c, req.Body.Name, scopes, req.Body.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return CreateMyToken201JSONResponse{Token: secret, Record: tokenOut(t)}, nil
}

func (s *server) RevokeMyToken(ctx context.Context, req RevokeMyTokenRequestObject) (RevokeMyTokenResponseObject, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.id.RevokeToken(ctx, c.Member.ID, req.Token); err != nil {
		return nil, err
	}
	return RevokeMyToken204Response{}, nil
}
