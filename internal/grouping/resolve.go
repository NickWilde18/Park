package grouping

import (
	"sort"
	"time"
)

// The authorities of claims, highest first (G3).
const (
	AuthorityManual     = 3
	AuthorityExternal   = 2
	AuthorityStructural = 1
)

// claim is one live claim on one pull request: a manual placement in a group, or a co-membership under an anchor.
type claim struct {
	Seen      time.Time
	Anchor    string // the anchor's identity for a co-membership; "" for a placement
	Kind      string // for a structural co-membership: issue, branch, or refs
	Assertion int64
	Group     int64 // a placement's group
	Authority int
}

// board is what resolution reads: the open pull requests of the components being resolved, their claims, the
// groups members took them out of and the anchors whose claims had placed them there, where each is now, where each
// anchor points, and the active groups.
type board struct {
	Claims          map[int64][]claim
	Excluded        map[int64]map[int64]bool
	ExcludedAnchors map[int64]map[string]bool
	Current         map[int64]int64
	Anchors         map[string]int64
	Groups          map[int64]time.Time // active group → created
	PRs             []int64
}

// placement is where resolution puts a pull request: a group — negative for one resolution creates — and the
// assertion that placed it (G12).
type placement struct {
	Group     int64
	Assertion int64
}

// result is what resolution decides.
type result struct {
	Member map[int64]placement
	// NewGroups are the groups to create, by their negative IDs: -1 is NewGroups[0], and so on, each for the anchor
	// whose claim needs it.
	NewGroups []string
	// Mapped are the anchors that now point to a group, negative for a new one.
	Mapped map[string]int64
	// Lost are the structural claims that lost, per pull request, pointing to the group they would have given.
	Lost map[int64][]lostClaim
}

type lostClaim struct {
	claim claim
	Group int64
}

// groupOf is the group a claim names: a placement's own, or its anchor's.
func (b *board) groupOf(c claim) int64 {
	if c.Anchor == "" {
		return c.Group
	}
	g := b.Anchors[c.Anchor]
	if _, active := b.Groups[g]; !active {
		return 0
	}
	return g
}

// excludes reports whether a member's exclusion keeps a claim off a pull request (G6): one that names the group the
// claim points to, or the claim's anchor, wherever that anchor points now.
func (b *board) excludes(pr int64, c claim) bool {
	if c.Anchor != "" && b.ExcludedAnchors[pr][c.Anchor] {
		return true
	}
	g := b.groupOf(c)
	return g != 0 && b.Excluded[pr][g]
}

// better reports whether claim a beats claim b for a pull request now in group current (G4): the higher authority;
// then, for manual and external claims, the newer, and for structural ones, the stronger kind; then the claim that
// keeps the pull request where it is; then the older; then, for a total order, the anchor and the assertion.
func (b *board) better(x, y claim, current int64) bool {
	if x.Authority != y.Authority {
		return x.Authority > y.Authority
	}
	if x.Authority == AuthorityStructural {
		if sx, sy := strength(x.Kind), strength(y.Kind); sx != sy {
			return sx < sy
		}
	} else if !x.Seen.Equal(y.Seen) {
		return x.Seen.After(y.Seen)
	}
	if current != 0 {
		if kx, ky := b.groupOf(x) == current, b.groupOf(y) == current; kx != ky {
			return kx
		}
	}
	if !x.Seen.Equal(y.Seen) {
		return x.Seen.Before(y.Seen)
	}
	if x.Anchor != y.Anchor {
		return x.Anchor < y.Anchor
	}
	return x.Assertion < y.Assertion
}

// canonical orders claims for mapping anchors: by authority, then when first seen, then by key (G14).
func canonical(x, y claim) bool {
	if x.Authority != y.Authority {
		return x.Authority > y.Authority
	}
	if !x.Seen.Equal(y.Seen) {
		return x.Seen.Before(y.Seen)
	}
	if x.Anchor != y.Anchor {
		return x.Anchor < y.Anchor
	}
	return x.Assertion < y.Assertion
}

// resolve decides every pull request's group, in two passes: each pull request's winning claim first, then the
// anchors of the winning claims, in canonical order, so that neither the order of events nor the order of pull
// requests changes the outcome (G14).
//
// An anchor not mapped yet goes to the active group that holds the most of the pull requests whose winning claim it
// is — the oldest on a tie — among groups none of them was taken out of, and that no other winning claim keeps:
// neither a mapped anchor with winners, nor a placement, nor an anchor mapped before it in this resolution. Else it
// starts a new group. Only an anchor's own winners count, and a group another claim still holds is not up for
// taking, so claims never chain into a union (G5).
func resolve(b *board) result {
	prs := append([]int64(nil), b.PRs...)
	sort.Slice(prs, func(i, j int) bool { return prs[i] < prs[j] })
	winner := map[int64]claim{}
	for _, pr := range prs {
		var best *claim
		for _, c := range b.Claims[pr] {
			if b.excludes(pr, c) {
				continue
			}
			if best == nil || b.better(c, *best, b.Current[pr]) {
				c := c
				best = &c
			}
		}
		if best != nil {
			winner[pr] = *best
		}
	}

	res := result{Member: map[int64]placement{}, Mapped: map[string]int64{}, Lost: map[int64][]lostClaim{}}
	winners := map[string][]int64{} // anchor → its winners
	claims := map[string]claim{}    // anchor → one of its winning claims, for ordering
	kept := map[int64]bool{}        // groups another winning claim keeps
	for _, pr := range prs {
		c, ok := winner[pr]
		if !ok {
			continue
		}
		if c.Anchor == "" {
			res.Member[pr] = placement{Group: c.Group, Assertion: c.Assertion}
			kept[c.Group] = true
			continue
		}
		winners[c.Anchor] = append(winners[c.Anchor], pr)
		if old, ok := claims[c.Anchor]; !ok || canonical(c, old) {
			claims[c.Anchor] = c
		}
		if g := b.groupOf(c); g != 0 {
			kept[g] = true
		}
	}
	anchors := make([]string, 0, len(claims))
	for a := range claims {
		anchors = append(anchors, a)
	}
	sort.Slice(anchors, func(i, j int) bool { return canonical(claims[anchors[i]], claims[anchors[j]]) })
	for _, a := range anchors {
		c := claims[a]
		g := b.groupOf(c)
		if g == 0 {
			counts := map[int64]int{}
			for _, pr := range winners[a] {
				cur := b.Current[pr]
				if _, active := b.Groups[cur]; !active || kept[cur] {
					continue
				}
				excluded := false
				for _, w := range winners[a] {
					if b.Excluded[w][cur] {
						excluded = true
						break
					}
				}
				if !excluded {
					counts[cur]++
				}
			}
			for cand, n := range counts {
				if g == 0 || n > counts[g] || (n == counts[g] && (b.Groups[cand].Before(b.Groups[g]) ||
					(b.Groups[cand].Equal(b.Groups[g]) && cand < g))) {
					g = cand
				}
			}
			if g == 0 {
				res.NewGroups = append(res.NewGroups, a)
				g = -int64(len(res.NewGroups))
			}
			res.Mapped[a] = g
			kept[g] = true
		}
		for _, pr := range winners[a] {
			res.Member[pr] = placement{Group: g, Assertion: c.Assertion}
		}
	}

	for _, pr := range prs {
		final := res.Member[pr].Group
		for _, c := range b.Claims[pr] {
			if c.Authority != AuthorityStructural || c.Assertion == winner[pr].Assertion {
				continue
			}
			if g := b.groupOf(c); g != 0 && g != final && !b.excludes(pr, c) {
				res.Lost[pr] = append(res.Lost[pr], lostClaim{claim: c, Group: g})
			}
		}
	}
	return res
}
