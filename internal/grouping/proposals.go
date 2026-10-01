package grouping

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/NickWilde18/Park/internal/fail"
)

// Proposal is what a bridge proposes: groups of pull requests under an anchor in its own system, with an optional
// title and evidence as IDs in its own system.
type Proposal struct {
	Anchor   string
	Title    string
	Evidence []string
	Groups   []ProposedGroup
}

// ProposedGroup is one group of a proposal: an optional name, and the pull requests the bridge named.
type ProposedGroup struct {
	Name string
	PRs  []PRRef
}

// ProposeResult is the live proposal under the anchor once a proposal is posted.
type ProposeResult struct {
	State string
	ID    int64
}

var errNoProposal = fail.New(fail.NotFound, "not_found", "There is no such open proposal.")

// Propose records a bridge's proposal (G16). The source is the bridge, never the request, and repositories are
// resolved as for assertions. When the live proposal under the anchor proposes the same groups, it stays as it is,
// decided or not, with the new title and evidence; otherwise it is retracted and the new one opens. A pull request
// Park has not collected yet is refreshed. Nothing is grouped until a member accepts.
func (s *Service) Propose(ctx context.Context, bridge int64, token string, p Proposal) (ProposeResult, error) {
	p.Anchor = strings.TrimSpace(p.Anchor)
	if p.Anchor == "" || len(p.Anchor) > 200 {
		return ProposeResult{}, fail.New(fail.Invalid, "invalid_anchor", "An anchor is 1 to 200 characters: an ID in the bridge's own system.")
	}
	if len(p.Groups) == 0 {
		return ProposeResult{}, fail.New(fail.Invalid, "no_groups", "A proposal has at least one group.")
	}
	for _, e := range p.Evidence {
		if strings.Contains(e, "://") {
			return ProposeResult{}, fail.New(fail.Invalid, "invalid_evidence", "Evidence is IDs in the bridge's own system, never links.")
		}
	}
	if p.Evidence == nil {
		p.Evidence = []string{}
	}
	var title *string
	if t := strings.TrimSpace(p.Title); t != "" {
		title = &t
	}
	source := "bridge:" + strconv.FormatInt(bridge, 10)
	var out ProposeResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.proposal'))"); err != nil {
			return err
		}
		names, err := watchedNames(ctx, tx)
		if err != nil {
			return err
		}
		groups, err := proposedMembers(names, p.Groups)
		if err != nil {
			return err
		}
		for _, g := range groups {
			for _, m := range g.Members {
				if err := collect(ctx, tx, m); err != nil {
					return err
				}
			}
		}
		sum := fingerprint(groups)
		var live int64
		var liveFingerprint, liveState string
		err = tx.QueryRow(ctx, "SELECT id, fingerprint, state FROM proposal WHERE source = $1 AND anchor = $2 AND retracted_at IS NULL",
			source, p.Anchor).Scan(&live, &liveFingerprint, &liveState)
		switch {
		case err == nil && liveFingerprint == sum:
			out = ProposeResult{ID: live, State: liveState}
			_, err = tx.Exec(ctx, "UPDATE proposal SET title = $2, evidence = $3 WHERE id = $1", live, title, p.Evidence)
			return err
		case err == nil:
			if _, err := tx.Exec(ctx, "UPDATE proposal SET retracted_at = now() WHERE id = $1", live); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		out.State = "open"
		if err := tx.QueryRow(ctx, `INSERT INTO proposal (source, anchor, title, evidence, fingerprint, token_id)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, source, p.Anchor, title, p.Evidence, sum, token).Scan(&out.ID); err != nil {
			return err
		}
		for i, g := range groups {
			data, err := json.Marshal(g.Members)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO proposal_group (proposal_id, position, name, members) VALUES ($1, $2, $3, $4)",
				out.ID, i+1, g.Name, data); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// storedGroup is a proposed group as it is stored: a name, when it has one, and its members.
type storedGroup struct {
	Name    *string
	Members []member
}

// proposedMembers resolves a proposal's groups to watched repositories' pull requests, each group's in order of
// repository and number, and refuses a group of fewer than two, a name too long, or a pull request in two groups.
func proposedMembers(names repoNames, groups []ProposedGroup) ([]storedGroup, error) {
	out := make([]storedGroup, 0, len(groups))
	in := map[member]bool{}
	for _, g := range groups {
		var sg storedGroup
		if name := strings.TrimSpace(g.Name); name != "" {
			if utf8.RuneCountInString(name) > 200 {
				return nil, fail.New(fail.Invalid, "invalid_name", "A group's name is 1 to 200 characters long.")
			}
			sg.Name = &name
		}
		seen := map[member]bool{}
		for _, ref := range g.PRs {
			m, ok := names.member(ref)
			if !ok {
				return nil, errUnknownRepo
			}
			if seen[m] {
				continue
			}
			if in[m] {
				return nil, fail.New(fail.Invalid, "duplicate_pull_request", "A pull request is in one proposed group at most.")
			}
			seen[m], in[m] = true, true
			sg.Members = append(sg.Members, m)
		}
		if len(sg.Members) < 2 {
			return nil, fail.New(fail.Invalid, "too_few_pull_requests", "A proposed group has at least two pull requests.")
		}
		sort.Slice(sg.Members, func(i, j int) bool {
			a, b := sg.Members[i], sg.Members[j]
			return a.Repo < b.Repo || (a.Repo == b.Repo && a.Number < b.Number)
		})
		out = append(out, sg)
	}
	return out, nil
}

// fingerprint identifies a proposal's groups — their names and members, in any order — so that a repost of the same
// groups is told from a new proposal.
func fingerprint(groups []storedGroup) string {
	keys := make([]string, 0, len(groups))
	for _, g := range groups {
		var b strings.Builder
		if g.Name != nil {
			b.WriteString(*g.Name)
		}
		for _, m := range g.Members {
			b.WriteString("\x00" + strconv.FormatInt(m.Repo, 10) + "/" + strconv.Itoa(m.Number))
		}
		keys = append(keys, b.String())
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x01")))
	return hex.EncodeToString(sum[:])
}

// RetractProposal retracts a bridge's live proposal under an anchor. Groups a member made by accepting it stay.
func (s *Service) RetractProposal(ctx context.Context, bridge int64, anchor string) error {
	tag, err := s.pool.Exec(ctx, "UPDATE proposal SET retracted_at = now() WHERE retracted_at IS NULL AND source = $1 AND anchor = $2",
		"bridge:"+strconv.FormatInt(bridge, 10), strings.TrimSpace(anchor))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fail.New(fail.NotFound, "not_found", "There is no live proposal under that anchor.")
	}
	return nil
}

// ProposalView is an open proposal as the groups page shows it.
type ProposalView struct {
	CreatedAt time.Time
	// Bridge is the name of the bridge that proposed it, LinkTemplate its link template, and Anchor its anchor there.
	Bridge       string
	LinkTemplate string
	Anchor       string
	Title        string
	Groups       []ProposedGroupView
	ID           int64
}

// ProposedGroupView is one proposed group: its name, and its open pull requests by ID.
type ProposedGroupView struct {
	Name string
	PRs  []int64
}

// Proposals lists the open proposals, oldest first, each with its groups that still have two or more open pull
// requests; a proposal with no such group is left out. A pull request Park has not collected yet is left out of its
// group until it is.
func (s *Service) Proposals(ctx context.Context) ([]ProposalView, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.anchor, COALESCE(p.title, ''), p.created_at, b.name, b.link_template, g.name,
			ARRAY(SELECT f.id FROM jsonb_to_recordset(g.members) AS m(repo bigint, number integer)
				JOIN pr_fact f ON f.repo_id = m.repo AND f.number = m.number AND f.open ORDER BY f.id)
		FROM proposal p
		JOIN bridge b ON p.source = 'bridge:' || b.id::text
		JOIN proposal_group g ON g.proposal_id = p.id
		WHERE p.state = 'open' AND p.retracted_at IS NULL
		ORDER BY p.created_at, p.id, g.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProposalView
	for rows.Next() {
		var v ProposalView
		var name *string
		var prs []int64
		if err := rows.Scan(&v.ID, &v.Anchor, &v.Title, &v.CreatedAt, &v.Bridge, &v.LinkTemplate, &name, &prs); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != v.ID {
			out = append(out, v)
		}
		if len(prs) < 2 {
			continue
		}
		g := ProposedGroupView{PRs: prs}
		if name != nil {
			g.Name = *name
		}
		last := &out[len(out)-1]
		last.Groups = append(last.Groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	shown := out[:0]
	for _, v := range out {
		if len(v.Groups) > 0 {
			shown = append(shown, v)
		}
	}
	return shown, nil
}

// EditedGroup is a proposed group as a member edited it before accepting: a name, and pull requests by ID.
type EditedGroup struct {
	Name string
	PRs  []int64
}

// AcceptProposal accepts an open proposal, as proposed or, with edited groups, as the member edited it. Each group
// with two or more open pull requests becomes a group of those pull requests, placed by the member, under its name —
// or, as proposed without one, its oldest pull request's title; the others are skipped. It returns the groups made,
// in order, and refuses when no group has two open pull requests.
func (s *Service) AcceptProposal(ctx context.Context, actor, id int64, edited []EditedGroup) ([]int64, error) {
	var made []int64
	err := s.act(ctx, func(tx pgx.Tx) error {
		var open bool
		if err := tx.QueryRow(ctx, "SELECT state = 'open' AND retracted_at IS NULL FROM proposal WHERE id = $1 FOR UPDATE", id).
			Scan(&open); errors.Is(err, pgx.ErrNoRows) || (err == nil && !open) {
			return errNoProposal
		} else if err != nil {
			return err
		}
		groups := edited
		if groups == nil {
			var err error
			if groups, err = asProposed(ctx, tx, id); err != nil {
				return err
			}
		}
		taken := map[int64]bool{}
		for _, g := range groups {
			name := g.Name
			if edited != nil {
				var err error
				if name, err = checkName(name); err != nil {
					return err
				}
			}
			var prs []int64
			for _, pr := range g.PRs {
				if taken[pr] {
					return fail.New(fail.Invalid, "duplicate_pull_request", "A pull request is in one group at most.")
				}
				taken[pr] = true
				var ok bool
				if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pr_fact WHERE id = $1 AND open)", pr).Scan(&ok); err != nil {
					return err
				}
				if ok {
					prs = append(prs, pr)
				}
			}
			if len(prs) < 2 {
				continue
			}
			if name == "" {
				if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(state->>'title', ''), 'Untitled') FROM pr_fact WHERE id = ANY($1)
					ORDER BY (state->>'created_at')::timestamptz, id LIMIT 1`, prs).Scan(&name); err != nil {
					return err
				}
			}
			g, err := newGroup(ctx, tx, name)
			if err != nil {
				return err
			}
			for _, pr := range prs {
				if err := s.retract(ctx, tx, "source = 'manual' AND pr_id = $1 AND NOT negative", pr); err != nil {
					return err
				}
				if err := s.place(ctx, tx, actor, pr, g, false); err != nil {
					return err
				}
			}
			made = append(made, g)
		}
		if len(made) == 0 {
			return fail.New(fail.Conflict, "nothing_to_group", "No group of the proposal has two open pull requests.")
		}
		_, err := tx.Exec(ctx, "UPDATE proposal SET state = 'accepted', decided_at = now(), decided_by = $2 WHERE id = $1", id, actor)
		return err
	})
	return made, err
}

// asProposed reads a proposal's groups as the bridge proposed them, with the pull requests Park has collected.
func asProposed(ctx context.Context, tx pgx.Tx, id int64) ([]EditedGroup, error) {
	rows, err := tx.Query(ctx, `SELECT COALESCE(g.name, ''),
			ARRAY(SELECT f.id FROM jsonb_to_recordset(g.members) AS m(repo bigint, number integer)
				JOIN pr_fact f ON f.repo_id = m.repo AND f.number = m.number ORDER BY f.id)
		FROM proposal_group g WHERE g.proposal_id = $1 ORDER BY g.position`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EditedGroup, error) {
		var g EditedGroup
		err := row.Scan(&g.Name, &g.PRs)
		return g, err
	})
}

// DismissProposal dismisses an open proposal; a repost of the same groups does not open it again.
func (s *Service) DismissProposal(ctx context.Context, actor, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE proposal SET state = 'dismissed', decided_at = now(), decided_by = $2
		WHERE id = $1 AND state = 'open' AND retracted_at IS NULL`, id, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNoProposal
	}
	return nil
}
