package verdicts

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/rules"
)

// Change is a unit whose verdict a draft would change: what the version in force concludes for it now, and what the
// draft would conclude instead.
type Change struct {
	Before Verdict
	After  Verdict
}

// Preview is what a draft of the rules would conclude for every open unit, beside what the version in force concludes:
// docs/design/05-rules.md#editing-and-publishing.
type Preview struct {
	EvaluatedAt time.Time
	// Changes are the units whose verdict the draft would change, as a person sees it.
	Changes []Change
	// Fallback are the draft's verdicts that leave the ball to the fallback, changed or not.
	Fallback []Verdict
	// Version is the version in force.
	Version int
	// Units is how many open units were evaluated.
	Units int
}

// Preview evaluates every open unit twice, with the version in force and with a draft, from the same facts at the same
// moment, so that every change it finds is the draft's. Like evaluating a unit, it reads only recorded judgements, so it
// never asks the model. It stores nothing and queues nothing; a holder who holds the ball now holds it since the unit's
// current verdict says.
func (s *Service) Preview(ctx context.Context, draft *rules.Compiled) (Preview, error) {
	version, compiled, err := s.rules.Current(ctx)
	if err != nil {
		return Preview{}, err
	}
	chains, err := s.groups.ChainsOf(ctx)
	if err != nil {
		return Preview{}, err
	}
	out := Preview{Version: version, EvaluatedAt: s.now()}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT unit FROM (SELECT DISTINCT unit FROM ("+openUnits+
			") o) u ORDER BY split_part(unit, ':', 1), split_part(unit, ':', 2)::bigint")
		if err != nil {
			return err
		}
		units, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, unit := range units {
			prs, err := unitPRs(ctx, tx, unit)
			if err != nil {
				return err
			}
			var open []stored
			for _, p := range prs {
				if p.open {
					open = append(open, p)
				}
			}
			previous, err := currentVerdict(ctx, tx, unit)
			if err != nil {
				return err
			}
			people, err := s.parties(ctx, tx, open, chains, strings.HasPrefix(unit, "group:"), out.EvaluatedAt)
			if err != nil {
				return err
			}
			before := Verdict{Unit: unit, RuleVersion: version, EvaluatedAt: out.EvaluatedAt}
			after := Verdict{Unit: unit, EvaluatedAt: out.EvaluatedAt}
			conclude(&before, people, compiled, previous)
			conclude(&after, people, draft, previous)
			out.Units++
			if before.visible() != after.visible() {
				out.Changes = append(out.Changes, Change{Before: before, After: after})
			}
			if after.Fallback {
				out.Fallback = append(out.Fallback, after)
			}
		}
		return nil
	})
	return out, err
}
