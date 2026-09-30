package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// Members lists every member, oldest first. Members are disabled, never deleted.
func (s *Service) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+memberColumns+" FROM member m ORDER BY m.id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMember)
}

// MemberPatch is a change to a member; nil fields stay as they are.
type MemberPatch struct {
	DisplayName *string
	Admin       *bool
	Rules       *bool
	Disabled    *bool
}

// UpdateMember changes a member, for an admin. Disabling a member ends their sessions and revokes their tokens.
// The last active admin can be neither demoted nor disabled, since Park would then have nobody to administer it.
func (s *Service) UpdateMember(ctx context.Context, c *Caller, id int64, p MemberPatch) (Member, error) {
	if err := requireAdmin(c); err != nil {
		return Member{}, err
	}
	if p.DisplayName != nil {
		name, err := checkDisplayName(*p.DisplayName)
		if err != nil {
			return Member{}, err
		}
		p.DisplayName = &name
	}
	var m Member
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Admin changes are serialized, so that two admins cannot demote each other at once.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.member.admin'))"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+memberColumns+" FROM member m WHERE m.id = $1 FOR UPDATE", id)
		if err != nil {
			return err
		}
		before, err := pgx.CollectExactlyOneRow(rows, scanMember)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail.New(fail.NotFound, "not_found", "There is no such member.")
		}
		if err != nil {
			return err
		}
		after := before
		if p.DisplayName != nil {
			after.DisplayName = *p.DisplayName
		}
		if p.Admin != nil {
			after.Admin = *p.Admin
		}
		if p.Rules != nil {
			after.Rules = *p.Rules
		}
		if p.Disabled != nil {
			after.Disabled = *p.Disabled
		}
		if before.Admin && !before.Disabled && (!after.Admin || after.Disabled) {
			var others int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM member WHERE admin AND NOT disabled AND id <> $1", id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return fail.New(fail.Conflict, "last_admin", "Park needs at least one active admin.")
			}
		}
		rows, err = tx.Query(ctx, `UPDATE member m SET display_name = $2, admin = $3, rules = $4, disabled = $5 WHERE id = $1
			RETURNING `+memberColumns, id, after.DisplayName, after.Admin, after.Rules, after.Disabled)
		if err != nil {
			return err
		}
		if m, err = pgx.CollectExactlyOneRow(rows, scanMember); err != nil {
			return err
		}
		if after.Disabled && !before.Disabled {
			if _, err := tx.Exec(ctx, "DELETE FROM browser_session WHERE member_id = $1", id); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "UPDATE api_token SET revoked_at = now() WHERE member_id = $1 AND revoked_at IS NULL RETURNING id", id)
			if err != nil {
				return err
			}
			tokens, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			return s.revoked(ctx, tx, Revoked{Tokens: tokens, Member: id, Disabled: true})
		}
		return err
	})
	return m, err
}

// Invite is an invitation to become a member, without its code.
type Invite struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedBy    *int64
	UsedAt    *time.Time
	ID        int64
	CreatedBy int64
}

// Invite lifetimes.
const (
	DefaultInviteLife = 7 * 24 * time.Hour
	MaxInviteLife     = 30 * 24 * time.Hour
)

// CreateInvite creates an invite, for an admin, and returns its code — the only time it is shown — with its record.
func (s *Service) CreateInvite(ctx context.Context, c *Caller, life time.Duration) (string, Invite, error) {
	if err := requireAdmin(c); err != nil {
		return "", Invite{}, err
	}
	if life == 0 {
		life = DefaultInviteLife
	}
	if life < time.Minute || life > MaxInviteLife {
		return "", Invite{}, fail.New(fail.Invalid, "invalid_expiry", "An invite lasts from a minute to 30 days.")
	}
	code := randomText(20)
	inv := Invite{CreatedBy: c.Member.ID, ExpiresAt: s.now().Add(life)}
	err := s.pool.QueryRow(ctx, "INSERT INTO invite (code_hash, created_by, expires_at) VALUES ($1, $2, $3) RETURNING id, created_at",
		digest(code), c.Member.ID, inv.ExpiresAt).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return "", Invite{}, err
	}
	return code, inv, nil
}

// Invites lists every invite, newest first.
func (s *Service) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, created_by, created_at, expires_at, used_by, used_at FROM invite ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Invite, error) {
		var inv Invite
		err := row.Scan(&inv.ID, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedBy, &inv.UsedAt)
		return inv, err
	})
}

// DeleteInvite withdraws an unused invite.
func (s *Service) DeleteInvite(ctx context.Context, c *Caller, id int64) error {
	if err := requireAdmin(c); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM invite WHERE id = $1 AND used_by IS NULL", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fail.New(fail.NotFound, "not_found", "There is no such unused invite.")
	}
	return nil
}

// MemberByID returns one member.
func (s *Service) MemberByID(ctx context.Context, id int64) (Member, error) { return s.member(ctx, id) }
