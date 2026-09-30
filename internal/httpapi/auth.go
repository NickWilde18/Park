package httpapi

import (
	"context"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/identity"
)

const (
	sessionCookie = "park_session"
	flowCookie    = "park_sign_in"
	csrfHeader    = "X-CSRF-Token"
)

type ctxKey int

const (
	requestKey ctxKey = iota
	callerKey
	sessionKey
)

// callerOf returns who is calling, or nil for an anonymous request.
func callerOf(ctx context.Context) *identity.Caller {
	c, _ := ctx.Value(callerKey).(*identity.Caller)
	return c
}

// sessionOf returns the browser session's ID, when the call came with one.
func sessionOf(ctx context.Context) string {
	s, _ := ctx.Value(sessionKey).(string)
	return s
}

func requestOf(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey).(*http.Request)
	return r
}

// clientAddress is the address the request came from, without its port.
func clientAddress(ctx context.Context) string {
	r := requestOf(ctx)
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var errSignInFirst = fail.New(fail.Unauthenticated, "unauthenticated", "Sign in first.")

// requireCaller returns the caller, or an error for an anonymous request.
func requireCaller(ctx context.Context) (*identity.Caller, error) {
	if c := callerOf(ctx); c != nil {
		return c, nil
	}
	return nil, errSignInFirst
}

// requireAdmin returns the caller if they are an admin. Admins read with a session or a read token; admin writes
// are checked again, for a session, by the service that makes them.
func requireAdmin(ctx context.Context) (*identity.Caller, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Member.Admin {
		return nil, fail.New(fail.Forbidden, "forbidden", "Only an admin may do this.")
	}
	return c, nil
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// csrfExempt are the requests that start a session rather than use one.
func csrfExempt(path string) bool {
	switch path {
	case "/api/v1/setup", "/api/v1/auth/login", "/api/v1/auth/register":
		return true
	}
	return false
}

// authenticate works out who is calling an API path: an API token in the Authorization header, or a browser session
// in its cookie. A token that does not work is refused outright. A session that changes state must carry its CSRF
// token. A token may only read, for now: nothing a token may write exists yet.
func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if auth := r.Header.Get("Authorization"); auth != "" {
			token, ok := strings.CutPrefix(auth, "Bearer ")
			if !ok {
				s.respondError(w, r, fail.New(fail.Unauthenticated, "invalid_token", "Send an API token as: Authorization: Bearer park_…"))
				return
			}
			c, err := s.id.TokenCaller(ctx, token)
			if err != nil {
				s.respondError(w, r, err)
				return
			}
			if unsafeMethod(r.Method) || !c.HasScope(identity.ScopeRead) {
				writeError(w, http.StatusForbidden, "insufficient_scope", "This token's scopes do not allow this request.")
				return
			}
			ctx = context.WithValue(ctx, callerKey, c)
		} else if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
			c, err := s.id.SessionCaller(ctx, ck.Value)
			var f *fail.Error
			switch {
			case err == nil:
				if unsafeMethod(r.Method) && !csrfExempt(r.URL.Path) && !s.id.CheckCSRF(ck.Value, r.Header.Get(csrfHeader)) {
					writeError(w, http.StatusForbidden, "csrf_failed", "The request lacks this session's CSRF token, from GET /me.")
					return
				}
				ctx = context.WithValue(ctx, callerKey, c)
				ctx = context.WithValue(ctx, sessionKey, ck.Value)
			case errors.As(err, &f) && f.Kind == fail.Unauthenticated:
				// An ended or expired session: the request goes on without one.
			default:
				s.respondError(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, requestKey, r)))
	})
}

// guardJSON refuses a request under /api/ that sends a body that is not JSON. Browsers send forms and text/plain
// across sites without asking first, so this keeps other sites from posting to the API.
func guardJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && unsafeMethod(r.Method) {
			ct := r.Header.Get("Content-Type")
			if ct == "" && r.ContentLength > 0 {
				ct = "none"
			}
			if ct != "" {
				if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
					writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send request bodies as application/json.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sessionCookieFor is the cookie that holds a session.
func sessionCookieFor(id string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: id, Path: "/", Expires: expires, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode}
}

func expired(name, path string) *http.Cookie {
	return &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// withSession answers a request that signed a browser in: it sets the session cookie, and sends the member.
type withSession struct {
	cookie *http.Cookie
	member Member
	status int
}

func (r withSession) write(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	writeJSON(w, r.status, r.member)
	return nil
}

func (r withSession) VisitCompleteSetupResponse(w http.ResponseWriter) error { return r.write(w) }
func (r withSession) VisitLoginResponse(w http.ResponseWriter) error         { return r.write(w) }
func (r withSession) VisitRegisterResponse(w http.ResponseWriter) error      { return r.write(w) }

func (s *server) signIn(ctx context.Context, m identity.Member, status int) (withSession, error) {
	id, expires, err := s.id.StartSession(ctx, m.ID)
	if err != nil {
		return withSession{}, err
	}
	return withSession{cookie: sessionCookieFor(id, expires), member: member(m), status: status}, nil
}

// signedOut answers a sign-out: it clears the session cookie.
type signedOut struct{}

func (signedOut) VisitLogoutResponse(w http.ResponseWriter) error {
	http.SetCookie(w, expired(sessionCookie, "/"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// redirect sends the browser elsewhere, setting cookies on the way.
type redirect struct {
	to      string
	cookies []*http.Cookie
}

func (r redirect) write(w http.ResponseWriter) error {
	for _, c := range r.cookies {
		http.SetCookie(w, c)
	}
	w.Header().Set("Location", r.to)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	return nil
}

func (r redirect) VisitStartSignInResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r redirect) VisitFinishSignInResponse(w http.ResponseWriter) error { return r.write(w) }

// signInFailed sends the browser back into Park with the error's code; an internal error is logged.
func (s *server) signInFailed(ctx context.Context, err error, cookies ...*http.Cookie) redirect {
	code := "internal"
	if f, ok := fail.As(err); ok {
		code = f.Code
	} else {
		s.log.ErrorContext(ctx, "forge sign-in failed", "error", err)
	}
	return redirect{to: "/?sign_in_error=" + url.QueryEscape(code), cookies: cookies}
}

func (s *server) GetSetup(ctx context.Context, _ GetSetupRequestObject) (GetSetupResponseObject, error) {
	needed, err := s.id.SetupNeeded(ctx)
	if err != nil {
		return nil, err
	}
	return GetSetup200JSONResponse{Needed: needed}, nil
}

func (s *server) CompleteSetup(ctx context.Context, req CompleteSetupRequestObject) (CompleteSetupResponseObject, error) {
	b := req.Body
	m, err := s.id.Setup(ctx, b.Code, b.Username, b.Password, b.DisplayName, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	return s.signIn(ctx, m, http.StatusCreated)
}

func (s *server) ListAuthProviders(ctx context.Context, _ ListAuthProvidersRequestObject) (ListAuthProvidersResponseObject, error) {
	ps, err := s.id.Providers(ctx)
	if err != nil {
		return nil, err
	}
	out := ListAuthProviders200JSONResponse{Local: true, Providers: []AuthProvider{}}
	for _, p := range ps {
		out.Providers = append(out.Providers, AuthProvider{Id: p.ID, Kind: AuthProviderKind(p.Kind), Name: p.Name})
	}
	return out, nil
}

func (s *server) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	m, err := s.id.Login(ctx, req.Body.Username, req.Body.Password, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	return s.signIn(ctx, m, http.StatusOK)
}

func (s *server) Register(ctx context.Context, req RegisterRequestObject) (RegisterResponseObject, error) {
	b := req.Body
	m, err := s.id.Register(ctx, b.Invite, b.Username, b.Password, b.DisplayName, clientAddress(ctx))
	if err != nil {
		return nil, err
	}
	return s.signIn(ctx, m, http.StatusCreated)
}

func (s *server) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	id := sessionOf(ctx)
	if id == "" {
		return nil, errSignInFirst
	}
	if err := s.id.EndSession(ctx, id); err != nil {
		return nil, err
	}
	return signedOut{}, nil
}

func flowCookieFor(value string) *http.Cookie {
	return &http.Cookie{Name: flowCookie, Value: value, Path: "/api/v1/auth/", MaxAge: int(identity.FlowLife.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

func (s *server) StartSignIn(ctx context.Context, req StartSignInRequestObject) (StartSignInResponseObject, error) {
	mode := identity.ModeSignIn
	if req.Params.Mode != nil {
		mode = string(*req.Params.Mode)
	}
	invite := ""
	if req.Params.Invite != nil {
		invite = *req.Params.Invite
	}
	to, flow, err := s.id.StartSignIn(ctx, req.Provider, mode, invite, callerOf(ctx))
	if err != nil {
		return s.signInFailed(ctx, err), nil
	}
	return redirect{to: to, cookies: []*http.Cookie{flowCookieFor(flow)}}, nil
}

func (s *server) FinishSignIn(ctx context.Context, req FinishSignInRequestObject) (FinishSignInResponseObject, error) {
	clear := expired(flowCookie, "/api/v1/auth/")
	if req.Params.Error != nil {
		return redirect{to: "/?sign_in_error=denied", cookies: []*http.Cookie{clear}}, nil
	}
	var flow string
	if r := requestOf(ctx); r != nil {
		if ck, err := r.Cookie(flowCookie); err == nil {
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
	res, err := s.id.FinishSignIn(ctx, req.Provider, code, state, flow, callerOf(ctx))
	if err != nil {
		return s.signInFailed(ctx, err, clear), nil
	}
	if res.Mode == identity.ModeLink {
		return redirect{to: "/settings", cookies: []*http.Cookie{clear}}, nil
	}
	id, expires, err := s.id.StartSession(ctx, res.Member.ID)
	if err != nil {
		return s.signInFailed(ctx, err, clear), nil
	}
	return redirect{to: "/", cookies: []*http.Cookie{clear, sessionCookieFor(id, expires)}}, nil
}
