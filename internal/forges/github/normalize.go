package github

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
)

// The GraphQL answer for one pull request, as the queries shape it.

type actor struct {
	Typename   string `json:"__typename"`
	Login      string `json:"login"`
	Name       string `json:"name"`
	DatabaseID int64  `json:"databaseId"`
}

type page struct {
	EndCursor   string `json:"endCursor"`
	HasNextPage bool   `json:"hasNextPage"`
}

type reviewNode struct {
	SubmittedAt *time.Time `json:"submittedAt"`
	Author      *actor     `json:"author"`
	Commit      *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	State      string `json:"state"`
	Body       string `json:"body"`
	DatabaseID int64  `json:"databaseId"`
	CanPush    bool   `json:"authorCanPushToRepository"`
}

type commentNode struct {
	CreatedAt    time.Time  `json:"createdAt"`
	LastEditedAt *time.Time `json:"lastEditedAt"`
	Author       *actor     `json:"author"`
	DatabaseID   int64      `json:"databaseId"`
}

type threadNode struct {
	ID    string `json:"id"`
	First struct {
		Nodes []commentNode `json:"nodes"`
	} `json:"first"`
	Recent struct {
		Nodes []commentNode `json:"nodes"`
	} `json:"recent"`
	IsResolved bool `json:"isResolved"`
	IsOutdated bool `json:"isOutdated"`
}

type checkNode struct {
	StartedAt  *time.Time `json:"startedAt"`
	CreatedAt  *time.Time `json:"createdAt"`
	Typename   string     `json:"__typename"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	Context    string     `json:"context"`
	State      string     `json:"state"`
	IsRequired bool       `json:"isRequired"`
}

type reviewer struct {
	Typename     string `json:"__typename"`
	Login        string `json:"login"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Organization *struct {
		Login string `json:"login"`
	} `json:"organization"`
	DatabaseID int64 `json:"databaseId"`
}

type prNode struct {
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	MergedAt       *time.Time `json:"mergedAt"`
	ClosedAt       *time.Time `json:"closedAt"`
	Author         *actor     `json:"author"`
	HeadRepository *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"headRepository"`
	Title             string `json:"title"`
	State             string `json:"state"`
	AuthorAssociation string `json:"authorAssociation"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	BaseRefName       string `json:"baseRefName"`
	Mergeable         string `json:"mergeable"`
	MergeStateStatus  string `json:"mergeStateStatus"`
	ReviewDecision    string `json:"reviewDecision"`
	BodyText          string `json:"bodyText"`
	Labels            struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	ClosingIssues struct {
		Nodes []struct {
			Repository struct {
				DatabaseID int64 `json:"databaseId"`
			} `json:"repository"`
			Number int `json:"number"`
		} `json:"nodes"`
	} `json:"closingIssuesReferences"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *reviewer `json:"requestedReviewer"`
			AsCodeOwner       bool      `json:"asCodeOwner"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Timeline struct {
		Nodes []struct {
			CreatedAt         time.Time `json:"createdAt"`
			RequestedReviewer *reviewer `json:"requestedReviewer"`
			Typename          string    `json:"__typename"`
		} `json:"nodes"`
	} `json:"timelineItems"`
	Reviews struct {
		Nodes    []reviewNode `json:"nodes"`
		PageInfo page         `json:"pageInfo"`
	} `json:"reviews"`
	Threads struct {
		Nodes    []threadNode `json:"nodes"`
		PageInfo page         `json:"pageInfo"`
	} `json:"reviewThreads"`
	Comments struct {
		Nodes    []commentNode `json:"nodes"`
		PageInfo page          `json:"pageInfo"`
	} `json:"comments"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate     time.Time `json:"committedDate"`
				OID               string    `json:"oid"`
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkNode `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Number  int  `json:"number"`
	IsDraft bool `json:"isDraft"`
	// Stack is the stack the pull request is in; asked only of a forge with stacks.
	Stack *stackNode `json:"stack"`
}

// stackNode is a pull request stack, by its number within its repository.
type stackNode struct {
	Number int `json:"number"`
}

// refTextLimit is how much of a description references are parsed from: later text is often pasted logs.
const refTextLimit = 2000

// accounts collects the display data of the accounts an answer names.
type accounts map[int64]forges.Account

func (a accounts) see(x *actor) int64 {
	if x == nil || x.DatabaseID == 0 {
		return 0
	}
	if _, ok := a[x.DatabaseID]; !ok || x.Name != "" {
		a[x.DatabaseID] = forges.Account{ID: x.DatabaseID, Login: x.Login, Name: x.Name}
	}
	return x.DatabaseID
}

// normalize turns the answer for one pull request into its facts. Protection is what the forge requires of the
// base branch, when a repository refresh could read it.
func normalize(n *prNode, protection facts.Protection, known bool) facts.Fetched {
	seen := accounts{}
	p := facts.PR{
		Number:     n.Number,
		Title:      n.Title,
		Author:     seen.see(n.Author),
		CreatedAt:  n.CreatedAt,
		UpdatedAt:  n.UpdatedAt,
		MergedAt:   n.MergedAt,
		ClosedAt:   n.ClosedAt,
		HeadBranch: n.HeadRefName,
		HeadSHA:    n.HeadRefOid,
		BaseBranch: n.BaseRefName,
		Labels:     []string{},
	}
	switch {
	case n.State == "MERGED":
		p.State = facts.StateMerged
	case n.State == "CLOSED":
		p.State = facts.StateClosed
	case n.IsDraft:
		p.State = facts.StateDraft
	default:
		p.State = facts.StateOpen
	}
	if n.HeadRepository != nil {
		p.HeadRepo = n.HeadRepository.DatabaseID
	}
	if n.Stack != nil {
		number := n.Stack.Number
		p.Stack = &number
	}
	for _, l := range n.Labels.Nodes {
		p.Labels = append(p.Labels, l.Name)
	}
	slices.Sort(p.Labels)
	if len(n.Commits.Nodes) > 0 {
		p.HeadPushedAt = n.Commits.Nodes[0].Commit.CommittedDate
	}

	requested := map[[2]int64]time.Time{} // {account, team} → latest request time
	for _, t := range n.Timeline.Nodes {
		switch t.Typename {
		case "ReadyForReviewEvent":
			at := t.CreatedAt
			p.ReadyAt = &at
		case "ReviewRequestedEvent":
			if r := t.RequestedReviewer; r != nil {
				requested[requestKey(r)] = t.CreatedAt
			}
		}
	}
	p.ReviewRequests = []facts.Request{}
	var teams []facts.Team
	for _, rr := range n.ReviewRequests.Nodes {
		r := rr.RequestedReviewer
		if r == nil || r.DatabaseID == 0 {
			continue
		}
		req := facts.Request{CodeOwner: rr.AsCodeOwner}
		if r.Typename == "Team" {
			req.Team = r.DatabaseID
			if r.Organization != nil {
				teams = append(teams, facts.Team{ID: r.DatabaseID, Org: r.Organization.Login, Slug: r.Slug, Name: r.Name})
			}
		} else {
			req.Account = seen.see(&actor{Login: r.Login, Name: r.Name, DatabaseID: r.DatabaseID})
		}
		if at, ok := requested[requestKey(r)]; ok {
			at := at
			req.RequestedAt = &at
		}
		p.ReviewRequests = append(p.ReviewRequests, req)
		if rr.AsCodeOwner {
			p.CodeOwnersNeeded = true
		}
	}

	p.Reviews = []facts.Review{}
	approvals := 0
	latest := map[int64]string{}
	for _, r := range n.Reviews.Nodes {
		if r.SubmittedAt == nil || r.State == "PENDING" {
			continue
		}
		rv := facts.Review{ID: r.DatabaseID, Author: seen.see(r.Author), At: *r.SubmittedAt,
			HasBody: strings.TrimSpace(r.Body) != "", Counts: r.CanPush, Bot: r.Author != nil && r.Author.Typename == "Bot"}
		if r.Commit != nil {
			rv.Commit = r.Commit.OID
		}
		switch r.State {
		case "APPROVED":
			rv.State = facts.ReviewApproved
		case "CHANGES_REQUESTED":
			rv.State = facts.ReviewChangesRequested
		case "DISMISSED":
			rv.State = facts.ReviewDismissed
		default:
			rv.State = facts.ReviewCommented
		}
		if rv.State != facts.ReviewCommented {
			latest[rv.Author] = rv.State
		}
		p.Reviews = append(p.Reviews, rv)
	}
	counts := map[int64]bool{}
	for _, r := range p.Reviews {
		counts[r.Author] = r.Counts
	}
	for who, state := range latest {
		if state == facts.ReviewApproved && counts[who] {
			approvals++
		}
	}
	sort.SliceStable(p.Reviews, func(i, j int) bool { return p.Reviews[i].At.Before(p.Reviews[j].At) })

	p.Threads = []facts.Thread{}
	for _, t := range n.Threads.Nodes {
		th := facts.Thread{ID: t.ID, Resolved: t.IsResolved, Outdated: t.IsOutdated, Comments: []facts.Comment{}}
		ids := map[int64]bool{}
		for _, c := range append(append([]commentNode{}, t.First.Nodes...), t.Recent.Nodes...) {
			if ids[c.DatabaseID] {
				continue
			}
			ids[c.DatabaseID] = true
			th.Comments = append(th.Comments, comment(c, seen))
		}
		sort.SliceStable(th.Comments, func(i, j int) bool { return th.Comments[i].At.Before(th.Comments[j].At) })
		p.Threads = append(p.Threads, th)
	}
	p.Conversation = []facts.Comment{}
	for _, c := range n.Comments.Nodes {
		p.Conversation = append(p.Conversation, comment(c, seen))
	}

	p.Checks, p.MissingChecks = checks(n, protection)
	switch n.Mergeable {
	case "MERGEABLE":
		p.Mergeable = facts.MergeableClean
	case "CONFLICTING":
		p.Mergeable = facts.MergeableConflicting
	default:
		p.Mergeable = facts.MergeableUnknown
	}
	if known && protection.RequiredApprovals >= 0 {
		p.RequiredApproval = protection.RequiredApprovals
	} else {
		p.RequiredApproval = inferRequired(n.ReviewDecision, approvals)
	}
	p.CodeOwnersNeeded = p.CodeOwnersNeeded || (known && protection.CodeOwners)
	p.CanMergeNow, p.MergeBlockers = verdict(n, &p)

	for _, c := range n.ClosingIssues.Nodes {
		p.ClosesIssues = append(p.ClosesIssues, facts.Ref{Repo: c.Repository.DatabaseID, Number: c.Number})
	}
	if p.ClosesIssues == nil {
		p.ClosesIssues = []facts.Ref{}
	}
	p.References = []facts.Ref{}

	out := facts.Fetched{PR: p, RefText: n.Title + "\n" + truncate(n.BodyText, refTextLimit), Association: n.AuthorAssociation,
		Teams: teams}
	for _, a := range seen {
		out.Accounts = append(out.Accounts, a)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].ID < out.Accounts[j].ID })
	if n.Author != nil {
		out.AuthorLogin = n.Author.Login
	}
	return out
}

func requestKey(r *reviewer) [2]int64 {
	if r.Typename == "Team" {
		return [2]int64{0, r.DatabaseID}
	}
	return [2]int64{r.DatabaseID, 0}
}

func comment(c commentNode, seen accounts) facts.Comment {
	return facts.Comment{ID: c.DatabaseID, Author: seen.see(c.Author), At: c.CreatedAt, EditedAt: c.LastEditedAt}
}

func truncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

// inferRequired is the number of approvals the forge requires, when its protection could not be read: the review
// decision says whether what it has is enough.
func inferRequired(decision string, approvals int) int {
	switch decision {
	case "REVIEW_REQUIRED":
		return approvals + 1
	case "APPROVED":
		return approvals
	case "CHANGES_REQUESTED":
		return max(approvals, 1)
	}
	return 0
}

// checks normalizes the head commit's checks. A check the forge requires that has not reported yet is missing,
// which counts as pending; a rerun check keeps only its latest run.
func checks(n *prNode, protection facts.Protection) ([]facts.Check, []string) {
	required := map[string]bool{}
	for _, name := range protection.RequiredChecks {
		required[name] = true
	}
	byName := map[string]facts.Check{}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		for _, c := range n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
			ch := check(c)
			ch.Required = ch.Required || required[ch.Name]
			if old, ok := byName[ch.Name]; ok && old.Since != nil && ch.Since != nil && old.Since.After(*ch.Since) {
				continue
			}
			byName[ch.Name] = ch
		}
	}
	out := make([]facts.Check, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	missing := []string{}
	for _, name := range protection.RequiredChecks {
		if _, ok := byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return out, missing
}

func check(c checkNode) facts.Check {
	if c.Typename == "StatusContext" {
		ch := facts.Check{Name: c.Context, Required: c.IsRequired, Since: c.CreatedAt}
		switch c.State {
		case "SUCCESS":
			ch.State = facts.CheckPassing
		case "ERROR", "FAILURE":
			ch.State = facts.CheckFailing
		default:
			ch.State = facts.CheckPending
		}
		return ch
	}
	ch := facts.Check{Name: c.Name, Required: c.IsRequired, Since: c.StartedAt}
	switch {
	case c.Status != "COMPLETED":
		ch.State = facts.CheckPending
	case c.Conclusion == "SUCCESS" || c.Conclusion == "NEUTRAL" || c.Conclusion == "SKIPPED":
		ch.State = facts.CheckPassing
	default:
		ch.State = facts.CheckFailing
	}
	return ch
}

// verdict is the forge's own merge verdict: whether the pull request can be merged now, and what blocks it —
// reviews, code_owners, checks, behind, conflicts, or other.
func verdict(n *prNode, p *facts.PR) (bool, []string) {
	blockers := []string{}
	switch n.MergeStateStatus {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
		return p.Open() && p.State != facts.StateDraft, blockers
	case "DIRTY":
		return false, append(blockers, "conflicts")
	case "BEHIND":
		return false, append(blockers, "behind")
	case "BLOCKED":
		if n.ReviewDecision == "REVIEW_REQUIRED" || n.ReviewDecision == "CHANGES_REQUESTED" {
			blockers = append(blockers, "reviews")
		}
		for _, r := range p.ReviewRequests {
			if r.CodeOwner {
				blockers = append(blockers, "code_owners")
				break
			}
		}
		for _, c := range p.Checks {
			if c.Required && c.State != facts.CheckPassing {
				blockers = append(blockers, "checks")
				break
			}
		}
		if len(p.MissingChecks) > 0 && !slices.Contains(blockers, "checks") {
			blockers = append(blockers, "checks")
		}
		if len(blockers) == 0 {
			blockers = append(blockers, "other")
		}
		return false, blockers
	}
	return false, blockers
}
