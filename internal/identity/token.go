package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// TokenPrefix starts every API token, so that tokens can be told apart in lists and found by secret scanners. A
// token is park_<id>_<secret>: the ID identifies it, and only a hash of the whole token is stored.
const TokenPrefix = "park_"

// Token is an API token, without its secret.
type Token struct {
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	BridgeID   *int64
	ID         string
	Name       string
	Scopes     []string
}

var errBadToken = fail.New(fail.Unauthenticated, "invalid_token", "The API token is unknown, expired, or revoked.")

// CreateToken creates an API token for the calling member, or for a bridge the member owns, and returns it — the only
// time it is shown — with its record. Tokens are made in a browser session, never with another token. A member's
// token reads and works tasks; a bridge's token only posts and retracts assertions.
func (s *Service) CreateToken(ctx context.Context, c *Caller, name string, scopes []string, expires *time.Time, bridge *int64) (string, Token, error) {
	if c.TokenID != "" {
		return "", Token{}, fail.New(fail.Forbidden, "session_required", "Create tokens from a browser session.")
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", Token{}, fail.New(fail.Invalid, "invalid_name", "A token's name is 1 to 100 characters long.")
	}
	scopes = slices.Compact(slices.Sorted(slices.Values(scopes)))
	if len(scopes) == 0 {
		return "", Token{}, fail.New(fail.Invalid, "invalid_scopes", "A token needs at least one scope.")
	}
	if bridge != nil {
		var owner int64
		err := s.pool.QueryRow(ctx, "SELECT owner_id FROM bridge WHERE id = $1", *bridge).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != c.Member.ID) {
			return "", Token{}, fail.New(fail.NotFound, "not_found", "You own no such bridge.")
		}
		if err != nil {
			return "", Token{}, err
		}
		if len(scopes) != 1 || scopes[0] != ScopeAssertionsWrite {
			return "", Token{}, fail.New(fail.Invalid, "invalid_scopes", "A bridge's token has the one scope assertions:write.")
		}
	} else {
		for _, sc := range scopes {
			if sc != ScopeRead && sc != ScopeTasksWork {
				return "", Token{}, fail.New(fail.Invalid, "invalid_scopes", "A member's token can have the scopes read and tasks:work.")
			}
		}
	}
	if expires != nil && !expires.After(s.now()) {
		return "", Token{}, fail.New(fail.Invalid, "invalid_expiry", "A token's expiry must be in the future.")
	}
	id := randomText(6)
	secret := TokenPrefix + id + "_" + randomText(32)
	t := Token{ID: id, Name: name, Scopes: scopes, ExpiresAt: expires, BridgeID: bridge}
	err := s.pool.QueryRow(ctx, `INSERT INTO api_token (id, member_id, name, secret_hash, scopes, expires_at, bridge_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`, id, c.Member.ID, name, digest(secret), scopes, expires, bridge).
		Scan(&t.CreatedAt)
	if err != nil {
		return "", Token{}, err
	}
	return secret, t, nil
}

// TokenCaller returns the caller an API token belongs to. Each use notes the time, at most once a minute.
func (s *Service) TokenCaller(ctx context.Context, token string) (*Caller, error) {
	rest, ok := strings.CutPrefix(token, TokenPrefix)
	id, _, ok2 := strings.Cut(rest, "_")
	if !ok || !ok2 || id == "" {
		return nil, errBadToken
	}
	var c Caller
	var hash []byte
	var expires, revoked, lastUsed *time.Time
	var bridge *int64
	err := s.pool.QueryRow(ctx, "SELECT "+memberColumns+", t.secret_hash, t.scopes, t.expires_at, t.revoked_at, t.last_used_at, t.bridge_id "+
		"FROM api_token t JOIN member m ON m.id = t.member_id WHERE t.id = $1", id).
		Scan(&c.Member.ID, &c.Member.DisplayName, &c.Member.Admin, &c.Member.Rules, &c.Member.Disabled, &c.Member.CreatedAt,
			&hash, &c.Scopes, &expires, &revoked, &lastUsed, &bridge)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errBadToken
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if subtle.ConstantTimeCompare(hash, digest(token)) != 1 || revoked != nil || (expires != nil && !expires.After(now)) ||
		c.Member.Disabled {
		return nil, errBadToken
	}
	if lastUsed == nil || now.Sub(*lastUsed) > time.Minute {
		if _, err := s.pool.Exec(ctx, "UPDATE api_token SET last_used_at = $2 WHERE id = $1", id, now); err != nil {
			s.log.WarnContext(ctx, "note a token as used", "token", id, "error", err)
		}
	}
	c.TokenID = id
	if bridge != nil {
		c.BridgeID = *bridge
	}
	return &c, nil
}

// Tokens lists a member's tokens, newest first.
func (s *Service) Tokens(ctx context.Context, memberID int64) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, scopes, created_at, last_used_at, expires_at, revoked_at, bridge_id
		FROM api_token WHERE member_id = $1 ORDER BY created_at DESC, id`, memberID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Token, error) {
		var t Token
		err := row.Scan(&t.ID, &t.Name, &t.Scopes, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt, &t.BridgeID)
		return t, err
	})
}

// RevokeToken revokes one of a member's tokens, at once; revoking it again changes nothing.
func (s *Service) RevokeToken(ctx context.Context, memberID int64, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var revoked *time.Time
		err := tx.QueryRow(ctx, "SELECT revoked_at FROM api_token WHERE id = $1 AND member_id = $2 FOR UPDATE", id, memberID).Scan(&revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail.New(fail.NotFound, "not_found", "There is no such token.")
		}
		if err != nil || revoked != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE api_token SET revoked_at = $2 WHERE id = $1", id, s.now()); err != nil {
			return err
		}
		return s.revoked(ctx, tx, Revoked{Tokens: []string{id}, Member: memberID})
	})
}
