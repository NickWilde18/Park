package bridges

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/fail"
)

// FlowLife is how long a browser has to come back from a platform's sign-in.
const FlowLife = 10 * time.Minute

// RedirectPath is where, after Park's public address, a platform sends the browser back to once an account signs in:
// the address to register with the platform.
const RedirectPath = "/api/v1/bridges/callback"

// flow is what connecting an account remembers while the browser is at the platform. It travels sealed in a
// short-lived cookie, so that only the browser that started it can finish it, and only for the member who did.
type flow struct {
	Expires  time.Time `json:"e"`
	State    string    `json:"s"`
	Verifier string    `json:"v"`
	Bridge   int64     `json:"b"`
	Member   int64     `json:"u"`
}

const flowContext = "bridge_connect_flow"

var errFlowExpired = fail.New(fail.Invalid, "connect_expired", "Connecting the account took too long, or was started in another "+
	"browser or by someone else. Try again.")

func (s *Service) redirectURI() string { return s.publicURL + RedirectPath }

// StartConnect begins connecting the account a Park-run bridge reads as, for a member: it returns where to send the
// browser, the plugin's sign-in page, and the sealed flow for its cookie. The bridge's plugin must be one Park has and
// switched on, and its credentials complete.
func (s *Service) StartConnect(ctx context.Context, member, id int64) (string, string, error) {
	if s.publicURL == "" {
		return "", "", fail.New(fail.Unavailable, "not_configured", "Connecting an account needs http.public_url to be configured.")
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	if b.Plugin == "" {
		return "", "", errExternal
	}
	p, err := s.usable(ctx, s.pool, b.Plugin)
	if err != nil {
		return "", "", err
	}
	creds, err := s.credentials(ctx, s.pool, id, p)
	if err != nil {
		return "", "", err
	}
	f := flow{State: randomText(), Verifier: randomText(), Bridge: id, Member: member, Expires: s.now().Add(FlowLife)}
	challenge := sha256.Sum256([]byte(f.Verifier))
	to, err := p.authorize(ctx, creds, s.redirectURI(), f.State, base64.RawURLEncoding.EncodeToString(challenge[:]))
	if err != nil {
		return "", "", s.pluginFailed(ctx, b, "authorize", err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		return "", "", err
	}
	return to, base64.RawURLEncoding.EncodeToString(s.box.Seal(data, flowContext)), nil
}

// FinishConnect completes connecting an account when the browser comes back from the platform with a code, and
// returns the bridge's ID. The state must match the flow sealed in the browser's cookie, and the member must be the
// one who started it. The account it signed in, and its tokens, replace any the bridge had, and it is no longer
// lapsed.
func (s *Service) FinishConnect(ctx context.Context, member int64, code, state, cookie string) (int64, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		return 0, errFlowExpired
	}
	data, err := s.box.Open(sealed, flowContext)
	if err != nil {
		return 0, errFlowExpired
	}
	var f flow
	if err := json.Unmarshal(data, &f); err != nil || !s.now().Before(f.Expires) || f.Member != member ||
		subtle.ConstantTimeCompare([]byte(f.State), []byte(state)) != 1 {
		return 0, errFlowExpired
	}
	b, err := s.Get(ctx, f.Bridge)
	if err != nil {
		return 0, err
	}
	if b.Plugin == "" {
		return 0, errExternal
	}
	p, err := s.usable(ctx, s.pool, b.Plugin)
	if err != nil {
		return 0, err
	}
	creds, err := s.credentials(ctx, s.pool, b.ID, p)
	if err != nil {
		return 0, err
	}
	account, tokens, err := p.exchange(ctx, creds, s.redirectURI(), code, f.Verifier)
	if err != nil {
		s.log.WarnContext(ctx, "a bridge's sign-in was not confirmed", "bridge", b.ID, "plugin", b.Plugin, "error", err)
		return 0, fail.New(fail.Forbidden, "sign_in_refused", "The platform did not confirm the sign-in. Try again.")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO bridge_account (bridge_id, subject, name, tokens, connected_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (bridge_id) DO UPDATE SET subject = EXCLUDED.subject, name = EXCLUDED.name, tokens = EXCLUDED.tokens,
			connected_by = EXCLUDED.connected_by, connected_at = now(), lapsed_at = NULL`,
		b.ID, strings.TrimSpace(account.ID), strings.TrimSpace(account.Name), s.box.Seal(tokens, tokensContext(b.ID)), member)
	return b.ID, err
}

// credentials returns a Park-run bridge's credentials, when they are complete: every field of its plugin set.
func (s *Service) credentials(ctx context.Context, q querier, id int64, p Plugin) (map[string]string, error) {
	var sealed []byte
	if err := q.QueryRow(ctx, "SELECT credentials FROM bridge WHERE id = $1", id).Scan(&sealed); err != nil {
		return nil, err
	}
	creds, err := s.openCredentials(id, sealed)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, f := range p.Fields {
		if creds[f.Key] == "" {
			missing = append(missing, f.Label)
		}
	}
	if len(missing) > 0 {
		return nil, fail.New(fail.Conflict, "credentials_missing", "Enter the bridge's credentials first: "+strings.Join(missing, ", ")+".")
	}
	return creds, nil
}

// pluginFailed logs why a plugin failed at an operation for a bridge, and answers what the caller can act on: a
// lapsed sign-in, or a failure that Park's log explains.
func (s *Service) pluginFailed(ctx context.Context, b Bridge, op string, err error) error {
	if ctx.Err() != nil {
		return err
	}
	var pe *Error
	if errors.As(err, &pe) && pe.Code == SignInLapsed {
		return fail.New(fail.Conflict, "signin_lapsed", "The bridge's account can no longer be used; connect it again.")
	}
	s.log.WarnContext(ctx, "a bridge's plugin failed", "bridge", b.ID, "plugin", b.Plugin, "operation", op, "error", err)
	return fail.New(fail.Unavailable, "plugin_failed", "The bridge's plugin failed; Park's log says why. Try again.")
}

// randomText is 32 random bytes as URL-safe text.
func randomText() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
