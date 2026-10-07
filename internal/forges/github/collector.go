package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
)

// collector is the GitHub collector for one installation.
type collector struct {
	api *client
	app *app // nil for a token
	// stacks is whether the forge has pull request stacks, nil until it has answered.
	stacks *bool
	mu     sync.Mutex
}

// queries gives the forms of the queries for this forge, asking it once whether it has stacks. A failed question is
// asked again next time, and fails the call meanwhile: a pull request is never read as in no stack for want of an
// answer.
func (c *collector) queries(ctx context.Context) (queries, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stacks == nil {
		var out struct {
			Type *struct {
				Name string `json:"name"`
			} `json:"__type"`
		}
		if err := c.api.query(ctx, stackProbe, map[string]any{}, &out); err != nil {
			return queries{}, err
		}
		has := out.Type != nil
		c.stacks = &has
	}
	if *c.stacks {
		return withStacks, nil
	}
	return withoutStacks, nil
}

func newCollector(f forges.Forge, c facts.Credential) (facts.Collector, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	cl := &client{http: hc, rest: APIBase(f.BaseAddress), graphql: graphQLBase(f.BaseAddress)}
	col := &collector{api: cl}
	switch c.Kind {
	case "token":
		if c.Token == "" {
			return nil, errors.New("the installation has no token")
		}
		token := c.Token
		cl.auth = func(context.Context) (string, error) { return "token " + token, nil }
	case "app":
		if c.App == nil {
			return nil, errors.New("the installation has no app credential")
		}
		a, err := newApp(cl.rest, c.App.AppID, c.App.InstallationID, c.App.PrivateKey, hc)
		if err != nil {
			return nil, err
		}
		col.app = a
		cl.auth = a.authorization
	default:
		return nil, fmt.Errorf("unknown credential kind %q", c.Kind)
	}
	return col, nil
}

func vars(r facts.Repo, number int) map[string]any {
	return map[string]any{"owner": r.Owner, "name": r.Name, "number": number}
}

func (c *collector) FetchPR(ctx context.Context, r facts.Repo, number int, protection map[string]facts.Protection) (facts.Fetched, error) {
	var out struct {
		Repository *struct {
			PullRequest *prNode `json:"pullRequest"`
			DatabaseID  int64   `json:"databaseId"`
		} `json:"repository"`
	}
	q, err := c.queries(ctx)
	if err != nil {
		return facts.Fetched{}, err
	}
	if err := c.api.query(ctx, q.pr, vars(r, number), &out); err != nil {
		return facts.Fetched{}, err
	}
	if out.Repository == nil || out.Repository.PullRequest == nil {
		return facts.Fetched{}, facts.ErrNotFound
	}
	n := out.Repository.PullRequest
	if err := c.rest(ctx, r, number, n); err != nil {
		return facts.Fetched{}, err
	}
	p, known := protection[n.BaseRefName]
	if n.Stack != nil && n.Stack.BaseRefName != "" {
		// A pull request in a stack is held to the review rules of the branch the stack merges into, not to those of
		// its own base, the branch of the pull request below; its required checks stay those of its own base.
		var s facts.Protection
		s, known = protection[n.Stack.BaseRefName]
		p.RequiredApprovals, p.CodeOwners = s.RequiredApprovals, s.CodeOwners
	}
	f := normalize(n, p, known)
	f.ForgeRepo = out.Repository.DatabaseID
	return f, nil
}

// rest pages through what did not fit in the first answer: reviews, threads, conversation comments.
func (c *collector) rest(ctx context.Context, r facts.Repo, number int, n *prNode) error {
	for next := n.Reviews.PageInfo; next.HasNextPage; {
		var out struct {
			Repository struct {
				PullRequest struct {
					Reviews struct {
						Nodes    []reviewNode `json:"nodes"`
						PageInfo page         `json:"pageInfo"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		v := vars(r, number)
		v["cursor"] = next.EndCursor
		if err := c.api.query(ctx, reviewsPage, v, &out); err != nil {
			return err
		}
		n.Reviews.Nodes = append(n.Reviews.Nodes, out.Repository.PullRequest.Reviews.Nodes...)
		next = out.Repository.PullRequest.Reviews.PageInfo
	}
	for next := n.Threads.PageInfo; next.HasNextPage; {
		var out struct {
			Repository struct {
				PullRequest struct {
					Threads struct {
						Nodes    []threadNode `json:"nodes"`
						PageInfo page         `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		v := vars(r, number)
		v["cursor"] = next.EndCursor
		if err := c.api.query(ctx, threadsPage, v, &out); err != nil {
			return err
		}
		n.Threads.Nodes = append(n.Threads.Nodes, out.Repository.PullRequest.Threads.Nodes...)
		next = out.Repository.PullRequest.Threads.PageInfo
	}
	for next := n.Comments.PageInfo; next.HasNextPage; {
		var out struct {
			Repository struct {
				PullRequest struct {
					Comments struct {
						Nodes    []commentNode `json:"nodes"`
						PageInfo page          `json:"pageInfo"`
					} `json:"comments"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		v := vars(r, number)
		v["cursor"] = next.EndCursor
		if err := c.api.query(ctx, commentsPage, v, &out); err != nil {
			return err
		}
		n.Comments.Nodes = append(n.Comments.Nodes, out.Repository.PullRequest.Comments.Nodes...)
		next = out.Repository.PullRequest.Comments.PageInfo
	}
	return nil
}

func (c *collector) ListOpen(ctx context.Context, r facts.Repo) ([]facts.Listed, error) {
	q, err := c.queries(ctx)
	if err != nil {
		return nil, err
	}
	var listed []facts.Listed
	cursor := ""
	for {
		var out struct {
			Repository *struct {
				PullRequests struct {
					Nodes []struct {
						UpdatedAt time.Time  `json:"updatedAt"`
						Stack     *stackNode `json:"stack"`
						Number    int        `json:"number"`
					} `json:"nodes"`
					PageInfo page `json:"pageInfo"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		v := map[string]any{"owner": r.Owner, "name": r.Name}
		if cursor != "" {
			v["cursor"] = cursor
		}
		if err := c.api.query(ctx, q.list, v, &out); err != nil {
			return nil, err
		}
		if out.Repository == nil {
			return nil, facts.ErrNotFound
		}
		for _, n := range out.Repository.PullRequests.Nodes {
			l := facts.Listed{Number: n.Number, UpdatedAt: n.UpdatedAt}
			if n.Stack != nil {
				l.Stack = n.Stack.Number
			}
			listed = append(listed, l)
		}
		if !out.Repository.PullRequests.PageInfo.HasNextPage {
			return listed, nil
		}
		cursor = out.Repository.PullRequests.PageInfo.EndCursor
	}
}

func (c *collector) Light(ctx context.Context, r facts.Repo, number int) (facts.Light, error) {
	var out struct {
		Repository *struct {
			PullRequest *prNode `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.api.query(ctx, lightQuery, vars(r, number), &out); err != nil {
		return facts.Light{}, err
	}
	if out.Repository == nil || out.Repository.PullRequest == nil {
		return facts.Light{}, facts.ErrNotFound
	}
	n := out.Repository.PullRequest
	cs, _ := checks(n, facts.Protection{})
	f := normalize(n, facts.Protection{}, false)
	return facts.Light{Mergeable: f.PR.Mergeable, HeadSHA: n.HeadRefOid, Checks: cs}, nil
}

func (c *collector) Repository(ctx context.Context, owner, name string) (facts.RepoFacts, error) {
	var out struct {
		Repository *struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
			DefaultBranchRef *struct {
				Name string `json:"name"`
			} `json:"defaultBranchRef"`
			Name       string `json:"name"`
			DatabaseID int64  `json:"databaseId"`
		} `json:"repository"`
	}
	if err := c.api.query(ctx, repoQuery, map[string]any{"owner": owner, "name": name}, &out); err != nil {
		return facts.RepoFacts{}, err
	}
	if out.Repository == nil {
		return facts.RepoFacts{}, facts.ErrNotFound
	}
	rf := facts.RepoFacts{Owner: out.Repository.Owner.Login, Name: out.Repository.Name, ForgeRepo: out.Repository.DatabaseID}
	if out.Repository.DefaultBranchRef != nil {
		rf.DefaultBranch = out.Repository.DefaultBranchRef.Name
	}
	return rf, nil
}

// Protection reads what GitHub requires of pull requests into each branch: the rulesets that apply to it, which
// read access can see, and the branch's classic protection. Required checks of classic protection are visible to
// read access; its required approvals need administration read access, and are otherwise unknown (-1), whether
// GitHub refuses with a 403 or hides them with a 404.
func (c *collector) Protection(ctx context.Context, r facts.Repo, branches []string) (map[string]facts.Protection, error) {
	out := map[string]facts.Protection{}
	for _, b := range branches {
		p := facts.Protection{RequiredChecks: []string{}}
		var rules []struct {
			Type       string `json:"type"`
			Parameters struct {
				RequiredStatusChecks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
				RequiredApprovals int  `json:"required_approving_review_count"`
				CodeOwnerReview   bool `json:"require_code_owner_review"`
			} `json:"parameters"`
		}
		path := fmt.Sprintf("/repos/%s/%s/rules/branches/%s", url.PathEscape(r.Owner), url.PathEscape(r.Name), url.PathEscape(b))
		if err := c.api.get(ctx, path, &rules); err != nil && !errors.Is(err, facts.ErrNotFound) {
			return nil, err
		}
		checks := map[string]bool{}
		fromRules := false
		for _, rule := range rules {
			switch rule.Type {
			case "pull_request":
				fromRules = true
				p.RequiredApprovals = max(p.RequiredApprovals, rule.Parameters.RequiredApprovals)
				p.CodeOwners = p.CodeOwners || rule.Parameters.CodeOwnerReview
			case "required_status_checks":
				for _, s := range rule.Parameters.RequiredStatusChecks {
					checks[s.Context] = true
				}
			}
		}

		var branch struct {
			Protection struct {
				RequiredStatusChecks *struct {
					Contexts []string `json:"contexts"`
				} `json:"required_status_checks"`
			} `json:"protection"`
			Protected bool `json:"protected"`
		}
		path = fmt.Sprintf("/repos/%s/%s/branches/%s", url.PathEscape(r.Owner), url.PathEscape(r.Name), url.PathEscape(b))
		err := c.api.get(ctx, path, &branch)
		switch {
		case errors.Is(err, facts.ErrNotFound):
			continue // the branch is gone
		case err != nil && !errors.Is(err, errForbidden):
			return nil, err
		}
		if branch.Protected {
			if rc := branch.Protection.RequiredStatusChecks; rc != nil {
				for _, s := range rc.Contexts {
					checks[s] = true
				}
			}
			var classic struct {
				Reviews *struct {
					Count      int  `json:"required_approving_review_count"`
					CodeOwners bool `json:"require_code_owner_reviews"`
				} `json:"required_pull_request_reviews"`
			}
			path = fmt.Sprintf("/repos/%s/%s/branches/%s/protection", url.PathEscape(r.Owner), url.PathEscape(r.Name), url.PathEscape(b))
			err := c.api.get(ctx, path, &classic)
			switch {
			case err == nil && classic.Reviews != nil:
				p.RequiredApprovals = max(p.RequiredApprovals, classic.Reviews.Count)
				p.CodeOwners = p.CodeOwners || classic.Reviews.CodeOwners
			case err == nil:
			case errors.Is(err, errForbidden), errors.Is(err, facts.ErrNotFound):
				// The branch is protected, so a 404 hides the protection from this credential rather than saying
				// there is none; GitHub answers those without administration read access that way.
				if !fromRules {
					p.RequiredApprovals = -1
				}
			default:
				return nil, err
			}
		}
		p.Protected = branch.Protected || len(rules) > 0
		for name := range checks {
			p.RequiredChecks = append(p.RequiredChecks, name)
		}
		sort.Strings(p.RequiredChecks)
		out[b] = p
	}
	return out, nil
}

func (c *collector) Access(ctx context.Context, r facts.Repo, login string) (string, error) {
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	path := fmt.Sprintf("/repos/%s/%s/collaborators/%s/permission", url.PathEscape(r.Owner), url.PathEscape(r.Name), url.PathEscape(login))
	if err := c.api.get(ctx, path, &out); err != nil {
		return "", err
	}
	switch out.RoleName {
	case "admin", "maintain", "write", "triage", "read":
		return out.RoleName, nil
	}
	return out.Permission, nil
}

func (c *collector) Team(ctx context.Context, org, slug string) ([]int64, error) {
	var members []int64
	for pageNo := 1; ; pageNo++ {
		var out []struct {
			ID int64 `json:"id"`
		}
		path := fmt.Sprintf("/orgs/%s/teams/%s/members?per_page=100&page=%d", url.PathEscape(org), url.PathEscape(slug), pageNo)
		err := c.api.get(ctx, path, &out)
		if errors.Is(err, errForbidden) || errors.Is(err, facts.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, m := range out {
			members = append(members, m.ID)
		}
		if len(out) < 100 {
			sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
			return members, nil
		}
	}
}

// Missed reads the app's delivery log for this installation's deliveries since a time. A token installation has
// no delivery log to read.
func (c *collector) Missed(ctx context.Context, since time.Time) ([]facts.Delivery, error) {
	if c.app == nil {
		return nil, facts.ErrNoDeliveryLog
	}
	asApp := &client{http: c.api.http, rest: c.api.rest, graphql: c.api.graphql, auth: c.app.appAuthorization}
	var found []facts.Delivery
	path := "/app/hook/deliveries?per_page=100"
	for {
		var list []struct {
			DeliveredAt    time.Time `json:"delivered_at"`
			GUID           string    `json:"guid"`
			Event          string    `json:"event"`
			ID             int64     `json:"id"`
			InstallationID int64     `json:"installation_id"`
		}
		next, err := asApp.getPage(ctx, path, &list)
		if err != nil {
			return nil, err
		}
		older := false
		for _, d := range list {
			if !d.DeliveredAt.After(since) {
				older = true
				continue
			}
			if d.InstallationID != c.app.installationID {
				continue
			}
			var detail struct {
				Request struct {
					Payload json.RawMessage `json:"payload"`
				} `json:"request"`
			}
			if err := asApp.get(ctx, fmt.Sprintf("/app/hook/deliveries/%d", d.ID), &detail); err != nil {
				return nil, err
			}
			del, err := concerns(d.GUID, d.Event, detail.Request.Payload)
			if err != nil {
				return nil, err
			}
			found = append(found, del)
		}
		if older || next == "" {
			return found, nil
		}
		path = next
	}
}
