package bridges

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/NickWilde18/Park/internal/fail"
)

// PluginState is a plugin as admins see it: what it said of itself when Park started, and whether an admin switched
// it on. Installed is false for a plugin whose executable has left the plugin directory since an admin switched it:
// such a plugin has only its kind. Where a plugin's executable is stays on the server.
type PluginState struct {
	ChangedAt *time.Time
	Kind      string
	Name      string
	Fields    []Field
	Enabled   bool
	Installed bool
}

// Plugins lists, by kind, the plugins Park has, and the ones an admin switched that it no longer has.
func (s *Service) Plugins(ctx context.Context) ([]PluginState, error) {
	byKind := map[string]PluginState{}
	for _, p := range s.plugins {
		byKind[p.Kind] = PluginState{Kind: p.Kind, Name: p.Name, Fields: p.Fields, Installed: true}
	}
	rows, err := s.pool.Query(ctx, "SELECT kind, enabled, changed_at FROM bridge_plugin")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var on bool
		var at time.Time
		if err := rows.Scan(&kind, &on, &at); err != nil {
			return nil, err
		}
		p, ok := byKind[kind]
		if !ok {
			p = PluginState{Kind: kind, Name: kind, Fields: []Field{}}
		}
		p.Enabled, p.ChangedAt = on, &at
		byKind[kind] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PluginState, 0, len(byKind))
	for _, p := range byKind {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b PluginState) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// SwitchPlugin switches a plugin on or off, as a member chose. Only a plugin Park has can be switched on; one it no
// longer has can still be switched off. While a plugin is off, its bridges have no rounds, and none can be added.
func (s *Service) SwitchPlugin(ctx context.Context, member int64, kind string, on bool) (PluginState, error) {
	if _, ok := s.installed(kind); !ok {
		if on {
			return PluginState{}, fail.New(fail.Conflict, "plugin_missing",
				"Park has no plugin of that kind; its executable is not in the plugin directory.")
		}
		tag, err := s.pool.Exec(ctx, "UPDATE bridge_plugin SET enabled = false, changed_by = $2, changed_at = now() WHERE kind = $1",
			kind, member)
		if err != nil {
			return PluginState{}, err
		}
		if tag.RowsAffected() == 0 {
			return PluginState{}, fail.New(fail.NotFound, "not_found", "There is no such plugin.")
		}
	} else if _, err := s.pool.Exec(ctx, `INSERT INTO bridge_plugin (kind, enabled, changed_by) VALUES ($1, $2, $3)
		ON CONFLICT (kind) DO UPDATE SET enabled = EXCLUDED.enabled, changed_by = EXCLUDED.changed_by, changed_at = now()`,
		kind, on, member); err != nil {
		return PluginState{}, err
	}
	all, err := s.Plugins(ctx)
	if err != nil {
		return PluginState{}, err
	}
	for _, p := range all {
		if p.Kind == kind {
			return p, nil
		}
	}
	return PluginState{}, fail.New(fail.NotFound, "not_found", "There is no such plugin.")
}
