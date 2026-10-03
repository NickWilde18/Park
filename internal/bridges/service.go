// Package bridges keeps Park's bridges: the external ones, which run outside Park and post with their tokens, and the
// ones Park runs itself. A bridge Park runs reads one chat, on one platform, through a plugin: an executable that
// knows the platform and nothing of Park's, which Park calls for one operation at a time
// (docs/design/12-bridge-plugins.md). Park keeps everything else: each plugin's switch; each bridge's switch,
// settings, and credentials; the account it reads as; and, sealed, that account's tokens, of which only the plugin
// knows the shape.
//
// The rest of Park never talks to a plugin, and never names a platform: what a plugin is called, and which fields its
// credentials have, come from the plugin.
package bridges

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/secret"
)

// Options configure the service.
type Options struct {
	// Now is the clock; time.Now by default.
	Now func() time.Time
	// PublicURL is Park's own address as browsers reach it, which a platform sends a browser back to; connecting an
	// account needs it.
	PublicURL string
	// Plugins are the plugins Park found when it started.
	Plugins []Plugin
}

// Service keeps the bridges.
type Service struct {
	pool      *pgxpool.Pool
	box       *secret.Box
	log       *slog.Logger
	now       func() time.Time
	plugins   map[string]Plugin
	publicURL string
}

// New returns the service over pool, sealing credentials and tokens with box.
func New(pool *pgxpool.Pool, box *secret.Box, log *slog.Logger, opts Options) *Service {
	s := &Service{pool: pool, box: box, log: log, now: opts.Now, plugins: map[string]Plugin{}, publicURL: opts.PublicURL}
	if s.now == nil {
		s.now = time.Now
	}
	for _, p := range opts.Plugins {
		s.plugins[p.Kind] = p
	}
	return s
}

var errNotFound = fail.New(fail.NotFound, "not_found", "There is no such bridge.")

// installed returns the plugin of a kind, if Park found it when it started.
func (s *Service) installed(kind string) (Plugin, bool) {
	p, ok := s.plugins[kind]
	return p, ok
}

// switchedOn reports whether an admin switched a plugin on.
func (s *Service) switchedOn(ctx context.Context, q querier, kind string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, "SELECT coalesce((SELECT enabled FROM bridge_plugin WHERE kind = $1), false)", kind).Scan(&on)
	return on, err
}

// usable returns a plugin that a bridge can be added with or run through: Park has it, and it is switched on.
func (s *Service) usable(ctx context.Context, q querier, kind string) (Plugin, error) {
	p, ok := s.installed(kind)
	if !ok {
		return Plugin{}, fail.New(fail.Conflict, "plugin_missing", "Park has no plugin of that kind; its executable is not in the plugin directory.")
	}
	on, err := s.switchedOn(ctx, q, kind)
	if err != nil {
		return Plugin{}, err
	}
	if !on {
		return Plugin{}, fail.New(fail.Conflict, "plugin_off", "That plugin is switched off; an admin switches it on first.")
	}
	return p, nil
}
