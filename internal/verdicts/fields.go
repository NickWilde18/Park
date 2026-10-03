package verdicts

import (
	"sort"
	"strconv"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/rules"
)

// Context is what a pull request's fields need beyond its own facts.
type Context struct {
	Now time.Time
	// Logins are accounts' logins, by account ID; an account Park has not seen is shown by its ID.
	Logins map[int64]string
	// Teams are the teams review is requested from, by ID. A team Park cannot resolve is missing, or has nil members.
	Teams map[int64]Team
	// Nudged is when each account last marked the pull request as nudged.
	Nudged map[int64]time.Time
	Repo   string
	Chain  grouping.Chain
	// GroupSize is the number of open pull requests in its group; 0 when it is in none.
	GroupSize int
	// MinimumApprovals is the team's minimum number of approvals.
	MinimumApprovals int
}

// Team is a forge team: its organisation/team slug and its members, nil when the forge would not tell.
type Team struct {
	Slug    string
	Members []int64
}

// Person is one person's role on a pull request, and what their role's rules read.
type Person struct {
	Input   rules.Input
	Role    rules.Role
	Account int64
}

// login is an account's login, or its ID when Park has not seen it.
func (c Context) login(id int64) string {
	if l, ok := c.Logins[id]; ok {
		return l
	}
	return strconv.FormatInt(id, 10)
}

// hours is whole hours from t to now.
func hours(now, t time.Time) int64 { return int64(now.Sub(t) / time.Hour) }

// People finds a pull request's people — its author, then its reviewers by login — and computes each one's fields:
// docs/design/04-verdicts.md#roles and docs/design/05-rules.md#the-field-catalog. A reviewer is anyone but the author
// with a pending review request, directly or through a team that has a member other than the author, or with a
// submitted review. Every judgement is left unknown.
func People(pr facts.PR, c Context) []Person {
	author := pr.Author
	login := c.login
	logins := func(ids map[int64]bool) []string {
		out := []string{}
		for id := range ids {
			out = append(out, login(id))
		}
		sort.Strings(out)
		return out
	}

	// Pending requests: to accounts, and to teams with a member other than the author.
	direct := map[int64]bool{}
	teamMembers := map[int64]bool{}
	var teams []string
	var oldest *time.Time
	owners := map[int64]bool{}
	var ownerTeams []string
	for _, r := range pr.ReviewRequests {
		var members []int64
		switch {
		case r.Account != 0 && r.Account != author:
			direct[r.Account] = true
			members = []int64{r.Account}
		case r.Team != 0:
			for _, m := range c.Teams[r.Team].Members {
				if m != author {
					members = append(members, m)
				}
			}
			if len(members) == 0 {
				continue
			}
			teams = append(teams, c.Teams[r.Team].Slug)
			for _, m := range members {
				teamMembers[m] = true
			}
		default:
			continue
		}
		if r.RequestedAt != nil && (oldest == nil || r.RequestedAt.Before(*oldest)) {
			oldest = r.RequestedAt
		}
		if r.CodeOwner && pr.CodeOwnersNeeded {
			for _, m := range members {
				owners[m] = true
			}
			if r.Team != 0 {
				ownerTeams = append(ownerTeams, c.Teams[r.Team].Slug)
			}
		}
	}
	sort.Strings(teams)
	sort.Strings(ownerTeams)

	// Each reviewer's reviews, and their decisive one: their latest that approves or requests changes.
	reviews := map[int64][]facts.Review{}
	for _, r := range pr.Reviews {
		if r.Author != author {
			reviews[r.Author] = append(reviews[r.Author], r)
		}
	}
	for _, rs := range reviews {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
	}
	decisive := map[int64]int{} // reviewer → index of their decisive review
	for p, rs := range reviews {
		for i, r := range rs {
			if r.State == facts.ReviewApproved || r.State == facts.ReviewChangesRequested || r.State == facts.ReviewDismissed {
				decisive[p] = i
			}
		}
	}
	decision := func(p int64) string {
		i, ok := decisive[p]
		if !ok || reviews[p][i].State == facts.ReviewDismissed {
			return "none"
		}
		return reviews[p][i].State
	}
	reviewers := map[int64]bool{}
	for _, set := range []map[int64]bool{direct, teamMembers} {
		for p := range set {
			reviewers[p] = true
		}
	}
	for p := range reviews {
		reviewers[p] = true
	}
	approved, blocked := map[int64]bool{}, map[int64]bool{}
	approvals := 0
	for p := range reviewers {
		switch decision(p) {
		case facts.ReviewApproved:
			approved[p] = true
			if reviews[p][decisive[p]].Counts {
				approvals++
			}
		case facts.ReviewChangesRequested:
			blocked[p] = true
		}
	}
	ownersPending := map[int64]bool{}
	for p := range owners {
		if !approved[p] {
			ownersPending[p] = true
		}
	}
	everyDirect := func(ps map[int64]bool) bool {
		for p := range ps {
			if !direct[p] {
				return false
			}
		}
		return true
	}

	// Written reviews nobody has answered: the author, by commenting or pushing after them, or their writer, by
	// approving after them. A bot's review waits for no answer; its comments on lines of code are threads, and count
	// there.
	answered := func(rs []facts.Review, t time.Time) bool {
		if pr.HeadPushedAt.After(t) {
			return true
		}
		for _, r := range rs {
			if r.State == facts.ReviewApproved && r.At.After(t) {
				return true
			}
		}
		for _, cm := range pr.Conversation {
			if cm.Author == author && cm.At.After(t) {
				return true
			}
		}
		for _, th := range pr.Threads {
			for _, cm := range th.Comments {
				if cm.Author == author && cm.At.After(t) {
					return true
				}
			}
		}
		return false
	}
	unanswered := int64(0)
	for _, rs := range reviews {
		for _, r := range rs {
			if r.HasBody && !r.Bot && (r.State == facts.ReviewChangesRequested || r.State == facts.ReviewCommented) && !answered(rs, r.At) {
				unanswered++
			}
		}
	}

	// Unresolved threads: who started each, who took part, and who spoke last.
	type thread struct {
		participants map[int64]bool
		starter      int64
		last         int64
	}
	var threads []thread
	for _, th := range pr.Threads {
		if th.Resolved || len(th.Comments) == 0 {
			continue
		}
		t := thread{participants: map[int64]bool{}, starter: th.Comments[0].Author, last: th.Comments[len(th.Comments)-1].Author}
		for _, cm := range th.Comments {
			t.participants[cm.Author] = true
		}
		threads = append(threads, t)
	}
	// Threads where the author spoke last wait on their other participants who have not approved.
	onReviewers, awaited := int64(0), map[int64]bool{}
	for _, t := range threads {
		if t.last != author {
			continue
		}
		waiting := false
		for p := range t.participants {
			if p != author && !approved[p] {
				awaited[p], waiting = true, true
			}
		}
		if waiting {
			onReviewers++
		}
	}

	// The required checks on the head commit.
	checks, failing := "none", []string{}
	var pendingSince *time.Time
	pending := len(pr.MissingChecks) > 0
	if pending && !pr.HeadPushedAt.IsZero() {
		at := pr.HeadPushedAt
		pendingSince = &at
	}
	anyRequired := pending
	for _, ch := range pr.Checks {
		if !ch.Required {
			continue
		}
		anyRequired = true
		switch ch.State {
		case facts.CheckFailing:
			failing = append(failing, ch.Name)
		case facts.CheckPending:
			pending = true
			if ch.Since != nil && (pendingSince == nil || ch.Since.Before(*pendingSince)) {
				pendingSince = ch.Since
			}
		}
	}
	var hoursPending any
	switch {
	case len(failing) > 0:
		checks = "failing"
	case pending:
		checks = "pending"
		if pendingSince != nil {
			hoursPending = hours(c.Now, *pendingSince)
		}
	case anyRequired:
		checks = "passing"
	}
	var hoursRequested any
	if oldest != nil {
		hoursRequested = hours(c.Now, *oldest)
	}
	position, length := int64(c.Chain.Position), int64(c.Chain.Length)
	if position == 0 {
		position, length = 1, 1
	}
	blockers := append([]string{}, pr.MergeBlockers...)
	base := map[string]any{
		"pr.repo":                 c.Repo,
		"pr.title":                pr.Title,
		"pr.labels":               append([]string{}, pr.Labels...),
		"pr.base":                 pr.BaseBranch,
		"pr.author":               login(author),
		"pr.draft":                pr.State == facts.StateDraft,
		"pr.conflicting":          pr.Mergeable == facts.MergeableConflicting,
		"pr.checks":               checks,
		"pr.failing_checks":       failing,
		"pr.hours_checks_pending": hoursPending,
		"pr.approvals":            int64(approvals),
		"pr.required_approvals":   int64(max(pr.RequiredApproval, c.MinimumApprovals)),
		"pr.blocked_by":           logins(blocked),
		"pr.blockers_rerequested": everyDirect(blocked),
		"pr.requested":            append(logins(direct), teams...),
		"pr.hours_requested":      hoursRequested,
		"pr.reviewed":             len(reviews) > 0,
		"pr.unanswered_reviews":   unanswered,
		"pr.threads_on_reviewers": onReviewers,
		"pr.awaited_reviewers":    logins(awaited),
		"pr.awaited_rerequested":  everyDirect(awaited),
		"pr.mergeable_now":        pr.CanMergeNow && pr.State != facts.StateDraft,
		"pr.merge_blocked_by":     blockers,
		"pr.code_owners_pending":  append(logins(ownersPending), ownerTeams...),
		"pr.author_can_dismiss":   pr.AuthorCanDismiss,
		"chain.position":          position,
		"chain.length":            length,
		"chain.open_below":        int64(c.Chain.Below),
		"group.size":              int64(c.GroupSize),
	}

	person := func(p int64, role rules.Role) Person {
		f := make(map[string]any, len(base)+12)
		for k, v := range base {
			f[k] = v
		}
		onMe, open := int64(0), int64(0)
		for _, t := range threads {
			if t.starter == p {
				open++
			}
			if (role == rules.Author && t.last != author) || (role == rules.Reviewer && t.participants[p] && t.last == author) {
				onMe++
			}
		}
		rs := reviews[p]
		f["me.role"] = string(role)
		f["me.threads_on_me"] = onMe
		f["me.threads_open"] = open
		f["me.requested"] = direct[p]
		f["me.code_owner"] = owners[p]
		f["me.reviewed"] = len(rs) > 0
		f["me.decision"] = decision(p)
		i, hasDecisive := decisive[p]
		f["me.reviewed_since_decision"] = hasDecisive && i < len(rs)-1
		f["me.new_commits"] = len(rs) > 0 && rs[len(rs)-1].Commit != pr.HeadSHA
		f["me.hours_since_review"] = nil
		if len(rs) > 0 {
			f["me.hours_since_review"] = hours(c.Now, rs[len(rs)-1].At)
		}
		f["me.hours_since_nudge"] = nil
		if at, ok := c.Nudged[p]; ok {
			f["me.hours_since_nudge"] = hours(c.Now, at)
		}
		return Person{Account: p, Role: role, Input: rules.Input{Fields: f}}
	}
	out := []Person{person(author, rules.Author)}
	ordered := make([]int64, 0, len(reviewers))
	for p := range reviewers {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if a, b := login(ordered[i]), login(ordered[j]); a != b {
			return a < b
		}
		return ordered[i] < ordered[j]
	})
	for _, p := range ordered {
		out = append(out, person(p, rules.Reviewer))
	}
	return out
}
