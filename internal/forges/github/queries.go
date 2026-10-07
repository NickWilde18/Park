package github

import "fmt"

// The GraphQL queries the collector sends. Each costs a point or two of the hourly limit.
//
// A pull request's checks are read only from its head commit: reading them for every commit would expand every
// check of every commit, and exhaust the limit. Threads, reviews, and conversation comments are paged to the end,
// since a partial list undercounts what waits on someone. A thread's comments are its first and its latest twenty:
// the first names who started it, the latest who spoke last and who else took part.
//
// A pull request's stack is asked only of a forge whose schema has stacks: an Enterprise Server's may not, and a
// query that names a field the schema lacks fails as a whole. The queries that ask for it are made in two forms.

// stackField is the stack a pull request is in, and the branch the stack merges into, put into prQuery and listQuery
// for a forge with stacks.
const stackField = " stack { number baseRefName }"

// stackProbe asks whether the forge's schema has pull request stacks: the answer names the type, or is null.
const stackProbe = `query { __type(name: "PullRequestStack") { name } }`

// queries are the forms of the queries that ask for a pull request's stack.
type queries struct {
	pr, list string
}

var (
	withStacks    = queries{pr: fmt.Sprintf(prQuery, stackField), list: fmt.Sprintf(listQuery, stackField)}
	withoutStacks = queries{pr: fmt.Sprintf(prQuery, ""), list: fmt.Sprintf(listQuery, "")}
)

const actorFields = `__typename login ... on User { databaseId name } ... on Bot { databaseId }`

const prQuery = `query($owner: String!, $name: String!, $number: Int!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    databaseId
    pullRequest(number: $number) {
      number title state isDraft createdAt updatedAt mergedAt closedAt authorAssociation
      author { ` + actorFields + ` }
      labels(first: 50) { nodes { name } }
      headRepository { databaseId } headRefName headRefOid baseRefName%s
      mergeable mergeStateStatus reviewDecision
      bodyText
      closingIssuesReferences(first: 25) { nodes { number repository { databaseId } } }
      reviewRequests(first: 100) { nodes { asCodeOwner requestedReviewer {
        __typename ... on User { databaseId login name } ... on Team { databaseId slug name organization { login } } } } }
      timelineItems(last: 100, itemTypes: [REVIEW_REQUESTED_EVENT, READY_FOR_REVIEW_EVENT]) { nodes { __typename
        ... on ReviewRequestedEvent { createdAt requestedReviewer { __typename ... on User { databaseId } ... on Team { databaseId } } }
        ... on ReadyForReviewEvent { createdAt } } }
      reviews(first: 100) { pageInfo { hasNextPage endCursor } nodes { ` + reviewFields + ` } }
      reviewThreads(first: 100) { pageInfo { hasNextPage endCursor } nodes { ` + threadFields + ` } }
      comments(first: 100) { pageInfo { hasNextPage endCursor } nodes { ` + commentFields + ` } }
      commits(last: 1) { nodes { commit { oid committedDate
        statusCheckRollup { contexts(first: 100) { nodes { ` + checkFields + ` } } } } } }
    }
  }
}`

const reviewFields = `databaseId state submittedAt authorCanPushToRepository body commit { oid } author { ` + actorFields + ` }`

const commentFields = `databaseId createdAt lastEditedAt author { ` + actorFields + ` }`

const threadFields = `id isResolved isOutdated
  first: comments(first: 1) { nodes { ` + commentFields + ` } }
  recent: comments(last: 20) { nodes { ` + commentFields + ` } }`

const checkFields = `__typename
  ... on CheckRun { name status conclusion startedAt isRequired(pullRequestNumber: $number) }
  ... on StatusContext { context state createdAt isRequired(pullRequestNumber: $number) }`

// The pages after the first of a pull request's reviews, threads, and conversation comments.
const (
	reviewsPage = `query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    reviews(first: 100, after: $cursor) { pageInfo { hasNextPage endCursor } nodes { ` + reviewFields + ` } } } } }`
	threadsPage = `query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    reviewThreads(first: 100, after: $cursor) { pageInfo { hasNextPage endCursor } nodes { ` + threadFields + ` } } } } }`
	commentsPage = `query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    comments(first: 100, after: $cursor) { pageInfo { hasNextPage endCursor } nodes { ` + commentFields + ` } } } } }`
)

const listQuery = `query($owner: String!, $name: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 100, after: $cursor) { pageInfo { hasNextPage endCursor } nodes { number updatedAt%s } }
  }
}`

const lightQuery = `query($owner: String!, $name: String!, $number: Int!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { mergeable headRefOid
    commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes { ` + checkFields + ` } } } } } } } }
}`

const repoQuery = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) { databaseId name owner { login } defaultBranchRef { name } }
}`
