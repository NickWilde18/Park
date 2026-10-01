package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
)

// The purposes of a forge sign-in flow.
const (
	ModeSignIn = "signin" // sign in, or join with an invite
	ModeLink   = "link"   // add a forge account to the signed-in member
)

// FlowLife is how long a browser has to come back from the forge.
const FlowLife = 10 * time.Minute

// flow is what a sign-in needs to remember while the browser is at the forge. It travels sealed in a short-lived
// cookie, so that only the browser that started a flow can finish it.
type flow struct {
	Expires  time.Time `json:"e"`
	State    string    `json:"s"`
	Verifier string    `json:"v"`
	Provider string    `json:"p"`
	Mode     string    `json:"m"`
	Invite   string    `json:"i,omitempty"`
	Member   int64     `json:"u,omitempty"`
}

const flowContext = "sign_in_flow"

// signInProvider is a provider with what signing in through it needs.
type signInProvider struct {
	forge  forges.Forge
	signIn forges.SignIn
	Provider
	secret string
}

func (s *Service) signInProvider(ctx context.Context, id string) (signInProvider, error) {
	var p signInProvider
	var sealed []byte
	var forgeID *int64
	err := s.pool.QueryRow(ctx, "SELECT id, kind, name, forge_id, client_id, client_secret FROM sign_in_provider WHERE id = $1", id).
		Scan(&p.ID, &p.Kind, &p.Name, &forgeID, &p.ClientID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errNoProvider
	}
	if err != nil {
		return p, err
	}
	if p.Kind != ProviderKindForge || forgeID == nil {
		return p, errNoProvider
	}
	if p.forge, err = forges.Get(ctx, s.pool, *forgeID); err != nil {
		return p, err
	}
	k, ok := forges.KindOf(p.forge.Kind)
	if !ok || k.SignIn == nil {
		return p, fmt.Errorf("forge kind %q offers no sign-in", p.forge.Kind)
	}
	p.signIn = k.SignIn
	secret, err := s.box.Open(sealed, secretContext(p.ID))
	if err != nil {
		return p, fmt.Errorf("open the client secret of provider %s: %w", p.ID, err)
	}
	p.secret = string(secret)
	return p, nil
}

func (s *Service) redirectURI(provider string) string {
	return s.publicURL + "/api/v1/auth/" + provider + "/callback"
}

// StartSignIn begins a forge sign-in, and returns where to send the browser and the sealed flow for its cookie.
// Linking needs a signed-in caller; signing in may carry an invite, used if the forge account is new to Park.
func (s *Service) StartSignIn(ctx context.Context, providerID, mode, invite string, c *Caller) (string, string, error) {
	if s.publicURL == "" {
		return "", "", fail.New(fail.Unavailable, "not_configured", "Forge sign-in needs http.public_url to be configured.")
	}
	p, err := s.signInProvider(ctx, providerID)
	if err != nil {
		return "", "", err
	}
	f := flow{Provider: providerID, Mode: mode, State: randomURLText(32), Verifier: randomURLText(32),
		Expires: s.now().Add(FlowLife)}
	switch mode {
	case ModeSignIn:
		f.Invite = invite
	case ModeLink:
		if c == nil || c.TokenID != "" {
			return "", "", errNoSession
		}
		f.Member = c.Member.ID
	default:
		return "", "", fail.New(fail.Invalid, "invalid_mode", "The mode is signin or link.")
	}
	data, err := json.Marshal(f)
	if err != nil {
		return "", "", err
	}
	challenge := sha256.Sum256([]byte(f.Verifier))
	to := p.signIn.AuthorizeURL(p.forge.BaseAddress, p.ClientID, s.redirectURI(providerID), f.State,
		base64.RawURLEncoding.EncodeToString(challenge[:]))
	return to, base64.RawURLEncoding.EncodeToString(s.box.Seal(data, flowContext)), nil
}

// SignedIn is the outcome of a forge sign-in.
type SignedIn struct {
	Mode   string
	Member Member
	// Joined is set when the sign-in created the member, with an invite.
	Joined bool
}

var errFlowExpired = fail.New(fail.Invalid, "sign_in_expired", "The sign-in took too long or was started in another browser. Try again.")

// FinishSignIn completes a forge sign-in when the browser comes back with an authorization code. The state must
// match the flow sealed in the browser's cookie, and a link must come from the member who started it.
func (s *Service) FinishSignIn(ctx context.Context, providerID, code, state, cookie string, c *Caller) (SignedIn, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		return SignedIn{}, errFlowExpired
	}
	data, err := s.box.Open(sealed, flowContext)
	if err != nil {
		return SignedIn{}, errFlowExpired
	}
	var f flow
	if err := json.Unmarshal(data, &f); err != nil || f.Provider != providerID || !s.now().Before(f.Expires) ||
		subtle.ConstantTimeCompare([]byte(f.State), []byte(state)) != 1 {
		return SignedIn{}, errFlowExpired
	}
	if f.Mode == ModeLink && (c == nil || c.TokenID != "" || c.Member.ID != f.Member) {
		return SignedIn{}, fail.New(fail.Forbidden, "not_signed_in", "Sign in again, then connect the account.")
	}
	p, err := s.signInProvider(ctx, providerID)
	if err != nil {
		return SignedIn{}, err
	}
	token, err := p.signIn.Exchange(ctx, p.forge.BaseAddress, p.ClientID, p.secret, code, s.redirectURI(providerID), f.Verifier)
	if err != nil {
		s.log.WarnContext(ctx, "forge sign-in refused", "provider", providerID, "error", err)
		return SignedIn{}, fail.New(fail.Forbidden, "sign_in_refused", "The forge did not confirm the sign-in. Try again.")
	}
	account, err := p.signIn.Account(ctx, p.forge.BaseAddress, token)
	if err != nil {
		return SignedIn{}, err
	}
	out := SignedIn{Mode: f.Mode}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO account (forge_id, account_id, login, name) VALUES ($1, $2, $3, $4)
			ON CONFLICT (forge_id, account_id) DO UPDATE SET login = EXCLUDED.login, name = EXCLUDED.name, updated_at = now()`,
			p.forge.ID, account.ID, account.Login, account.Name); err != nil {
			return err
		}
		var owner int64
		err := tx.QueryRow(ctx, "SELECT member_id FROM member_identity WHERE forge_id = $1 AND subject = $2 FOR UPDATE",
			p.forge.ID, account.ID).Scan(&owner)
		known := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case f.Mode == ModeLink && known && owner != f.Member:
			return fail.New(fail.Conflict, "already_linked", "That forge account belongs to another member. An admin can sort it out.")
		case f.Mode == ModeLink && known:
			out.Member = c.Member
			return nil
		case f.Mode == ModeLink:
			out.Member = c.Member
			return linkIdentity(ctx, tx, f.Member, p.forge.ID, account.ID, nil)
		case known:
			rows, err := tx.Query(ctx, "SELECT "+memberColumns+" FROM member m WHERE m.id = $1", owner)
			if err != nil {
				return err
			}
			if out.Member, err = pgx.CollectExactlyOneRow(rows, scanMember); err != nil {
				return err
			}
			if out.Member.Disabled {
				return fail.New(fail.Forbidden, "disabled", "This member is disabled. Ask an admin.")
			}
			return nil
		case f.Invite == "":
			return fail.New(fail.Forbidden, "not_invited", "This forge account is not a member of Park. Ask an admin for an invite.")
		}
		inviteID, err := s.takeInvite(ctx, tx, f.Invite)
		if err != nil {
			return err
		}
		name := account.Name
		if name == "" {
			name = account.Login
		}
		if name, err = checkDisplayName(name); err != nil {
			name = "Member " + strconv.FormatInt(account.ID, 10)
		}
		if err := tx.QueryRow(ctx, "INSERT INTO member (display_name) VALUES ($1) RETURNING "+
			"id, display_name, admin, rules, disabled, created_at", name).
			Scan(&out.Member.ID, &out.Member.DisplayName, &out.Member.Admin, &out.Member.Rules, &out.Member.Disabled,
				&out.Member.CreatedAt); err != nil {
			return err
		}
		out.Joined = true
		if err := linkIdentity(ctx, tx, out.Member.ID, p.forge.ID, account.ID, nil); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE invite SET used_by = $2, used_at = now() WHERE id = $1", inviteID, out.Member.ID)
		return err
	})
	return out, err
}

var errAlreadyLinked = fail.New(fail.Conflict, "already_linked", "That forge account belongs to a member already.")

// linkIdentity records a forge account as a member's, inside tx: by the member, through the forge, or by an admin.
func linkIdentity(ctx context.Context, tx pgx.Tx, member, forge, account int64, by *int64) error {
	_, err := tx.Exec(ctx, "INSERT INTO member_identity (member_id, forge_id, subject, linked_by) VALUES ($1, $2, $3, $4)",
		member, forge, account, by)
	if isUniqueViolation(err, "member_identity_forge_id_subject_key") {
		return errAlreadyLinked
	}
	return err
}

// Identity is a forge account linked to a member.
type Identity struct {
	LinkedAt  time.Time
	LinkedBy  *int64
	ForgeName string
	Login     string
	Name      string
	ID        int64
	ForgeID   int64
	AccountID int64
}

// Identities lists a member's forge accounts.
func (s *Service) Identities(ctx context.Context, memberID int64) ([]Identity, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id, i.forge_id, f.name, i.subject, COALESCE(a.login, ''), COALESCE(a.name, ''),
			i.linked_by, i.linked_at
		FROM member_identity i JOIN forge f ON f.id = i.forge_id
		LEFT JOIN account a ON a.forge_id = i.forge_id AND a.account_id = i.subject
		WHERE i.member_id = $1 ORDER BY i.id`, memberID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Identity, error) {
		var i Identity
		err := row.Scan(&i.ID, &i.ForgeID, &i.ForgeName, &i.AccountID, &i.Login, &i.Name, &i.LinkedBy, &i.LinkedAt)
		return i, err
	})
}

// Unlink removes one of a member's forge accounts, unless it is their last way in (I10). A way in is a local
// password, or a linked account on a forge with a sign-in provider: without one, the next sign-in through the forge
// would find no member for the account. The member's row is locked, so that two unlinks at once cannot each leave the
// other the last way in and both go.
func (s *Service) Unlink(ctx context.Context, memberID, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM member WHERE id = $1 FOR UPDATE", memberID); err != nil {
			return err
		}
		var signsIn bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sign_in_provider p WHERE p.forge_id = i.forge_id)
			FROM member_identity i WHERE i.id = $1 AND i.member_id = $2`, id, memberID).Scan(&signsIn)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail.New(fail.NotFound, "not_found", "There is no such linked account.")
		}
		if err != nil {
			return err
		}
		if signsIn {
			var ways int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM local_credential WHERE member_id = $1) +
				(SELECT count(*) FROM member_identity i WHERE i.member_id = $1
					AND EXISTS (SELECT 1 FROM sign_in_provider p WHERE p.forge_id = i.forge_id))`, memberID).Scan(&ways); err != nil {
				return err
			}
			if ways <= 1 {
				return fail.New(fail.Conflict, "last_sign_in",
					"You sign in with this account, and have no other way in; connect another account first.")
			}
		}
		_, err = tx.Exec(ctx, "DELETE FROM member_identity WHERE id = $1", id)
		return err
	})
}

// LinkByAdmin links a forge account to a member by hand, for an admin, and records who did it. The account is
// named by its numeric ID on the forge, never by a login, which anyone could claim.
func (s *Service) LinkByAdmin(ctx context.Context, c *Caller, memberID, forgeID, accountID int64) (Identity, error) {
	if err := requireAdmin(c); err != nil {
		return Identity{}, err
	}
	if accountID <= 0 {
		return Identity{}, fail.New(fail.Invalid, "invalid_account", "Name the account by its numeric ID on the forge.")
	}
	if _, err := s.member(ctx, memberID); err != nil {
		return Identity{}, err
	}
	if _, err := forges.Get(ctx, s.pool, forgeID); errors.Is(err, forges.ErrNotFound) {
		return Identity{}, fail.New(fail.Invalid, "invalid_forge", "There is no such forge.")
	} else if err != nil {
		return Identity{}, err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return linkIdentity(ctx, tx, memberID, forgeID, accountID, &c.Member.ID)
	})
	if err != nil {
		return Identity{}, err
	}
	ids, err := s.Identities(ctx, memberID)
	if err != nil {
		return Identity{}, err
	}
	for _, i := range ids {
		if i.ForgeID == forgeID && i.AccountID == accountID {
			return i, nil
		}
	}
	return Identity{}, errors.New("the new link is missing")
}
