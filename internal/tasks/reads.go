package tasks

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Task returns a task, with its items and its events.
func (s *Service) Task(ctx context.Context, id int64) (Task, error) {
	var t Task
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if t, err = load(ctx, tx, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id, kind, actor, state, detail, at FROM task_event WHERE task_id = $1 ORDER BY id", id)
		if err != nil {
			return err
		}
		t.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
			var e Event
			var detail []byte
			if err := row.Scan(&e.ID, &e.Kind, &e.Actor, &e.State, &detail, &e.At); err != nil {
				return e, err
			}
			return e, json.Unmarshal(detail, &e.Detail)
		})
		return err
	})
	return t, err
}

// Tasks lists a member's tasks, or everyone's, newest first, with their items.
func (s *Service) Tasks(ctx context.Context, member int64, everyone bool) ([]Task, error) {
	var out []Task
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id FROM task WHERE $2 OR member_id = $1 ORDER BY id DESC", member, everyone)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			t, err := load(ctx, tx, id, false)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}
