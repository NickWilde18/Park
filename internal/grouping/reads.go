package grouping

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// GroupView is a group with its pull requests.
type GroupView struct {
	CreatedAt  time.Time
	Name       string
	NameSource string
	State      string
	Members    []MemberView
	ID         int64
}

// MemberView is one pull request of a group: how it came to be there, and where it sits in its chain.
type MemberView struct {
	Since time.Time
	// Source is manual, external, or structural; Reason says which signal or who.
	Source string
	Reason string
	Chain  Chain
	PR     int64
	Open   bool
}

// ChainsOf derives the chains of the open pull requests.
func (s *Service) ChainsOf(ctx context.Context) (map[int64]Chain, error) {
	var chains map[int64]Chain
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		o, repos, err := s.loadOpen(ctx, tx)
		if err != nil {
			return err
		}
		chains = Chains(o.nodes, repos, s.settings.LongLived)
		return nil
	})
	return chains, err
}

// UnitOf returns the group a pull request belongs to, or 0 when it stands alone: its unit is then itself.
func (s *Service) UnitOf(ctx context.Context, pr int64) (int64, error) {
	var g int64
	err := s.pool.QueryRow(ctx, "SELECT group_id FROM membership WHERE pr_id = $1", pr).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

// Groups lists the active groups, oldest first, with their pull requests.
func (s *Service) Groups(ctx context.Context) ([]GroupView, error) {
	return s.groups(ctx, "g.state = 'active'", nil)
}

// Group returns one group, following an absorbed group's alias to the group it joined.
func (s *Service) Group(ctx context.Context, id int64) (GroupView, error) {
	for range 16 {
		var next int64
		err := s.pool.QueryRow(ctx, "SELECT alias_of FROM group_alias WHERE group_id = $1", id).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return GroupView{}, err
		}
		id = next
	}
	gs, err := s.groups(ctx, "g.id = $1", []any{id})
	if err != nil {
		return GroupView{}, err
	}
	if len(gs) == 0 {
		return GroupView{}, errGroupNotFound
	}
	return gs[0], nil
}

var reasons = map[string]string{KindStack: "same stack", KindIssue: "closes the same issue", KindBranch: "same branch name",
	KindRefs: "mutual references"}

func (s *Service) groups(ctx context.Context, where string, args []any) ([]GroupView, error) {
	chains, err := s.ChainsOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.name, g.name_source, g.state, g.created_at, m.pr_id, m.since, p.open,
			a.source, a.kind, b.name
		FROM pr_group g
		LEFT JOIN membership m ON m.group_id = g.id
		LEFT JOIN pr_fact p ON p.id = m.pr_id
		LEFT JOIN assertion a ON a.id = m.assertion_id
		LEFT JOIN bridge b ON a.source = 'bridge:' || b.id::text
		WHERE `+where+` ORDER BY g.created_at, g.id, m.pr_id`, args...)
	if err != nil {
		return nil, err
	}
	var out []GroupView
	for rows.Next() {
		var g GroupView
		var pr *int64
		var since *time.Time
		var open *bool
		var source, kind, bridge *string
		if err := rows.Scan(&g.ID, &g.Name, &g.NameSource, &g.State, &g.CreatedAt, &pr, &since, &open, &source, &kind, &bridge); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != g.ID {
			out = append(out, g)
		}
		if pr == nil {
			continue
		}
		m := MemberView{PR: *pr, Since: *since, Open: *open, Chain: chains[*pr]}
		switch {
		case source == nil:
		case *source == "manual":
			m.Source, m.Reason = "manual", "placed by a member"
		case *source == "structural":
			m.Source = "structural"
			if kind != nil {
				m.Reason = reasons[*kind]
			}
		default:
			m.Source, m.Reason = "external", "a bridge"
			if bridge != nil {
				m.Reason = *bridge
			}
		}
		last := &out[len(out)-1]
		last.Members = append(last.Members, m)
	}
	return out, rows.Err()
}

// SuggestionView is an open suggestion.
type SuggestionView struct {
	CreatedAt time.Time
	Reason    string
	ID        int64
	PR        int64
	Group     int64
}

// Suggestions lists the open suggestions, oldest first.
func (s *Service) Suggestions(ctx context.Context) ([]SuggestionView, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.pr_id, COALESCE(s.group_id, 0), s.reason, s.created_at FROM suggestion s
		JOIN pr_fact p ON p.id = s.pr_id WHERE s.state = 'open' AND p.open ORDER BY s.created_at, s.id`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SuggestionView, error) {
		var v SuggestionView
		err := row.Scan(&v.ID, &v.PR, &v.Group, &v.Reason, &v.CreatedAt)
		return v, err
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}
