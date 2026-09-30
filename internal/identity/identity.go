// Package identity is who members are, how they sign in, which forge accounts are theirs, and what they may do:
// docs/design/07-identity.md.
//
// Nothing secret is stored in the clear. Passwords are argon2id hashes; setup codes, invite codes, session IDs,
// and API tokens are stored as SHA-256 hashes, since they are long random values; sign-in providers' client
// secrets are sealed with the server's secret key. None of them is ever logged.
package identity

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/secret"
)

// Member is a person who uses Park.
type Member struct {
	CreatedAt   time.Time
	DisplayName string
	ID          int64
	// Admin allows installations, repositories, members, invites, and providers; Rules allows publishing rules.
	Admin    bool
	Rules    bool
	Disabled bool
}

// Caller is who is calling: a member, through a browser session or through an API token.
type Caller struct {
	// TokenID is set when the call came with an API token, whose scopes then limit it.
	TokenID string
	Scopes  []string
	Member  Member
	// BridgeID is set for a bridge's token: the bridge, not the token, is the source of what it posts.
	BridgeID int64
}

// The scopes a member's API token can carry.
const (
	ScopeRead            = "read"
	ScopeTasksWork       = "tasks:work"
	ScopeAssertionsWrite = "assertions:write" // for a bridge's token only
)

// HasScope reports whether the call may use scope: a browser session may do anything its member may.
func (c *Caller) HasScope(scope string) bool {
	return c.TokenID == "" || slices.Contains(c.Scopes, scope)
}

// Options configure the service.
type Options struct {
	// PublicURL is Park's own address, such as https://park.example.com, where a forge sends browsers back after
	// sign-in. Forge sign-in is unavailable without it.
	PublicURL string
	// SecretKey is the server's secret key; the CSRF key is derived from it.
	SecretKey string
}

// Service is the identity service.
type Service struct {
	pool      *pgxpool.Pool
	box       *secret.Box
	log       *slog.Logger
	limiter   *limiter
	now       func() time.Time
	publicURL string
	csrfKey   []byte
}

// New returns the identity service.
func New(pool *pgxpool.Pool, box *secret.Box, log *slog.Logger, opts Options) (*Service, error) {
	csrfKey, err := hkdf.Key(sha256.New, []byte(opts.SecretKey), nil, "park csrf v1", 32)
	if err != nil {
		return nil, fmt.Errorf("derive the CSRF key: %w", err)
	}
	return &Service{
		pool:      pool,
		box:       box,
		log:       log,
		limiter:   newLimiter(15 * time.Minute),
		now:       time.Now,
		publicURL: strings.TrimSuffix(opts.PublicURL, "/"),
		csrfKey:   csrfKey,
	}, nil
}

// randomText returns n random bytes as unpadded lower-case base32.
func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// randomURLText returns n random bytes as unpadded base64url.
func randomURLText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func digest(s string) []byte {
	d := sha256.Sum256([]byte(s))
	return d[:]
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// checkNewAccount validates a new local account and returns its normalized username and display name.
func checkNewAccount(username, password, displayName string) (string, string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return "", "", fail.New(fail.Invalid, "invalid_username",
			"A username is 1 to 64 letters, digits, dots, dashes, or underscores, starting with a letter or digit.")
	}
	if n := utf8.RuneCountInString(password); n < 12 || n > 256 {
		return "", "", fail.New(fail.Invalid, "invalid_password", "A password is 12 to 256 characters long.")
	}
	displayName, err := checkDisplayName(displayName)
	return username, displayName, err
}

func checkDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", fail.New(fail.Invalid, "invalid_display_name", "A display name is 1 to 100 characters long.")
	}
	return name, nil
}

var errUsernameTaken = fail.New(fail.Conflict, "username_taken", "That username is taken.")

// isUniqueViolation reports a unique-constraint violation on the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

// createLocalMember creates a member with a local account, inside tx.
func createLocalMember(ctx context.Context, tx pgx.Tx, username, password, displayName string, admin bool) (Member, error) {
	var m Member
	err := tx.QueryRow(ctx, `INSERT INTO member (display_name, admin, rules) VALUES ($1, $2, $2)
		RETURNING id, display_name, admin, rules, disabled, created_at`, displayName, admin).
		Scan(&m.ID, &m.DisplayName, &m.Admin, &m.Rules, &m.Disabled, &m.CreatedAt)
	if err != nil {
		return Member{}, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO local_credential (member_id, username, password_hash) VALUES ($1, $2, $3)",
		m.ID, username, hashPassword(password))
	if isUniqueViolation(err, "local_credential_username_key") {
		return Member{}, errUsernameTaken
	}
	return m, err
}

const memberColumns = "m.id, m.display_name, m.admin, m.rules, m.disabled, m.created_at"

func scanMember(row pgx.CollectableRow) (Member, error) {
	var m Member
	err := row.Scan(&m.ID, &m.DisplayName, &m.Admin, &m.Rules, &m.Disabled, &m.CreatedAt)
	return m, err
}

// member reads one member.
func (s *Service) member(ctx context.Context, id int64) (Member, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+memberColumns+" FROM member m WHERE m.id = $1", id)
	if err != nil {
		return Member{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, scanMember)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, fail.New(fail.NotFound, "not_found", "There is no such member.")
	}
	return m, err
}

var errAdminOnly = fail.New(fail.Forbidden, "forbidden", "Only an admin may do this.")

// requireAdmin checks that the caller is an admin in a browser session: tokens carry no admin scope.
func requireAdmin(c *Caller) error {
	if c == nil || !c.Member.Admin || c.TokenID != "" {
		return errAdminOnly
	}
	return nil
}
