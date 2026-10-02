package rules

// Role is the part a person plays on a pull request, and names the rule list evaluated for them.
type Role string

// The roles, each with its own list.
const (
	Author   Role = "author"
	Reviewer Role = "reviewer"
)

// Roles are the roles, in the order their lists are shown.
var Roles = []Role{Author, Reviewer}

// Type is a field's type, as the field catalog names it.
type Type string

// The field types of docs/design/05-rules.md#the-field-catalog. Numbers are whole numbers; hours are whole hours.
// Accounts are logins, and teams are organisation/team slugs.
const (
	Boolean         Type = "boolean"
	Number          Type = "number"
	OptionalNumber  Type = "optional number"
	Choice          Type = "choice"
	Text            Type = "text"
	Account         Type = "account"
	TextList        Type = "list of text"
	AccountList     Type = "list of accounts"
	AccountTeamList Type = "list of accounts and teams"
	ChoiceList      Type = "list of choices"
	Judgement       Type = "judgement"
)

func (t Type) list() bool {
	return t == TextList || t == AccountList || t == AccountTeamList || t == ChoiceList
}

// Field is a field a condition or a reason can read.
type Field struct {
	Name    string
	Type    Type
	Meaning string
	// Choices are the values of a choice, or of a list of choices.
	Choices []string
}

// fields are the field catalog but for judgements, which come from the questions.
var fields = []Field{
	{Name: "pr.repo", Type: Text,
		Meaning: "Repository name"},
	{Name: "pr.title", Type: Text,
		Meaning: "Title"},
	{Name: "pr.labels", Type: TextList,
		Meaning: "Labels"},
	{Name: "pr.base", Type: Text,
		Meaning: "Base branch"},
	{Name: "pr.author", Type: Account,
		Meaning: "The author"},
	{Name: "pr.draft", Type: Boolean,
		Meaning: "The pull request is a draft"},
	{Name: "pr.conflicting", Type: Boolean,
		Meaning: "The forge reports conflicts with the base branch"},
	{Name: "pr.checks", Type: Choice, Choices: []string{"failing", "pending", "passing", "none"},
		Meaning: "The required checks on the head commit: `failing` if any failed; else `pending` if any is still running or has not reported on this head yet; else `passing` if there are any; else `none`"},
	{Name: "pr.failing_checks", Type: TextList,
		Meaning: "Names of the failing required checks"},
	{Name: "pr.hours_checks_pending", Type: OptionalNumber,
		Meaning: "Hours since the required checks on the head commit started pending"},
	{Name: "pr.approvals", Type: Number,
		Meaning: "Reviewers whose decisive review approves, is not dismissed, and counts toward the forge's requirement — for example, because they have write access"},
	{Name: "pr.required_approvals", Type: Number,
		Meaning: "The larger of the forge's requirement for the base branch and the team's minimum, 1 by default"},
	{Name: "pr.blocked_by", Type: AccountList,
		Meaning: "Reviewers whose decisive review requests changes and is not dismissed"},
	{Name: "pr.blockers_rerequested", Type: Boolean,
		Meaning: "Everyone in `pr.blocked_by` has a pending direct review request"},
	{Name: "pr.requested", Type: AccountTeamList,
		Meaning: "Pending review requests: accounts, and teams with at least one member other than the author"},
	{Name: "pr.hours_requested", Type: OptionalNumber,
		Meaning: "Hours since the oldest pending review request"},
	{Name: "pr.reviewed", Type: Boolean,
		Meaning: "Someone other than the author has submitted a review"},
	{Name: "pr.unanswered_reviews", Type: Number,
		Meaning: "Reviews with a written body that request changes or only comment, written by someone other than a bot, after which the author has neither commented on the pull request nor pushed"},
	{Name: "pr.threads_on_reviewers", Type: Number,
		Meaning: "Unresolved threads whose latest comment is the author's, and whose other participants include someone who has not approved"},
	{Name: "pr.awaited_reviewers", Type: AccountList,
		Meaning: "Those participants: the ones who have not approved"},
	{Name: "pr.awaited_rerequested", Type: Boolean,
		Meaning: "Everyone in `pr.awaited_reviewers` has a pending direct review request"},
	{Name: "pr.mergeable_now", Type: Boolean,
		Meaning: "The forge says the pull request can be merged now"},
	{Name: "pr.merge_blocked_by", Type: ChoiceList, Choices: []string{"reviews", "code_owners", "checks", "behind", "conflicts", "other"},
		Meaning: "What the forge says blocks merging: `reviews`, `code_owners`, `checks`, `behind`, `conflicts`, or `other`"},
	{Name: "pr.code_owners_pending", Type: AccountTeamList,
		Meaning: "Code owners whose approval the forge still requires"},
	{Name: "pr.author_can_dismiss", Type: Boolean,
		Meaning: "The author is allowed to dismiss reviews in this repository"},
	{Name: "me.role", Type: Choice, Choices: []string{"author", "reviewer"},
		Meaning: "`author` or `reviewer`"},
	{Name: "me.threads_on_me", Type: Number,
		Meaning: "Unresolved threads waiting on me. For the author: those whose latest comment is not the author's. For a reviewer: those I started or commented in whose latest comment is the author's"},
	{Name: "me.threads_open", Type: Number,
		Meaning: "Unresolved threads I started"},
	{Name: "me.requested", Type: Boolean,
		Meaning: "A review is requested from me directly, not only through a team"},
	{Name: "me.code_owner", Type: Boolean,
		Meaning: "The forge requires my approval as a code owner"},
	{Name: "me.reviewed", Type: Boolean,
		Meaning: "I have submitted a review"},
	{Name: "me.decision", Type: Choice, Choices: []string{"approved", "changes_requested", "none"},
		Meaning: "My decisive review: `approved`, `changes_requested`, or `none` if there is none or it was dismissed"},
	{Name: "me.reviewed_since_decision", Type: Boolean,
		Meaning: "I have submitted another review, such as a comment, since my decisive review"},
	{Name: "me.new_commits", Type: Boolean,
		Meaning: "The head commit differs from the one my latest review was on"},
	{Name: "me.hours_since_review", Type: OptionalNumber,
		Meaning: "Hours since my latest review"},
	{Name: "me.hours_since_nudge", Type: OptionalNumber,
		Meaning: "Hours since I last marked this pull request as nudged"},
	{Name: "chain.position", Type: Number,
		Meaning: "1 for a pull request that stacks on no open pull request"},
	{Name: "chain.length", Type: Number,
		Meaning: "Open pull requests in its chain; 1 when it is not stacked"},
	{Name: "chain.open_below", Type: Number,
		Meaning: "Open pull requests it stacks on, directly or not"},
	{Name: "group.size", Type: Number,
		Meaning: "Open pull requests in its group; 0 when it has none"},
}

// Question is a question the model answers, for people of one role: docs/design/04-verdicts.md#questions.
type Question struct {
	Name string
	Role Role
	Asks string
}

// Questions are the questions judgements answer.
var Questions = []Question{
	{Name: "commits_address_my_comments", Role: Reviewer,
		Asks: "Do the commits pushed since my latest review respond to my unresolved comments?"},
	{Name: "comments_blocking", Role: Reviewer,
		Asks: "Does any of my unresolved comments raise a real problem, rather than a matter of taste?"},
}

// Answers are a judgement's answers.
var Answers = []string{"yes", "no"}

const judgementPrefix = "judgement."

// Catalog is every field a condition can read, judgements last.
func Catalog() []Field {
	out := append([]Field(nil), fields...)
	for _, q := range Questions {
		out = append(out, Field{Name: judgementPrefix + q.Name, Type: Judgement, Meaning: q.Asks, Choices: Answers})
	}
	return out
}

var catalog = func() map[string]Field {
	m := map[string]Field{}
	for _, f := range Catalog() {
		m[f.Name] = f
	}
	return m
}()

// Action is something a part can ask a person to do: docs/design/04-verdicts.md#actions.
type Action struct {
	Name string
	Role Role
	// Task says whether the action can be a task.
	Task bool
}

// Actions are the actions, by role.
var Actions = []Action{
	{Name: "resolve_conflicts", Role: Author, Task: true},
	{Name: "fix_checks", Role: Author, Task: true},
	{Name: "address_comments", Role: Author, Task: true},
	{Name: "request_review", Role: Author, Task: true},
	{Name: "review", Role: Reviewer, Task: true},
	{Name: "unblock", Role: Author},
	{Name: "merge", Role: Author},
}

var actions = func() map[string]Action {
	m := map[string]Action{}
	for _, a := range Actions {
		m[a.Name] = a
	}
	return m
}()

// The kinds of a part.
const (
	KindAct    = "act"
	KindHold   = "hold"
	KindFinish = "finish"
	KindIdle   = "idle"
)

// The priorities of a part.
const (
	PriorityNormal = "normal"
	PriorityLow    = "low"
)
