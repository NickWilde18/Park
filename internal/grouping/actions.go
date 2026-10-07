package grouping

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

var (
	errGroupNotFound = fail.New(fail.NotFound, "not_found", "There is no such group.")
	errPRNotFound    = fail.New(fail.NotFound, "not_found", "There is no such open pull request.")
)

// act runs a member's action and then resolution, in one transaction, so that the answer shows its effect.
func (s *Service) act(ctx context.Context, f func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.grouping'))"); err != nil {
			return err
		}
		if err := f(tx); err != nil {
			return err
		}
		return s.resolveTx(ctx, tx)
	})
}

// target follows aliases to the group an ID now names, and checks it is active.
func target(ctx context.Context, tx pgx.Tx, id int64) (int64, error) {
	for range 16 {
		var next int64
		err := tx.QueryRow(ctx, "SELECT alias_of FROM group_alias WHERE group_id = $1", id).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, err
		}
		id = next
	}
	var state string
	err := tx.QueryRow(ctx, "SELECT state FROM pr_group WHERE id = $1", id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state != "active") {
		return 0, errGroupNotFound
	}
	return id, err
}

func openPR(ctx context.Context, tx pgx.Tx, id int64) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pr_fact WHERE id = $1 AND open)", id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errPRNotFound
	}
	return nil
}

func (s *Service) retract(ctx context.Context, tx pgx.Tx, q string, args ...any) error {
	_, err := tx.Exec(ctx, "UPDATE assertion SET retracted_at = now() WHERE retracted_at IS NULL AND "+q, args...)
	return err
}

// place records a member's placement of a pull request in a group, or its exclusion from it.
func (s *Service) place(ctx context.Context, tx pgx.Tx, actor, pr, group int64, negative bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO assertion (source, authority, pr_id, group_id, negative, actor_member, seen_at)
		VALUES ('manual', $1, $2, $3, $4, $5, now())`, AuthorityManual, pr, group, negative, actor)
	return err
}

// Move puts a pull request into a group: a placement replacing its earlier manual placement, lifting any exclusion
// of it from that group.
func (s *Service) Move(ctx context.Context, actor, pr, group int64) error {
	return s.act(ctx, func(tx pgx.Tx) error {
		g, err := target(ctx, tx, group)
		if err != nil {
			return err
		}
		if err := openPR(ctx, tx, pr); err != nil {
			return err
		}
		if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND (NOT negative OR group_id = $2)", pr, g); err != nil {
			return err
		}
		return s.place(ctx, tx, actor, pr, g, false)
	})
}

// exclude takes a pull request out of a group: a negative placement of it there, which also names the anchors whose
// live claims placed it there, so that it holds against them wherever they point later — also once the group is
// archived and they are released, when their next claim would start a group of its own (G6).
func (s *Service) exclude(ctx context.Context, tx pgx.Tx, actor, pr, group int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO assertion (source, authority, pr_id, group_id, negative, actor_member, seen_at, excluded_anchors)
		SELECT 'manual', $1, $2, $3, true, $4, now(),
			coalesce(jsonb_agg(jsonb_build_object('source', an.source, 'key', an.key) ORDER BY an.source, an.key), '[]')
		FROM anchor an
		WHERE an.group_id = $3 AND EXISTS (SELECT 1 FROM assertion a, pr_fact p, jsonb_array_elements(a.members) m
			WHERE a.source = an.source AND a.anchor = an.key AND a.retracted_at IS NULL AND p.id = $2
				AND (m->>'repo')::bigint = p.repo_id AND (m->>'number')::int = p.number)`,
		AuthorityManual, pr, group, actor)
	return err
}

// TakeOut takes a pull request out of a group: an exclusion, which holds against every source (G6), replacing its
// manual placement there. It falls back to its next-best claim, or stands alone; never to one of the claims that had
// placed it in the group, even once the group is archived.
func (s *Service) TakeOut(ctx context.Context, actor, pr, group int64) error {
	return s.act(ctx, func(tx pgx.Tx) error {
		g, err := target(ctx, tx, group)
		if err != nil {
			return err
		}
		if err := openPR(ctx, tx, pr); err != nil {
			return err
		}
		if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND group_id = $2", pr, g); err != nil {
			return err
		}
		return s.exclude(ctx, tx, actor, pr, g)
	})
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 200 {
		return "", fail.New(fail.Invalid, "invalid_name", "A group's name is 1 to 200 characters long.")
	}
	return name, nil
}

// newGroup creates a group a member named.
func newGroup(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	var g int64
	err := tx.QueryRow(ctx, "INSERT INTO pr_group (name, name_source) VALUES ($1, 'manual') RETURNING id", name).Scan(&g)
	return g, err
}

// Create makes a group of pull requests, with placements of each in it.
func (s *Service) Create(ctx context.Context, actor int64, name string, prs []int64) (int64, error) {
	name, err := checkName(name)
	if err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, fail.New(fail.Invalid, "no_pull_requests", "A group starts with at least one pull request.")
	}
	var g int64
	err = s.act(ctx, func(tx pgx.Tx) error {
		if g, err = newGroup(ctx, tx, name); err != nil {
			return err
		}
		for _, pr := range prs {
			if err := openPR(ctx, tx, pr); err != nil {
				return err
			}
			if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND NOT negative", pr); err != nil {
				return err
			}
			if err := s.place(ctx, tx, actor, pr, g, false); err != nil {
				return err
			}
		}
		return nil
	})
	return g, err
}

// Rename names a group; the name is then the members' own.
func (s *Service) Rename(ctx context.Context, group int64, name string) error {
	name, err := checkName(name)
	if err != nil {
		return err
	}
	return s.act(ctx, func(tx pgx.Tx) error {
		g, err := target(ctx, tx, group)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE pr_group SET name = $2, name_source = 'manual' WHERE id = $1", g, name)
		return err
	})
}

// Merge merges one group into another: an alias from the absorbed group, whose anchors, memberships, placements,
// and exclusions move to the one it joins, and its description too when the one it joins has none. Where an
// exclusion that moved contradicts a placement there, the newer of the two stands.
func (s *Service) Merge(ctx context.Context, actor, into, from int64) error {
	return s.act(ctx, func(tx pgx.Tx) error {
		to, err := target(ctx, tx, into)
		if err != nil {
			return err
		}
		absorbed, err := target(ctx, tx, from)
		if err != nil {
			return err
		}
		if to == absorbed {
			return fail.New(fail.Invalid, "same_group", "A group cannot merge into itself.")
		}
		if _, err := tx.Exec(ctx, "INSERT INTO group_alias (group_id, alias_of) VALUES ($1, $2)", absorbed, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE anchor SET group_id = $2 WHERE group_id = $1", absorbed, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE membership SET group_id = $2 WHERE group_id = $1", absorbed, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE group_doc SET group_id = $2 WHERE group_id = $1
			AND NOT EXISTS (SELECT 1 FROM group_doc WHERE group_id = $2)`, absorbed, to); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT pr_id, negative FROM assertion
			WHERE retracted_at IS NULL AND source = 'manual' AND group_id = $1 ORDER BY id`, absorbed)
		if err != nil {
			return err
		}
		type moved struct {
			pr       int64
			negative bool
		}
		ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (moved, error) {
			var m moved
			err := row.Scan(&m.pr, &m.negative)
			return m, err
		})
		if err != nil {
			return err
		}
		if err := s.retract(ctx, tx, "source = 'manual' AND group_id = $1", absorbed); err != nil {
			return err
		}
		for _, m := range ms {
			// The moved claim is the newest, so it replaces what the pull request had in the group it joins.
			if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND group_id = $2", m.pr, to); err != nil {
				return err
			}
			if err := s.place(ctx, tx, actor, m.pr, to, m.negative); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE pr_group SET state = 'archived', archived_at = now() WHERE id = $1", absorbed)
		return err
	})
}

// Split moves pull requests out of a group into a new one: placements in the new group, and exclusions from the old.
func (s *Service) Split(ctx context.Context, actor, group int64, name string, prs []int64) (int64, error) {
	name, err := checkName(name)
	if err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, fail.New(fail.Invalid, "no_pull_requests", "Name the pull requests to split out.")
	}
	var fresh int64
	err = s.act(ctx, func(tx pgx.Tx) error {
		old, err := target(ctx, tx, group)
		if err != nil {
			return err
		}
		if fresh, err = newGroup(ctx, tx, name); err != nil {
			return err
		}
		for _, pr := range prs {
			if err := openPR(ctx, tx, pr); err != nil {
				return err
			}
			if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND (NOT negative OR group_id = $2)", pr, old); err != nil {
				return err
			}
			if err := s.place(ctx, tx, actor, pr, fresh, false); err != nil {
				return err
			}
			if err := s.exclude(ctx, tx, actor, pr, old); err != nil {
				return err
			}
		}
		return nil
	})
	return fresh, err
}

// Accept accepts a suggestion: a manual placement of its pull request in its group.
func (s *Service) Accept(ctx context.Context, actor, id int64) error {
	return s.decide(ctx, actor, id, true)
}

// Dismiss dismisses a suggestion; the same suggestion is not opened again.
func (s *Service) Dismiss(ctx context.Context, actor, id int64) error {
	return s.decide(ctx, actor, id, false)
}

func (s *Service) decide(ctx context.Context, actor, id int64, accept bool) error {
	return s.act(ctx, func(tx pgx.Tx) error {
		var pr int64
		var group *int64
		err := tx.QueryRow(ctx, "SELECT pr_id, group_id FROM suggestion WHERE id = $1 AND state = 'open' FOR UPDATE", id).Scan(&pr, &group)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail.New(fail.NotFound, "not_found", "There is no such open suggestion.")
		}
		if err != nil {
			return err
		}
		state := "dismissed"
		if accept {
			state = "accepted"
			if group == nil {
				return fail.New(fail.Conflict, "no_group", "The suggestion names no group.")
			}
			g, err := target(ctx, tx, *group)
			if err != nil {
				return err
			}
			if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND (NOT negative OR group_id = $2)", pr, g); err != nil {
				return err
			}
			if err := s.place(ctx, tx, actor, pr, g, false); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE suggestion SET state = $2, decided_at = now(), decided_by = $3 WHERE id = $1", id, state, actor)
		return err
	})
}
