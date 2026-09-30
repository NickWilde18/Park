package grouping

import (
	"path"
	"sort"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
)

// Node is an open pull request, as chains and structural signals see it.
type Node struct {
	CreatedAt    time.Time
	Title        string
	HeadBranch   string
	BaseBranch   string
	ClosesIssues []facts.Ref
	References   []facts.Ref
	// ID is Park's ID of the pull request, and Repo of its repository.
	ID     int64
	Repo   int64
	Author int64
	// HeadRepo and RepoForge are the forge's IDs of the head's repository and of the pull request's own.
	HeadRepo  int64
	RepoForge int64
	Number    int
}

// RepoInfo is what decides a repository's long-lived branches.
type RepoInfo struct {
	Protected     map[string]bool
	DefaultBranch string
}

// Chain is where a pull request sits in its chain. A pull request in no chain has position 1 and length 1.
type Chain struct {
	Parents []int64
	// ID names the chain: its lowest pull request ID; 0 for a pull request in no chain.
	ID       int64
	Position int
	Length   int
	// Below counts the open pull requests it stacks on, directly or not.
	Below int
}

// matches reports whether a branch name matches one of the patterns, such as release/*.
func matches(branch string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok || p == branch {
			return true
		}
	}
	return false
}

// Chains derives the chains of open pull requests (G1, G15). X depends on Y when X's base branch is Y's head branch,
// in the same repository, Y's head is in that repository — not a fork — and Y's head branch is not long-lived: the
// default branch, a protected branch, or a long-lived name. X depends on every open pull request with that head,
// as a backport's does. A connected set of such edges is one chain, a tree when two share a parent; a set with a
// cycle is no chain until the cycle resolves.
func Chains(nodes []Node, repos map[int64]RepoInfo, longLived []string) map[int64]Chain {
	byID := map[int64]*Node{}
	heads := map[[2]any][]int64{} // {repo, head branch} → pull requests
	for i := range nodes {
		n := &nodes[i]
		byID[n.ID] = n
		info := repos[n.Repo]
		if n.HeadRepo != n.RepoForge || n.HeadBranch == info.DefaultBranch || info.Protected[n.HeadBranch] ||
			matches(n.HeadBranch, longLived) {
			continue
		}
		k := [2]any{n.Repo, n.HeadBranch}
		heads[k] = append(heads[k], n.ID)
	}
	parents := map[int64][]int64{}
	adjacent := map[int64][]int64{}
	for i := range nodes {
		n := &nodes[i]
		for _, p := range heads[[2]any{n.Repo, n.BaseBranch}] {
			if p == n.ID {
				continue
			}
			parents[n.ID] = append(parents[n.ID], p)
			adjacent[n.ID] = append(adjacent[n.ID], p)
			adjacent[p] = append(adjacent[p], n.ID)
		}
	}
	out := map[int64]Chain{}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, start := range ids {
		if seen[start] {
			continue
		}
		var members []int64
		stack := []int64{start}
		seen[start] = true
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			members = append(members, x)
			for _, y := range adjacent[x] {
				if !seen[y] {
					seen[y] = true
					stack = append(stack, y)
				}
			}
		}
		if len(members) < 2 || cyclic(members, parents) {
			for _, m := range members {
				out[m] = Chain{Position: 1, Length: 1}
			}
			continue
		}
		id := members[0]
		for _, m := range members {
			id = min(id, m)
		}
		position := map[int64]int{}
		var pos func(int64) int
		pos = func(x int64) int {
			if p, ok := position[x]; ok {
				return p
			}
			p := 1
			for _, y := range parents[x] {
				p = max(p, pos(y)+1)
			}
			position[x] = p
			return p
		}
		for _, m := range members {
			ps := append([]int64(nil), parents[m]...)
			sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
			out[m] = Chain{ID: id, Position: pos(m), Length: len(members), Below: len(ancestors(m, parents)), Parents: ps}
		}
	}
	return out
}

func ancestors(x int64, parents map[int64][]int64) map[int64]bool {
	found := map[int64]bool{}
	stack := append([]int64(nil), parents[x]...)
	for len(stack) > 0 {
		y := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if found[y] {
			continue
		}
		found[y] = true
		stack = append(stack, parents[y]...)
	}
	return found
}

// cyclic reports whether the dependency edges among members form a cycle.
func cyclic(members []int64, parents map[int64][]int64) bool {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[int64]int{}
	var visit func(int64) bool
	visit = func(x int64) bool {
		switch state[x] {
		case visiting:
			return true
		case done:
			return false
		}
		state[x] = visiting
		for _, y := range parents[x] {
			if visit(y) {
				return true
			}
		}
		state[x] = done
		return false
	}
	for _, m := range members {
		if visit(m) {
			return true
		}
	}
	return false
}
