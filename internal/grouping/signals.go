package grouping

import (
	"sort"
	"strconv"
	"strings"
)

// The kinds of structural signal, strongest first: closing the same issue beats the same branch name, which beats
// mutual references.
const (
	KindIssue  = "issue"
	KindBranch = "branch"
	KindRefs   = "refs"
)

func strength(kind string) int {
	switch kind {
	case KindIssue:
		return 0
	case KindBranch:
		return 1
	case KindRefs:
		return 2
	}
	return 3
}

// Signal is a structural co-membership: these pull requests belong together, under an anchor.
type Signal struct {
	Anchor string
	Kind   string
	PRs    []int64
}

// Weak is a weak signal, only ever a suggestion (G10): a one-way reference across repositories between pull requests
// with similar branch names.
type Weak struct {
	Reason string
	PR     int64
	Other  int64
}

// Signals derives the structural signals among open pull requests: two or more closing the same issue; the same head
// branch name in two or more repositories, by the same author, unless the name is never grouped; and references in
// both directions, connected pairs forming one signal. No signal pairs two pull requests of the same chain (G9): of
// a chain's pull requests, a signal keeps the one nearest the root.
func Signals(nodes []Node, chains map[int64]Chain, neverGrouped []string) ([]Signal, []Weak) {
	byID := map[int64]*Node{}
	byRef := map[[2]int64]int64{} // {repo, number} → pull request
	for i := range nodes {
		n := &nodes[i]
		byID[n.ID] = n
		byRef[[2]int64{n.Repo, int64(n.Number)}] = n.ID
	}
	var signals []Signal
	add := func(anchor, kind string, prs []int64) {
		prs = onePerChain(prs, chains)
		if len(prs) >= 2 {
			signals = append(signals, Signal{Anchor: anchor, Kind: kind, PRs: prs})
		}
	}

	issues := map[[2]int64][]int64{}
	for _, n := range nodes {
		for _, iss := range n.ClosesIssues {
			k := [2]int64{iss.Repo, int64(iss.Number)}
			issues[k] = append(issues[k], n.ID)
		}
	}
	for k, prs := range issues {
		add("issue:"+strconv.FormatInt(k[0], 10)+"/"+strconv.FormatInt(k[1], 10), KindIssue, prs)
	}

	branches := map[string][]int64{}
	for _, n := range nodes {
		if n.Author == 0 || matches(n.HeadBranch, neverGrouped) {
			continue
		}
		k := strconv.FormatInt(n.Author, 10) + "/" + n.HeadBranch
		branches[k] = append(branches[k], n.ID)
	}
	for k, prs := range branches {
		repos := map[int64]bool{}
		for _, p := range prs {
			repos[byID[p].Repo] = true
		}
		if len(repos) >= 2 {
			add("branch:"+k, KindBranch, prs)
		}
	}

	refs := func(n *Node) map[int64]bool {
		out := map[int64]bool{}
		for _, r := range n.References {
			if id, ok := byRef[[2]int64{r.Repo, int64(r.Number)}]; ok && id != n.ID {
				out[id] = true
			}
		}
		return out
	}
	outgoing := map[int64]map[int64]bool{}
	for i := range nodes {
		outgoing[nodes[i].ID] = refs(&nodes[i])
	}
	mutual := map[int64][]int64{}
	for a, to := range outgoing {
		for b := range to {
			if outgoing[b][a] && !sameChain(a, b, chains) {
				mutual[a] = append(mutual[a], b)
			}
		}
	}
	seen := map[int64]bool{}
	ids := sortedKeys(mutual)
	for _, start := range ids {
		if seen[start] {
			continue
		}
		var set []int64
		stack := []int64{start}
		seen[start] = true
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			set = append(set, x)
			for _, y := range mutual[x] {
				if !seen[y] {
					seen[y] = true
					stack = append(stack, y)
				}
			}
		}
		oldest := set[0]
		for _, p := range set {
			if byID[p].CreatedAt.Before(byID[oldest].CreatedAt) || (byID[p].CreatedAt.Equal(byID[oldest].CreatedAt) && p < oldest) {
				oldest = p
			}
		}
		add("refs:"+strconv.FormatInt(oldest, 10), KindRefs, set)
	}

	noise := noisyWords(nodes)
	var weak []Weak
	for _, a := range ids64(outgoing) {
		for _, b := range sortedSet(outgoing[a]) {
			na, nb := byID[a], byID[b]
			if na.Repo == nb.Repo || outgoing[b][a] || sameChain(a, b, chains) || !similar(na.HeadBranch, nb.HeadBranch, noise) {
				continue
			}
			weak = append(weak, Weak{PR: a, Other: b, Reason: "refers to it, with a similar branch name"})
		}
	}
	sort.Slice(signals, func(i, j int) bool { return signals[i].Anchor < signals[j].Anchor })
	return signals, weak
}

// onePerChain keeps, of each chain's pull requests in a set, the one nearest the root.
func onePerChain(prs []int64, chains map[int64]Chain) []int64 {
	sort.Slice(prs, func(i, j int) bool { return prs[i] < prs[j] })
	best := map[int64]int64{} // chain → pull request
	var out []int64
	for _, p := range prs {
		c := chains[p]
		if c.ID == 0 {
			out = append(out, p)
			continue
		}
		if cur, ok := best[c.ID]; !ok || c.Position < chains[cur].Position {
			best[c.ID] = p
		}
	}
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameChain(a, b int64, chains map[int64]Chain) bool {
	ca, cb := chains[a], chains[b]
	return ca.ID != 0 && ca.ID == cb.ID
}

func words(branch string) []string {
	return strings.FieldsFunc(strings.ToLower(branch), func(r rune) bool { return r == '/' || r == '-' || r == '_' || r == '.' })
}

// noisyWords are the words in more than a fifth of the open pull requests' branch names: which words are noise
// depends on the team, so it is measured.
func noisyWords(nodes []Node) map[string]bool {
	count := map[string]int{}
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, w := range words(n.HeadBranch) {
			if !seen[w] {
				seen[w] = true
				count[w]++
			}
		}
	}
	noise := map[string]bool{}
	for w, c := range count {
		if c*5 > len(nodes) {
			noise[w] = true
		}
	}
	return noise
}

// similar reports branch names that share at least two words, making up more than half of the shorter name's words,
// noise aside.
func similar(a, b string, noise map[string]bool) bool {
	keep := func(ws []string) map[string]bool {
		out := map[string]bool{}
		for _, w := range ws {
			if !noise[w] {
				out[w] = true
			}
		}
		return out
	}
	wa, wb := keep(words(a)), keep(words(b))
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	shorter := min(len(wa), len(wb))
	return shared >= 2 && shared*2 > shorter
}

func sortedKeys(m map[int64][]int64) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func ids64(m map[int64]map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedSet(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
