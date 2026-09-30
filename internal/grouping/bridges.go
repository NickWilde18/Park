package grouping

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/work"
)

// Bridge is a registered bridge.
type Bridge struct {
	CreatedAt    time.Time
	Name         string
	LinkTemplate string
	ID           int64
	OwnerID      int64
}

// CreateBridge registers a bridge owned by a member. Its link template, if any, is an http or https address with
// {id} where an anchor or evidence ID goes, used to render links when they are shown.
func (s *Service) CreateBridge(ctx context.Context, owner int64, name, linkTemplate string) (Bridge, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return Bridge{}, fail.New(fail.Invalid, "invalid_name", "A bridge's name is 1 to 100 characters long.")
	}
	if linkTemplate != "" {
		u, err := url.Parse(strings.ReplaceAll(linkTemplate, "{id}", "x"))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !strings.Contains(linkTemplate, "{id}") {
			return Bridge{}, fail.New(fail.Invalid, "invalid_link_template", "A link template is an http or https address with {id} in it.")
		}
	}
	b := Bridge{Name: name, LinkTemplate: linkTemplate, OwnerID: owner}
	err := s.pool.QueryRow(ctx, "INSERT INTO bridge (name, owner_id, link_template) VALUES ($1, $2, $3) RETURNING id, created_at",
		name, owner, linkTemplate).Scan(&b.ID, &b.CreatedAt)
	return b, err
}

// Bridges lists the registered bridges.
func (s *Service) Bridges(ctx context.Context) ([]Bridge, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, name, owner_id, link_template, created_at FROM bridge ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Bridge, error) {
		var b Bridge
		err := row.Scan(&b.ID, &b.Name, &b.OwnerID, &b.LinkTemplate, &b.CreatedAt)
		return b, err
	})
}

// PRRef names a pull request as a bridge does: by repository name — owner/name, or a name that only one watched
// repository has — and number.
type PRRef struct {
	Repo   string
	Number int
}

// Assertion is what a bridge posts: an anchor in its own system, the pull requests it links, an optional title, and
// evidence as IDs in its own system.
type Assertion struct {
	Anchor   string
	Title    string
	PRs      []PRRef
	Evidence []string
}

// AssertResult is what Park did with an assertion.
type AssertResult struct {
	ID      int64
	Applied bool
}

var errUnknownRepo = fail.New(fail.Invalid, "unknown_repo", "A repository is not one Park watches.")

// Assert records a bridge's co-membership assertion, retracting the live one under the same anchor — history is kept,
// and only one assertion per anchor is live (G13) — and resolves. The source is the bridge, never the request. A
// repository that is unknown, not watched, or not visible gets the same refusal. An assertion naming more pull
// requests than the largest applied is recorded but not applied: it is usually a progress report, not a unit of
// review. A pull request Park has not collected yet is refreshed, and joins once it is.
func (s *Service) Assert(ctx context.Context, bridge int64, token string, a Assertion) (AssertResult, error) {
	a.Anchor = strings.TrimSpace(a.Anchor)
	if a.Anchor == "" || len(a.Anchor) > 200 {
		return AssertResult{}, fail.New(fail.Invalid, "invalid_anchor", "An anchor is 1 to 200 characters: an ID in the bridge's own system.")
	}
	if len(a.PRs) < 2 {
		return AssertResult{}, fail.New(fail.Invalid, "too_few_pull_requests", "An assertion links at least two pull requests.")
	}
	for _, e := range a.Evidence {
		if strings.Contains(e, "://") {
			return AssertResult{}, fail.New(fail.Invalid, "invalid_evidence", "Evidence is IDs in the bridge's own system, never links.")
		}
	}
	source := "bridge:" + strconv.FormatInt(bridge, 10)
	var out AssertResult
	err := s.act(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id, owner || '/' || name, name FROM repo WHERE watched")
		if err != nil {
			return err
		}
		full := map[string]int64{}
		short := map[string][]int64{}
		for rows.Next() {
			var id int64
			var f, n string
			if err := rows.Scan(&id, &f, &n); err != nil {
				return err
			}
			full[strings.ToLower(f)] = id
			short[strings.ToLower(n)] = append(short[strings.ToLower(n)], id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var members []member
		seen := map[member]bool{}
		for _, ref := range a.PRs {
			name := strings.ToLower(strings.TrimSpace(ref.Repo))
			id, ok := full[name]
			if !ok && len(short[name]) == 1 {
				id, ok = short[name][0], true
			}
			if !ok || ref.Number <= 0 {
				return errUnknownRepo
			}
			m := member{Repo: id, Number: ref.Number}
			if seen[m] {
				continue
			}
			seen[m] = true
			members = append(members, m)
			var stored bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pr_fact WHERE repo_id = $1 AND number = $2)", id, ref.Number).Scan(&stored); err != nil {
				return err
			}
			if !stored {
				if err := work.Enqueue(ctx, tx, facts.KindRefreshPR, strconv.FormatInt(id, 10)+"/"+strconv.Itoa(ref.Number), time.Time{}); err != nil {
					return err
				}
			}
		}
		data, err := json.Marshal(members)
		if err != nil {
			return err
		}
		seenAt := s.now()
		var prior *time.Time
		if err := tx.QueryRow(ctx, "SELECT min(seen_at) FROM assertion WHERE source = $1 AND anchor = $2", source, a.Anchor).Scan(&prior); err != nil {
			return err
		}
		if prior != nil {
			seenAt = *prior
		}
		if err := s.retract(ctx, tx, "source = $1 AND anchor = $2", source, a.Anchor); err != nil {
			return err
		}
		out.Applied = len(members) <= s.settings.MaxExternal
		var title *string
		if t := strings.TrimSpace(a.Title); t != "" {
			title = &t
		}
		if a.Evidence == nil {
			a.Evidence = []string{}
		}
		return tx.QueryRow(ctx, `INSERT INTO assertion (source, authority, anchor, members, title, evidence, applied, token_id, seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`, source, AuthorityExternal, a.Anchor, data, title, a.Evidence,
			out.Applied, token, seenAt).Scan(&out.ID)
	})
	return out, err
}

// Retract retracts a bridge's live assertion under an anchor, and resolves.
func (s *Service) Retract(ctx context.Context, bridge int64, anchor string) error {
	source := "bridge:" + strconv.FormatInt(bridge, 10)
	return s.act(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE assertion SET retracted_at = now() WHERE retracted_at IS NULL AND source = $1 AND anchor = $2",
			source, strings.TrimSpace(anchor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail.New(fail.NotFound, "not_found", "There is no live assertion under that anchor.")
		}
		return nil
	})
}
