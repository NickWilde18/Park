package bridges

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/bridges/bridgestest"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

// env is the service over a database of its own, with the reference plugin installed, an admin, Ana, and a chats file
// for the plugin to read.
type env struct {
	ctx   context.Context
	s     *Service
	pool  *pgxpool.Pool
	logs  *bytes.Buffer
	chats string
	ana   int64
	now   time.Time
}

const chatsFile = `{"account": {"id": "u-ana", "name": "Ana on the platform"},
	"chats": [{"id": "c-reviews", "name": "Reviews", "threads": []}, {"id": "c-team", "name": "Team", "threads": []}]}`

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	plugins, err := Load(ctx, bridgestest.Dir(t, filePlugin))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	pool := storetest.MigratedPool(t)
	e := &env{ctx: ctx, pool: pool, logs: &bytes.Buffer{}, chats: filepath.Join(t.TempDir(), "chats.json"),
		now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(e.logs, nil))
	e.s = New(pool, box, log, Options{PublicURL: "https://park.test", Plugins: plugins, Groups: grouping.New(pool, log),
		Now: func() time.Time { return e.now }})
	e.write(t, chatsFile)
	must(t, pool.QueryRow(ctx, "INSERT INTO member (display_name, admin) VALUES ('Ana', true) RETURNING id").Scan(&e.ana))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// write replaces the chats file the plugin reads.
func (e *env) write(t *testing.T, content string) {
	t.Helper()
	must(t, os.WriteFile(e.chats, []byte(content), 0o644))
}

// wantCode checks that err is the error the caller can act on with code.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	f, ok := fail.As(err)
	if !ok || f.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

// ready makes a Park-run bridge with the file plugin switched on and its credential set.
func (e *env) ready(t *testing.T) Bridge {
	t.Helper()
	_, err := e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	b, err := e.s.Create(e.ctx, e.ana, "Reviews", "", "file")
	must(t, err)
	b, err = e.s.Update(e.ctx, b.ID, Change{Credentials: map[string]string{"file": e.chats}})
	must(t, err)
	return b
}

// connect connects the bridge's account as a member, through the plugin's sign-in, and returns the bridge's ID.
func (e *env) connect(t *testing.T, member, id int64) int64 {
	t.Helper()
	to, flow, err := e.s.StartConnect(e.ctx, member, id)
	must(t, err)
	u, err := url.Parse(to)
	must(t, err)
	got, err := e.s.FinishConnect(e.ctx, member, u.Query().Get("code"), u.Query().Get("state"), flow)
	must(t, err)
	return got
}

// uses opens a bridge's stored tokens, which the reference plugin makes count the operations that used them.
func (e *env) uses(t *testing.T, id int64) int {
	t.Helper()
	var sealed []byte
	must(t, e.pool.QueryRow(e.ctx, "SELECT tokens FROM bridge_account WHERE bridge_id = $1", id).Scan(&sealed))
	data, err := e.s.box.Open(sealed, tokensContext(id))
	must(t, err)
	var tokens struct{ Uses int }
	must(t, json.Unmarshal(data, &tokens))
	return tokens.Uses
}

// The plugins page lists the plugins Park has, off until an admin switches them on. Only a plugin Park has can be
// switched on; one that has left the plugin directory still shows, by its kind, while its switch row lasts, and can be
// switched off, but not on.
func TestPluginSwitches(t *testing.T) {
	e := newEnv(t)
	ps, err := e.s.Plugins(e.ctx)
	must(t, err)
	if len(ps) != 1 || ps[0].Kind != "file" || ps[0].Enabled || !ps[0].Installed || ps[0].ChangedAt != nil {
		t.Fatalf("got %+v, want the file plugin, off and never switched", ps)
	}
	p, err := e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	if !p.Enabled || p.ChangedAt == nil || p.Name != "File" || len(p.Fields) != 1 {
		t.Fatalf("switched on: %+v", p)
	}
	_, err = e.s.SwitchPlugin(e.ctx, e.ana, "gone", true)
	wantCode(t, err, "plugin_missing")
	_, err = e.s.SwitchPlugin(e.ctx, e.ana, "gone", false)
	wantCode(t, err, "not_found")

	_, err = e.pool.Exec(e.ctx, "INSERT INTO bridge_plugin (kind, enabled, changed_by) VALUES ('gone', true, $1)", e.ana)
	must(t, err)
	ps, err = e.s.Plugins(e.ctx)
	must(t, err)
	if len(ps) != 2 || ps[1].Kind != "gone" || ps[1].Installed || !ps[1].Enabled || ps[1].Name != "gone" {
		t.Fatalf("got %+v, want the gone plugin shown by its kind, not installed", ps)
	}
	p, err = e.s.SwitchPlugin(e.ctx, e.ana, "gone", false)
	must(t, err)
	if p.Enabled {
		t.Fatalf("the gone plugin stayed on: %+v", p)
	}
}

// A Park-run bridge needs its plugin switched on to be added, and starts with the default settings and nothing set.
// Its settings are checked: credentials only for the plugin's fields, merged, and an empty value removes one; a chat
// that is the platform's ID, never a link; a period, a window, and a size within their bounds. It keeps its plugin.
func TestParkRunBridgeSettings(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Create(e.ctx, e.ana, "Reviews", "", "file")
	wantCode(t, err, "plugin_off")
	_, err = e.s.Create(e.ctx, e.ana, "Reviews", "", "gone")
	wantCode(t, err, "plugin_missing")
	_, err = e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	_, err = e.s.Create(e.ctx, e.ana, " ", "", "file")
	wantCode(t, err, "invalid_name")
	b, err := e.s.Create(e.ctx, e.ana, " Reviews ", "", "file")
	must(t, err)
	if b.Name != "Reviews" || b.Plugin != "file" || !b.Enabled || b.EveryMinutes != 10 || b.LookbackDays != 45 || b.ProposeFrom != 4 ||
		len(b.Credentials) != 0 || b.Chat.ID != "" || b.Account != nil {
		t.Fatalf("created %+v", b)
	}

	b, err = e.s.Update(e.ctx, b.ID, Change{Credentials: map[string]string{"file": e.chats}})
	must(t, err)
	if len(b.Credentials) != 1 || b.Credentials[0] != "file" {
		t.Fatalf("credentials are %v, want the key set", b.Credentials)
	}
	_, err = e.s.Update(e.ctx, b.ID, Change{Credentials: map[string]string{"token": "x"}})
	wantCode(t, err, "unknown_credential")
	b, err = e.s.Update(e.ctx, b.ID, Change{Credentials: map[string]string{"file": ""}})
	must(t, err)
	if len(b.Credentials) != 0 {
		t.Fatalf("credentials are %v after removing the only one", b.Credentials)
	}

	for _, c := range []struct {
		change Change
		code   string
	}{
		{Change{Chat: &Chat{ID: "https://chat.test/c/1", Name: "Reviews"}}, "invalid_chat"},
		{Change{Chat: &Chat{ID: "c-1", Name: ""}}, "invalid_chat"},
		{Change{EveryMinutes: ptr(0)}, "invalid_every"},
		{Change{EveryMinutes: ptr(1441)}, "invalid_every"},
		{Change{LookbackDays: ptr(366)}, "invalid_lookback"},
		{Change{ProposeFrom: ptr(1)}, "invalid_propose_from"},
		{Change{Plugin: ptr("other")}, "plugin_fixed"},
	} {
		_, err := e.s.Update(e.ctx, b.ID, c.change)
		wantCode(t, err, c.code)
	}
	b, err = e.s.Update(e.ctx, b.ID, Change{Chat: &Chat{ID: " c-reviews ", Name: "Reviews"}, EveryMinutes: ptr(5), LookbackDays: ptr(30),
		ProposeFrom: ptr(0), Enabled: ptr(false), Name: ptr("Review threads")})
	must(t, err)
	if b.Chat != (Chat{ID: "c-reviews", Name: "Reviews"}) || b.EveryMinutes != 5 || b.LookbackDays != 30 || b.ProposeFrom != 0 || b.Enabled ||
		b.Name != "Review threads" {
		t.Fatalf("updated %+v", b)
	}
}

func ptr[T any](v T) *T { return &v }

// An external bridge has no settings in Park, only its name and switch; while it is off, it may not post. Given a
// plugin, it becomes one Park runs: the same bridge, with its tokens revoked, and nothing posts for it through the
// API any more.
func TestExternalBridges(t *testing.T) {
	e := newEnv(t)
	var id int64
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO bridge (name, owner_id) VALUES ('Tracker', $1) RETURNING id", e.ana).Scan(&id))
	_, err := e.pool.Exec(e.ctx, `INSERT INTO api_token (id, member_id, name, secret_hash, scopes, bridge_id)
		VALUES ('t1', $1, 'tracker', '\x00', '{assertions:write}', $2)`, e.ana, id)
	must(t, err)
	must(t, e.s.Accepting(e.ctx, id))
	_, err = e.s.Update(e.ctx, id, Change{EveryMinutes: ptr(5)})
	wantCode(t, err, "external_bridge")
	_, err = e.s.Update(e.ctx, id, Change{Enabled: ptr(false)})
	must(t, err)
	wantCode(t, e.s.Accepting(e.ctx, id), "bridge_disabled")
	_, err = e.s.Update(e.ctx, id, Change{Enabled: ptr(true)})
	must(t, err)

	_, err = e.s.Update(e.ctx, id, Change{Plugin: ptr("file")})
	wantCode(t, err, "plugin_off")
	_, err = e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	b, err := e.s.Update(e.ctx, id, Change{Plugin: ptr("file"), EveryMinutes: ptr(5)})
	must(t, err)
	if b.ID != id || b.Plugin != "file" || b.EveryMinutes != 5 {
		t.Fatalf("converted %+v", b)
	}
	var revoked bool
	must(t, e.pool.QueryRow(e.ctx, "SELECT revoked_at IS NOT NULL FROM api_token WHERE id = 't1'").Scan(&revoked))
	if !revoked {
		t.Fatal("the external bridge's token was not revoked")
	}
	wantCode(t, e.s.Accepting(e.ctx, id), "bridge_run_by_park")
	wantCode(t, e.s.Accepting(e.ctx, id+100), "not_found")
}

// Connecting an account goes through the plugin's sign-in and back. It needs Park's public address and the bridge's
// credentials. Coming back, the state must be the flow's, the member the one who started it, and the flow not
// expired; then the account and its tokens are kept, sealed, and the bridge lists its chats through them.
func TestConnect(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.SwitchPlugin(e.ctx, e.ana, "file", true)
	must(t, err)
	b, err := e.s.Create(e.ctx, e.ana, "Reviews", "", "file")
	must(t, err)
	_, _, err = e.s.StartConnect(e.ctx, e.ana, b.ID)
	wantCode(t, err, "credentials_missing")
	_, err = e.s.Chats(e.ctx, b.ID)
	wantCode(t, err, "credentials_missing")
	_, err = e.s.Update(e.ctx, b.ID, Change{Credentials: map[string]string{"file": e.chats}})
	must(t, err)
	_, err = e.s.Chats(e.ctx, b.ID)
	wantCode(t, err, "no_account")

	unconfigured := New(e.pool, e.s.box, e.s.log, Options{Plugins: []Plugin{e.s.plugins["file"]}})
	_, _, err = unconfigured.StartConnect(e.ctx, e.ana, b.ID)
	wantCode(t, err, "not_configured")

	to, flow, err := e.s.StartConnect(e.ctx, e.ana, b.ID)
	must(t, err)
	u, err := url.Parse(to)
	must(t, err)
	if u.Host != "park.test" || u.Path != RedirectPath || u.Query().Get("state") == "" || u.Query().Get("code") == "" {
		t.Fatalf("the plugin sent the browser to %s", to)
	}
	code, state := u.Query().Get("code"), u.Query().Get("state")
	var bob int64
	must(t, e.pool.QueryRow(e.ctx, "INSERT INTO member (display_name, admin) VALUES ('Bob', true) RETURNING id").Scan(&bob))
	for name, try := range map[string]func() (int64, error){
		"another state":  func() (int64, error) { return e.s.FinishConnect(e.ctx, e.ana, code, state+"x", flow) },
		"another member": func() (int64, error) { return e.s.FinishConnect(e.ctx, bob, code, state, flow) },
		"no cookie":      func() (int64, error) { return e.s.FinishConnect(e.ctx, e.ana, code, state, "") },
	} {
		if _, err := try(); err == nil {
			t.Fatalf("%s: connected", name)
		} else if f, ok := fail.As(err); !ok || f.Code != "connect_expired" {
			t.Fatalf("%s: got %v, want connect_expired", name, err)
		}
	}
	_, err = e.s.FinishConnect(e.ctx, e.ana, "not the code", state, flow)
	wantCode(t, err, "sign_in_refused")
	e.now = e.now.Add(FlowLife)
	_, err = e.s.FinishConnect(e.ctx, e.ana, code, state, flow)
	wantCode(t, err, "connect_expired")
	e.now = e.now.Add(-FlowLife)

	got, err := e.s.FinishConnect(e.ctx, e.ana, code, state, flow)
	must(t, err)
	if got != b.ID {
		t.Fatalf("connected bridge %d, want %d", got, b.ID)
	}
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if a := b.Account; a == nil || a.ID != "u-ana" || a.Name != "Ana on the platform" || a.ConnectedBy != e.ana || a.LapsedAt != nil {
		t.Fatalf("the bridge's account is %+v", b.Account)
	}
	chats, err := e.s.Chats(e.ctx, b.ID)
	must(t, err)
	if len(chats) != 2 || chats[0] != (Chat{ID: "c-reviews", Name: "Reviews"}) {
		t.Fatalf("got chats %+v", chats)
	}
}

// Every operation the plugin answers with renewed tokens leaves them stored, also when it answers with an error
// besides: the old ones are spent. An error saying the sign-in lapsed marks the account lapsed, and says so; the next
// operation that works clears the mark. Any other failure says to look at Park's log, which says why.
func TestTokensAreKept(t *testing.T) {
	e := newEnv(t)
	b := e.ready(t)
	e.connect(t, e.ana, b.ID)
	if n := e.uses(t, b.ID); n != 0 {
		t.Fatalf("a fresh sign-in's tokens were used %d times", n)
	}
	_, err := e.s.Chats(e.ctx, b.ID)
	must(t, err)
	_, err = e.s.Chats(e.ctx, b.ID)
	must(t, err)
	if n := e.uses(t, b.ID); n != 2 {
		t.Fatalf("the stored tokens were used %d times, want the ones renewed by both listings", n)
	}

	e.write(t, `{"error": {"code": "signin_lapsed", "message": "the refresh token expired"}}`)
	_, err = e.s.Chats(e.ctx, b.ID)
	wantCode(t, err, "signin_lapsed")
	if n := e.uses(t, b.ID); n != 3 {
		t.Fatalf("the stored tokens were used %d times, want the ones renewed alongside the error", n)
	}
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if b.Account.LapsedAt == nil {
		t.Fatal("the account was not marked lapsed")
	}

	e.write(t, `{"error": {"code": "rate_limited", "message": "slow down"}}`)
	_, err = e.s.Chats(e.ctx, b.ID)
	wantCode(t, err, "plugin_failed")
	if !strings.Contains(e.logs.String(), "slow down") {
		t.Fatalf("the log does not say why the plugin failed: %s", e.logs)
	}

	e.write(t, chatsFile)
	_, err = e.s.Chats(e.ctx, b.ID)
	must(t, err)
	b, err = e.s.Get(e.ctx, b.ID)
	must(t, err)
	if b.Account.LapsedAt != nil {
		t.Fatal("the lapsed mark stayed after an operation worked")
	}
}

// Operations that use a bridge's account take turns: five listings at once each get the tokens the one before
// renewed, so none is lost, and the stored tokens count all five.
func TestOperationsTakeTurns(t *testing.T) {
	e := newEnv(t)
	b := e.ready(t)
	e.connect(t, e.ana, b.ID)
	errs := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := e.s.Chats(e.ctx, b.ID)
			errs <- err
		}()
	}
	for range 5 {
		must(t, <-errs)
	}
	if n := e.uses(t, b.ID); n != 5 {
		t.Fatalf("the stored tokens count %d uses, want 5: operations used the same tokens at once", n)
	}
}

// Credentials and tokens are stored sealed, each for its own bridge: neither shows in the database, and a sealed value
// copied to another bridge does not open there.
func TestSecretsAreSealed(t *testing.T) {
	e := newEnv(t)
	b := e.ready(t)
	e.connect(t, e.ana, b.ID)
	var creds, tokens []byte
	must(t, e.pool.QueryRow(e.ctx, `SELECT b.credentials, a.tokens FROM bridge b JOIN bridge_account a ON a.bridge_id = b.id
		WHERE b.id = $1`, b.ID).Scan(&creds, &tokens))
	if bytes.Contains(creds, []byte(e.chats)) || bytes.Contains(tokens, []byte("uses")) {
		t.Fatal("a credential or a token is stored in the clear")
	}
	other, err := e.s.Create(e.ctx, e.ana, "Other", "", "file")
	must(t, err)
	_, err = e.pool.Exec(e.ctx, "UPDATE bridge SET credentials = $2 WHERE id = $1", other.ID, creds)
	must(t, err)
	if _, err := e.s.Get(e.ctx, other.ID); err == nil {
		t.Fatal("another bridge's sealed credentials opened")
	}
}
