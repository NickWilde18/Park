// Package grouping maintains chains and groups: docs/design/03-grouping.md.
//
// Chains are derived from branches whenever they are read. Groups come from assertions — members' placements,
// bridges' and structural co-memberships — which resolution turns into memberships. Resolution runs over every
// open pull request at once, in one transaction under the stage's lock, so that no two resolutions interleave and
// the outcome does not depend on the order events arrived in.
package grouping

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/work"
)

// KindResolve is the work of resolving memberships; there is one such item, keyed "all".
const KindResolve = "grouping.resolve"

// Settings are grouping's configuration, from docs/design/03-grouping.md#defaults.
type Settings struct {
	NeverGrouped []string
	LongLived    []string
	// MaxExternal is the largest external assertion applied; a larger one is recorded but not applied.
	MaxExternal int
}

// DefaultSettings are the documented defaults.
var DefaultSettings = Settings{
	NeverGrouped: []string{"main", "master", "develop", "dev", "trunk", "release/*", "hotfix/*", "patch-*", "dependabot/*", "renovate/*"},
	LongLived:    []string{"main", "master", "develop", "dev", "trunk", "release/*"},
	MaxExternal:  3,
}

// Follower is told, inside the resolving transaction, which pull requests changed group, so that a later stage can
// queue its work.
type Follower func(ctx context.Context, tx pgx.Tx, prs []int64) error

// Service is the grouping stage.
type Service struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	now       func() time.Time
	followers []Follower
	settings  Settings
}

// New returns the grouping stage.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log, now: time.Now, settings: DefaultSettings}
}

// Follow adds a follower. It must be called before the runner starts.
func (s *Service) Follow(f Follower) { s.followers = append(s.followers, f) }

// Register gives the runner the grouping stage's work.
func (s *Service) Register(r *work.Runner) {
	r.Handle(KindResolve, func(ctx context.Context, _ work.Item) error { return s.Resolve(ctx) })
}

// relevant are the facts grouping reads.
var relevant = map[string]bool{"state": true, "head_branch": true, "base_branch": true, "head_repo": true, "author": true,
	"closes_issues": true, "references": true, "title": true, "stack": true}

// FactsFollower queues a resolution when a pull request's facts that grouping reads change.
func (s *Service) FactsFollower() facts.Follower {
	return func(ctx context.Context, tx pgx.Tx, _ int64, changed []string) error {
		for _, f := range changed {
			if relevant[f] {
				return work.Enqueue(ctx, tx, KindResolve, "all", time.Time{})
			}
		}
		return nil
	}
}

// Resolve refreshes the structural assertions and resolves every open pull request's group.
func (s *Service) Resolve(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.resolveTx(ctx, tx) })
}

// member is a co-membership's member, as repository and number.
type member struct {
	Repo   int64 `json:"repo"`
	Number int   `json:"number"`
}

// open is what resolution reads of the open pull requests.
type open struct {
	nodes  []Node
	byRef  map[member]int64
	titles map[int64]string
	byID   map[int64]*Node
}

func (s *Service) loadOpen(ctx context.Context, tx pgx.Tx) (open, map[int64]RepoInfo, error) {
	o := open{byRef: map[member]int64{}, titles: map[int64]string{}, byID: map[int64]*Node{}}
	rows, err := tx.Query(ctx, `SELECT p.id, p.repo_id, p.number, p.state, r.forge_repo_id, r.default_branch, r.protection
		FROM pr_fact p JOIN repo r ON r.id = p.repo_id WHERE p.open AND r.watched ORDER BY p.id`)
	if err != nil {
		return o, nil, err
	}
	repos := map[int64]RepoInfo{}
	for rows.Next() {
		var n Node
		var raw, protection []byte
		var def string
		if err := rows.Scan(&n.ID, &n.Repo, &n.Number, &raw, &n.RepoForge, &def, &protection); err != nil {
			return o, nil, err
		}
		var p facts.PR
		if err := json.Unmarshal(raw, &p); err != nil {
			return o, nil, err
		}
		n.Title, n.HeadBranch, n.BaseBranch, n.Author, n.HeadRepo = p.Title, p.HeadBranch, p.BaseBranch, p.Author, p.HeadRepo
		n.CreatedAt, n.ClosesIssues, n.References = p.CreatedAt, p.ClosesIssues, p.References
		if p.Stack != nil {
			n.Stack = *p.Stack
		}
		o.nodes = append(o.nodes, n)
		if _, ok := repos[n.Repo]; !ok {
			info := RepoInfo{DefaultBranch: def, Protected: map[string]bool{}}
			var prot map[string]facts.Protection
			if err := json.Unmarshal(protection, &prot); err != nil {
				return o, nil, err
			}
			for b, pr := range prot {
				info.Protected[b] = pr.Protected
			}
			repos[n.Repo] = info
		}
	}
	if err := rows.Err(); err != nil {
		return o, nil, err
	}
	for i := range o.nodes {
		n := &o.nodes[i]
		o.byID[n.ID] = n
		o.byRef[member{Repo: n.Repo, Number: n.Number}] = n.ID
		o.titles[n.ID] = n.Title
	}
	return o, repos, nil
}

// refreshStructural makes the live structural assertions the signals of now: a signal whose members changed is
// reposted under its anchor, keeping when it was first seen; a signal that is gone is retracted.
func (s *Service) refreshStructural(ctx context.Context, tx pgx.Tx, o open, signals []Signal) error {
	type live struct {
		seen    time.Time
		members string
		id      int64
	}
	current := map[string]live{}
	rows, err := tx.Query(ctx, "SELECT id, anchor, members, seen_at FROM assertion WHERE source = 'structural' AND retracted_at IS NULL")
	if err != nil {
		return err
	}
	for rows.Next() {
		var l live
		var anchor string
		var members []byte
		if err := rows.Scan(&l.id, &anchor, &members, &l.seen); err != nil {
			return err
		}
		l.members = string(members)
		current[anchor] = l
	}
	if err := rows.Err(); err != nil {
		return err
	}
	now := s.now()
	wanted := map[string]bool{}
	for _, sig := range signals {
		wanted[sig.Anchor] = true
		ms := make([]member, 0, len(sig.PRs))
		for _, id := range sig.PRs {
			n := o.byID[id]
			ms = append(ms, member{Repo: n.Repo, Number: n.Number})
		}
		data, err := json.Marshal(ms)
		if err != nil {
			return err
		}
		seen := now
		if l, ok := current[sig.Anchor]; ok {
			if sameJSON(l.members, string(data)) {
				continue
			}
			seen = l.seen
			if _, err := tx.Exec(ctx, "UPDATE assertion SET retracted_at = $2 WHERE id = $1", l.id, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assertion (source, authority, anchor, kind, members, seen_at, created_at)
			VALUES ('structural', $1, $2, $3, $4, $5, $6)`, AuthorityStructural, sig.Anchor, sig.Kind, data, seen, now); err != nil {
			return err
		}
	}
	for anchor, l := range current {
		if !wanted[anchor] {
			if _, err := tx.Exec(ctx, "UPDATE assertion SET retracted_at = $2 WHERE id = $1", l.id, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

func anchorID(source, key string) string { return source + "\x00" + key }

func splitAnchor(id string) (string, string) {
	source, key, _ := strings.Cut(id, "\x00")
	return source, key
}

// liveAssertion is a live assertion, for resolution.
type liveAssertion struct {
	Seen      time.Time
	Anchor    *string
	Kind      *string
	Title     *string
	PR        *int64
	Group     *int64
	Source    string
	Members   []member
	ID        int64
	Authority int
	Negative  bool
}

func (s *Service) loadAssertions(ctx context.Context, tx pgx.Tx) ([]liveAssertion, error) {
	rows, err := tx.Query(ctx, `SELECT a.id, a.source, a.authority, a.anchor, a.kind, a.members, a.pr_id,
			COALESCE(al.alias_of, a.group_id), a.negative, a.title, a.seen_at
		FROM assertion a LEFT JOIN group_alias al ON al.group_id = a.group_id
		WHERE a.retracted_at IS NULL AND a.applied ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (liveAssertion, error) {
		var a liveAssertion
		var members []byte
		err := row.Scan(&a.ID, &a.Source, &a.Authority, &a.Anchor, &a.Kind, &members, &a.PR, &a.Group, &a.Negative, &a.Title, &a.Seen)
		if err == nil && members != nil {
			err = json.Unmarshal(members, &a.Members)
		}
		return a, err
	})
}

// resolveTx is one resolution, inside tx.
func (s *Service) resolveTx(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('park.grouping'))"); err != nil {
		return err
	}
	o, repos, err := s.loadOpen(ctx, tx)
	if err != nil {
		return err
	}
	chains := Chains(o.nodes, repos, s.settings.LongLived)
	signals, weak := Signals(o.nodes, chains, s.settings.NeverGrouped)
	if err := s.refreshStructural(ctx, tx, o, signals); err != nil {
		return err
	}
	assertions, err := s.loadAssertions(ctx, tx)
	if err != nil {
		return err
	}
	b := &board{Claims: map[int64][]claim{}, Excluded: map[int64]map[int64]bool{}, Current: map[int64]int64{},
		Anchors: map[string]int64{}, Groups: map[int64]time.Time{}}
	for _, n := range o.nodes {
		b.PRs = append(b.PRs, n.ID)
	}
	byAssertion := map[int64]liveAssertion{}
	for _, a := range assertions {
		byAssertion[a.ID] = a
		switch {
		case a.PR != nil && a.Negative:
			if b.Excluded[*a.PR] == nil {
				b.Excluded[*a.PR] = map[int64]bool{}
			}
			b.Excluded[*a.PR][*a.Group] = true
		case a.PR != nil:
			b.Claims[*a.PR] = append(b.Claims[*a.PR], claim{Group: *a.Group, Authority: a.Authority, Assertion: a.ID, Seen: a.Seen})
		case a.Anchor != nil:
			kind := ""
			if a.Kind != nil {
				kind = *a.Kind
			}
			for _, m := range a.Members {
				if pr, ok := o.byRef[m]; ok {
					b.Claims[pr] = append(b.Claims[pr], claim{Anchor: anchorID(a.Source, *a.Anchor), Kind: kind, Authority: a.Authority,
						Assertion: a.ID, Seen: a.Seen})
				}
			}
		}
	}
	rows, err := tx.Query(ctx, "SELECT pr_id, group_id, assertion_id FROM membership")
	if err != nil {
		return err
	}
	placedBy := map[int64]int64{}
	for rows.Next() {
		var pr, g, a int64
		if err := rows.Scan(&pr, &g, &a); err != nil {
			return err
		}
		b.Current[pr] = g
		placedBy[pr] = a
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, "SELECT source, key, group_id FROM anchor WHERE group_id IS NOT NULL")
	if err != nil {
		return err
	}
	for rows.Next() {
		var source, key string
		var g int64
		if err := rows.Scan(&source, &key, &g); err != nil {
			return err
		}
		b.Anchors[anchorID(source, key)] = g
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, "SELECT id, created_at FROM pr_group WHERE state = 'active'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var g int64
		var at time.Time
		if err := rows.Scan(&g, &at); err != nil {
			return err
		}
		b.Groups[g] = at
	}
	if err := rows.Err(); err != nil {
		return err
	}

	res := resolve(b)
	now := s.now()
	made := map[int64]int64{}
	for i, anchor := range res.NewGroups {
		name, source := s.nameOf(anchor, res, byAssertion, o)
		var g int64
		if err := tx.QueryRow(ctx, "INSERT INTO pr_group (name, name_source, created_at) VALUES ($1, $2, $3) RETURNING id",
			name, source, now).Scan(&g); err != nil {
			return err
		}
		made[-int64(i+1)] = g
	}
	real := func(g int64) int64 {
		if g < 0 {
			return made[g]
		}
		return g
	}
	for anchor, g := range res.Mapped {
		source, key := splitAnchor(anchor)
		if _, err := tx.Exec(ctx, `INSERT INTO anchor (source, key, group_id) VALUES ($1, $2, $3)
			ON CONFLICT (source, key) DO UPDATE SET group_id = EXCLUDED.group_id`, source, key, real(g)); err != nil {
			return err
		}
	}
	var changed []int64
	for _, pr := range b.PRs {
		p, placed := res.Member[pr]
		cur, had := b.Current[pr]
		switch {
		case placed && (!had || real(p.Group) != cur):
			changed = append(changed, pr)
			if _, err := tx.Exec(ctx, `INSERT INTO membership (pr_id, group_id, assertion_id, since) VALUES ($1, $2, $3, $4)
				ON CONFLICT (pr_id) DO UPDATE SET group_id = EXCLUDED.group_id, assertion_id = EXCLUDED.assertion_id, since = EXCLUDED.since`,
				pr, real(p.Group), p.Assertion, now); err != nil {
				return err
			}
		case placed && p.Assertion != placedBy[pr]:
			if _, err := tx.Exec(ctx, "UPDATE membership SET assertion_id = $2 WHERE pr_id = $1", pr, p.Assertion); err != nil {
				return err
			}
		case !placed && had:
			changed = append(changed, pr)
			if _, err := tx.Exec(ctx, "DELETE FROM membership WHERE pr_id = $1", pr); err != nil {
				return err
			}
		}
	}
	if err := s.suggest(ctx, tx, res, weak, o); err != nil {
		return err
	}
	if err := s.archive(ctx, tx, now); err != nil {
		return err
	}
	if err := s.rename(ctx, tx, byAssertion, o); err != nil {
		return err
	}
	if len(changed) > 0 {
		for _, f := range s.followers {
			if err := f(ctx, tx, changed); err != nil {
				return err
			}
		}
	}
	return nil
}

// nameOf names a new group from the claim it is made for, as the group names section orders them.
func (s *Service) nameOf(anchor string, res result, assertions map[int64]liveAssertion, o open) (string, string) {
	var a liveAssertion
	var winners []int64
	for pr, p := range res.Member {
		if as, ok := assertions[p.Assertion]; ok && as.Anchor != nil && anchorID(as.Source, *as.Anchor) == anchor {
			a = as
			winners = append(winners, pr)
		}
	}
	return claimName(a, winners, o)
}

// claimName is the name a claim offers, and where it comes from: an external title; for a stack, issue, or reference
// anchor, the title of the oldest pull request among its members; for a branch anchor, the branch.
func claimName(a liveAssertion, winners []int64, o open) (string, string) {
	oldest := func() string {
		best := int64(0)
		for _, pr := range winners {
			n := o.byID[pr]
			if n == nil {
				continue
			}
			if best == 0 || n.CreatedAt.Before(o.byID[best].CreatedAt) || (n.CreatedAt.Equal(o.byID[best].CreatedAt) && pr < best) {
				best = pr
			}
		}
		if best == 0 {
			return "Untitled"
		}
		return o.titles[best]
	}
	kind := ""
	if a.Kind != nil {
		kind = *a.Kind
	}
	switch {
	case a.Authority == AuthorityExternal:
		if a.Title != nil && strings.TrimSpace(*a.Title) != "" {
			return strings.TrimSpace(*a.Title), "external"
		}
		return oldest(), "refs"
	case kind == KindBranch && a.Anchor != nil:
		_, name, _ := strings.Cut(strings.TrimPrefix(*a.Anchor, "branch:"), "/")
		return name, "branch"
	case kind == KindStack:
		return oldest(), "stack"
	case kind == KindIssue:
		return oldest(), "issue"
	}
	return oldest(), "refs"
}

// nameRank orders the sources of a group's name, highest first.
var nameRank = map[string]int{"manual": 0, "external": 1, "stack": 2, "issue": 3, "branch": 4, "refs": 5}

// rename gives each active group the name of the highest source that offers one, unless a member named it.
func (s *Service) rename(ctx context.Context, tx pgx.Tx, assertions map[int64]liveAssertion, o open) error {
	rows, err := tx.Query(ctx, `SELECT g.id, g.name, g.name_source, m.pr_id, m.assertion_id
		FROM pr_group g JOIN membership m ON m.group_id = g.id WHERE g.state = 'active' AND g.name_source <> 'manual' ORDER BY g.id`)
	if err != nil {
		return err
	}
	type group struct {
		name, source string
		byClaim      map[int64][]int64
	}
	groups := map[int64]*group{}
	var order []int64
	for rows.Next() {
		var id, pr, a int64
		var name, source string
		if err := rows.Scan(&id, &name, &source, &pr, &a); err != nil {
			return err
		}
		g, ok := groups[id]
		if !ok {
			g = &group{name: name, source: source, byClaim: map[int64][]int64{}}
			groups[id] = g
			order = append(order, id)
		}
		g.byClaim[a] = append(g.byClaim[a], pr)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range order {
		g := groups[id]
		bestName, bestSource := "", ""
		claims := make([]int64, 0, len(g.byClaim))
		for a := range g.byClaim {
			claims = append(claims, a)
		}
		sort.Slice(claims, func(i, j int) bool { return claims[i] > claims[j] }) // the newest first
		for _, a := range claims {
			as, ok := assertions[a]
			if !ok || as.Anchor == nil {
				continue
			}
			name, source := claimName(as, g.byClaim[a], o)
			if bestSource == "" || nameRank[source] < nameRank[bestSource] {
				bestName, bestSource = name, source
			}
		}
		if bestSource != "" && (bestName != g.name || bestSource != g.source) {
			if _, err := tx.Exec(ctx, "UPDATE pr_group SET name = $2, name_source = $3 WHERE id = $1", id, bestName, bestSource); err != nil {
				return err
			}
		}
	}
	return nil
}

// suggest opens a suggestion for each structural claim that lost to a claim of another group, and for each weak
// signal, unless the same suggestion is open or was dismissed; and drops open suggestions that came true.
func (s *Service) suggest(ctx context.Context, tx pgx.Tx, res result, weak []Weak, o open) error {
	groupOf := func(pr int64) int64 {
		var g int64
		_ = tx.QueryRow(ctx, "SELECT group_id FROM membership WHERE pr_id = $1", pr).Scan(&g)
		return g
	}
	insert := func(pr, g int64, reason, key string) error {
		_, err := tx.Exec(ctx, `INSERT INTO suggestion (pr_id, group_id, reason, source, key) VALUES ($1, $2, $3, 'structural', $4)
			ON CONFLICT (pr_id, key) DO NOTHING`, pr, g, reason, key)
		return err
	}
	prs := make([]int64, 0, len(res.Lost))
	for pr := range res.Lost {
		prs = append(prs, pr)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i] < prs[j] })
	for _, pr := range prs {
		for _, l := range res.Lost[pr] {
			_, key := splitAnchor(l.claim.Anchor)
			reason := map[string]string{KindStack: "is in the same stack", KindIssue: "closes the same issue",
				KindBranch: "has the same branch name", KindRefs: "refers to them, and they to it"}[l.claim.Kind]
			if err := insert(pr, l.Group, reason, "claim:"+key+"->"+strconv.FormatInt(l.Group, 10)); err != nil {
				return err
			}
		}
	}
	for _, w := range weak {
		g := groupOf(w.Other)
		if g == 0 || g == groupOf(w.PR) {
			continue
		}
		if err := insert(w.PR, g, w.Reason, "weak:"+strconv.FormatInt(w.Other, 10)+"->"+strconv.FormatInt(g, 10)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM suggestion s USING membership m
		WHERE s.state = 'open' AND m.pr_id = s.pr_id AND m.group_id = s.group_id`)
	return err
}

// archive archives the active groups left with no open pull request, and releases their anchors: a later claim
// under the same anchor starts a new group. A closed pull request keeps the group it had (G2).
func (s *Service) archive(ctx context.Context, tx pgx.Tx, now time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE pr_group g SET state = 'archived', archived_at = $1
		WHERE g.state = 'active' AND NOT EXISTS (
			SELECT 1 FROM membership m JOIN pr_fact p ON p.id = m.pr_id WHERE m.group_id = g.id AND p.open)
		RETURNING g.id`, now)
	if err != nil {
		return err
	}
	archived, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(archived) == 0 {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE anchor SET group_id = NULL WHERE group_id = ANY($1)", archived)
	return err
}
