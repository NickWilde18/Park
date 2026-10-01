package github

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/facts"
	"github.com/NickWilde18/Park/internal/forges"
)

// fakeAPI is GitHub's API at an Enterprise Server's address: GraphQL answers chosen by a piece of text only their
// query contains, and REST answers by path.
type fakeAPI struct {
	srv     *httptest.Server
	graphql map[string]string // a query's distinguishing text → answer
	rest    map[string]fakeREST
	seen    []string
	auth    []string
	mu      sync.Mutex
}

type fakeREST struct {
	header http.Header
	body   string
	status int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{graphql: map[string]string{}, rest: map[string]fakeREST{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/api/graphql" {
			var req struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			for key, answer := range f.graphql {
				if strings.Contains(req.Query, key) {
					f.seen = append(f.seen, key)
					_, _ = io.WriteString(w, answer)
					return
				}
			}
			http.Error(w, "no answer for this query", http.StatusTeapot)
			return
		}
		path := strings.TrimPrefix(r.URL.RequestURI(), "/api/v3")
		ans, ok := f.rest[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.seen = append(f.seen, path)
		for k, vs := range ans.header {
			w.Header()[k] = vs
		}
		if ans.status != 0 {
			w.WriteHeader(ans.status)
		}
		_, _ = io.WriteString(w, ans.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tokenCollector(t *testing.T, f *fakeAPI) *collector {
	t.Helper()
	c, err := newCollector(forges.Forge{Kind: Kind, BaseAddress: f.srv.URL}, facts.Credential{Kind: "token", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*collector)
}

var repo = facts.Repo{Owner: "acme", Name: "api", ForgeRepo: 1001}

// probeKey picks out the collector's question whether the forge has stacks, and stacksSupported answers it as
// github.com does.
const (
	probeKey        = `__type(name: "PullRequestStack")`
	stacksSupported = `{"data": {"__type": {"name": "PullRequestStack"}}}`
)

// A pull request's state is normalized from GitHub's answer, with every page of its threads. The fork's head
// repository is kept; a required check that has not reported is missing; reviews keep whether they had a body; the
// author's pending review, and all text and addresses, are dropped. The forge has stacks, so the query asks for the
// pull request's, and the state keeps its number.
func TestFetchPR(t *testing.T) {
	f := newFakeAPI(t)
	f.graphql[probeKey] = stacksSupported
	f.graphql["baseRefName stack { number }"] = fixture(t, "pr.json")
	f.graphql["reviewThreads(first: 100, after: $cursor)"] = fixture(t, "threads_page2.json")
	c := tokenCollector(t, f)
	got, err := c.FetchPR(context.Background(), repo, 12, map[string]facts.Protection{
		"main": {RequiredChecks: []string{"build", "e2e"}, RequiredApprovals: 2, CodeOwners: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := got.PR
	if p.Number != 12 || p.State != facts.StateOpen || p.Author != 501 || p.HeadRepo != 2002 || got.ForgeRepo != 1001 ||
		p.HeadSHA != "headsha12" || p.BaseBranch != "main" || p.ReadyAt == nil || !slices.Equal(p.Labels, []string{"billing", "feature"}) ||
		p.Stack == nil || *p.Stack != 40 {
		t.Fatalf("identity, branches, and stack: %+v", p)
	}
	if len(p.Threads) != 2 || p.Threads[1].ID != "THREAD_B" || len(p.Threads[0].Comments) != 2 || p.Threads[0].Comments[1].EditedAt == nil {
		t.Fatalf("threads: %+v", p.Threads)
	}
	if len(p.Reviews) != 4 {
		t.Fatalf("reviews: %+v", p.Reviews)
	}
	byID := map[int64]facts.Review{}
	for _, r := range p.Reviews {
		byID[r.ID] = r
	}
	if byID[9001].HasBody || !byID[9002].HasBody || byID[9002].State != facts.ReviewChangesRequested || !byID[9003].HasBody ||
		byID[9003].Counts || byID[9004].State != facts.ReviewDismissed {
		t.Fatalf("reviews: %+v", byID)
	}
	if !slices.Equal(p.MissingChecks, []string{"e2e"}) || len(p.Checks) != 3 {
		t.Fatalf("checks: %+v missing %v", p.Checks, p.MissingChecks)
	}
	for _, ch := range p.Checks {
		want := map[string]string{"build": facts.CheckPassing, "lint": facts.CheckFailing, "deploy/preview": facts.CheckPending}[ch.Name]
		if ch.State != want || (ch.Name == "build") != ch.Required {
			t.Errorf("check %+v", ch)
		}
	}
	if p.RequiredApproval != 2 || !p.CodeOwnersNeeded || p.CanMergeNow || !slices.Equal(p.MergeBlockers, []string{"reviews", "code_owners", "checks"}) {
		t.Fatalf("merge: required %d, code owners %v, can merge %v, blockers %v", p.RequiredApproval, p.CodeOwnersNeeded, p.CanMergeNow, p.MergeBlockers)
	}
	if len(p.ReviewRequests) != 2 || p.ReviewRequests[0].Account != 503 || p.ReviewRequests[0].RequestedAt == nil ||
		p.ReviewRequests[1].Team != 77 || !p.ReviewRequests[1].CodeOwner {
		t.Fatalf("requests: %+v", p.ReviewRequests)
	}
	if len(got.Teams) != 1 || got.Teams[0] != (facts.Team{ID: 77, Org: "acme", Slug: "billing", Name: "Billing"}) {
		t.Fatalf("teams %+v", got.Teams)
	}
	if len(p.ClosesIssues) != 1 || p.ClosesIssues[0].Number != 40 || len(p.Conversation) != 1 || p.Conversation[0].Author != 900 {
		t.Fatalf("issues %v, conversation %v", p.ClosesIssues, p.Conversation)
	}
	if !strings.Contains(got.RefText, "web#7") || got.AuthorLogin != "ana" || got.Association != "CONTRIBUTOR" {
		t.Fatalf("ref text %q, login %q, association %q", got.RefText, got.AuthorLogin, got.Association)
	}
	stored, _ := json.Marshal(p)
	for _, text := range []string{"rename", "drive-by", "Exports invoices", "http", "example.test"} {
		if strings.Contains(string(stored), text) {
			t.Errorf("the stored state holds %q", text)
		}
	}
	logins := map[int64]string{}
	for _, a := range got.Accounts {
		logins[a.ID] = a.Login
	}
	if logins[501] != "ana" || logins[502] != "ben" || logins[900] != "ci-bot" {
		t.Fatalf("accounts %v", logins)
	}
}

// Without readable protection, the required approvals come from the review decision.
func TestInferRequired(t *testing.T) {
	for _, c := range []struct {
		decision  string
		approvals int
		want      int
	}{{"REVIEW_REQUIRED", 1, 2}, {"REVIEW_REQUIRED", 0, 1}, {"APPROVED", 2, 2}, {"CHANGES_REQUESTED", 0, 1}, {"", 3, 0}} {
		if got := inferRequired(c.decision, c.approvals); got != c.want {
			t.Errorf("%s with %d: %d, want %d", c.decision, c.approvals, got, c.want)
		}
	}
}

// Protection comes from the rulesets for a branch and its classic protection. When the classic protection's
// reviews cannot be read and no ruleset says, the required approvals are unknown.
func TestProtection(t *testing.T) {
	f := newFakeAPI(t)
	f.rest["/repos/acme/api/rules/branches/main"] = fakeREST{body: `[
		{"type": "pull_request", "parameters": {"required_approving_review_count": 2, "require_code_owner_review": true}},
		{"type": "required_status_checks", "parameters": {"required_status_checks": [{"context": "build"}]}}]`}
	f.rest["/repos/acme/api/branches/main"] = fakeREST{body: `{"protected": false}`}
	f.rest["/repos/acme/api/rules/branches/release"] = fakeREST{body: `[]`}
	f.rest["/repos/acme/api/branches/release"] = fakeREST{body: `{"protected": true, "protection": {"required_status_checks": {"contexts": ["e2e"]}}}`}
	f.rest["/repos/acme/api/branches/release/protection"] = fakeREST{status: http.StatusForbidden, body: `{}`}
	c := tokenCollector(t, f)
	got, err := c.Protection(context.Background(), repo, []string{"main", "release", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if m := got["main"]; m.RequiredApprovals != 2 || !m.CodeOwners || !slices.Equal(m.RequiredChecks, []string{"build"}) || !m.Protected {
		t.Errorf("main: %+v", m)
	}
	if r := got["release"]; r.RequiredApprovals != -1 || !slices.Equal(r.RequiredChecks, []string{"e2e"}) || !r.Protected {
		t.Errorf("release: %+v", r)
	}
	if _, ok := got["gone"]; ok {
		t.Error("a missing branch has protection")
	}
}

// A delivery counts only with a matching signature, and says only what it concerns.
func TestDelivery(t *testing.T) {
	c := &collector{}
	body := []byte(`{"action": "submitted", "pull_request": {"number": 12, "body": "text"}, "repository": {"id": 1001}}`)
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	mac.Write(body)
	h := http.Header{}
	h.Set("X-GitHub-Event", "pull_request_review")
	h.Set("X-GitHub-Delivery", "d-1")
	h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	d, err := c.Delivery(h, body, []byte("hook-secret"))
	if err != nil || d.ID != "d-1" || d.ForgeRepo != 1001 || !slices.Equal(d.PRs, []int{12}) {
		t.Fatalf("got %+v, %v", d, err)
	}
	for name, secret := range map[string][]byte{"another secret": []byte("other"), "no secret": nil} {
		if _, err := c.Delivery(h, body, secret); !errors.Is(err, facts.ErrBadSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h.Set("X-Hub-Signature-256", "sha1=abc")
	if _, err := c.Delivery(h, body, []byte("hook-secret")); !errors.Is(err, facts.ErrBadSignature) {
		t.Errorf("a sha1 signature: %v", err)
	}
}

// Each event names what to refresh.
func TestConcerns(t *testing.T) {
	for _, c := range []struct {
		event, body string
		want        facts.Delivery
	}{
		{"pull_request", `{"pull_request": {"number": 3}, "repository": {"id": 9}}`, facts.Delivery{PRs: []int{3}, ForgeRepo: 9}},
		{"issue_comment", `{"issue": {"number": 4, "pull_request": {}}, "repository": {"id": 9}}`, facts.Delivery{PRs: []int{4}, ForgeRepo: 9}},
		{"issue_comment", `{"issue": {"number": 5}, "repository": {"id": 9}}`, facts.Delivery{ForgeRepo: 9}},
		{"check_run", `{"check_run": {"head_sha": "abc"}, "repository": {"id": 9}}`, facts.Delivery{SHAs: []string{"abc"}, ForgeRepo: 9}},
		{"check_suite", `{"check_suite": {"head_sha": "abc"}, "repository": {"id": 9}}`, facts.Delivery{SHAs: []string{"abc"}, ForgeRepo: 9}},
		{"status", `{"sha": "def", "repository": {"id": 9}}`, facts.Delivery{SHAs: []string{"def"}, ForgeRepo: 9}},
		{"push", `{"ref": "refs/heads/feat/x", "repository": {"id": 9}}`, facts.Delivery{Pushed: "feat/x", ForgeRepo: 9}},
		{"push", `{"ref": "refs/tags/v1", "repository": {"id": 9}}`, facts.Delivery{ForgeRepo: 9}},
		{"repository_ruleset", `{"repository": {"id": 9}}`, facts.Delivery{RepoChanged: true, ForgeRepo: 9}},
		{"membership", `{"team": {"id": 77}}`, facts.Delivery{Teams: []int64{77}}},
		{"star", `{"repository": {"id": 9}}`, facts.Delivery{ForgeRepo: 9}},
	} {
		got, err := concerns("d", c.event, []byte(c.body))
		c.want.ID, c.want.Event = "d", c.event
		if err != nil || !deliveryEqual(got, c.want) {
			t.Errorf("%s %s: got %+v, %v", c.event, c.body, got, err)
		}
	}
}

func deliveryEqual(a, b facts.Delivery) bool {
	return a.ID == b.ID && a.Event == b.Event && a.ForgeRepo == b.ForgeRepo && slices.Equal(a.PRs, b.PRs) &&
		slices.Equal(a.SHAs, b.SHAs) && a.Pushed == b.Pushed && a.RepoChanged == b.RepoChanged && slices.Equal(a.Teams, b.Teams)
}

// A rate limit becomes RateLimited, with the time it resets.
func TestRateLimited(t *testing.T) {
	f := newFakeAPI(t)
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	f.rest["/repos/acme/api/collaborators/ana/permission"] = fakeREST{status: http.StatusForbidden, body: `{}`,
		header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}}
	_, err := tokenCollector(t, f).Access(context.Background(), repo, "ana")
	var rl *facts.RateLimited
	if !errors.As(err, &rl) || !rl.Until.Equal(reset) {
		t.Fatalf("got %v", err)
	}
}

// An app installation signs a JWT with its key to buy an installation token, which it then reuses.
func TestAppAuth(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	f := newFakeAPI(t)
	var jwts []string
	f.srv.Config.Handler = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v3/app/installations/55/access_tokens" {
				jwts = append(jwts, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"token": "installation-token", "expires_at": "`+time.Now().Add(time.Hour).Format(time.RFC3339)+`"}`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}(f.srv.Config.Handler)
	f.rest["/repos/acme/api/collaborators/ana/permission"] = fakeREST{body: `{"permission": "write", "role_name": "write"}`}
	c, err := newCollector(forges.Forge{Kind: Kind, BaseAddress: f.srv.URL},
		facts.Credential{Kind: "app", App: &facts.AppCredential{AppID: 7, InstallationID: 55, PrivateKey: pemKey}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if level, err := c.Access(context.Background(), repo, "ana"); err != nil || level != "write" {
			t.Fatalf("access %q, %v", level, err)
		}
	}
	if len(jwts) != 1 {
		t.Fatalf("%d installation tokens bought, want 1", len(jwts))
	}
	parts := strings.Split(jwts[0], ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil || !strings.Contains(string(claims), `"iss":"7"`) {
		t.Fatalf("JWT %s: %v", claims, err)
	}
	if last := f.auth[len(f.auth)-1]; last != "token installation-token" {
		t.Fatalf("called with %q", last)
	}
}

// Missed deliveries are read from the app's delivery log, page by page, for this installation and since a time.
func TestMissed(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	f := newFakeAPI(t)
	since := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	f.rest["/app/hook/deliveries?per_page=100"] = fakeREST{
		header: http.Header{"Link": {`<` + f.srv.URL + `/api/v3/app/hook/deliveries?per_page=100&cursor=v1_2>; rel="next"`}},
		body: `[{"id": 3, "guid": "g-3", "event": "pull_request", "delivered_at": "2026-09-01T14:00:00Z", "installation_id": 55},
			{"id": 2, "guid": "g-2", "event": "pull_request", "delivered_at": "2026-09-01T13:00:00Z", "installation_id": 66}]`}
	f.rest["/app/hook/deliveries?per_page=100&cursor=v1_2"] = fakeREST{
		body: `[{"id": 1, "guid": "g-1", "event": "push", "delivered_at": "2026-09-01T11:00:00Z", "installation_id": 55}]`}
	f.rest["/app/hook/deliveries/3"] = fakeREST{body: `{"request": {"payload": {"pull_request": {"number": 12}, "repository": {"id": 1001}}}}`}
	c, err := newCollector(forges.Forge{Kind: Kind, BaseAddress: f.srv.URL},
		facts.Credential{Kind: "app", App: &facts.AppCredential{AppID: 7, InstallationID: 55, PrivateKey: pemKey}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Missed(context.Background(), since)
	if err != nil || len(got) != 1 || got[0].ID != "g-3" || !slices.Equal(got[0].PRs, []int{12}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := tokenCollector(t, f).Missed(context.Background(), since); !errors.Is(err, facts.ErrNoDeliveryLog) {
		t.Fatalf("a token installation: %v", err)
	}
}

// The listing of open pull requests follows every page, with each pull request's stack: 0 for one in none.
func TestListOpen(t *testing.T) {
	f := newFakeAPI(t)
	f.graphql[probeKey] = stacksSupported
	f.graphql["updatedAt stack { number }"] = `{"data": {"repository": {"pullRequests": {"pageInfo": {"hasNextPage": false},
		"nodes": [{"number": 12, "updatedAt": "2026-09-03T12:00:00Z", "stack": {"number": 40}},
			{"number": 13, "updatedAt": "2026-09-04T12:00:00Z", "stack": null}]}}}}`
	got, err := tokenCollector(t, f).ListOpen(context.Background(), repo)
	if err != nil || len(got) != 2 || got[0].Stack != 40 || got[1].Number != 13 || got[1].Stack != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A forge whose schema has no stacks, as an Enterprise Server's may not, is never asked for one: a query that names
// the field fails as a whole there, as this fake answers it. Its pull requests are in no stack, and the schema is
// asked once for every call after.
func TestNoStacks(t *testing.T) {
	f := newFakeAPI(t)
	f.graphql[probeKey] = `{"data": {"__type": null}}`
	f.graphql["stack { number }"] = `{"errors": [{"message": "Field 'stack' doesn't exist on type 'PullRequest'"}]}`
	f.graphql["baseRefName\n"] = strings.Replace(fixture(t, "pr.json"), `"stack": {"number": 40}, `, "", 1)
	f.graphql["reviewThreads(first: 100, after: $cursor)"] = fixture(t, "threads_page2.json")
	f.graphql["nodes { number updatedAt }"] = `{"data": {"repository": {"pullRequests": {"pageInfo": {"hasNextPage": false},
		"nodes": [{"number": 12, "updatedAt": "2026-09-03T12:00:00Z"}]}}}}`
	c := tokenCollector(t, f)
	got, err := c.FetchPR(context.Background(), repo, 12, nil)
	if err != nil || got.PR.Stack != nil || got.PR.Number != 12 {
		t.Fatalf("fetched %+v, %v", got.PR, err)
	}
	listed, err := c.ListOpen(context.Background(), repo)
	if err != nil || len(listed) != 1 || listed[0].Stack != 0 {
		t.Fatalf("listed %+v, %v", listed, err)
	}
	if n := strings.Count(strings.Join(f.seen, "\n"), probeKey); n != 1 {
		t.Fatalf("the schema was asked %d times, want 1", n)
	}
}

// A question about stacks that fails fails the call, so that no pull request is read as in no stack for want of an
// answer, though the forge would answer the listing without stacks; the next call asks again.
func TestStackProbeFails(t *testing.T) {
	f := newFakeAPI(t)
	f.graphql["updatedAt stack { number }"] = `{"data": {"repository": {"pullRequests": {"pageInfo": {"hasNextPage": false},
		"nodes": [{"number": 12, "updatedAt": "2026-09-03T12:00:00Z", "stack": {"number": 40}}]}}}}`
	f.graphql["nodes { number updatedAt }"] = `{"data": {"repository": {"pullRequests": {"pageInfo": {"hasNextPage": false},
		"nodes": [{"number": 12, "updatedAt": "2026-09-03T12:00:00Z"}]}}}}`
	c := tokenCollector(t, f)
	if got, err := c.ListOpen(context.Background(), repo); err == nil {
		t.Fatalf("listed without knowing whether the forge has stacks: %+v", got)
	}
	f.mu.Lock()
	f.graphql[probeKey] = stacksSupported
	f.mu.Unlock()
	got, err := c.ListOpen(context.Background(), repo)
	if err != nil || len(got) != 1 || got[0].Stack != 40 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// GitHub hides a protected branch's protection from those who may not read it with a 404, not a 403. The branch is
// known to be protected, so its required approvals are unknown then, not zero.
func TestProtectionHiddenBehindNotFound(t *testing.T) {
	f := newFakeAPI(t)
	f.rest["/repos/acme/api/rules/branches/trunk"] = fakeREST{body: `[{"type": "copilot_code_review", "parameters": {}}]`}
	f.rest["/repos/acme/api/branches/trunk"] = fakeREST{body: `{"protected": true, "protection": {"required_status_checks": {"contexts": ["build"]}}}`}
	f.rest["/repos/acme/api/branches/trunk/protection"] = fakeREST{status: http.StatusNotFound, body: `{"message": "Not Found"}`}
	got, err := tokenCollector(t, f).Protection(context.Background(), repo, []string{"trunk"})
	if err != nil {
		t.Fatal(err)
	}
	if p := got["trunk"]; p.RequiredApprovals != -1 || !slices.Equal(p.RequiredChecks, []string{"build"}) {
		t.Fatalf("trunk: %+v", p)
	}
}
