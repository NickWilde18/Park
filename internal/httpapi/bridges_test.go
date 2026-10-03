package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NickWilde18/Park/internal/bridges"
	"github.com/NickWilde18/Park/internal/bridges/bridgestest"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

// filePlugin is the reference bridge plugin, cmd/park-bridge-file, built once for the package's tests.
var filePlugin string

func TestMain(m *testing.M) {
	path, done, err := bridgestest.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	filePlugin = path
	code := m.Run()
	done()
	os.Exit(code)
}

// bridgesAPI is Park's handler with the reference plugin in its plugin directory, an admin's browser, and a chats
// file for the plugin to read.
type bridgesAPI struct {
	*harness
	admin   *browser
	plugins string
	chats   string
}

func newBridgesAPI(t *testing.T) *bridgesAPI {
	t.Helper()
	pool := storetest.MigratedPool(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	box, err := secret.New(secretKey)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := identity.New(pool, box, log, identity.Options{PublicURL: "https://park.test", SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	dir := bridgestest.Dir(t, filePlugin)
	plugins, err := bridges.Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Ready: ready, UI: built, Logger: log, Identity: ids, Grouping: grouping.New(pool, log),
		Bridges: bridges.New(pool, box, log, bridges.Options{PublicURL: "https://park.test", Plugins: plugins}), Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	a := &bridgesAPI{harness: &harness{h: h, ids: ids, pool: pool, logs: logs}, plugins: dir,
		chats: filepath.Join(t.TempDir(), "chats.json")}
	if err := os.WriteFile(a.chats, []byte(`{"account": {"id": "u-1", "name": "Ana on the platform"},
		"chats": [{"id": "c-reviews", "name": "Reviews", "threads": []}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a.admin = a.harness.admin(t)
	return a
}

// member signs a member who is not an admin in, and returns their browser.
func (a *bridgesAPI) member(t *testing.T) *browser {
	t.Helper()
	var id int64
	if err := a.pool.QueryRow(context.Background(), "INSERT INTO member (display_name) VALUES ('Bob') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	session, _, err := a.ids.StartSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	b := a.browser()
	b.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: session}
	b.refreshCSRF(t)
	return b
}

// follow sends the browser to a Location it was redirected to, within Park, keeping its cookies.
func follow(t *testing.T, b *browser, w interface{ Header() http.Header }) string {
	t.Helper()
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Host != "park.test" {
		t.Fatalf("redirected to %q, want an address in Park", w.Header().Get("Location"))
	}
	res := b.raw("GET", u.RequestURI(), "", "")
	if res.Code != http.StatusFound {
		t.Fatalf("got %d %q, want a redirect back into the dashboard", res.Code, res.Body)
	}
	return res.Header().Get("Location")
}

// An admin sees the plugins Park has — by kind, name, and credential fields, never where the executable is — and
// switches them, from a session only. A member who is not an admin does neither.
func TestBridgePluginsOverHTTP(t *testing.T) {
	a := newBridgesAPI(t)
	var ps BridgePlugins
	w := a.admin.call(t, "GET", "/api/v1/admin/bridge-plugins", nil, &ps)
	wantStatus(t, w, http.StatusOK, "")
	if len(ps.Plugins) != 1 || ps.Plugins[0].Kind != "file" || ps.Plugins[0].Name != "File" || ps.Plugins[0].Enabled ||
		!ps.Plugins[0].Installed || len(ps.Plugins[0].Fields) != 1 {
		t.Fatalf("got %+v", ps)
	}
	if strings.Contains(w.Body.String(), a.plugins) || strings.Contains(w.Body.String(), "park-bridge-file") {
		t.Fatalf("the plugins' listing shows where the plugin is: %s", w.Body)
	}
	var p BridgePlugin
	wantStatus(t, a.admin.call(t, "PUT", "/api/v1/admin/bridge-plugins/file", map[string]bool{"enabled": true}, &p), http.StatusOK, "")
	if !p.Enabled || p.ChangedAt == nil {
		t.Fatalf("switched on: %+v", p)
	}
	wantStatus(t, a.admin.call(t, "PUT", "/api/v1/admin/bridge-plugins/gone", map[string]bool{"enabled": true}, nil),
		http.StatusConflict, "plugin_missing")

	var tok NewToken
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "admin reads", "scopes": []string{"read"}}, &tok),
		http.StatusCreated, "")
	reader := a.browser()
	reader.bearer = tok.Token
	wantStatus(t, reader.call(t, "GET", "/api/v1/admin/bridge-plugins", nil, nil), http.StatusOK, "")
	wantStatus(t, reader.call(t, "PUT", "/api/v1/admin/bridge-plugins/file", map[string]bool{"enabled": false}, nil),
		http.StatusForbidden, "insufficient_scope")
	bob := a.member(t)
	wantStatus(t, bob.call(t, "GET", "/api/v1/admin/bridge-plugins", nil, nil), http.StatusForbidden, "")
	wantStatus(t, bob.call(t, "PUT", "/api/v1/admin/bridge-plugins/file", map[string]bool{"enabled": false}, nil), http.StatusForbidden, "")
}

// An admin adds a bridge Park runs, gives it its credential — which no answer ever shows again — and connects its
// account: Park sends the browser to the plugin's sign-in page with a cookie that only the callback reads, and the
// platform sends it back, connected. The bridge then lists its chats, and the chosen one is kept.
func TestConnectBridgeOverHTTP(t *testing.T) {
	a := newBridgesAPI(t)
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/admin/bridges", map[string]any{"name": "Reviews", "plugin": "file"}, nil),
		http.StatusConflict, "plugin_off")
	a.admin.call(t, "PUT", "/api/v1/admin/bridge-plugins/file", map[string]bool{"enabled": true}, nil)
	var b Bridge
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/admin/bridges", map[string]any{"name": "Reviews", "plugin": "file"}, &b),
		http.StatusCreated, "")
	if b.Plugin == nil || *b.Plugin != "file" || !b.Enabled || b.EveryMinutes != 10 || b.LookbackDays != 45 || b.ProposeFrom != 4 ||
		len(b.Credentials) != 0 || b.Chat != nil || b.Account != nil {
		t.Fatalf("created %+v", b)
	}
	path := "/api/v1/admin/bridges/" + strconv.FormatInt(b.Id, 10)

	w := a.admin.raw("GET", path+"/connect", "", "")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/settings?bridge_error=credentials_missing" {
		t.Fatalf("connecting without credentials: %d to %q", w.Code, w.Header().Get("Location"))
	}
	w = a.admin.call(t, "PATCH", path, map[string]any{"credentials": map[string]string{"file": a.chats}}, &b)
	wantStatus(t, w, http.StatusOK, "")
	if len(b.Credentials) != 1 || b.Credentials[0] != "file" || strings.Contains(w.Body.String(), a.chats) {
		t.Fatalf("the credential is %v, in %s", b.Credentials, w.Body)
	}

	w = a.admin.raw("GET", path+"/connect", "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("connecting: %d %q", w.Code, w.Body)
	}
	cookie := a.admin.cookies[bridgeFlowCookie]
	if cookie == nil || cookie.Path != "/api/v1/bridges/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("the flow's cookie is %+v", cookie)
	}
	to := w.Header().Get("Location")
	if got := follow(t, a.admin, w); got != "/settings?bridge_connected="+strconv.FormatInt(b.Id, 10) {
		t.Fatalf("back to %q", got)
	}
	if a.admin.cookies[bridgeFlowCookie] != nil {
		t.Fatal("the flow's cookie outlived the flow")
	}
	u, _ := url.Parse(to)
	if got := a.admin.raw("GET", u.RequestURI(), "", "").Header().Get("Location"); got != "/settings?bridge_error=connect_expired" {
		t.Fatalf("coming back twice: back to %q", got)
	}
	if got := a.admin.raw("GET", "/api/v1/bridges/callback?error=access_denied", "", "").Header().Get("Location"); got !=
		"/settings?bridge_error=denied" {
		t.Fatalf("a refused sign-in: back to %q", got)
	}
	if got := a.browser().raw("GET", u.RequestURI(), "", "").Header().Get("Location"); !strings.HasPrefix(got, "/settings?bridge_error=") {
		t.Fatalf("coming back signed out: back to %q", got)
	}

	var all Bridges
	wantStatus(t, a.admin.call(t, "GET", "/api/v1/admin/bridges", nil, &all), http.StatusOK, "")
	if len(all.Bridges) != 1 || all.Bridges[0].Account == nil || all.Bridges[0].Account.Id != "u-1" ||
		all.Bridges[0].Account.Name != "Ana on the platform" || all.Bridges[0].Account.LapsedAt != nil {
		t.Fatalf("listed %+v", all.Bridges)
	}
	var chats BridgeChats
	wantStatus(t, a.admin.call(t, "GET", path+"/chats", nil, &chats), http.StatusOK, "")
	if len(chats.Chats) != 1 || chats.Chats[0] != (BridgeChat{Id: "c-reviews", Name: "Reviews"}) {
		t.Fatalf("got chats %+v", chats)
	}
	wantStatus(t, a.admin.call(t, "PATCH", path, map[string]any{"chat": chats.Chats[0], "every_minutes": 5}, &b), http.StatusOK, "")
	if b.Chat == nil || b.Chat.Id != "c-reviews" || b.EveryMinutes != 5 {
		t.Fatalf("chose the chat: %+v", b)
	}
	wantStatus(t, a.admin.call(t, "PATCH", path, map[string]any{"plugin": "other"}, nil), http.StatusConflict, "plugin_fixed")
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "reviews", "scopes": []string{"assertions:write"},
		"bridge_id": b.Id}, nil), http.StatusConflict, "bridge_run_by_park")
	bob := a.member(t)
	wantStatus(t, bob.call(t, "GET", "/api/v1/admin/bridges", nil, nil), http.StatusForbidden, "")
	wantStatus(t, bob.call(t, "PATCH", path, map[string]any{"enabled": false}, nil), http.StatusForbidden, "")
	if got := bob.raw("GET", path+"/connect", "", "").Header().Get("Location"); !strings.HasPrefix(got, "/settings?bridge_error=") {
		t.Fatalf("a member connecting: sent to %q", got)
	}
}

// An external bridge that is switched off has its posts and retractions refused. Given a plugin, it becomes one Park
// runs, its token is revoked, and so the program that ran it cannot post alongside Park.
func TestExternalBridgeSwitchOverHTTP(t *testing.T) {
	a := newBridgesAPI(t)
	var b Bridge
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/admin/bridges", map[string]any{"name": "Tracker"}, &b), http.StatusCreated, "")
	if b.Plugin != nil || !b.Enabled {
		t.Fatalf("created %+v", b)
	}
	var bt NewToken
	wantStatus(t, a.admin.call(t, "POST", "/api/v1/me/tokens", map[string]any{"name": "tracker", "scopes": []string{"assertions:write"},
		"bridge_id": b.Id}, &bt), http.StatusCreated, "")
	tracker := a.browser()
	tracker.bearer = bt.Token
	post := map[string]any{"anchor": "t-1", "pull_requests": []map[string]any{{"repo": "acme/api", "number": 1}, {"repo": "acme/api", "number": 2}}}
	wantStatus(t, tracker.call(t, "POST", "/api/v1/assertions", post, nil), http.StatusBadRequest, "unknown_repo")

	path := "/api/v1/admin/bridges/" + strconv.FormatInt(b.Id, 10)
	wantStatus(t, a.admin.call(t, "PATCH", path, map[string]any{"enabled": false}, &b), http.StatusOK, "")
	if b.Enabled {
		t.Fatalf("switched off: %+v", b)
	}
	wantStatus(t, tracker.call(t, "POST", "/api/v1/assertions", post, nil), http.StatusConflict, "bridge_disabled")
	wantStatus(t, tracker.call(t, "DELETE", "/api/v1/assertions?anchor=t-1", nil, nil), http.StatusConflict, "bridge_disabled")
	wantStatus(t, tracker.call(t, "POST", "/api/v1/proposals", map[string]any{"anchor": "t-1", "groups": []map[string]any{
		{"pull_requests": []map[string]any{{"repo": "acme/api", "number": 1}, {"repo": "acme/api", "number": 2}}}}}, nil),
		http.StatusConflict, "bridge_disabled")
	wantStatus(t, tracker.call(t, "DELETE", "/api/v1/proposals?anchor=t-1", nil, nil), http.StatusConflict, "bridge_disabled")
	wantStatus(t, a.admin.call(t, "PATCH", path, map[string]any{"every_minutes": 5}, nil), http.StatusConflict, "external_bridge")

	a.admin.call(t, "PUT", "/api/v1/admin/bridge-plugins/file", map[string]bool{"enabled": true}, nil)
	wantStatus(t, a.admin.call(t, "PATCH", path, map[string]any{"enabled": true, "plugin": "file"}, &b), http.StatusOK, "")
	if b.Plugin == nil || *b.Plugin != "file" {
		t.Fatalf("converted %+v", b)
	}
	wantStatus(t, tracker.call(t, "POST", "/api/v1/assertions", post, nil), http.StatusUnauthorized, "")
}
