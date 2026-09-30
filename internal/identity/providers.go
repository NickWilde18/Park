package identity

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/forges"
)

// Provider is a configured way to sign in, without its client secret.
type Provider struct {
	CreatedAt time.Time
	ID        string
	Kind      string
	Name      string
	ClientID  string
	ForgeID   int64
}

// ProviderKindForge is a sign-in provider that signs in through a forge's OAuth.
const ProviderKindForge = "forge"

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var errNoProvider = fail.New(fail.NotFound, "not_found", "There is no such sign-in provider.")

// Providers lists the sign-in providers, by ID.
func (s *Service) Providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, kind, name, forge_id, client_id, created_at FROM sign_in_provider ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanProvider)
}

func scanProvider(row pgx.CollectableRow) (Provider, error) {
	var p Provider
	var forge *int64
	err := row.Scan(&p.ID, &p.Kind, &p.Name, &forge, &p.ClientID, &p.CreatedAt)
	if forge != nil {
		p.ForgeID = *forge
	}
	return p, err
}

// ProviderInput is a new sign-in provider.
type ProviderInput struct {
	ID           string
	Kind         string
	Name         string
	ClientID     string
	ClientSecret string
	ForgeID      int64
}

func secretContext(id string) string { return "sign_in_provider:" + id + ":client_secret" }

// CreateProvider adds a sign-in provider, for an admin. Only forge providers exist so far.
func (s *Service) CreateProvider(ctx context.Context, c *Caller, in ProviderInput) (Provider, error) {
	if err := requireAdmin(c); err != nil {
		return Provider{}, err
	}
	if !providerID.MatchString(in.ID) {
		return Provider{}, fail.New(fail.Invalid, "invalid_id",
			"A provider's ID is 1 to 32 lower-case letters, digits, or dashes, starting with a letter or digit.")
	}
	if in.Kind != ProviderKindForge {
		return Provider{}, fail.New(fail.Invalid, "invalid_kind", "The kind of sign-in provider must be forge.")
	}
	forge, err := forges.Get(ctx, s.pool, in.ForgeID)
	if errors.Is(err, forges.ErrNotFound) {
		return Provider{}, fail.New(fail.Invalid, "invalid_forge", "There is no such forge.")
	}
	if err != nil {
		return Provider{}, err
	}
	if k, ok := forges.KindOf(forge.Kind); !ok || k.SignIn == nil {
		return Provider{}, fail.New(fail.Invalid, "invalid_forge", "That kind of forge offers no sign-in.")
	}
	if err := checkProviderText(in.Name, in.ClientID, in.ClientSecret); err != nil {
		return Provider{}, err
	}
	rows, err := s.pool.Query(ctx, `INSERT INTO sign_in_provider (id, kind, name, forge_id, client_id, client_secret)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, kind, name, forge_id, client_id, created_at`,
		in.ID, in.Kind, strings.TrimSpace(in.Name), in.ForgeID, strings.TrimSpace(in.ClientID),
		s.box.Seal([]byte(in.ClientSecret), secretContext(in.ID)))
	if isUniqueViolation(err, "sign_in_provider_pkey") {
		return Provider{}, fail.New(fail.Conflict, "provider_exists", "A sign-in provider with that ID exists.")
	}
	if err != nil {
		return Provider{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanProvider)
	if isUniqueViolation(err, "sign_in_provider_pkey") {
		return Provider{}, fail.New(fail.Conflict, "provider_exists", "A sign-in provider with that ID exists.")
	}
	return p, err
}

func checkProviderText(name, clientID, clientSecret string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > 100 {
		return fail.New(fail.Invalid, "invalid_name", "A provider's name is 1 to 100 characters long.")
	}
	if strings.TrimSpace(clientID) == "" || clientSecret == "" {
		return fail.New(fail.Invalid, "invalid_client", "A provider needs its client ID and client secret.")
	}
	return nil
}

// ProviderPatch is a change to a sign-in provider; nil fields stay as they are.
type ProviderPatch struct {
	Name         *string
	ClientID     *string
	ClientSecret *string
}

// UpdateProvider changes a sign-in provider, for an admin. Its ID, kind, and forge never change.
func (s *Service) UpdateProvider(ctx context.Context, c *Caller, id string, p ProviderPatch) (Provider, error) {
	if err := requireAdmin(c); err != nil {
		return Provider{}, err
	}
	var out Provider
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id, kind, name, forge_id, client_id, created_at FROM sign_in_provider WHERE id = $1 FOR UPDATE", id)
		if err != nil {
			return err
		}
		cur, err := pgx.CollectExactlyOneRow(rows, scanProvider)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoProvider
		}
		if err != nil {
			return err
		}
		if p.Name != nil {
			cur.Name = strings.TrimSpace(*p.Name)
		}
		if p.ClientID != nil {
			cur.ClientID = strings.TrimSpace(*p.ClientID)
		}
		secretCheck := "unchanged"
		if p.ClientSecret != nil {
			secretCheck = *p.ClientSecret
		}
		if err := checkProviderText(cur.Name, cur.ClientID, secretCheck); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sign_in_provider SET name = $2, client_id = $3 WHERE id = $1", id, cur.Name, cur.ClientID); err != nil {
			return err
		}
		if p.ClientSecret != nil {
			if _, err := tx.Exec(ctx, "UPDATE sign_in_provider SET client_secret = $2 WHERE id = $1", id,
				s.box.Seal([]byte(*p.ClientSecret), secretContext(id))); err != nil {
				return err
			}
		}
		out = cur
		return nil
	})
	return out, err
}

// DeleteProvider removes a sign-in provider, for an admin. Identities belong to forges, not to providers, so none
// is lost.
func (s *Service) DeleteProvider(ctx context.Context, c *Caller, id string) error {
	if err := requireAdmin(c); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM sign_in_provider WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNoProvider
	}
	return nil
}
