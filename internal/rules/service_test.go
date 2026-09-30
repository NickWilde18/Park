package rules

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/store/storetest"
)

type env struct {
	s      *Service
	pool   *pgxpool.Pool
	ctx    context.Context
	member int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.MigratedPool(t)
	e := &env{s: New(pool, slog.New(slog.NewTextHandler(io.Discard, nil))), pool: pool, ctx: context.Background()}
	if err := pool.QueryRow(e.ctx, "INSERT INTO member (display_name, rules) VALUES ('Ana', true) RETURNING id").Scan(&e.member); err != nil {
		t.Fatal(err)
	}
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func hasProblem(ps []Problem, code string) bool {
	for _, p := range ps {
		if p.Code == code {
			return true
		}
	}
	return false
}

// At first start the default rule set is published as version 1, by no one. Servers starting at once publish it
// once, and later starts publish nothing.
func TestBootstrap(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	published := make([]bool, 4)
	errs := make([]error, 4)
	for i := range published {
		wg.Add(1)
		go func() {
			defer wg.Done()
			published[i], errs[i] = e.s.Bootstrap(e.ctx)
		}()
	}
	wg.Wait()
	n := 0
	for i := range published {
		must(t, errs[i])
		if published[i] {
			n++
		}
	}
	vs, err := e.s.Versions(e.ctx)
	must(t, err)
	if n != 1 || len(vs) != 1 || vs[0].Number != 1 || vs[0].PublishedBy != nil || vs[0].BasedOn != nil ||
		!reflect.DeepEqual(vs[0].Rules, Default()) {
		t.Fatalf("published %d times; versions %+v", n, vs)
	}
	if again, err := e.s.Bootstrap(e.ctx); err != nil || again {
		t.Fatalf("a later start published again: %v %v", again, err)
	}
}

// A draft starts from the version published now, with that version's rules or imported ones. A draft with problems
// is saved, and lists them; it is published only once it has none, as the next version, based on its base, by its
// publisher — and only once.
func TestDrafts(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Bootstrap(e.ctx)
	must(t, err)
	d, err := e.s.CreateDraft(e.ctx, e.member, nil, nil)
	must(t, err)
	if d.BaseVersion == nil || *d.BaseVersion != 1 || !reflect.DeepEqual(d.Rules, Default()) || len(d.Problems) > 0 {
		t.Fatalf("a new draft: %+v", d)
	}
	broken := Default()
	broken.Author[0].When = term("pr.nope", "is_true", nil)
	d, err = e.s.UpdateDraft(e.ctx, d.ID, nil, broken)
	must(t, err)
	if !hasProblem(d.Problems, "invalid_condition") || d.Problems[0].Role != Author || d.Problems[0].Rule != 1 {
		t.Fatalf("problems %+v", d.Problems)
	}
	if _, err := e.s.Publish(e.ctx, e.member, d.ID); code(err) != "draft_has_problems" {
		t.Fatalf("a draft with problems published: %v", err)
	}
	fixed := Default()
	fixed.Author[0].Then.Reason = "Still a draft"
	d, err = e.s.UpdateDraft(e.ctx, d.ID, nil, fixed)
	must(t, err)
	if len(d.Problems) > 0 {
		t.Fatalf("problems %+v", d.Problems)
	}
	v, err := e.s.Publish(e.ctx, e.member, d.ID)
	must(t, err)
	if v.Number != 2 || v.BasedOn == nil || *v.BasedOn != 1 || v.PublishedBy == nil || *v.PublishedBy != e.member ||
		v.Rules.Author[0].Then.Reason != "Still a draft" {
		t.Fatalf("version %+v", v)
	}
	if _, err := e.s.Publish(e.ctx, e.member, d.ID); code(err) != "draft_published" {
		t.Fatalf("published twice: %v", err)
	}
	if _, err := e.s.UpdateDraft(e.ctx, d.ID, nil, fixed); code(err) != "draft_published" {
		t.Fatalf("a published draft changed: %v", err)
	}
	if got, _ := e.s.Version(e.ctx, 2); got.Rules.Author[0].Then.Reason != "Still a draft" {
		t.Fatalf("version 2 changed: %+v", got.Rules.Author[0])
	}
	vs, err := e.s.Versions(e.ctx)
	must(t, err)
	if len(vs) != 2 || vs[0].Number != 2 || vs[1].Number != 1 {
		t.Fatalf("versions %+v", vs)
	}
	imported := Default()
	imported.Reviewer[0].Name = "A draft"
	d, err = e.s.CreateDraft(e.ctx, e.member, nil, &imported)
	must(t, err)
	if *d.BaseVersion != 2 || d.Rules.Reviewer[0].Name != "A draft" {
		t.Fatalf("an imported draft: %+v", d)
	}
	for _, err := range []error{
		func() error { _, err := e.s.Version(e.ctx, 9); return err }(),
		func() error { base := 9; _, err := e.s.CreateDraft(e.ctx, e.member, &base, nil); return err }(),
		func() error { base := 9; _, err := e.s.UpdateDraft(e.ctx, d.ID, &base, imported); return err }(),
		func() error { _, err := e.s.Publish(e.ctx, e.member, 999); return err }(),
	} {
		if code(err) != "not_found" {
			t.Errorf("an unknown version or draft: %v", err)
		}
	}
}

// Before any version is published, a draft starts from the default rule set, with no base, and publishes as
// version 1.
func TestFirstDraft(t *testing.T) {
	e := newEnv(t)
	d, err := e.s.CreateDraft(e.ctx, e.member, nil, nil)
	must(t, err)
	if d.BaseVersion != nil || !reflect.DeepEqual(d.Rules, Default()) || len(d.Problems) > 0 {
		t.Fatalf("draft %+v", d)
	}
	v, err := e.s.Publish(e.ctx, e.member, d.ID)
	must(t, err)
	if v.Number != 1 || v.BasedOn != nil {
		t.Fatalf("version %+v", v)
	}
}

// A draft started from an older version cannot be published until it is brought up to date, so that publishing never
// silently undoes someone else's change; bringing it up to date moves its base.
func TestStaleDraft(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Bootstrap(e.ctx)
	must(t, err)
	a, err := e.s.CreateDraft(e.ctx, e.member, nil, nil)
	must(t, err)
	b, err := e.s.CreateDraft(e.ctx, e.member, nil, nil)
	must(t, err)
	_, err = e.s.Publish(e.ctx, e.member, a.ID)
	must(t, err)
	b, err = e.s.Draft(e.ctx, b.ID)
	must(t, err)
	if len(b.Problems) != 1 || b.Problems[0].Code != "stale_draft" || b.Problems[0].Part != PartBase ||
		!strings.Contains(b.Problems[0].Message, "version 2 is published now") {
		t.Fatalf("problems %+v", b.Problems)
	}
	if _, err := e.s.Publish(e.ctx, e.member, b.ID); code(err) != "stale_draft" {
		t.Fatalf("a stale draft published: %v", err)
	}
	two := 2
	b, err = e.s.UpdateDraft(e.ctx, b.ID, &two, b.Rules)
	must(t, err)
	v, err := e.s.Publish(e.ctx, e.member, b.ID)
	must(t, err)
	if v.Number != 3 || *v.BasedOn != 2 {
		t.Fatalf("version %+v", v)
	}
}

// Two drafts of one version published at once: one becomes the next version, and the other is stale. Both publishes
// are held on their drafts' rows until both are under way, then let go together.
func TestConcurrentPublish(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Bootstrap(e.ctx)
	must(t, err)
	var ids []int64
	for range 2 {
		d, err := e.s.CreateDraft(e.ctx, e.member, nil, nil)
		must(t, err)
		ids = append(ids, d.ID)
	}
	hold, err := e.pool.Begin(e.ctx)
	must(t, err)
	defer func() { _ = hold.Rollback(e.ctx) }()
	_, err = hold.Exec(e.ctx, "SELECT 1 FROM rule_draft WHERE id = ANY($1) FOR UPDATE", ids)
	must(t, err)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.s.Publish(e.ctx, e.member, id)
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting int
		must(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting))
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d publishes waiting, not 2", waiting)
		}
	}
	must(t, hold.Rollback(e.ctx))
	wg.Wait()
	codes := []string{code(errs[0]), code(errs[1])}
	if (errs[0] == nil) == (errs[1] == nil) || (codes[0] != "stale_draft" && codes[1] != "stale_draft") {
		t.Fatalf("errors %v", errs)
	}
	vs, err := e.s.Versions(e.ctx)
	must(t, err)
	if len(vs) != 2 {
		t.Fatalf("versions %+v", vs)
	}
}

// A published version never changes: the database refuses to update or delete one (R7).
func TestVersionsNeverChange(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Bootstrap(e.ctx)
	must(t, err)
	for _, sql := range []string{"UPDATE rule_version SET rules = '{}' WHERE version = 1", "DELETE FROM rule_version WHERE version = 1"} {
		if _, err := e.pool.Exec(e.ctx, sql); err == nil || !strings.Contains(err.Error(), "never changes") {
			t.Errorf("%s: %v", sql, err)
		}
	}
}
