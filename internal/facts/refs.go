package facts

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// refPattern finds #N, name#N, and owner/name#N. What precedes a match is checked separately: a bare #N counts
// only after a space, punctuation, or the start, so that a URL fragment such as page#12 is no reference.
var refPattern = regexp.MustCompile(`(?:([A-Za-z0-9_.-]+)/)?([A-Za-z0-9_.-]+)?#(\d{1,7})\b`)

// WatchedRepo is a watched repository, for resolving references.
type WatchedRepo struct {
	Owner string
	Name  string
	ID    int64
}

// ParseRefs finds the pull requests a text refers to, among the watched repositories of the same forge. A bare #N
// is in the same repository, never another; name#N or owner/name#N counts only when it names exactly one watched
// repository. The pull request's own number is not a reference.
func ParseRefs(text string, self WatchedRepo, number int, watched []WatchedRepo) []Ref {
	seen := map[Ref]bool{}
	out := []Ref{}
	for _, m := range refPattern.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > 0 && strings.ContainsAny(text[m[0]-1:m[0]], "/&#@") {
			continue
		}
		owner, name := part(text, m, 2), part(text, m, 4)
		n, err := strconv.Atoi(text[m[6]:m[7]])
		if err != nil {
			continue
		}
		repo, ok := self.ID, true
		switch {
		case name == "" && owner == "":
		case name == "":
			ok = false
		default:
			repo, ok = resolve(owner, name, watched)
		}
		r := Ref{Repo: repo, Number: n}
		if !ok || (repo == self.ID && n == number) || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Number < out[j].Number
	})
	return out
}

func part(text string, m []int, i int) string {
	if m[i] < 0 {
		return ""
	}
	return text[m[i]:m[i+1]]
}

// resolve names exactly one watched repository, by owner and name or by name alone.
func resolve(owner, name string, watched []WatchedRepo) (int64, bool) {
	var found []int64
	for _, w := range watched {
		if strings.EqualFold(w.Name, name) && (owner == "" || strings.EqualFold(w.Owner, owner)) {
			found = append(found, w.ID)
		}
	}
	if len(found) != 1 {
		return 0, false
	}
	return found[0], true
}
