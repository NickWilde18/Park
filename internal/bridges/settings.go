package bridges

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
)

// Change is what an admin changes of a bridge; a nil field stays as it is. Credentials are merged into those the
// bridge has: a key with a value sets it, and a key with an empty value removes it.
type Change struct {
	Credentials  map[string]string
	Name         *string
	Enabled      *bool
	Plugin       *string
	Chat         *Chat
	EveryMinutes *int
	LookbackDays *int
	ProposeFrom  *int
}

var errExternal = fail.New(fail.Conflict, "external_bridge",
	"Park does not run this bridge: it has no plugin, credentials, chat, or schedule in Park.")

// Update changes a bridge, and returns it as it is then.
//
// Giving an external bridge a plugin makes it one Park runs, through a plugin Park has and that is switched on. It
// keeps its ID, so what it posted, and under which anchors, stays where it is; and its tokens are revoked, so that the
// program that ran it cannot post alongside Park. A Park-run bridge keeps its plugin: its anchors are that platform's.
func (s *Service) Update(ctx context.Context, id int64, c Change) (Bridge, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := s.get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if c.Name != nil {
			name, err := grouping.CheckBridge(*c.Name, b.LinkTemplate)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE bridge SET name = $2 WHERE id = $1", id, name); err != nil {
				return err
			}
		}
		if c.Enabled != nil {
			if _, err := tx.Exec(ctx, "UPDATE bridge SET enabled = $2 WHERE id = $1", id, *c.Enabled); err != nil {
				return err
			}
		}
		if c.Plugin != nil && *c.Plugin != b.Plugin {
			if b.Plugin != "" {
				return fail.New(fail.Conflict, "plugin_fixed", "A bridge Park runs keeps its plugin: its anchors belong to that platform.")
			}
			if _, err := s.usable(ctx, tx, *c.Plugin); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE bridge SET plugin = $2 WHERE id = $1", id, *c.Plugin); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE api_token SET revoked_at = now() WHERE bridge_id = $1 AND revoked_at IS NULL", id); err != nil {
				return err
			}
			b.Plugin = *c.Plugin
		}
		if c.Credentials == nil && c.Chat == nil && c.EveryMinutes == nil && c.LookbackDays == nil && c.ProposeFrom == nil {
			return nil
		}
		if b.Plugin == "" {
			return errExternal
		}
		return s.settle(ctx, tx, b, c)
	})
	if err != nil {
		return Bridge{}, err
	}
	return s.Get(ctx, id)
}

// settle checks and writes a Park-run bridge's settings.
func (s *Service) settle(ctx context.Context, tx pgx.Tx, b Bridge, c Change) error {
	if c.Credentials != nil {
		p, ok := s.installed(b.Plugin)
		if !ok {
			return fail.New(fail.Conflict, "plugin_missing", "Park has no plugin of this bridge's kind, so it cannot tell its credentials.")
		}
		known := map[string]bool{}
		for _, f := range p.Fields {
			known[f.Key] = true
		}
		var sealed []byte
		if err := tx.QueryRow(ctx, "SELECT credentials FROM bridge WHERE id = $1", b.ID).Scan(&sealed); err != nil {
			return err
		}
		creds, err := s.openCredentials(b.ID, sealed)
		if err != nil {
			return err
		}
		for key, value := range c.Credentials {
			if !known[key] {
				return fail.New(fail.Invalid, "unknown_credential", "The plugin has no credential "+quote(key)+".")
			}
			if utf8.RuneCountInString(value) > 10000 {
				return fail.New(fail.Invalid, "invalid_credential", "A credential is at most 10,000 characters.")
			}
			if value == "" {
				delete(creds, key)
			} else {
				creds[key] = value
			}
		}
		var stored []byte
		if len(creds) > 0 {
			data, err := json.Marshal(creds)
			if err != nil {
				return err
			}
			stored = s.box.Seal(data, credentialsContext(b.ID))
		}
		if _, err := tx.Exec(ctx, "UPDATE bridge SET credentials = $2 WHERE id = $1", b.ID, stored); err != nil {
			return err
		}
	}
	if c.Chat != nil {
		chat := Chat{ID: strings.TrimSpace(c.Chat.ID), Name: strings.TrimSpace(c.Chat.Name)}
		if !validChat(chat) {
			return fail.New(fail.Invalid, "invalid_chat", "A chat is the platform's ID for it and its name, each 1 to 200 characters, "+
				"and neither a link.")
		}
		if _, err := tx.Exec(ctx, "UPDATE bridge SET chat = $2, chat_name = $3 WHERE id = $1", b.ID, chat.ID, chat.Name); err != nil {
			return err
		}
	}
	if v := c.EveryMinutes; v != nil {
		if *v < 1 || *v > 1440 {
			return fail.New(fail.Invalid, "invalid_every", "A bridge runs every 1 to 1,440 minutes.")
		}
		if _, err := tx.Exec(ctx, "UPDATE bridge SET every_minutes = $2 WHERE id = $1", b.ID, *v); err != nil {
			return err
		}
	}
	if v := c.LookbackDays; v != nil {
		if *v < 1 || *v > 365 {
			return fail.New(fail.Invalid, "invalid_lookback", "A bridge counts threads from 1 to 365 days back.")
		}
		if _, err := tx.Exec(ctx, "UPDATE bridge SET lookback_days = $2 WHERE id = $1", b.ID, *v); err != nil {
			return err
		}
	}
	if v := c.ProposeFrom; v != nil {
		if *v != 0 && (*v < 2 || *v > 100) {
			return fail.New(fail.Invalid, "invalid_propose_from",
				"A bridge asks the model about threads that link 2 to 100 pull requests or more, or, with 0, about none.")
		}
		if _, err := tx.Exec(ctx, "UPDATE bridge SET propose_from = $2 WHERE id = $1", b.ID, *v); err != nil {
			return err
		}
	}
	return nil
}

// quote quotes a key for a message.
func quote(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40]) + "…"
	}
	return `"` + s + `"`
}
