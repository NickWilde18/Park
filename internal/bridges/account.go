package bridges

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// operation is one plugin operation that uses a bridge's account: it gets the plugin, the bridge's credentials, and
// the account's tokens, and answers the tokens the plugin answered with, if any.
type operation func(p Plugin, credentials map[string]string, tokens json.RawMessage) (json.RawMessage, error)

var errNoAccount = fail.New(fail.Conflict, "no_account", "Connect the bridge's account first.")

// withAccount runs an operation of a Park-run bridge's plugin that uses the bridge's account, holding the account's row
// until it ends, so that two operations never use the same tokens at once: renewing tokens spends the old ones.
// Whatever tokens the plugin answers with are kept, also alongside an error. An error saying the sign-in lapsed marks
// the account lapsed; an operation that succeeds clears that mark. Errors come back as what the caller can act on.
func (s *Service) withAccount(ctx context.Context, id int64, name string, op operation) error {
	var b Bridge
	var failed error
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if b, err = s.get(ctx, tx, id, false); err != nil {
			return err
		}
		if b.Plugin == "" {
			return errExternal
		}
		p, err := s.usable(ctx, tx, b.Plugin)
		if err != nil {
			return err
		}
		creds, err := s.credentials(ctx, tx, id, p)
		if err != nil {
			return err
		}
		var sealed []byte
		err = tx.QueryRow(ctx, "SELECT tokens FROM bridge_account WHERE bridge_id = $1 FOR UPDATE", id).Scan(&sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoAccount
		}
		if err != nil {
			return err
		}
		tokens, err := s.box.Open(sealed, tokensContext(id))
		if err != nil {
			return err
		}
		renewed, err := op(p, creds, tokens)
		failed = err
		if renewed != nil {
			if _, err := tx.Exec(ctx, "UPDATE bridge_account SET tokens = $2 WHERE bridge_id = $1", id,
				s.box.Seal(renewed, tokensContext(id))); err != nil {
				return err
			}
		}
		lapsed := "NULL"
		var pe *Error
		if errors.As(failed, &pe) && pe.Code == SignInLapsed {
			lapsed = "now()"
		} else if failed != nil {
			return nil
		}
		_, err = tx.Exec(ctx, "UPDATE bridge_account SET lapsed_at = "+lapsed+" WHERE bridge_id = $1", id)
		return err
	})
	if err != nil {
		return err
	}
	if failed != nil {
		return s.pluginFailed(ctx, b, name, failed)
	}
	return nil
}

// Chats lists the chats a Park-run bridge's account can read, for an admin to choose the one the bridge reads.
func (s *Service) Chats(ctx context.Context, id int64) ([]Chat, error) {
	out := []Chat{}
	err := s.withAccount(ctx, id, "chats", func(p Plugin, creds map[string]string, tokens json.RawMessage) (json.RawMessage, error) {
		chats, renewed, err := p.chats(ctx, creds, tokens)
		if chats != nil {
			out = chats
		}
		return renewed, err
	})
	return out, err
}

// Accepting reports whether an external bridge may post or retract through the API: it is switched on, and Park does
// not run it.
func (s *Service) Accepting(ctx context.Context, id int64) error {
	var plugin *string
	var on bool
	err := s.pool.QueryRow(ctx, "SELECT plugin, enabled FROM bridge WHERE id = $1", id).Scan(&plugin, &on)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errNotFound
	case err != nil:
		return err
	case plugin != nil:
		return fail.New(fail.Conflict, "bridge_run_by_park", "Park runs this bridge; nothing posts for it through the API.")
	case !on:
		return fail.New(fail.Conflict, "bridge_disabled", "This bridge is switched off; an admin switches it on.")
	}
	return nil
}
