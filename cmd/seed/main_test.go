package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/secret"
	"github.com/NickWilde18/Park/internal/store/storetest"
)

// Seeding an empty database stores each pull request as a refresh would have: its fact events, numbered from 1 up to
// its sequence number, replay to its stored state (F1). The merged pull request's verdict is closed out. Ben's task
// was tried three times, failed verification each time, and needs a person; his other one is dismissed. Seeding the
// database again is refused.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	url := storetest.Database(t)
	env := map[string]string{"PARK_DATABASE_URL": url, "PARK_SECRET_KEY": "0123456789abcdef0123456789abcdef"}
	getenv := func(key string) string { return env[key] }
	if err := seed(ctx, getenv, io.Discard); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box, err := secret.New(env["PARK_SECRET_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	collected := facts.New(pool, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rows, err := pool.Query(ctx, "SELECT id FROM pr_fact ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 6 {
		t.Fatalf("%d pull requests seeded, want 6", len(ids))
	}
	for _, id := range ids {
		stored, err := collected.PR(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		events, err := collected.Timeline(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		changes := make([]map[string]facts.Change, len(events))
		for i, e := range events {
			if e.Seq != int64(i+1) {
				t.Fatalf("pull request %d: event %d has seq %d", id, i, e.Seq)
			}
			changes[i] = e.Changes
		}
		if int64(len(events)) != stored.Seq {
			t.Fatalf("pull request %d: %d events, seq %d", id, len(events), stored.Seq)
		}
		replayed, err := facts.Replay(changes)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(stored.State)
		got, _ := json.Marshal(replayed)
		if !bytes.Equal(got, want) {
			t.Fatalf("pull request %d replays to\n%s\nnot\n%s", id, got, want)
		}
	}
	var closed string
	if err := pool.QueryRow(ctx, `SELECT v.closed FROM verdict v JOIN pr_fact p ON v.unit = 'pr:' || p.id
		WHERE p.number = 11 AND v.current`).Scan(&closed); err != nil || closed != "merged" {
		t.Fatalf("the merged pull request's verdict: %q, %v", closed, err)
	}
	var states, failures string
	if err := pool.QueryRow(ctx, `SELECT string_agg(state || ' ' || attempt, ', ' ORDER BY id) FROM task t
		WHERE member_id = (SELECT member_id FROM local_credential WHERE username = 'ben')`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*)::text FROM task_event WHERE kind = 'verification_failed'").Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if states != "needs_person 3, dismissed 0" || failures != "3" {
		t.Fatalf("Ben's tasks: %s, with %s failed verifications", states, failures)
	}
	if err := seed(ctx, getenv, io.Discard); err == nil {
		t.Fatal("a second seed was not refused")
	}
}
