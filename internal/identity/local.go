package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// Failure limits, per fifteen minutes.
const (
	accountFailures = 5  // wrong passwords for one username
	addressFailures = 30 // failed sign-ins, setups, or registrations from one client address
)

// setupCodeLife is how long a setup code works.
const setupCodeLife = time.Hour

// IssueSetupCode prepares the first start. When Park has no members, it stores a setup code valid for an hour —
// given, when the operator put one in the environment, or else a new one, which it returns to be printed — and
// reports that setup is needed. Starting again issues a new code.
func (s *Service) IssueSetupCode(ctx context.Context, given string) (code string, needed bool, err error) {
	var members int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM member").Scan(&members); err != nil {
		return "", false, err
	}
	if members > 0 {
		_, err := s.pool.Exec(ctx, "DELETE FROM setup_code")
		return "", false, err
	}
	code = given
	if code == "" {
		t := strings.ToUpper(randomText(10))[:12]
		code = t[:4] + "-" + t[4:8] + "-" + t[8:]
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO setup_code (code_hash, expires_at) VALUES ($1, $2)
		ON CONFLICT (only_one) DO UPDATE SET code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at`,
		digest(normalizeCode(code)), s.now().Add(setupCodeLife))
	if err != nil {
		return "", false, err
	}
	if given != "" {
		return "", true, nil
	}
	return code, true, nil
}

// normalizeCode lets a setup code be typed in any case, with or without its dashes.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

// SetupNeeded reports whether Park still has no members.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	var members int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM member").Scan(&members)
	return members == 0, err
}

var errInvalidSetupCode = fail.New(fail.Forbidden, "invalid_setup_code",
	"The setup code is wrong or has expired. Restart Park for a new one.")

// Setup creates the first administrator, with a local account, given the setup code. The code then stops working.
func (s *Service) Setup(ctx context.Context, code, username, password, displayName, addr string) (Member, error) {
	username, displayName, err := checkNewAccount(username, password, displayName)
	if err != nil {
		return Member{}, err
	}
	if err := s.checkAddress(addr); err != nil {
		return Member{}, err
	}
	var m Member
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var hash []byte
		var expires time.Time
		var members int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM member").Scan(&members); err != nil {
			return err
		}
		if members > 0 {
			return fail.New(fail.Conflict, "setup_done", "Park is already set up.")
		}
		err := tx.QueryRow(ctx, "SELECT code_hash, expires_at FROM setup_code FOR UPDATE").Scan(&hash, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalidSetupCode
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(hash, digest(normalizeCode(code))) != 1 || !s.now().Before(expires) {
			return errInvalidSetupCode
		}
		if m, err = createLocalMember(ctx, tx, username, password, displayName, true); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "DELETE FROM setup_code")
		return err
	})
	if errors.Is(err, errInvalidSetupCode) {
		s.limiter.fail("address:"+addr, s.now())
	}
	return m, err
}

var errInvalidCredentials = fail.New(fail.Unauthenticated, "invalid_credentials", "The username or password is wrong.")

// Login checks a username and password. Failures are limited per username and per client address; a disabled
// member is refused as if the password were wrong.
func (s *Service) Login(ctx context.Context, username, password, addr string) (Member, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	now := s.now()
	if wait := max(s.limiter.wait("account:"+username, accountFailures, now),
		s.limiter.wait("address:"+addr, addressFailures, now)); wait > 0 {
		return Member{}, rateLimited(wait)
	}
	var m Member
	var hash string
	err := s.pool.QueryRow(ctx, "SELECT "+memberColumns+", c.password_hash FROM local_credential c "+
		"JOIN member m ON m.id = c.member_id WHERE c.username = $1", username).
		Scan(&m.ID, &m.DisplayName, &m.Admin, &m.Rules, &m.Disabled, &m.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = dummyHash
	} else if err != nil {
		return Member{}, err
	}
	ok, err := checkPassword(hash, password)
	if err != nil {
		return Member{}, err
	}
	if !ok || m.ID == 0 || m.Disabled {
		s.limiter.fail("account:"+username, now)
		s.limiter.fail("address:"+addr, now)
		return Member{}, errInvalidCredentials
	}
	s.limiter.clear("account:" + username)
	return m, nil
}

var errInvalidInvite = fail.New(fail.Forbidden, "invalid_invite", "The invite is wrong, used, or expired. Ask an admin for a new one.")

// Register creates a member with a local account, given an invite, which is then used up.
func (s *Service) Register(ctx context.Context, invite, username, password, displayName, addr string) (Member, error) {
	username, displayName, err := checkNewAccount(username, password, displayName)
	if err != nil {
		return Member{}, err
	}
	if err := s.checkAddress(addr); err != nil {
		return Member{}, err
	}
	var m Member
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		inviteID, err := s.takeInvite(ctx, tx, invite)
		if err != nil {
			return err
		}
		if m, err = createLocalMember(ctx, tx, username, password, displayName, false); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE invite SET used_by = $2, used_at = now() WHERE id = $1", inviteID, m.ID)
		return err
	})
	if errors.Is(err, errInvalidInvite) {
		s.limiter.fail("address:"+addr, s.now())
	}
	return m, err
}

// takeInvite locks a usable invite by its code, inside tx.
func (s *Service) takeInvite(ctx context.Context, tx pgx.Tx, code string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM invite WHERE code_hash = $1 AND used_by IS NULL AND expires_at > $2 FOR UPDATE`,
		digest(strings.TrimSpace(code)), s.now()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errInvalidInvite
	}
	return id, err
}

// checkAddress refuses a client address that failed too often.
func (s *Service) checkAddress(addr string) error {
	if wait := s.limiter.wait("address:"+addr, addressFailures, s.now()); wait > 0 {
		return rateLimited(wait)
	}
	return nil
}

func rateLimited(wait time.Duration) error {
	e := fail.New(fail.RateLimited, "rate_limited", "Too many failed attempts. Try again later.")
	e.RetryAfter = wait
	return e
}
