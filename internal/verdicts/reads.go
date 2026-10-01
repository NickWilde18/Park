package verdicts

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/rules"
)

const verdictColumns = `id, unit, current, side, closed, fallback, holders, parts, reminders, inputs, seqs, rule_version,
	evaluated_at, created_at`

func scanVerdict(row pgx.Row) (Verdict, error) {
	var v Verdict
	var side, closed *string
	var holders, parts, reminders, inputs, seqs []byte
	if err := row.Scan(&v.ID, &v.Unit, &v.Current, &side, &closed, &v.Fallback, &holders, &parts, &reminders, &inputs, &seqs,
		&v.RuleVersion, &v.EvaluatedAt, &v.CreatedAt); err != nil {
		return Verdict{}, err
	}
	if side != nil {
		v.Side = rules.Role(*side)
	}
	if closed != nil {
		v.Closed = *closed
	}
	for _, doc := range []struct {
		data []byte
		into any
	}{{holders, &v.Holders}, {parts, &v.Parts}, {reminders, &v.Reminders}, {inputs, &v.Inputs}, {seqs, &v.Seqs}} {
		if err := json.Unmarshal(doc.data, doc.into); err != nil {
			return Verdict{}, err
		}
	}
	return v, nil
}

func currentVerdict(ctx context.Context, tx pgx.Tx, unit string) (*Verdict, error) {
	v, err := scanVerdict(tx.QueryRow(ctx, "SELECT "+verdictColumns+" FROM verdict WHERE unit = $1 AND current", unit))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Verdict returns a verdict, current or superseded.
func (s *Service) Verdict(ctx context.Context, id int64) (Verdict, error) {
	v, err := scanVerdict(s.pool.QueryRow(ctx, "SELECT "+verdictColumns+" FROM verdict WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Verdict{}, fail.New(fail.NotFound, "not_found", "There is no such verdict.")
	}
	return v, err
}

func (s *Service) list(ctx context.Context, where string, args ...any) ([]Verdict, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+verdictColumns+" FROM verdict WHERE current AND closed IS NULL"+where+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Verdict, error) { return scanVerdict(row) })
}

// Current lists the current verdicts of the open units.
func (s *Service) Current(ctx context.Context) ([]Verdict, error) { return s.list(ctx, "") }

// CurrentOf returns a unit's current verdict, closed out or not, or nil when it has none.
func (s *Service) CurrentOf(ctx context.Context, unit string) (*Verdict, error) {
	v, err := scanVerdict(s.pool.QueryRow(ctx, "SELECT "+verdictColumns+" FROM verdict WHERE current AND unit = $1", unit))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Queue returns a member's queue — the open units whose ball they hold — and their waiting list — the open units they
// have a part in, where the ball is with someone else (V10).
func (s *Service) Queue(ctx context.Context, member int64) (holding, waiting []Verdict, err error) {
	rows, err := s.pool.Query(ctx, "SELECT forge_id::text || '/' || subject::text FROM member_identity WHERE member_id = $1", member)
	if err != nil {
		return nil, nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	if holding, err = s.list(ctx, " AND holder_keys && $1", keys); err != nil {
		return nil, nil, err
	}
	waiting, err = s.list(ctx, " AND party_keys && $1 AND NOT holder_keys && $1", keys)
	return holding, waiting, err
}
