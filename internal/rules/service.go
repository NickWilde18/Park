// Package rules holds rule sets: docs/design/05-rules.md.
//
// A rule set is each role's ordered list of rules. Conditions — structured, or expressions — compile to CEL against
// the field catalog, and a person's list is evaluated with every judgement not recorded left unknown. Rule sets are
// published as immutable, numbered versions, from drafts that pass every publish check.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/fail"
)

// Follower is told, inside the publishing transaction, that a version was published, so that a later stage can queue
// its work.
type Follower func(ctx context.Context, tx pgx.Tx, version int) error

// Service keeps rule sets: the published versions, and drafts of the next one.
type Service struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	followers []Follower
	// compiled holds each version compiled, once: versions never change.
	compiled sync.Map
}

// Follow adds a follower. It must be called before anything is published.
func (s *Service) Follow(f Follower) { s.followers = append(s.followers, f) }

// Current returns the number of the version in force, and the version compiled for evaluation.
func (s *Service) Current(ctx context.Context) (int, *Compiled, error) {
	n, err := current(ctx, s.pool)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		return 0, nil, errNoVersion
	}
	if c, ok := s.compiled.Load(n); ok {
		return n, c.(*Compiled), nil
	}
	v, err := s.Version(ctx, n)
	if err != nil {
		return 0, nil, err
	}
	c, problems := Compile(v.Rules)
	if len(problems) > 0 {
		return 0, nil, fmt.Errorf("version %d does not compile: %s", n, problems[0].Message)
	}
	s.compiled.Store(n, c)
	return n, c, nil
}

// New returns the rule-set service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log}
}

// Version is a published rule set.
type Version struct {
	PublishedAt time.Time
	// BasedOn is the version its draft started from; nil for the first.
	BasedOn *int
	// PublishedBy is the member who published it; nil for the default rule set, published by Park.
	PublishedBy *int64
	Rules       RuleSet
	Number      int
}

// Draft is a draft of the next version, with the problems that keep it from being published.
type Draft struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	// BaseVersion is the version it started from; nil before the first version.
	BaseVersion *int
	// PublishedVersion is the version it became, once published.
	PublishedVersion *int
	Rules            RuleSet
	Problems         []Problem
	ID               int64
	CreatedBy        int64
}

var (
	errNoVersion      = fail.New(fail.NotFound, "not_found", "There is no such rule set version.")
	errNoDraft        = fail.New(fail.NotFound, "not_found", "There is no such draft.")
	errDraftPublished = fail.New(fail.Conflict, "draft_published", "This draft is published already; start a new one.")
)

// lock serializes publishing, so that a draft's base is compared with the version current when it is published.
func lock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.rules'))")
	return err
}

// current is the number of the version published now; 0 before the first.
func current(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT COALESCE(max(version), 0) FROM rule_version").Scan(&n)
	return n, err
}

// Bootstrap publishes the default rule set as version 1 when no version is published yet, and reports whether it did.
func (s *Service) Bootstrap(ctx context.Context) (bool, error) {
	rs := Default()
	if _, problems := Compile(rs); len(problems) > 0 {
		return false, fmt.Errorf("the default rule set has %d problems, the first: %s", len(problems), problems[0].Message)
	}
	data, err := json.Marshal(rs)
	if err != nil {
		return false, err
	}
	published := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lock(ctx, tx); err != nil {
			return err
		}
		n, err := current(ctx, tx)
		if err != nil || n > 0 {
			return err
		}
		published = true
		_, err = tx.Exec(ctx, "INSERT INTO rule_version (version, rules) VALUES (1, $1)", data)
		return err
	})
	if published && err == nil {
		s.log.InfoContext(ctx, "published the default rule set", "version", 1)
	}
	return published, err
}

const versionColumns = "version, rules, based_on, published_by, published_at"

func scanVersion(row pgx.Row) (Version, error) {
	var v Version
	var data []byte
	if err := row.Scan(&v.Number, &data, &v.BasedOn, &v.PublishedBy, &v.PublishedAt); err != nil {
		return Version{}, err
	}
	return v, json.Unmarshal(data, &v.Rules)
}

// Versions lists the published versions, newest first: the first is the one in force.
func (s *Service) Versions(ctx context.Context) ([]Version, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+versionColumns+" FROM rule_version ORDER BY version DESC")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) { return scanVersion(row) })
}

// Version returns one published version.
func (s *Service) Version(ctx context.Context, n int) (Version, error) {
	v, err := scanVersion(s.pool.QueryRow(ctx, "SELECT "+versionColumns+" FROM rule_version WHERE version = $1", n))
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, errNoVersion
	}
	return v, err
}

// CreateDraft starts a draft. Its base is the given version, or the one published now; its rules are the given ones —
// an imported rule-set file — or its base's.
func (s *Service) CreateDraft(ctx context.Context, actor int64, base *int, rs *RuleSet) (Draft, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if base == nil {
			n, err := current(ctx, tx)
			if err != nil {
				return err
			}
			if n > 0 {
				base = &n
			}
		}
		if err := versionExists(ctx, tx, base); err != nil {
			return err
		}
		var data []byte
		switch {
		case rs != nil:
			var err error
			if data, err = json.Marshal(rs); err != nil {
				return err
			}
		case base != nil:
			if err := tx.QueryRow(ctx, "SELECT rules FROM rule_version WHERE version = $1", *base).Scan(&data); err != nil {
				return err
			}
		default:
			var err error
			if data, err = json.Marshal(Default()); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, "INSERT INTO rule_draft (base_version, rules, created_by) VALUES ($1, $2, $3) RETURNING id",
			base, data, actor).Scan(&id)
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Draft(ctx, id)
}

// UpdateDraft replaces a draft's rules, and its base when one is given: bringing a draft up to date moves its base to
// the version published now.
func (s *Service) UpdateDraft(ctx context.Context, id int64, base *int, rs RuleSet) (Draft, error) {
	data, err := json.Marshal(rs)
	if err != nil {
		return Draft{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := versionExists(ctx, tx, base); err != nil {
			return err
		}
		var published bool
		err := tx.QueryRow(ctx, "SELECT published_version IS NOT NULL FROM rule_draft WHERE id = $1 FOR UPDATE", id).Scan(&published)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errNoDraft
		case err != nil:
			return err
		case published:
			return errDraftPublished
		}
		_, err = tx.Exec(ctx, `UPDATE rule_draft SET rules = $2, base_version = COALESCE($3, base_version), updated_at = now()
			WHERE id = $1`, id, data, base)
		return err
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Draft(ctx, id)
}

// versionExists refuses a base version that was never published.
func versionExists(ctx context.Context, tx pgx.Tx, base *int) error {
	if base == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM rule_version WHERE version = $1)", *base).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errNoVersion
	}
	return nil
}

// Draft returns a draft, with every problem that keeps it from being published now.
func (s *Service) Draft(ctx context.Context, id int64) (Draft, error) {
	var d Draft
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT id, base_version, rules, created_by, created_at, updated_at, published_version
		FROM rule_draft WHERE id = $1`, id).Scan(&d.ID, &d.BaseVersion, &data, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt,
		&d.PublishedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, errNoDraft
	}
	if err != nil {
		return Draft{}, err
	}
	if err := json.Unmarshal(data, &d.Rules); err != nil {
		return Draft{}, err
	}
	n, err := current(ctx, s.pool)
	if err != nil {
		return Draft{}, err
	}
	d.Problems = problems(d, n)
	return d, nil
}

// problems are a draft's publish problems, given the version published now; a published draft has none.
func problems(d Draft, current int) []Problem {
	if d.PublishedVersion != nil {
		return nil
	}
	var out []Problem
	if base := baseOf(d); base != current {
		out = append(out, Problem{Part: PartBase, Code: "stale_draft", Message: fmt.Sprintf(
			"This draft started from version %d, and version %d is published now; bring it up to date first.", base, current)})
	}
	_, compiled := Compile(d.Rules)
	return append(out, compiled...)
}

func baseOf(d Draft) int {
	if d.BaseVersion == nil {
		return 0
	}
	return *d.BaseVersion
}

// Publish makes a draft the next version, if it passes every check: its rules, and its base, which must be the version
// published now, so that publishing never silently undoes someone else's change.
func (s *Service) Publish(ctx context.Context, actor, id int64) (Version, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lock(ctx, tx); err != nil {
			return err
		}
		var d Draft
		var data []byte
		err := tx.QueryRow(ctx, "SELECT base_version, rules, published_version FROM rule_draft WHERE id = $1 FOR UPDATE", id).
			Scan(&d.BaseVersion, &data, &d.PublishedVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoDraft
		}
		if err != nil {
			return err
		}
		if d.PublishedVersion != nil {
			return errDraftPublished
		}
		if err := json.Unmarshal(data, &d.Rules); err != nil {
			return err
		}
		now, err := current(ctx, tx)
		if err != nil {
			return err
		}
		found := problems(d, now)
		if len(found) > 0 && found[0].Code == "stale_draft" {
			return fail.New(fail.Conflict, "stale_draft", found[0].Message)
		}
		if len(found) > 0 {
			return fail.New(fail.Invalid, "draft_has_problems", fmt.Sprintf(
				"The draft has %d problems, the first: %s", len(found), found[0].Message))
		}
		n = now + 1
		if _, err := tx.Exec(ctx, "INSERT INTO rule_version (version, rules, based_on, published_by) VALUES ($1, $2, $3, $4)",
			n, data, d.BaseVersion, actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE rule_draft SET published_version = $2, updated_at = now() WHERE id = $1", id, n); err != nil {
			return err
		}
		for _, f := range s.followers {
			if err := f(ctx, tx, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Version{}, err
	}
	s.log.InfoContext(ctx, "published a rule set", "version", n, "draft", id, "member", actor)
	return s.Version(ctx, n)
}
