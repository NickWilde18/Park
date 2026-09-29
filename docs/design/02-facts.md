# 2. Facts

Park learns about the world through facts: immutable, attributed records. This chapter covers forge facts — how they are collected and stored. The other kinds of facts (judgements, assertions, member actions) are defined in the chapters that produce them and follow the same rules.

## Responsibilities

- Keep an up-to-date, replayable record of every open pull request in the watched repositories.
- Take changes from webhooks and from polling through one path, so that later stages cannot tell them apart.
- Tell later stages which pull requests changed.

## Data

| Record | Holds | Written by |
|---|---|---|
| `installation` | A connection to one forge: forge kind, base address, credentials, webhook secret, watched repositories | An admin |
| `repo` | Forge repository ID, display name, default branch, required checks, required approvals, watched flag | Repository refresh |
| `account` | Forge account ID, display login, display name | Refreshes, as a side effect |
| `team` | Forge team ID, member account IDs | Repository refresh; used to resolve team review requests |
| `pr_fact` | The current state of one pull request (below) | The refresh flow only |
| `fact_event` | One change to one pull request: the changed fields with old and new values, source, observation time, and a per-pull-request sequence number | The refresh flow only; never updated or deleted |
| `delivery` | IDs of webhook deliveries already processed | The webhook flow |

`pr_fact` holds:

| Part | Fields |
|---|---|
| Identity | Repository ID, number, author account ID, title, labels |
| Lifecycle | State (open, draft, merged, closed); created, ready-for-review, updated, and merged or closed times |
| Branches | Head branch, head commit, base branch |
| Merge | Mergeability: clean, conflicting, or unknown |
| Review requests | Each requested account or team, with the time of the request |
| Reviews | Each review: author, state (approved, changes requested, or commented), the commit it was on, time, and whether it was dismissed |
| Threads | Each review thread: ID, resolved, outdated, and every comment's ID, author, time, and last edit time |
| Checks | Each check on the head commit: name, state, and whether it is required |
| References | Pull requests referenced in the title and description; issues the pull request closes |

## Normalization

- **People** are recorded by forge account ID. Logins and names go to `account` and are display data only.
- **Comment text is not stored.** Threads keep each comment's ID, author, and times; a question that needs the text fetches it from the forge when it is asked (see [Verdicts](04-verdicts.md)). The description is not stored either — only the references parsed from it.
- **Required checks** come from the repository's branch protection. Without protection, every check on the head commit counts as required, and one approval is required.
- **Mergeability** that the forge has not finished computing is stored as `unknown`, and another refresh is scheduled shortly after.
- **References** are parsed from the title and the first 2,000 characters of the description; later text is often pasted logs, where `#N` is a line or build number. A bare `#N` refers to the same repository and never to another. `name#N` or `owner/name#N` counts only when it names exactly one watched repository. Closed issues come from the forge's own closing references, not from parsing.

## Flow: refresh one pull request

Every collection path ends here. Given a repository ID and a pull request number:

1. Take the per-pull-request lock, so that refreshes of one pull request never interleave.
2. Fetch the pull request's full current state from the forge: the pull request itself, its review requests with their times, its reviews, its threads, the checks on its head commit, and the issues it closes.
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
5. Schedule the refreshes. They coalesce: several deliveries about one pull request within a short window become one refresh.
6. Acknowledge within the forge's delivery timeout. The refreshes run afterward.

## Flow: polling

For each watched repository, on a schedule:

1. List its open pull requests with their update times — one inexpensive, paginated query.
2. Refresh those not stored yet, and those whose update time is newer than the stored one.
3. Refresh every stored open pull request missing from the list: it was merged or closed.

Less often, a full sweep refreshes every open pull request regardless of update time, because some changes — a check run finishing, for example — do not move a pull request's update time.

Collection obeys the forge's rate-limit signals. While limited, refreshes scheduled by webhooks keep priority and polling pauses until the limit resets.

## Flow: repository refresh

On a schedule, and whenever a delivery reports a settings change, refresh each watched repository: its default branch; its branch protection, which gives the required checks and the number of required approvals; the membership of its teams; and which linked accounts can read it (used for visibility — see [Identity](07-identity.md)).

## Rules

- **F1. Facts are append-only.** `fact_event` rows are never updated or deleted. `pr_fact` can be rebuilt by replaying them.
- **F2. Payloads are never trusted for state.** A delivery says which pull request changed, never what it changed to.
- **F3. Refreshes are idempotent.** The same forge state produces no new event, so duplicate deliveries cost nothing and a late one costs at most an empty diff.
- **F4. One writer per pull request.** Refreshes of one pull request are serialized. Events carry a per-pull-request sequence number, and order comes from that number, not from delivery order.
- **F5. Stable identifiers.** Keys are forge IDs. URLs are built at render time from the installation's base address; logins and names are display data only.
- **F6. Every fact is attributed.** It records where it came from and when it was observed. Rules cannot read the source of a forge fact: a webhook fact and a polled fact are the same fact.
- **F7. Read-only.** Collection never writes to a forge, and an installation's credentials ask for read access only.

## Defaults

| Setting | Default |
|---|---|
| Coalescing window for webhook refreshes | 5 seconds |
| Delay before refreshing pull requests whose base branch moved | 1 minute |
| Polling | Every 10 minutes |
| Full sweep | Every 6 hours |
| Repository refresh | Every hour |
| Delivery IDs kept | 7 days |
