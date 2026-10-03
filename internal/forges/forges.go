// Package forges keeps the forge instances Park knows — each a kind, such as github, and a base address — and what
// each kind provides. A forge's base address is configuration, stored only in its record: links and API addresses
// are built from it when they are needed.
//
// The rest of Park talks to a forge through the interfaces here, and never imports a vendor's package; each kind's
// package, such as forges/github, registers itself.
package forges

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Forge is one forge instance.
type Forge struct {
	CreatedAt   time.Time
	Kind        string
	Name        string
	BaseAddress string
	ID          int64
}

// Account is a person's account on a forge. ID is stable; Login and Name are display data.
type Account struct {
	Login string
	Name  string
	ID    int64
}

// SignIn is a forge kind's OAuth sign-in: the authorization-code flow with PKCE.
type SignIn interface {
	// AuthorizeURL is where the browser goes to authorize.
	AuthorizeURL(base, clientID, redirectURI, state, challenge string) string
	// Exchange trades an authorization code for an access token.
	Exchange(ctx context.Context, base, clientID, clientSecret, code, redirectURI, verifier string) (string, error)
	// Account reads the account an access token belongs to.
	Account(ctx context.Context, base, accessToken string) (Account, error)
}

// Kind is what a forge kind provides.
type Kind struct {
	SignIn SignIn
	// PullURL is the address of a pull request, built from the forge's base address when it is shown.
	PullURL func(base, owner, name string, number int) string
	// PullPattern matches the address of a pull request on the forge at base, wherever it is in a text, with the
	// repository's owner, its name, and the number as its three groups: the reverse of PullURL.
	PullPattern func(base string) *regexp.Regexp
}

var kinds = map[string]Kind{}

// Register makes a kind available. Kind packages call it from init.
func Register(name string, k Kind) {
	kinds[name] = k
}

// KindOf returns what a kind provides, and whether it is known.
func KindOf(name string) (Kind, bool) {
	k, ok := kinds[name]
	return k, ok
}

// ErrNotFound reports a forge that does not exist.
var ErrNotFound = errors.New("no such forge")

// ErrInvalid reports a forge record that cannot be stored.
type ErrInvalid struct{ Reason string }

func (e ErrInvalid) Error() string { return e.Reason }

// validate checks a forge's kind and base address: an absolute http or https address with no path, query, or
// credentials, which links and API addresses are built from.
func validate(kind, name, base string) error {
	if _, ok := kinds[kind]; !ok {
		return ErrInvalid{fmt.Sprintf("unknown forge kind %q", kind)}
	}
	if strings.TrimSpace(name) == "" {
		return ErrInvalid{"a forge needs a name"}
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ErrInvalid{"the base address must be an absolute http or https address, such as https://github.com"}
	}
	return nil
}

// normalize drops a trailing slash from a base address.
func normalize(base string) string {
	return strings.TrimSuffix(base, "/")
}

// List returns every forge, oldest first.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Forge, error) {
	rows, err := pool.Query(ctx, "SELECT id, kind, name, base_address, created_at FROM forge ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

// Get returns one forge.
func Get(ctx context.Context, pool *pgxpool.Pool, id int64) (Forge, error) {
	rows, err := pool.Query(ctx, "SELECT id, kind, name, base_address, created_at FROM forge WHERE id = $1", id)
	if err != nil {
		return Forge{}, err
	}
	f, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Forge{}, ErrNotFound
	}
	return f, err
}

// Create stores a new forge.
func Create(ctx context.Context, pool *pgxpool.Pool, kind, name, base string) (Forge, error) {
	if err := validate(kind, name, base); err != nil {
		return Forge{}, err
	}
	rows, err := pool.Query(ctx, `INSERT INTO forge (kind, name, base_address) VALUES ($1, $2, $3)
		RETURNING id, kind, name, base_address, created_at`, kind, strings.TrimSpace(name), normalize(base))
	if err != nil {
		return Forge{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scan)
}

// Update renames a forge, or moves it to a new base address; its kind never changes.
func Update(ctx context.Context, pool *pgxpool.Pool, id int64, name, base *string) (Forge, error) {
	f, err := Get(ctx, pool, id)
	if err != nil {
		return Forge{}, err
	}
	if name != nil {
		f.Name = strings.TrimSpace(*name)
	}
	if base != nil {
		f.BaseAddress = *base
	}
	if err := validate(f.Kind, f.Name, f.BaseAddress); err != nil {
		return Forge{}, err
	}
	rows, err := pool.Query(ctx, `UPDATE forge SET name = $2, base_address = $3 WHERE id = $1
		RETURNING id, kind, name, base_address, created_at`, id, f.Name, normalize(f.BaseAddress))
	if err != nil {
		return Forge{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scan)
}

func scan(row pgx.CollectableRow) (Forge, error) {
	var f Forge
	err := row.Scan(&f.ID, &f.Kind, &f.Name, &f.BaseAddress, &f.CreatedAt)
	return f, err
}
