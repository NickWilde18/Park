// Package facts keeps an up-to-date, replayable record of every open pull request in the watched repositories:
// docs/design/02-facts.md.
//
// Every path that learns of a change — a webhook delivery, polling, a manual refresh — ends in the same refresh:
// fetch the pull request's full state from the forge, normalize it into a PR, compare it with the stored one, and
// if anything differs, append a fact event and store the new state, in one transaction. Deliveries only say which
// pull requests to refresh; what they changed to always comes from the forge.
//
// The forge is reached through a Collector, which each forge kind's package registers; this package never imports
// one. Comment text, descriptions, and URLs are never stored: threads and reviews keep who spoke and when, and
// links are built from the forge's base address when they are shown.
package facts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/NickWilde18/Park/internal/forges"
)

// PR is the normalized state of one pull request. Fields are compared one by one: a fact event records the ones
// that changed.
type PR struct {
	HeadPushedAt     time.Time  `json:"head_pushed_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ReadyAt          *time.Time `json:"ready_at"`
	MergedAt         *time.Time `json:"merged_at"`
	ClosedAt         *time.Time `json:"closed_at"`
	Title            string     `json:"title"`
	State            string     `json:"state"`
	HeadBranch       string     `json:"head_branch"`
	HeadSHA          string     `json:"head_sha"`
	BaseBranch       string     `json:"base_branch"`
	Mergeable        string     `json:"mergeable"`
	Labels           []string   `json:"labels"`
	MergeBlockers    []string   `json:"merge_blockers"`
	ReviewRequests   []Request  `json:"review_requests"`
	Reviews          []Review   `json:"reviews"`
	Threads          []Thread   `json:"threads"`
	Conversation     []Comment  `json:"conversation"`
	Checks           []Check    `json:"checks"`
	MissingChecks    []string   `json:"missing_required_checks"`
	References       []Ref      `json:"references"`
	ClosesIssues     []Ref      `json:"closes_issues"`
	RequiredApproval int        `json:"required_approvals"`
	Author           int64      `json:"author"`
	HeadRepo         int64      `json:"head_repo"`
	Number           int        `json:"number"`
	AuthorCanDismiss bool       `json:"author_can_dismiss"`
	CanMergeNow      bool       `json:"can_merge_now"`
	CodeOwnersNeeded bool       `json:"code_owner_review_required"`
}

// The states of a pull request.
const (
	StateOpen   = "open"
	StateDraft  = "draft"
	StateMerged = "merged"
	StateClosed = "closed"
)

// Open reports whether the pull request is open, as a draft or not.
func (p *PR) Open() bool { return p.State == StateOpen || p.State == StateDraft }

// Mergeability.
const (
	MergeableClean       = "clean"
	MergeableConflicting = "conflicting"
	MergeableUnknown     = "unknown"
)

// Request is a pending review request, to an account or to a team.
type Request struct {
	RequestedAt *time.Time `json:"requested_at"`
	Account     int64      `json:"account,omitempty"`
	Team        int64      `json:"team,omitempty"`
	CodeOwner   bool       `json:"code_owner"`
}

// Review is one submitted review.
type Review struct {
	At      time.Time `json:"at"`
	State   string    `json:"state"`
	Commit  string    `json:"commit"`
	ID      int64     `json:"id"`
	Author  int64     `json:"author"`
	HasBody bool      `json:"has_body"`
	// Counts is whether the author's approval counts toward the forge's requirement.
	Counts bool `json:"counts"`
}

// Review states. A dismissed review keeps nothing of its decision.
const (
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewCommented        = "commented"
	ReviewDismissed        = "dismissed"
)

// Thread is one review thread.
type Thread struct {
	ID       string    `json:"id"`
	Comments []Comment `json:"comments"`
	Resolved bool      `json:"resolved"`
	Outdated bool      `json:"outdated"`
}

// Comment is one comment: who, and when. Its text is never stored.
type Comment struct {
	At       time.Time  `json:"at"`
	EditedAt *time.Time `json:"edited_at"`
	ID       int64      `json:"id"`
	Author   int64      `json:"author"`
}

// Check is one check on the head commit.
type Check struct {
	// Since is when it started, or last changed state.
	Since    *time.Time `json:"since"`
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Required bool       `json:"required"`
}

// Check states.
const (
	CheckPending = "pending"
	CheckPassing = "passing"
	CheckFailing = "failing"
)

// Ref names a pull request or an issue: a repository — Park's ID for a pull request in a watched repository, the
// forge's ID for an issue — and a number.
type Ref struct {
	Repo   int64 `json:"repo"`
	Number int   `json:"number"`
}

// Fetched is what a collector returns for one pull request: its state, the text its references are parsed from —
// the title and the start of the description, used once and never stored — and the accounts it saw, with their
// display data.
type Fetched struct {
	PR      PR
	RefText string
	// AuthorLogin is how to ask the forge about the author's access to the repository.
	AuthorLogin string
	// Association is the author's standing in the repository — owner, member, collaborator, contributor, none —
	// for when their access cannot be read.
	Association string
	Accounts    []forges.Account
	// Teams are the teams asked for a review, to look their members up.
	Teams []Team
	// ForgeRepo is the forge's ID of the repository, which a rename does not change.
	ForgeRepo int64
}

// Team is a forge team, as a review request names it.
type Team struct {
	Org  string
	Slug string
	Name string
	ID   int64
}

// Listed is an open pull request as a listing shows it.
type Listed struct {
	UpdatedAt time.Time
	Number    int
}

// Light is what changes without moving a pull request's update time: its head, mergeability, and checks.
type Light struct {
	Mergeable string
	HeadSHA   string
	Checks    []Check
}

// RepoFacts is a repository's settings.
type RepoFacts struct {
	Owner         string
	Name          string
	DefaultBranch string
	ForgeRepo     int64
}

// Protection is what the forge requires of a pull request into one base branch.
type Protection struct {
	RequiredChecks []string `json:"required_checks"`
	// Protected is whether branch protection or a ruleset applies to the branch; a protected branch is long-lived,
	// and never the parent of a chain.
	Protected bool `json:"protected"`
	// RequiredApprovals is -1 when the forge would not say.
	RequiredApprovals int  `json:"required_approvals"`
	CodeOwners        bool `json:"code_owners"`
}

// Delivery is what a webhook delivery concerns, and nothing else of its payload.
type Delivery struct {
	ID    string
	Event string
	// ForgeRepo is the forge's ID of the repository it concerns; 0 for none.
	ForgeRepo int64
	PRs       []int
	SHAs      []string
	// Pushed is the branch a push moved.
	Pushed string
	// RepoChanged calls for a repository refresh.
	RepoChanged bool
	Teams       []int64
}

// Repo is a watched repository as a collector needs it.
type Repo struct {
	Owner     string
	Name      string
	ForgeRepo int64
}

// Collector is what the fact layer needs from a forge, through one installation's credentials.
type Collector interface {
	// FetchPR fetches a pull request's full current state. Protection is what the forge requires of its base
	// branch, from the last repository refresh.
	FetchPR(ctx context.Context, r Repo, number int, protection map[string]Protection) (Fetched, error)
	// ListOpen lists the open pull requests with their update times.
	ListOpen(ctx context.Context, r Repo) ([]Listed, error)
	// Light fetches what can change without moving the update time.
	Light(ctx context.Context, r Repo, number int) (Light, error)
	// Repository fetches a repository's settings, by owner and name.
	Repository(ctx context.Context, owner, name string) (RepoFacts, error)
	// Protection fetches what the forge requires of pull requests into each of the branches.
	Protection(ctx context.Context, r Repo, branches []string) (map[string]Protection, error)
	// Access returns an account's access level in a repository: admin, maintain, write, triage, read, or none.
	Access(ctx context.Context, r Repo, login string) (string, error)
	// Team fetches a team's members; nil when the forge would not tell.
	Team(ctx context.Context, org, slug string) ([]int64, error)
	// Delivery verifies a webhook delivery with the secret and works out what it concerns.
	Delivery(header http.Header, body, secret []byte) (Delivery, error)
	// Missed lists the deliveries of this installation since a time, with what each concerns, for when Park was
	// down; ErrNoDeliveryLog when the credential cannot read the forge's delivery log.
	Missed(ctx context.Context, since time.Time) ([]Delivery, error)
}

// Credential is an installation's secret, as its kind's collector reads it.
type Credential struct {
	// Kind is token or app.
	Kind string
	// Token is a personal access token.
	Token string
	// App is an app installation.
	App *AppCredential
}

// AppCredential is an app installation's credential.
type AppCredential struct {
	PrivateKey     string `json:"private_key"`
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
}

// NewCollector makes a collector for one forge, with one installation's credential.
type NewCollector func(f forges.Forge, c Credential) (Collector, error)

var collectors = map[string]NewCollector{}

// Register makes a forge kind's collector available. Kind packages call it from init.
func Register(kind string, n NewCollector) { collectors[kind] = n }

// collectorFor makes the collector of a forge's kind.
func collectorFor(f forges.Forge, c Credential) (Collector, error) {
	n, ok := collectors[f.Kind]
	if !ok {
		return nil, fmt.Errorf("forge kind %q has no collector", f.Kind)
	}
	return n(f, c)
}

// RateLimited is returned by a collector while the forge's rate limit holds; Until is when it resets.
type RateLimited struct {
	Until time.Time
}

func (e *RateLimited) Error() string {
	return "the forge's rate limit holds until " + e.Until.UTC().Format(time.RFC3339)
}

// ErrNotFound is returned by a collector for a pull request or repository the forge does not have.
var ErrNotFound = errors.New("the forge has no such thing")

// ErrRefused is returned by a collector when the forge refuses the installation's credential: a token it no longer
// takes, or an app key it cannot verify.
var ErrRefused = errors.New("the forge refused the installation's credential")

// ErrBadSignature is returned for a delivery whose signature does not match the installation's secret.
var ErrBadSignature = errors.New("the delivery's signature does not match")

// ErrNoDeliveryLog is returned by Missed when the credential cannot read the forge's delivery log.
var ErrNoDeliveryLog = errors.New("this credential cannot read the forge's delivery log")
