package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// SessionLife is how long a browser session lasts.
const SessionLife = 30 * 24 * time.Hour

var errNoSession = fail.New(fail.Unauthenticated, "unauthenticated", "Sign in first.")

// StartSession starts a browser session for a member, and returns its ID — the cookie's value — and its expiry.
func (s *Service) StartSession(ctx context.Context, memberID int64) (string, time.Time, error) {
	id := randomURLText(32)
	expires := s.now().Add(SessionLife)
	_, err := s.pool.Exec(ctx, "INSERT INTO browser_session (id_hash, member_id, expires_at) VALUES ($1, $2, $3)",
		digest(id), memberID, expires)
	if err != nil {
		return "", time.Time{}, err
	}
	return id, expires, nil
}

// SessionCaller returns the caller a session ID belongs to: an unknown or expired session, or a disabled member,
// is not signed in.
func (s *Service) SessionCaller(ctx context.Context, id string) (*Caller, error) {
	var c Caller
	var lastSeen time.Time
	err := s.pool.QueryRow(ctx, "SELECT "+memberColumns+", b.last_seen_at FROM browser_session b "+
		"JOIN member m ON m.id = b.member_id WHERE b.id_hash = $1 AND b.expires_at > $2", digest(id), s.now()).
		Scan(&c.Member.ID, &c.Member.DisplayName, &c.Member.Admin, &c.Member.Rules, &c.Member.Disabled,
			&c.Member.CreatedAt, &lastSeen)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.Member.Disabled) {
		return nil, errNoSession
	}
	if err != nil {
		return nil, err
	}
	if s.now().Sub(lastSeen) > 5*time.Minute {
		if _, err := s.pool.Exec(ctx, "UPDATE browser_session SET last_seen_at = $2 WHERE id_hash = $1", digest(id), s.now()); err != nil {
			s.log.WarnContext(ctx, "note a session as seen", "member", c.Member.ID, "error", err)
		}
	}
	return &c, nil
}

// EndSession signs a browser out.
func (s *Service) EndSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM browser_session WHERE id_hash = $1", digest(id))
	return err
}

// CSRF is the token that requests changing state must carry in their X-CSRF-Token header, tied to the session.
func (s *Service) CSRF(sessionID string) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckCSRF reports whether token is the session's CSRF token.
func (s *Service) CheckCSRF(sessionID, token string) bool {
	return hmac.Equal([]byte(s.CSRF(sessionID)), []byte(token))
}
