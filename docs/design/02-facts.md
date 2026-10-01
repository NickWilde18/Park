# 2. Facts

Park learns about the world through facts: immutable, attributed records. This chapter covers forge facts — how they are collected and stored. The other kinds of facts (judgements, assertions, member actions) are defined in the chapters that produce them and follow the same rules.

## Responsibilities

- Keep an up-to-date, replayable record of every open pull request in the watched repositories.
- Take changes from webhooks and from polling through one path, so that later stages cannot tell them apart.
- Tell later stages which pull requests changed.

## Data

| Record | Holds | Written by |
|---|---|---|
| `forge` | One forge instance — github.com, or a company's own server: its kind, its base address, and a stable ID. Every external ID below is unique only within its forge, so every key includes the forge's ID | An admin |
| `installation` | A connection to a forge, such as one organization's app installation: the forge, credentials, webhook secret, watched repositories. Installations on one forge share its keys | An admin |
| `repo` | Forge repository ID, display name, default branch, watched flag; what its branch protection and rulesets require of pull requests into the default branch and each base branch in use — required checks, required approvals, code-owner review | Repository refresh |
| `account` | Forge account ID, display login, display name | Refreshes, as a side effect |
| `team` | Forge team ID, member account IDs | Repository refresh; used to resolve team review requests |
| `pr_fact` | The current state of one pull request (below) | The refresh flow only |
| `fact_event` | One change to one pull request: the changed fields with old and new values, source, observation time, and a per-pull-request sequence number | The refresh flow only; never updated or deleted |
| `delivery` | IDs of webhook deliveries already processed | The webhook flow |

`pr_fact` holds:

| Part | Fields |
|---|---|
| Identity | Repository ID, number, author account ID, title, labels; whether the author may dismiss reviews in this repository |
| Lifecycle | State (open, draft, merged, closed); created, ready-for-review, updated, and merged or closed times |
| Branches | Head repository and head branch — the head repository differs for a pull request from a fork — head commit and the time it was pushed, base branch |
| Stack | The number of the stack the forge records the pull request in, unique within its repository; none for a pull request in no stack, and on a forge that records no stacks |
| Merge | Mergeability: clean, conflicting, or unknown; and the forge's own merge verdict: whether it can be merged now, and what blocks it — reviews, code owners, checks, being behind the base, conflicts, or something else |
| Review requests | Each requested account or team, with the time of the request, and whether it was made to a code owner |
| Reviews | Each review: ID, author, state (approved, changes requested, or commented), the commit it was on, time, whether it was dismissed, whether it has a written body, and whether its author's approval counts toward the forge's requirement |
| Threads | Each review thread: ID, resolved, outdated, and the ID, author, time, and last edit time of its first comment and its latest twenty ([D49](10-design-log.md#d49-collecting-within-the-forges-limits)) |
| Conversation | The ID, author, and time of each comment on the pull request itself |
| Checks | Each check on the head commit: name, state, and whether it is required; the required checks that have not reported on it yet |
| References | Pull requests referenced in the title and description; issues the pull request closes |

## Normalization

- **People** are recorded by forge account ID. Logins and names go to `account` and are display data only.
- **Comment text is not stored.** Threads, reviews, and the conversation keep each comment's ID, author, and times, and a review only whether it has a written body; a question that needs the text fetches it from the forge when it is asked (see [Verdicts](04-verdicts.md)). The description is not stored either — only the references parsed from it.
- **Required checks and approvals** come from the branch protection and rulesets that apply to the pull request's base branch. Without protection, every check on the head commit counts as required, and the forge requires no approval; the team's minimum still applies ([Rules](05-rules.md#the-field-catalog)). A required check that has not reported on the head commit counts as pending, not as passing.
- **Mergeability** that the forge has not finished computing is stored as `unknown`, and another refresh is scheduled shortly after.
- **Required approvals** the forge will not reveal — a branch's protection needs administration read access — are worked out from the forge's own review decision: one more than the approvals that count while it asks for review, and the approvals there are once it is satisfied.
- **Whether the author may dismiss** comes from their access level in the repository as the forge reports it — write or more — kept for an hour; when the forge will not say, from their standing in the repository: owner, member, or collaborator.
- **References** are parsed from the title and the first 2,000 characters of the description; later text is often pasted logs, where `#N` is a line or build number. A bare `#N` refers to the same repository and never to another. `name#N` or `owner/name#N` counts only when it names exactly one watched repository. Closed issues come from the forge's own closing references, not from parsing.
- **Stacks** are read from a forge that records them; a forge of the same kind may not, such as an older server. Whether it does is asked of the forge once, and until it has answered, a refresh fails and is retried rather than store a pull request as in no stack.

## Flow: refresh one pull request

Every collection path ends here. Given a repository ID and a pull request number:

1. Take the per-pull-request lock, so that refreshes of one pull request never interleave.
2. Fetch the pull request's full current state from the forge: the pull request itself, its review requests with their times, its reviews, its threads, its conversation, the checks on its head commit, the forge's merge verdict, the issues it closes, and the stack it is in.
3. Normalize it into the `pr_fact` shape.
4. Diff against the stored `pr_fact`.
5. If nothing differs, stop. Otherwise, in one transaction, append a `fact_event` with the next sequence number and the changed fields, update `pr_fact`, and queue work for the later stages: grouping, verdicts, and the verification of any task that touches this pull request.
6. Release the lock.

If the fetch fails, it is retried with backoff, and `pr_fact` is marked stale with the time of its last successful refresh. The dashboard shows staleness wherever it shows that pull request.

## Flow: webhook delivery

1. Receive the delivery at `/hooks/{installation_id}`. The path names the installation, never the forge.
2. Verify the signature with the installation's webhook secret. Reject the delivery if it fails.
3. If the delivery ID is already recorded, acknowledge and stop.
4. Work out what the delivery concerns, and ignore the rest of the payload:
   - a pull request, review, thread, or comment delivery names a pull request;
   - a check or status delivery names a commit: the open pull requests whose head it is;
   - a push names a branch: the open pull requests whose head it is, and — after a delay, since forges recompute mergeability asynchronously — those whose base it is;
   - a repository, branch protection, or team delivery calls for a repository refresh.
5. Schedule the refreshes. They coalesce while they wait: several deliveries about one pull request within a short window become one refresh. A refresh that has already started is not joined; it is marked dirty, and runs again when it finishes, so a change that arrives mid-fetch is never lost.
6. Acknowledge within the forge's delivery timeout. The refreshes run afterward.

Forges do not redeliver on their own. When Park starts, it reads the forge's delivery log for each installation and processes the deliveries it missed while it was down.

## Flow: polling

For each watched repository, on a schedule:

1. List its open pull requests with their update times and stacks — one inexpensive, paginated query.
2. Refresh those not stored yet, those whose update time is newer than the stored one, and those whose stack differs from the stored one: joining a stack does not move a pull request's update time.
3. Refresh every stored open pull request missing from the list: it was merged or closed.

Less often, a full sweep refreshes every open pull request regardless of update time, because some changes — a check run finishing, for example — do not move a pull request's update time.

Pull requests that are about to change without moving their update time are polled on a short period instead: those whose required checks are pending, those whose mergeability is unknown, and those a verifying task touches ([Tasks](06-tasks.md)). A short poll is a cheap look at the head, the checks, and mergeability, and calls for a full refresh only when they changed. This keeps Park correct without webhooks, and quick to notice checks finishing.

A member can also ask for a poll now, of every watched repository or of one ([D64](10-design-log.md#d64-a-member-can-ask-for-a-poll-now)). It is the scheduled poll, run early: a poll already waiting moves earlier rather than running twice, and one waiting out a failure's backoff keeps it.

Collection obeys the forge's rate-limit signals. While limited, collection through that installation waits until the limit resets, and a failure is retried with a backoff; neither counts against anything else.

## Flow: repository refresh

On a schedule, and whenever a delivery reports a settings change, refresh each watched repository: its default branch; its branch protection and rulesets by branch pattern, which give the required checks, the number of required approvals, whether code owners must approve, and who may dismiss reviews; the membership of its teams; and which linked accounts can read it (used for visibility — see [Identity](07-identity.md)).

## Rules

- **F1. Facts are append-only.** `fact_event` rows are never updated or deleted. `pr_fact` can be rebuilt by replaying them.
- **F2. Payloads are never trusted for state.** A delivery says which pull request changed, never what it changed to.
- **F3. Refreshes are idempotent.** The same forge state produces no new event, so duplicate deliveries cost nothing and a late one costs at most an empty diff.
- **F4. One writer per pull request.** Refreshes of one pull request are serialized. Events carry a per-pull-request sequence number, and order comes from that number, not from delivery order.
- **F5. Stable identifiers.** Keys are forge IDs, qualified by their forge: the same numeric ID on two forges is two different things. URLs are built at render time from the forge's base address; logins and names are display data only.
- **F6. Every fact is attributed.** It records where it came from and when it was observed. Rules cannot read the source of a forge fact: a webhook fact and a polled fact are the same fact.
- **F7. Read-only.** Collection never writes to a forge, and an installation's credentials ask for read access only.

## Defaults

| Setting | Default |
|---|---|
| Coalescing window for webhook refreshes | 5 seconds |
| Delay before refreshing pull requests whose base branch moved | 1 minute |
| Polling | Every 10 minutes |
| Short polling: pending checks, unknown mergeability, verifying tasks | Every 30 seconds |
| Full sweep | Every 6 hours |
| Repository refresh | Every hour |
| Delivery IDs kept | 7 days |
