package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/forges"
	_ "github.com/NickWilde18/Park/internal/forges/github"
	"github.com/NickWilde18/Park/internal/forges/github/githubtest"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/rules"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

const secretKey = "0123456789abcdef0123456789abcdef"

// syncBuffer is a log buffer written to from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is Park's handler over a database of its own, with a fake GitHub to sign in through.
type harness struct {
	h     http.Handler
	ids   *identity.Service
	rules *rules.Service
	pool  *pgxpool.Pool
	logs  *syncBuffer
	forge *githubtest.Fake
}

func newAPI(t *testing.T) *harness {
	t.Helper()
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	box, err := secret.New(secretKey)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := identity.New(pool, box, log, identity.Options{PublicURL: "https://park.test", SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	ruleSets := rules.New(pool, log)
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Rules: ruleSets, Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{h: h, ids: ids, rules: ruleSets, pool: pool, logs: logs, forge: githubtest.New(t, "client-secret")}
}

// browser is one client of the API: its cookies, and its CSRF token once it has one.
type browser struct {
	a       *harness
	cookies map[string]*http.Cookie
	csrf    string
	bearer  string
}

func (a *harness) browser() *browser { return &browser{a: a, cookies: map[string]*http.Cookie{}} }

// raw sends a request with a body as it is, and keeps the cookies the answer sets.
func (b *browser) raw(method, target, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	if b.csrf != "" {
		req.Header.Set(csrfHeader, b.csrf)
	}
	if b.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	w := httptest.NewRecorder()
	b.a.h.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return w
}

// call sends a JSON request, and decodes a JSON answer into out, if given.
func (b *browser) call(t *testing.T, method, target string, body, out any) *httptest.ResponseRecorder {
	t.Helper()
	var text, ct string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		text, ct = string(data), "application/json"
	}
	w := b.raw(method, target, ct, text)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v in %q", method, target, err, w.Body)
		}
	}
	return w
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got %d %q, want %d %s", w.Code, w.Body, status, code)
	}
	if code != "" {
		if got := errorCode(t, w); got != code {
			t.Fatalf("got code %q, want %q", got, code)
		}
	}
}

// admin sets Park up through the API and returns the signed-in browser, with its CSRF token.
func (a *harness) admin(t *testing.T) *browser {
	t.Helper()
	code, _, err := a.ids.IssueSetupCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	b := a.browser()
	w := b.call(t, "POST", "/api/v1/setup", map[string]string{"code": code, "username": "root", "password": "a long enough password",
		"display_name": "Root"}, nil)
	wantStatus(t, w, http.StatusCreated, "")
	b.refreshCSRF(t)
	return b
}

func (b *browser) refreshCSRF(t *testing.T) {
	t.Helper()
	var me Me
	wantStatus(t, b.call(t, "GET", "/api/v1/me", nil, &me), http.StatusOK, "")
	if me.CsrfToken == nil {
		t.Fatal("GET /me gave a session no CSRF token")
	}
	b.csrf = *me.CsrfToken
}

// Setting up signs the first admin in with a session cookie that scripts cannot read and other sites do not send;
// a session changes state only with its CSRF token; signing out ends it.
func TestSessionOverHTTP(t *testing.T) {
	a := newAPI(t)
	if _, _, err := a.ids.IssueSetupCode(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	b := a.browser()
	var status SetupStatus
	if w := b.call(t, "GET", "/api/v1/setup", nil, &status); w.Code != http.StatusOK || !status.Needed {
		t.Fatalf("setup needed: %d %+v", w.Code, status)
	}
	w := b.call(t, "POST", "/api/v1/setup", map[string]string{"code": "WRONG-CODE-0000", "username": "root",
		"password": "a long enough password", "display_name": "Root"}, nil)
	wantStatus(t, w, http.StatusForbidden, "invalid_setup_code")

	b = a.admin(t)
	c := b.cookies[sessionCookie]
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("session cookie %+v", c)
	}
	csrf := b.csrf
	b.csrf = ""
	wantStatus(t, b.call(t, "POST", "/api/v1/auth/logout", nil, nil), http.StatusForbidden, "csrf_failed")
	b.csrf = csrf + "x"
	wantStatus(t, b.call(t, "POST", "/api/v1/auth/logout", nil, nil), http.StatusForbidden, "csrf_failed")
	b.csrf = csrf
	wantStatus(t, b.call(t, "POST", "/api/v1/auth/logout", nil, nil), http.StatusNoContent, "")
	if _, ok := b.cookies[sessionCookie]; ok {
		t.Fatal("signing out left the session cookie")
	}
	b.cookies[sessionCookie] = c
	wantStatus(t, b.call(t, "GET", "/api/v1/me", nil, nil), http.StatusUnauthorized, "unauthenticated")

	w = b.call(t, "POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": "a long enough password"}, nil)
	wantStatus(t, w, http.StatusOK, "")
	b.refreshCSRF(t)
}

// The API takes bodies only as JSON, so that other sites cannot post forms or text to it without asking first.
func TestJSONOnly(t *testing.T) {
	a := newAPI(t)
	a.admin(t)
	body := `{"username": "root", "password": "a long enough password"}`
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		w := a.browser().raw("POST", "/api/v1/auth/login", ct, body)
		wantStatus(t, w, http.StatusUnsupportedMediaType, "unsupported_media_type")
	}
	w := a.browser().raw("POST", "/api/v1/auth/login", "application/json; charset=utf-8", body)
	wantStatus(t, w, http.StatusOK, "")
}

// Failed sign-ins are limited, and the answer says when to try again.
func TestLoginRateLimitOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.admin(t)
	for range 5 {
		w := a.browser().call(t, "POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": "wrong password"}, nil)
		wantStatus(t, w, http.StatusUnauthorized, "invalid_credentials")
	}
	w := a.browser().call(t, "POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": "a long enough password"}, nil)
	wantStatus(t, w, http.StatusTooManyRequests, "rate_limited")
	if s, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || s <= 0 {
		t.Fatalf("Retry-After %q", w.Header().Get("Retry-After"))
	}
}

// A token is shown once, when it is made from a browser session; it reads with its scopes, and writes nothing; a
// revoked token stops working at once; a token is never taken from anywhere but the Authorization header.
func TestTokensOverHTTP(t *testing.T) {
	a := newAPI(t)
	b := a.admin(t)
	var made NewToken
	w := b.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "executor", "scopes": []string{"read", "tasks:work"}}, &made)
	wantStatus(t, w, http.StatusCreated, "")
	var list Tokens
	b.call(t, "GET", "/api/v1/me/tokens", nil, &list)
	listed, _ := json.Marshal(list)
	if len(list.Tokens) != 1 || strings.Contains(string(listed), made.Token) {
		t.Fatalf("the list shows the token: %s", listed)
	}

	m := a.browser()
	m.bearer = made.Token
	var me Me
	wantStatus(t, m.call(t, "GET", "/api/v1/me", nil, &me), http.StatusOK, "")
	if me.Scopes == nil || len(*me.Scopes) != 2 || me.CsrfToken != nil {
		t.Fatalf("me %+v", me)
	}
	wantStatus(t, m.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "x", "scopes": []string{"read"}}, nil),
		http.StatusForbidden, "insufficient_scope")
	wantStatus(t, m.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, nil), http.StatusForbidden, "insufficient_scope")
	m.bearer = "park_nope_nope"
	wantStatus(t, m.call(t, "GET", "/api/v1/me", nil, nil), http.StatusUnauthorized, "invalid_token")

	readOnly := a.browser()
	var ro NewToken
	b.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "tasks only", "scopes": []string{"tasks:work"}}, &ro)
	readOnly.bearer = ro.Token
	wantStatus(t, readOnly.call(t, "GET", "/api/v1/me", nil, nil), http.StatusForbidden, "insufficient_scope")

	wantStatus(t, b.call(t, "DELETE", "/api/v1/me/tokens/"+made.Record.Id, nil, nil), http.StatusNoContent, "")
	m.bearer = made.Token
	wantStatus(t, m.call(t, "GET", "/api/v1/me", nil, nil), http.StatusUnauthorized, "invalid_token")

	inURL := a.browser()
	wantStatus(t, inURL.call(t, "GET", "/api/v1/me?access_token="+url.QueryEscape(ro.Token), nil, nil), http.StatusUnauthorized, "unauthenticated")
}

// Admin endpoints are for admins; a configured provider appears on the sign-in page without its secret.
func TestAdminOverHTTP(t *testing.T) {
	a := newAPI(t)
	b := a.admin(t)
	var inv NewInvite
	wantStatus(t, b.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	member := a.browser()
	w := member.call(t, "POST", "/api/v1/auth/register", map[string]string{"invite": inv.Code, "username": "ana",
		"password": "a long enough password", "display_name": "Ana"}, nil)
	wantStatus(t, w, http.StatusCreated, "")
	member.refreshCSRF(t)
	wantStatus(t, member.call(t, "GET", "/api/v1/admin/members", nil, nil), http.StatusForbidden, "forbidden")
	wantStatus(t, member.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "x",
		"base_address": "https://github.com"}, nil), http.StatusForbidden, "forbidden")

	var f Forge
	w = b.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "GitHub", "base_address": a.forge.URL + "/"}, &f)
	wantStatus(t, w, http.StatusCreated, "")
	if f.BaseAddress != a.forge.URL {
		t.Fatalf("base address %q", f.BaseAddress)
	}
	wantStatus(t, b.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "x",
		"base_address": "https://github.com/some/path"}, nil), http.StatusBadRequest, "invalid_forge")
	w = b.call(t, "POST", "/api/v1/admin/providers", map[string]any{"id": "hub", "kind": "forge", "name": "GitHub", "forge_id": f.Id,
		"client_id": "client-id", "client_secret": "client-secret"}, nil)
	wantStatus(t, w, http.StatusCreated, "")
	var providers Providers
	b.call(t, "GET", "/api/v1/admin/providers", nil, &providers)
	var page AuthProviders
	a.browser().call(t, "GET", "/api/v1/auth/providers", nil, &page)
	if !page.Local || len(page.Providers) != 1 || page.Providers[0].Name != "GitHub" || page.Providers[0].Kind != AuthProviderKindForge {
		t.Fatalf("sign-in page %+v", page)
	}
	all, _ := json.Marshal([]any{providers, page})
	if strings.Contains(string(all), "client-secret") {
		t.Fatal("a response shows the client secret")
	}
	var members Members
	b.call(t, "GET", "/api/v1/admin/members", nil, &members)
	if len(members.Members) != 2 {
		t.Fatalf("members %+v", members)
	}
	no := false
	wantStatus(t, b.call(t, "PATCH", "/api/v1/admin/members/"+strconv.FormatInt(members.Members[0].Id, 10),
		map[string]any{"admin": no}, nil), http.StatusConflict, "last_admin")
}

// signInThroughForge follows a forge sign-in in a browser: start, the forge's approval, and the way back.
func (a *harness) signInThroughForge(t *testing.T, b *browser, query string, account forges.Account) *httptest.ResponseRecorder {
	t.Helper()
	w := b.raw("GET", "/api/v1/auth/hub/start"+query, "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %q", w.Code, w.Body)
	}
	to, err := url.Parse(w.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(to.String(), a.forge.URL+"/login/oauth/authorize") {
		t.Fatalf("start went to %q", w.Header().Get("Location"))
	}
	fc := b.cookies[flowCookie]
	if fc == nil || !fc.HttpOnly || !fc.Secure || fc.Path != "/api/v1/auth/" || fc.MaxAge <= 0 {
		t.Fatalf("flow cookie %+v", fc)
	}
	code := a.forge.Authorize(to.Query().Get("code_challenge"), account)
	return b.raw("GET", "/api/v1/auth/hub/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(to.Query().Get("state")), "", "")
}

func (a *harness) hub(t *testing.T, admin *browser) {
	t.Helper()
	var f Forge
	admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "GitHub", "base_address": a.forge.URL}, &f)
	w := admin.call(t, "POST", "/api/v1/admin/providers", map[string]any{"id": "hub", "kind": "forge", "name": "GitHub", "forge_id": f.Id,
		"client_id": "client-id", "client_secret": "client-secret"}, nil)
	wantStatus(t, w, http.StatusCreated, "")
}

// Signing in through a forge: connecting an account from a session, then signing in with it in another browser;
// every failure comes back into Park as a code, never as a page of JSON.
func TestForgeSignInOverHTTP(t *testing.T) {
	a := newAPI(t)
	admin := a.admin(t)
	a.hub(t, admin)
	account := forges.Account{ID: 42, Login: "root-gh", Name: "Root"}

	w := a.signInThroughForge(t, admin, "?mode=link", account)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/settings" {
		t.Fatalf("link: %d %q", w.Code, w.Header().Get("Location"))
	}
	if _, ok := admin.cookies[flowCookie]; ok {
		t.Fatal("the flow cookie outlived the flow")
	}
	var ids Identities
	admin.call(t, "GET", "/api/v1/me/identities", nil, &ids)
	if len(ids.Identities) != 1 || ids.Identities[0].AccountId != 42 || ids.Identities[0].LinkedBy != nil {
		t.Fatalf("identities %+v", ids)
	}

	other := a.browser()
	w = a.signInThroughForge(t, other, "", account)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || other.cookies[sessionCookie] == nil {
		t.Fatalf("sign in: %d %q", w.Code, w.Header().Get("Location"))
	}
	other.refreshCSRF(t)

	stranger := a.browser()
	w = a.signInThroughForge(t, stranger, "", forges.Account{ID: 7, Login: "stranger"})
	if w.Header().Get("Location") != "/?sign_in_error=not_invited" || stranger.cookies[sessionCookie] != nil {
		t.Fatalf("stranger: %q", w.Header().Get("Location"))
	}
	w = stranger.raw("GET", "/api/v1/auth/hub/callback?error=access_denied", "", "")
	if w.Header().Get("Location") != "/?sign_in_error=denied" {
		t.Fatalf("denied: %q", w.Header().Get("Location"))
	}
	w = a.browser().raw("GET", "/api/v1/auth/hub/callback?code=x&state=y", "", "")
	if w.Header().Get("Location") != "/?sign_in_error=sign_in_expired" {
		t.Fatalf("no flow cookie: %q", w.Header().Get("Location"))
	}
	w = a.browser().raw("GET", "/api/v1/auth/missing/start", "", "")
	if w.Header().Get("Location") != "/?sign_in_error=not_found" {
		t.Fatalf("unknown provider: %q", w.Header().Get("Location"))
	}
}

// I10. A member keeps a way in. One who joined through forge sign-in has no password, so the account they signed in
// with is their only way in: unlinking it is refused, and they still sign in as themselves. The admin, who has a
// password, may unlink the account they connected; and once the member has connected a second account on the forge,
// they may unlink the first, and sign in with the second.
func TestUnlinkKeepsAWayIn(t *testing.T) {
	a := newAPI(t)
	admin := a.admin(t)
	a.hub(t, admin)
	var inv NewInvite
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv), http.StatusCreated, "")
	first := forges.Account{ID: 9, Login: "joiner"}
	joiner := a.browser()
	if w := a.signInThroughForge(t, joiner, "?invite="+url.QueryEscape(inv.Code), first); w.Header().Get("Location") != "/" {
		t.Fatalf("joining: %q", w.Header().Get("Location"))
	}
	joiner.refreshCSRF(t)
	var me Me
	joiner.call(t, "GET", "/api/v1/me", nil, &me)
	linked := func(b *browser) []Identity {
		t.Helper()
		var ids Identities
		wantStatus(t, b.call(t, "GET", "/api/v1/me/identities", nil, &ids), http.StatusOK, "")
		return ids.Identities
	}
	unlink := func(b *browser, id int64) *httptest.ResponseRecorder {
		return b.call(t, "DELETE", "/api/v1/me/identities/"+strconv.FormatInt(id, 10), nil, nil)
	}
	signsInAs := func(account forges.Account) int64 {
		t.Helper()
		b := a.browser()
		if w := a.signInThroughForge(t, b, "", account); w.Header().Get("Location") != "/" {
			t.Fatalf("signing in as %s: %q", account.Login, w.Header().Get("Location"))
		}
		b.refreshCSRF(t)
		var who Me
		b.call(t, "GET", "/api/v1/me", nil, &who)
		return who.Member.Id
	}

	ids := linked(joiner)
	if len(ids) != 1 || ids[0].AccountId != 9 {
		t.Fatalf("the joiner's accounts: %+v", ids)
	}
	wantStatus(t, unlink(joiner, ids[0].Id), http.StatusConflict, "last_sign_in")
	if got := signsInAs(first); got != me.Member.Id {
		t.Fatalf("the joiner signed in as member %d, not %d", got, me.Member.Id)
	}

	// An account on a forge with no sign-in provider, which an admin linked by hand, is no way in: it does not count
	// toward the joiner's ways in, and unlinking it is no loss.
	var other Forge
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/forges", map[string]string{"kind": "github", "name": "Other",
		"base_address": "https://other.example.test"}, &other), http.StatusCreated, "")
	wantStatus(t, admin.call(t, "POST", "/api/v1/admin/members/"+strconv.FormatInt(me.Member.Id, 10)+"/identities",
		map[string]any{"forge_id": other.Id, "account_id": 77}, nil), http.StatusCreated, "")
	ids = linked(joiner)
	if len(ids) != 2 || ids[0].AccountId != 9 || ids[1].AccountId != 77 {
		t.Fatalf("the joiner's accounts with one linked by hand: %+v", ids)
	}
	wantStatus(t, unlink(joiner, ids[0].Id), http.StatusConflict, "last_sign_in")
	wantStatus(t, unlink(joiner, ids[1].Id), http.StatusNoContent, "")

	a.signInThroughForge(t, admin, "?mode=link", forges.Account{ID: 42, Login: "root-gh"})
	mine := linked(admin)
	if len(mine) != 1 {
		t.Fatalf("the admin's accounts: %+v", mine)
	}
	wantStatus(t, unlink(admin, mine[0].Id), http.StatusNoContent, "")

	second := forges.Account{ID: 10, Login: "joiner-two"}
	a.signInThroughForge(t, joiner, "?mode=link", second)
	if ids = linked(joiner); len(ids) != 2 {
		t.Fatalf("the joiner's accounts after connecting another: %+v", ids)
	}
	wantStatus(t, unlink(joiner, ids[0].Id), http.StatusNoContent, "")
	if got := signsInAs(second); got != me.Member.Id {
		t.Fatalf("with the second account, the joiner signed in as member %d, not %d", got, me.Member.Id)
	}
}

// I9. Passwords, tokens, session IDs, codes, and provider credentials never reach the log, whatever happens.
func TestNoSecretsInLogs(t *testing.T) {
	a := newAPI(t)
	code, _, _ := a.ids.IssueSetupCode(context.Background(), "")
	b := a.browser()
	b.call(t, "POST", "/api/v1/setup", map[string]string{"code": "WRONG-WRONG-0", "username": "root", "password": "wrong but long enough", "display_name": "Root"}, nil)
	b.call(t, "POST", "/api/v1/setup", map[string]string{"code": code, "username": "root", "password": "the admin password", "display_name": "Root"}, nil)
	b.refreshCSRF(t)
	a.browser().call(t, "POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": "a mistyped password"}, nil)
	var made NewToken
	b.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "t", "scopes": []string{"read"}}, &made)
	tok := a.browser()
	tok.bearer = made.Token
	tok.call(t, "GET", "/api/v1/me", nil, nil)
	tok.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "t", "scopes": []string{"read"}}, nil)
	tok.bearer = made.Token + "x"
	tok.call(t, "GET", "/api/v1/me", nil, nil)
	var inv NewInvite
	b.call(t, "POST", "/api/v1/admin/invites", map[string]any{}, &inv)
	a.hub(t, b)
	stranger := a.browser()
	a.signInThroughForge(t, stranger, "?invite="+url.QueryEscape(inv.Code), forges.Account{ID: 9, Login: "joiner"})
	a.signInThroughForge(t, a.browser(), "", forges.Account{ID: 10, Login: "stranger"})

	logs := a.logs.String()
	if logs == "" {
		t.Fatal("nothing was logged")
	}
	secrets := map[string]string{
		"setup code": code, "password": "the admin password", "wrong password": "a mistyped password",
		"token": made.Token, "token secret": strings.TrimPrefix(made.Token, "park_"+made.Record.Id+"_"),
		"session": b.cookies[sessionCookie].Value, "csrf": b.csrf, "invite": inv.Code, "client secret": "client-secret",
	}
	if sc := stranger.cookies[sessionCookie]; sc != nil {
		secrets["joiner's session"] = sc.Value
	}
	for what, s := range secrets {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("the log holds the %s", what)
		}
	}
	if strings.Contains(logs, "code-") || strings.Contains(logs, "user-token-") {
		t.Error("the log holds a forge authorization code or access token")
	}
}
