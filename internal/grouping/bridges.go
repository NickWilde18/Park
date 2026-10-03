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

// CreateBridge registers an external bridge owned by a member. Its link template, if any, is an http or https
// address with {id} where an anchor or evidence ID goes, used to render links when they are shown.
func (s *Service) CreateBridge(ctx context.Context, owner int64, name, linkTemplate string) (Bridge, error) {
	name, err := CheckBridge(name, linkTemplate)
	if err != nil {
		return Bridge{}, err
	}
	b := Bridge{Name: name, LinkTemplate: linkTemplate, OwnerID: owner}
	err = s.pool.QueryRow(ctx, "INSERT INTO bridge (name, owner_id, link_template) VALUES ($1, $2, $3) RETURNING id, created_at",
		name, owner, linkTemplate).Scan(&b.ID, &b.CreatedAt)
	return b, err
}

// CheckBridge checks what every bridge has, whether Park runs it or not — a name of 1 to 100 characters, and a link
// template that is empty or an http or https address with {id} in it — and returns the name trimmed.
func CheckBridge(name, linkTemplate string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", fail.New(fail.Invalid, "invalid_name", "A bridge's name is 1 to 100 characters long.")
	}
	if linkTemplate != "" {
		u, err := url.Parse(strings.ReplaceAll(linkTemplate, "{id}", "x"))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !strings.Contains(linkTemplate, "{id}") {
			return "", fail.New(fail.Invalid, "invalid_link_template", "A link template is an http or https address with {id} in it.")
		}
	}
	return name, nil
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
		names, err := watchedNames(ctx, tx)
		if err != nil {
			return err
		}
		var members []member
		seen := map[member]bool{}
		for _, ref := range a.PRs {
			m, ok := names.member(ref)
			if !ok {
				return errUnknownRepo
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			members = append(members, m)
			if err := collect(ctx, tx, m); err != nil {
				return err
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
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9) RETURNING id`, source, AuthorityExternal, a.Anchor, data, title, a.Evidence,
			out.Applied, token, seenAt).Scan(&out.ID)
	})
	return out, err
}

// repoNames resolves the repository names bridges use — owner/name, or a name only one watched repository has — to
// watched repositories.
type repoNames struct {
	full  map[string]int64
	short map[string][]int64
}

// watchedNames reads the watched repositories' names.
func watchedNames(ctx context.Context, tx pgx.Tx) (repoNames, error) {
	rows, err := tx.Query(ctx, "SELECT id, owner || '/' || name, name FROM repo WHERE watched")
	if err != nil {
		return repoNames{}, err
	}
	defer rows.Close()
	n := repoNames{full: map[string]int64{}, short: map[string][]int64{}}
	for rows.Next() {
		var id int64
		var full, short string
		if err := rows.Scan(&id, &full, &short); err != nil {
			return repoNames{}, err
		}
		n.full[strings.ToLower(full)] = id
		n.short[strings.ToLower(short)] = append(n.short[strings.ToLower(short)], id)
	}
	return n, rows.Err()
}

// member resolves a bridge's reference to a pull request; false when its repository is unknown or not watched, or
// its number cannot be a pull request's.
func (n repoNames) member(ref PRRef) (member, bool) {
	name := strings.ToLower(strings.TrimSpace(ref.Repo))
	id, ok := n.full[name]
	if !ok && len(n.short[name]) == 1 {
		id, ok = n.short[name][0], true
	}
	if !ok || ref.Number <= 0 {
		return member{}, false
	}
	return member{Repo: id, Number: ref.Number}, true
}

// collect queues a refresh of a pull request Park has not collected yet, so that it joins once it is.
func collect(ctx context.Context, tx pgx.Tx, m member) error {
	var stored bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pr_fact WHERE repo_id = $1 AND number = $2)", m.Repo, m.Number).
		Scan(&stored); err != nil || stored {
		return err
	}
	return work.Enqueue(ctx, tx, facts.KindRefreshPR, strconv.FormatInt(m.Repo, 10)+"/"+strconv.Itoa(m.Number), time.Time{})
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
