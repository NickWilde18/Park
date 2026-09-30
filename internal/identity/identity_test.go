package identity

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
	_ "github.com/NickWilde18/Park/internal/forges/github"
	"github.com/NickWilde18/Park/internal/forges/github/githubtest"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

const serverKey = "0123456789abcdef0123456789abcdef"

type env struct {
	s     *Service
	pool  *pgxpool.Pool
	clock *clock
	forge *githubtest.Fake
	ctx   context.Context
}

// clock is a settable time.
type clock struct {
	t  time.Time
	mu sync.Mutex
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	box, err := secret.New(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(pool, box, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Options{PublicURL: "https://park.test", SecretKey: serverKey})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Now()}
	s.now = c.now
	return &env{s: s, pool: pool, clock: c, forge: githubtest.New(t, "client-secret"), ctx: context.Background()}
}

// code returns the stable code of a fail.Error, or "" for any other error.
func code(err error) string {
	if e, ok := fail.As(err); ok {
		return e.Code
	}
	return ""
}

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if got := code(err); got != want {
		t.Fatalf("got error %v (code %q), want code %q", err, got, want)
	}
}

// admin sets Park up and returns its first administrator, as a browser-session caller.
func (e *env) admin(t *testing.T) *Caller {
	t.Helper()
	code, needed, err := e.s.IssueSetupCode(e.ctx, "")
	if err != nil || !needed {
		t.Fatalf("setup code: %v, needed %v", err, needed)
	}
	m, err := e.s.Setup(e.ctx, code, "root", "a long enough password", "Root", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return &Caller{Member: m}
}

// member creates a member with a local account through an invite, and returns them as a browser-session caller.
func (e *env) member(t *testing.T, admin *Caller, username string) *Caller {
	t.Helper()
	invite, _, err := e.s.CreateInvite(e.ctx, admin, 0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.s.Register(e.ctx, invite, username, "a long enough password", strings.ToUpper(username[:1])+username[1:], "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	return &Caller{Member: m}
}

// provider creates a forge at the fake's address and a sign-in provider for it.
func (e *env) provider(t *testing.T, admin *Caller, id string) forges.Forge {
	t.Helper()
	f, err := forges.Create(e.ctx, e.pool, "github", "Forge "+id, e.forge.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.CreateProvider(e.ctx, admin, ProviderInput{ID: id, Kind: ProviderKindForge, Name: "Forge", ForgeID: f.ID,
		ClientID: "client-id", ClientSecret: "client-secret"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// signIn runs a whole forge sign-in: start, the forge's approval, and the browser coming back.
func (e *env) signIn(t *testing.T, provider, mode, invite string, c *Caller, a forges.Account) (SignedIn, error) {
	t.Helper()
	to, cookie, err := e.s.StartSignIn(e.ctx, provider, mode, invite, c)
	if err != nil {
		return SignedIn{}, err
	}
	u, err := url.Parse(to)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(to, e.forge.URL+"/login/oauth/authorize?") || u.Query().Get("code_challenge_method") != "S256" ||
		u.Query().Get("redirect_uri") != "https://park.test/api/v1/auth/"+provider+"/callback" {
		t.Fatalf("authorize at %s", to)
	}
	code := e.forge.Authorize(u.Query().Get("code_challenge"), a)
	return e.s.FinishSignIn(e.ctx, provider, code, u.Query().Get("state"), cookie, c)
}

// I1. With no members, Park has no account to sign in to, and the first administrator needs the setup code — a
// code that is wrong, expired, or already used does not work.
func TestNoDefaultAccount(t *testing.T) {
	e := newEnv(t)
	if needed, err := e.s.SetupNeeded(e.ctx); err != nil || !needed {
		t.Fatalf("setup needed: %v, %v", needed, err)
	}
	for _, pw := range []string{"admin", "password", ""} {
		_, err := e.s.Login(e.ctx, "admin", pw, "10.0.0.9")
		wantCode(t, err, "invalid_credentials")
	}
	code, needed, err := e.s.IssueSetupCode(e.ctx, "")
	if err != nil || !needed || len(code) != 14 {
		t.Fatalf("code %q, needed %v, %v", code, needed, err)
	}
	_, err = e.s.Setup(e.ctx, "WRONG-CODE-0000", "root", "a long enough password", "Root", "10.0.0.1")
	wantCode(t, err, "invalid_setup_code")

	e.clock.advance(61 * time.Minute)
	_, err = e.s.Setup(e.ctx, code, "root", "a long enough password", "Root", "10.0.0.1")
	wantCode(t, err, "invalid_setup_code")

	code, _, _ = e.s.IssueSetupCode(e.ctx, "") // restarting issues a new code
	m, err := e.s.Setup(e.ctx, strings.ToLower(strings.ReplaceAll(code, "-", "")), "root", "a long enough password", "Root", "10.0.0.1")
	if err != nil || !m.Admin || !m.Rules {
		t.Fatalf("setup: %+v, %v", m, err)
	}
	_, err = e.s.Setup(e.ctx, code, "root2", "a long enough password", "Root", "10.0.0.1")
	wantCode(t, err, "setup_done")
	if _, needed, err := e.s.IssueSetupCode(e.ctx, ""); err != nil || needed {
		t.Fatalf("after setup: needed %v, %v", needed, err)
	}
}

// A setup code taken from the environment works, and is not echoed back to be printed.
func TestSetupCodeFromEnvironment(t *testing.T) {
	e := newEnv(t)
	code, needed, err := e.s.IssueSetupCode(e.ctx, "OPER-ATOR-CODE")
	if err != nil || !needed || code != "" {
		t.Fatalf("code %q, needed %v, %v", code, needed, err)
	}
	if _, err := e.s.Setup(e.ctx, "OPER-ATOR-CODE", "root", "a long enough password", "Root", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
}

// I2. Passwords are stored as argon2id hashes, and failed sign-ins are limited per username and per address.
func TestPasswords(t *testing.T) {
	e := newEnv(t)
	e.admin(t)
	var hash string
	if err := e.pool.QueryRow(e.ctx, "SELECT password_hash FROM local_credential WHERE username = 'root'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") || strings.Contains(hash, "a long enough password") {
		t.Fatalf("stored %q", hash)
	}
	if _, err := e.s.Login(e.ctx, "ROOT", "a long enough password", "10.0.0.1"); err != nil {
		t.Fatalf("usernames are not case-sensitive: %v", err)
	}
}

func TestLoginRateLimits(t *testing.T) {
	e := newEnv(t)
	e.admin(t)
	for i := range accountFailures {
		_, err := e.s.Login(e.ctx, "root", "wrong password "+strconv.Itoa(i), "10.0.0."+strconv.Itoa(i))
		wantCode(t, err, "invalid_credentials")
	}
	_, err := e.s.Login(e.ctx, "root", "a long enough password", "10.0.0.99")
	wantCode(t, err, "rate_limited")
	if e, _ := fail.As(err); e.RetryAfter <= 0 || e.RetryAfter > 15*time.Minute {
		t.Fatalf("retry after %v", e.RetryAfter)
	}
	e.clock.advance(15 * time.Minute)
	if _, err := e.s.Login(e.ctx, "root", "a long enough password", "10.0.0.99"); err != nil {
		t.Fatalf("after the window: %v", err)
	}

	// From one address, failures across many usernames add up.
	for i := range addressFailures {
		_, _ = e.s.Login(e.ctx, "nobody"+strconv.Itoa(i), "wrong password", "10.9.9.9")
	}
	_, err = e.s.Login(e.ctx, "root", "a long enough password", "10.9.9.9")
	wantCode(t, err, "rate_limited")
	if _, err := e.s.Login(e.ctx, "root", "a long enough password", "10.0.0.98"); err != nil {
		t.Fatalf("another address: %v", err)
	}
}

// Local accounts: an invite makes one member, once; usernames are unique; weak input is refused.
func TestRegister(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	invite, inv, err := e.s.CreateInvite(e.ctx, admin, 0)
	if err != nil || !inv.ExpiresAt.After(e.clock.now().Add(6*24*time.Hour)) {
		t.Fatalf("invite %+v, %v", inv, err)
	}
	_, err = e.s.Register(e.ctx, invite, "ana", "short", "Ana", "10.0.0.2")
	wantCode(t, err, "invalid_password")
	_, err = e.s.Register(e.ctx, invite, "root", "a long enough password", "Ana", "10.0.0.2")
	wantCode(t, err, "username_taken")
	m, err := e.s.Register(e.ctx, invite, "Ana", "a long enough password", "Ana", "10.0.0.2")
	if err != nil || m.Admin || m.Rules {
		t.Fatalf("register: %+v, %v", m, err)
	}
	_, err = e.s.Register(e.ctx, invite, "ben", "a long enough password", "Ben", "10.0.0.2")
	wantCode(t, err, "invalid_invite")

	expiring, _, _ := e.s.CreateInvite(e.ctx, admin, time.Hour)
	e.clock.advance(2 * time.Hour)
	_, err = e.s.Register(e.ctx, expiring, "ben", "a long enough password", "Ben", "10.0.0.2")
	wantCode(t, err, "invalid_invite")

	if _, _, err := e.s.CreateInvite(e.ctx, &Caller{Member: m}, 0); code(err) != "forbidden" {
		t.Fatalf("a member created an invite: %v", err)
	}
}

// Sessions: a session signs its browser in until it ends or expires. The CSRF token belongs to its session.
func TestSessions(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	id, expires, err := e.s.StartSession(e.ctx, admin.Member.ID)
	if err != nil || !expires.After(e.clock.now().Add(29*24*time.Hour)) {
		t.Fatalf("start: %v, %v", expires, err)
	}
	var stored []byte
	if err := e.pool.QueryRow(e.ctx, "SELECT id_hash FROM browser_session").Scan(&stored); err != nil || bytes.Contains(stored, []byte(id)) {
		t.Fatalf("stored %x, %v", stored, err)
	}
	if c, err := e.s.SessionCaller(e.ctx, id); err != nil || c.Member.ID != admin.Member.ID || c.TokenID != "" {
		t.Fatalf("caller %+v, %v", c, err)
	}
	other, _, _ := e.s.StartSession(e.ctx, admin.Member.ID)
	if !e.s.CheckCSRF(id, e.s.CSRF(id)) || e.s.CheckCSRF(id, e.s.CSRF(other)) || e.s.CheckCSRF(id, "") {
		t.Fatal("the CSRF token is not tied to its session")
	}
	if err := e.s.EndSession(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.SessionCaller(e.ctx, id)
	wantCode(t, err, "unauthenticated")
	e.clock.advance(SessionLife + time.Minute)
	_, err = e.s.SessionCaller(e.ctx, other)
	wantCode(t, err, "unauthenticated")
}

// I6. A token is shown once and stored only as a hash; it carries its scopes; revoking or expiry ends it at once;
// and tokens are made only in a browser session.
func TestTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	token, rec, err := e.s.CreateToken(e.ctx, admin, "executor", []string{ScopeTasksWork, ScopeRead, ScopeRead}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "park_"+rec.ID+"_") || len(rec.Scopes) != 2 {
		t.Fatalf("token %q, record %+v", token, rec)
	}
	var row string
	if err := e.pool.QueryRow(e.ctx, "SELECT concat_ws(' ', id, name, encode(secret_hash, 'escape'), scopes::text) FROM api_token").Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, strings.TrimPrefix(token, "park_"+rec.ID+"_")) {
		t.Fatal("the token's secret is stored")
	}
	list, err := e.s.Tokens(e.ctx, admin.Member.ID)
	if err != nil || len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("list %+v, %v", list, err)
	}
	c, err := e.s.TokenCaller(e.ctx, token)
	if err != nil || c.Member.ID != admin.Member.ID || !c.HasScope(ScopeTasksWork) || c.HasScope("assertions:write") {
		t.Fatalf("caller %+v, %v", c, err)
	}
	_, _, err = e.s.CreateToken(e.ctx, c, "from a token", []string{ScopeRead}, nil, nil)
	wantCode(t, err, "session_required")
	_, _, err = e.s.CreateToken(e.ctx, admin, "bridge", []string{"assertions:write"}, nil, nil)
	wantCode(t, err, "invalid_scopes")

	for _, bad := range []string{"", "park_", "park_" + rec.ID, "park_" + rec.ID + "_wrong", strings.Replace(token, "park_", "bark_", 1)} {
		_, err := e.s.TokenCaller(e.ctx, bad)
		wantCode(t, err, "invalid_token")
	}
	if err := e.s.RevokeToken(e.ctx, admin.Member.ID, rec.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.TokenCaller(e.ctx, token)
	wantCode(t, err, "invalid_token")

	soon := e.clock.now().Add(time.Hour)
	expiring, _, err := e.s.CreateToken(e.ctx, admin, "short-lived", []string{ScopeRead}, &soon, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.advance(2 * time.Hour)
	_, err = e.s.TokenCaller(e.ctx, expiring)
	wantCode(t, err, "invalid_token")
}

// I3 and I5. A member links a forge account by signing in to the forge; the link is verified, belongs to one member,
// and afterwards signing in through the forge is the same member.
func TestLinkThroughForge(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	ana := e.member(t, admin, "ana")
	account := forges.Account{ID: 42, Login: "ana-gh", Name: "Ana"}

	got, err := e.signIn(t, "hub", ModeLink, "", ana, account)
	if err != nil || got.Member.ID != ana.Member.ID {
		t.Fatalf("link: %+v, %v", got, err)
	}
	ids, err := e.s.Identities(e.ctx, ana.Member.ID)
	if err != nil || len(ids) != 1 || ids[0].AccountID != 42 || ids[0].Login != "ana-gh" || ids[0].LinkedBy != nil {
		t.Fatalf("identities %+v, %v", ids, err)
	}
	if got, err := e.signIn(t, "hub", ModeSignIn, "", nil, account); err != nil || got.Member.ID != ana.Member.ID || got.Joined {
		t.Fatalf("sign in: %+v, %v", got, err)
	}
	ben := e.member(t, admin, "ben")
	_, err = e.signIn(t, "hub", ModeLink, "", ben, account)
	wantCode(t, err, "already_linked")
	_, err = e.signIn(t, "hub", ModeLink, "", nil, account)
	wantCode(t, err, "unauthenticated")
}

// A flow finishes only in the browser that started it, with its state, before it expires; and a link finishes only
// for the member who started it.
func TestSignInFlowIsBound(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	ana := e.member(t, admin, "ana")
	ben := e.member(t, admin, "ben")
	account := forges.Account{ID: 42, Login: "ana-gh"}

	start := func(c *Caller, mode string) (string, string, string) {
		to, cookie, err := e.s.StartSignIn(e.ctx, "hub", mode, "", c)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(to)
		return e.forge.Authorize(u.Query().Get("code_challenge"), account), u.Query().Get("state"), cookie
	}
	code, state, cookie := start(ana, ModeLink)
	_, err := e.s.FinishSignIn(e.ctx, "hub", code, "another state", cookie, ana)
	wantCode(t, err, "sign_in_expired")
	_, otherState, otherCookie := start(ana, ModeLink)
	_, err = e.s.FinishSignIn(e.ctx, "hub", code, state, otherCookie, ana)
	wantCode(t, err, "sign_in_expired")
	_, err = e.s.FinishSignIn(e.ctx, "hub", code, otherState, "not a sealed flow", ana)
	wantCode(t, err, "sign_in_expired")
	_, err = e.s.FinishSignIn(e.ctx, "other", code, state, cookie, ana)
	wantCode(t, err, "sign_in_expired")
	_, err = e.s.FinishSignIn(e.ctx, "hub", code, state, cookie, ben)
	wantCode(t, err, "not_signed_in")
	e.clock.advance(FlowLife + time.Second)
	_, err = e.s.FinishSignIn(e.ctx, "hub", code, state, cookie, ana)
	wantCode(t, err, "sign_in_expired")
}

// I4. Identities are keyed by forge and numeric account ID: a renamed login is the same member, and the same number
// on another forge is someone else.
func TestStableSubjects(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	e.provider(t, admin, "hub-two")
	ana := e.member(t, admin, "ana")
	if _, err := e.signIn(t, "hub", ModeLink, "", ana, forges.Account{ID: 42, Login: "ana"}); err != nil {
		t.Fatal(err)
	}
	got, err := e.signIn(t, "hub", ModeSignIn, "", nil, forges.Account{ID: 42, Login: "ana-renamed"})
	if err != nil || got.Member.ID != ana.Member.ID {
		t.Fatalf("renamed: %+v, %v", got, err)
	}
	if ids, _ := e.s.Identities(e.ctx, ana.Member.ID); ids[0].Login != "ana-renamed" {
		t.Fatalf("display login not updated: %+v", ids)
	}
	_, err = e.signIn(t, "hub-two", ModeSignIn, "", nil, forges.Account{ID: 42, Login: "ana"})
	wantCode(t, err, "not_invited")
}

// An unknown forge account joins only with an invite, which it uses up.
func TestJoinThroughForge(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	_, err := e.signIn(t, "hub", ModeSignIn, "", nil, forges.Account{ID: 7, Login: "carol"})
	wantCode(t, err, "not_invited")
	invite, _, _ := e.s.CreateInvite(e.ctx, admin, 0)
	got, err := e.signIn(t, "hub", ModeSignIn, invite, nil, forges.Account{ID: 7, Login: "carol", Name: "Carol"})
	if err != nil || !got.Joined || got.Member.DisplayName != "Carol" || got.Member.Admin {
		t.Fatalf("join: %+v, %v", got, err)
	}
	_, err = e.signIn(t, "hub", ModeSignIn, invite, nil, forges.Account{ID: 8, Login: "dave"})
	wantCode(t, err, "invalid_invite")
}

// I3. An admin can link an account by its numeric ID, and the link records who made it; members cannot.
func TestAdminLink(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	f := e.provider(t, admin, "hub")
	ana := e.member(t, admin, "ana")
	_, err := e.s.LinkByAdmin(e.ctx, ana, ana.Member.ID, f.ID, 42)
	wantCode(t, err, "forbidden")
	id, err := e.s.LinkByAdmin(e.ctx, admin, ana.Member.ID, f.ID, 42)
	if err != nil || id.LinkedBy == nil || *id.LinkedBy != admin.Member.ID {
		t.Fatalf("link %+v, %v", id, err)
	}
	_, err = e.s.LinkByAdmin(e.ctx, admin, admin.Member.ID, f.ID, 42)
	wantCode(t, err, "already_linked")
	if err := e.s.Unlink(e.ctx, ana.Member.ID, id.ID); err != nil {
		t.Fatal(err)
	}
}

// I8. Disabling a member ends their sessions, revokes their tokens, and refuses their sign-ins; they stay on record.
// The last active admin cannot be disabled or demoted.
func TestDisableMember(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	ana := e.member(t, admin, "ana")
	if _, err := e.signIn(t, "hub", ModeLink, "", ana, forges.Account{ID: 42, Login: "ana"}); err != nil {
		t.Fatal(err)
	}
	session, _, _ := e.s.StartSession(e.ctx, ana.Member.ID)
	token, _, _ := e.s.CreateToken(e.ctx, ana, "t", []string{ScopeRead}, nil, nil)
	yes := true
	m, err := e.s.UpdateMember(e.ctx, admin, ana.Member.ID, MemberPatch{Disabled: &yes})
	if err != nil || !m.Disabled {
		t.Fatalf("disable: %+v, %v", m, err)
	}
	_, err = e.s.SessionCaller(e.ctx, session)
	wantCode(t, err, "unauthenticated")
	_, err = e.s.TokenCaller(e.ctx, token)
	wantCode(t, err, "invalid_token")
	_, err = e.s.Login(e.ctx, "ana", "a long enough password", "10.0.0.5")
	wantCode(t, err, "invalid_credentials")
	_, err = e.signIn(t, "hub", ModeSignIn, "", nil, forges.Account{ID: 42, Login: "ana"})
	wantCode(t, err, "disabled")
	members, _ := e.s.Members(e.ctx)
	if len(members) != 2 {
		t.Fatalf("members %+v", members)
	}

	no := false
	_, err = e.s.UpdateMember(e.ctx, admin, admin.Member.ID, MemberPatch{Admin: &no})
	wantCode(t, err, "last_admin")
	_, err = e.s.UpdateMember(e.ctx, admin, admin.Member.ID, MemberPatch{Disabled: &yes})
	wantCode(t, err, "last_admin")
	_, err = e.s.UpdateMember(e.ctx, ana, admin.Member.ID, MemberPatch{Rules: &no})
	wantCode(t, err, "forbidden")
}

// Sign-in providers: an admin configures them; the client secret is sealed at rest and never returned.
func TestProviders(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	f := e.provider(t, admin, "hub")
	var sealed []byte
	if err := e.pool.QueryRow(e.ctx, "SELECT client_secret FROM sign_in_provider WHERE id = 'hub'").Scan(&sealed); err != nil ||
		bytes.Contains(sealed, []byte("client-secret")) {
		t.Fatalf("stored %q, %v", sealed, err)
	}
	_, err := e.s.CreateProvider(e.ctx, admin, ProviderInput{ID: "hub", Kind: ProviderKindForge, Name: "Again", ForgeID: f.ID,
		ClientID: "x", ClientSecret: "y"})
	wantCode(t, err, "provider_exists")
	_, err = e.s.CreateProvider(e.ctx, admin, ProviderInput{ID: "Bad ID", Kind: ProviderKindForge, Name: "x", ForgeID: f.ID,
		ClientID: "x", ClientSecret: "y"})
	wantCode(t, err, "invalid_id")
	_, err = e.s.CreateProvider(e.ctx, admin, ProviderInput{ID: "nope", Kind: ProviderKindForge, Name: "x", ForgeID: 999,
		ClientID: "x", ClientSecret: "y"})
	wantCode(t, err, "invalid_forge")

	secret := "rotated-secret"
	if _, err := e.s.UpdateProvider(e.ctx, admin, "hub", ProviderPatch{ClientSecret: &secret}); err != nil {
		t.Fatal(err)
	}
	// The rotated secret is the one the forge now gets; the fake forge accepts only the old one.
	_, err = e.signIn(t, "hub", ModeSignIn, "", nil, forges.Account{ID: 1, Login: "x"})
	wantCode(t, err, "sign_in_refused")
	if err := e.s.DeleteProvider(e.ctx, admin, "hub"); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.s.StartSignIn(e.ctx, "hub", ModeSignIn, "", nil)
	wantCode(t, err, "not_found")
}

// Without Park's own address, forge sign-in is unavailable rather than broken.
func TestSignInNeedsPublicURL(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	e.provider(t, admin, "hub")
	e.s.publicURL = ""
	_, _, err := e.s.StartSignIn(e.ctx, "hub", ModeSignIn, "", nil)
	if f, ok := fail.As(err); !ok || f.Kind != fail.Unavailable {
		t.Fatalf("got %v", err)
	}
}

// A bridge's token belongs to the bridge's owner, carries only assertions:write, and names the bridge as its caller's.
func TestBridgeTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.admin(t)
	ana := e.member(t, admin, "ana")
	var bridge int64
	if err := e.pool.QueryRow(e.ctx, "INSERT INTO bridge (name, owner_id) VALUES ('Tracker', $1) RETURNING id", ana.Member.ID).Scan(&bridge); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.s.CreateToken(e.ctx, admin, "not mine", []string{ScopeAssertionsWrite}, nil, &bridge)
	wantCode(t, err, "not_found")
	_, _, err = e.s.CreateToken(e.ctx, ana, "too much", []string{ScopeRead}, nil, &bridge)
	wantCode(t, err, "invalid_scopes")
	token, rec, err := e.s.CreateToken(e.ctx, ana, "tracker bridge", []string{ScopeAssertionsWrite}, nil, &bridge)
	if err != nil || rec.BridgeID == nil || *rec.BridgeID != bridge {
		t.Fatalf("token %+v, %v", rec, err)
	}
	c, err := e.s.TokenCaller(e.ctx, token)
	if err != nil || c.BridgeID != bridge || !c.HasScope(ScopeAssertionsWrite) || c.HasScope(ScopeRead) {
		t.Fatalf("caller %+v, %v", c, err)
	}
}
