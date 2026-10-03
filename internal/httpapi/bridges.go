package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NickWilde18/Park/internal/bridges"
	"github.com/NickWilde18/Park/internal/fail"
)

// bridgeFlowCookie holds a sealed connect flow while the browser is at a platform's sign-in page. Its path covers the
// callback, where it is read, and nothing else.
const bridgeFlowCookie = "park_bridge_connect"

func bridgeFlowCookieFor(value string) *http.Cookie {
	return &http.Cookie{Name: bridgeFlowCookie, Value: value, Path: "/api/v1/bridges/", MaxAge: int(bridges.FlowLife.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

func bridgeOut(b bridges.Bridge) Bridge {
	out := Bridge{Id: b.ID, Name: b.Name, OwnerId: b.OwnerID, LinkTemplate: b.LinkTemplate, CreatedAt: b.CreatedAt,
		Enabled: b.Enabled, Credentials: b.Credentials, EveryMinutes: b.EveryMinutes, LookbackDays: b.LookbackDays,
		ProposeFrom: b.ProposeFrom}
	if b.Plugin != "" {
		out.Plugin = &b.Plugin
	}
	if b.Chat.ID != "" {
		out.Chat = &BridgeChat{Id: b.Chat.ID, Name: b.Chat.Name}
	}
	if a := b.Account; a != nil {
		out.Account = &BridgeAccount{Id: a.ID, Name: a.Name, ConnectedBy: a.ConnectedBy, ConnectedAt: a.ConnectedAt, LapsedAt: a.LapsedAt}
	}
	return out
}

func pluginOut(p bridges.PluginState) BridgePlugin {
	out := BridgePlugin{Kind: p.Kind, Name: p.Name, Enabled: p.Enabled, Installed: p.Installed, ChangedAt: p.ChangedAt,
		Fields: []BridgeField{}}
	for _, f := range p.Fields {
		out.Fields = append(out.Fields, BridgeField{Key: f.Key, Label: f.Label, Secret: f.Secret})
	}
	return out
}

func (s *server) ListBridges(ctx context.Context, _ ListBridgesRequestObject) (ListBridgesResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	bs, err := s.bridges.List(ctx)
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
	var b bridges.Bridge
	if req.Body.Plugin != nil && *req.Body.Plugin != "" {
		b, err = s.bridges.Create(ctx, owner, req.Body.Name, template, *req.Body.Plugin)
	} else {
		external, gerr := s.grouping.CreateBridge(ctx, owner, req.Body.Name, template)
		if gerr != nil {
			return nil, gerr
		}
		b, err = s.bridges.Get(ctx, external.ID)
	}
	if err != nil {
		return nil, err
	}
	return CreateBridge201JSONResponse(bridgeOut(b)), nil
}

func (s *server) UpdateBridge(ctx context.Context, req UpdateBridgeRequestObject) (UpdateBridgeResponseObject, error) {
	if _, err := requireAdminSession(ctx); err != nil {
		return nil, err
	}
	c := bridges.Change{Name: req.Body.Name, Enabled: req.Body.Enabled, Plugin: req.Body.Plugin, EveryMinutes: req.Body.EveryMinutes,
		LookbackDays: req.Body.LookbackDays, ProposeFrom: req.Body.ProposeFrom}
	if req.Body.Credentials != nil {
		c.Credentials = *req.Body.Credentials
	}
	if req.Body.Chat != nil {
		c.Chat = &bridges.Chat{ID: req.Body.Chat.Id, Name: req.Body.Chat.Name}
	}
	b, err := s.bridges.Update(ctx, req.Id, c)
	if err != nil {
		return nil, err
	}
	return UpdateBridge200JSONResponse(bridgeOut(b)), nil
}

func (s *server) ListBridgeChats(ctx context.Context, req ListBridgeChatsRequestObject) (ListBridgeChatsResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	chats, err := s.bridges.Chats(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := ListBridgeChats200JSONResponse{Chats: []BridgeChat{}}
	for _, c := range chats {
		out.Chats = append(out.Chats, BridgeChat{Id: c.ID, Name: c.Name})
	}
	return out, nil
}

func (s *server) ListBridgePlugins(ctx context.Context, _ ListBridgePluginsRequestObject) (ListBridgePluginsResponseObject, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	ps, err := s.bridges.Plugins(ctx)
	if err != nil {
		return nil, err
	}
	out := ListBridgePlugins200JSONResponse{Plugins: []BridgePlugin{}}
	for _, p := range ps {
		out.Plugins = append(out.Plugins, pluginOut(p))
	}
	return out, nil
}

func (s *server) SwitchBridgePlugin(ctx context.Context, req SwitchBridgePluginRequestObject) (SwitchBridgePluginResponseObject, error) {
	c, err := requireAdminSession(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.bridges.SwitchPlugin(ctx, c.Member.ID, req.Kind, req.Body.Enabled)
	if err != nil {
		return nil, err
	}
	return SwitchBridgePlugin200JSONResponse(pluginOut(p)), nil
}

// ConnectBridge sends the browser to the plugin's sign-in page, or, when connecting cannot begin, back to the
// settings page, saying why.
func (s *server) ConnectBridge(ctx context.Context, req ConnectBridgeRequestObject) (ConnectBridgeResponseObject, error) {
	c, err := requireAdminSession(ctx)
	if err != nil {
		return s.connectFailed(ctx, err), nil
	}
	to, flow, err := s.bridges.StartConnect(ctx, c.Member.ID, req.Id)
	if err != nil {
		return s.connectFailed(ctx, err), nil
	}
	return redirect{to: to, cookies: []*http.Cookie{bridgeFlowCookieFor(flow)}}, nil
}

// FinishBridgeConnect takes the browser back to the settings page, saying which bridge's account it connected, or
// why it did not.
func (s *server) FinishBridgeConnect(ctx context.Context, req FinishBridgeConnectRequestObject) (FinishBridgeConnectResponseObject, error) {
	clear := expired(bridgeFlowCookie, "/api/v1/bridges/")
	if req.Params.Error != nil {
		return redirect{to: "/settings?bridge_error=denied", cookies: []*http.Cookie{clear}}, nil
	}
	c, err := requireAdminSession(ctx)
	if err != nil {
		return s.connectFailed(ctx, err, clear), nil
	}
	var flow string
	if r := requestOf(ctx); r != nil {
		if ck, err := r.Cookie(bridgeFlowCookie); err == nil {
			flow = ck.Value
		}
	}
	code, state := "", ""
	if req.Params.Code != nil {
		code = *req.Params.Code
	}
	if req.Params.State != nil {
		state = *req.Params.State
	}
	id, err := s.bridges.FinishConnect(ctx, c.Member.ID, code, state, flow)
	if err != nil {
		return s.connectFailed(ctx, err, clear), nil
	}
	return redirect{to: "/settings?bridge_connected=" + strconv.FormatInt(id, 10), cookies: []*http.Cookie{clear}}, nil
}

// connectFailed takes the browser back to the settings page with the code of what went wrong.
func (s *server) connectFailed(ctx context.Context, err error, cookies ...*http.Cookie) redirect {
	code := "internal"
	if f, ok := fail.As(err); ok {
		code = f.Code
	} else {
		s.log.ErrorContext(ctx, "connecting a bridge's account failed", "error", err)
	}
	return redirect{to: "/settings?bridge_error=" + url.QueryEscape(code), cookies: cookies}
}

func (r redirect) VisitConnectBridgeResponse(w http.ResponseWriter) error       { return r.write(w) }
func (r redirect) VisitFinishBridgeConnectResponse(w http.ResponseWriter) error { return r.write(w) }
