# 5. Rules

Rules turn facts into verdicts. They live in the database, are drawn and edited on the dashboard, and are versioned. How they are evaluated is described in [Verdicts](04-verdicts.md); this chapter covers what a rule is, what it can read, the rules Park ships with, and how rules change.

## A rule

| Part | Meaning |
|---|---|
| Role | `author` or `reviewer`. Each role has its own ordered list |
| Position | Its place in that list |
| Name | Shown in a verdict's path when the rule matches: "Checks failing" |
| Passed-over label | Optional; shown in the path when the rule does not match: "checks not failing" |
| Condition | Structured, or an expression (below) |
| Conclusion | Ball, action, waiting-on, priority, reason, and whether it may become a task (below) |

The first rule in the list whose condition matches decides; later rules are not evaluated. A verdict's path therefore reads like a walk down a tree:

```
author → not a draft → no conflicts → checks not failing → Changes requested, all answered
```

A rule with no passed-over label leaves no trace when it does not match. Rules the evaluation never reached do not appear at all: a step missing from the path was not checked — it did not pass.

## Conditions

Two forms, evaluated by one engine:

- **Structured** (the default): `(field, operator, value)` terms joined by AND and OR, edited with drop-downs.
- **Expression**: a CEL (Common Expression Language) expression over the same fields, for conditions the structured form cannot state.

Structured conditions compile to CEL, so there is one evaluator, one test harness, and one audit trail; the structured editor is a view over a subset of the language. The dashboard marks expression rules wherever rules are listed, so they stay exceptions.

| Field type | Structured operators |
|---|---|
| Boolean | is true, is false |
| Number | =, ≠, <, ≤, >, ≥ |
| Choice | is, is not, is one of |
| Text | is, is not, starts with, contains |
| List | is empty, is not empty, contains |
| Optional | is set, is not set, and the operators of its type. A comparison is false when the field is not set |
| Judgement | is `yes`, is `no` |

A structured condition and what it compiles to:

```
me.threads_on_me  >  0         AND   pr.blocked_by  is not empty
me.threads_on_me > 0 && size(pr.blocked_by) > 0
```

A condition only an expression can state — every failing required check is an end-to-end test:

```
pr.checks == 'failing' && pr.failing_checks.all(c, c.startsWith('e2e-'))
```

## The field catalog

Everything a condition can read. Fields are computed for one member on one pull request. A reviewer's **decisive review** is their latest review that approves or requests changes; reviews that only comment do not change it.

**Pull request**

| Field | Type | Meaning |
|---|---|---|
| `pr.repo` | text | Repository name |
| `pr.title` | text | Title |
| `pr.labels` | list of text | Labels |
| `pr.base` | text | Base branch |
| `pr.author` | account | The author |
| `pr.draft` | boolean | The pull request is a draft |
| `pr.conflicting` | boolean | The forge reports conflicts with the base branch |
| `pr.checks` | choice | The required checks on the head commit: `failing` if any failed; else `pending` if any is still running; else `passing` if there are any; else `none` |
| `pr.failing_checks` | list of text | Names of the failing required checks |
| `pr.approvals` | number | Reviewers whose decisive review approves and is not dismissed |
| `pr.required_approvals` | number | From the repository's branch protection; 1 without it |
| `pr.blocked_by` | list of accounts | Reviewers whose decisive review requests changes and is not dismissed |
| `pr.requested` | list of accounts and teams | Pending review requests |
| `pr.hours_requested` | optional number | Hours since the oldest pending review request |
| `pr.reviewed` | boolean | Someone other than the author has submitted a review |
| `pr.threads_on_reviewers` | number | Unresolved threads whose latest comment is the author's, and whose other participants include someone who has not approved |
| `pr.awaited_reviewers` | list of accounts | Those participants: the ones who have not approved |
| `pr.awaited_rerequested` | boolean | Everyone in `pr.awaited_reviewers` has a pending review request |

**The member**

| Field | Type | Meaning |
|---|---|---|
| `me.role` | choice | `author` or `reviewer` |
| `me.threads_on_me` | number | Unresolved threads waiting on me. For the author: those whose latest comment is not the author's. For a reviewer: those I started or commented in whose latest comment is the author's |
| `me.threads_open` | number | Unresolved threads I started |
| `me.requested` | boolean | A review is requested from me directly |
| `me.reviewed` | boolean | I have submitted a review |
| `me.decision` | choice | My decisive review: `approved`, `changes_requested`, or `none` if there is none or it was dismissed |
| `me.reviewed_since_decision` | boolean | I have submitted another review, such as a comment, since my decisive review |
| `me.new_commits` | boolean | The head commit differs from the one my latest review was on |
| `me.hours_since_review` | optional number | Hours since my latest review |
| `me.hours_since_nudge` | optional number | Hours since I last marked this pull request as nudged |

**Chain and group**

| Field | Type | Meaning |
|---|---|---|
| `chain.position` | number | 1 for a pull request that stacks on no open pull request |
| `chain.length` | number | Open pull requests in its chain; 1 when it is not stacked |
| `chain.open_below` | number | Open pull requests it stacks on, directly or not |
| `group.size` | number | Open pull requests in its group; 0 when it has none |

**Judgements** — see [Verdicts](04-verdicts.md#questions)

| Field | Type |
|---|---|
| `judgement.commits_address_my_comments` | judgement |
| `judgement.author_awaits_me` | judgement |
| `judgement.comments_blocking` | judgement |

No field exposes the forge's own summary review decision. It stays at "changes requested" after the author has answered everything but not yet asked for another review, and goes back to "review required" on every push, pulling reviewers who already reviewed back in. The fields above are built from individual reviews and threads, which do not drift that way.

No field exposes where a fact came from, a task's state, or another member's verdict.

## Conclusions

| Part | Values |
|---|---|
| Ball | `me`, `others`, or `nobody` |
| Action | Required when the ball is `me`: an action allowed for the role ([Verdicts](04-verdicts.md#actions)) |
| Targets | For `request_review`: an account-list field naming the reviewers to hand the pull request back to, such as `pr.blocked_by`. Without one, anyone will do |
| Waiting on | Required when the ball is `others`: an account or account-list field, such as `pr.author` or `pr.blocked_by` |
| Priority | `normal` (the default) or `low` |
| Reason | A template, with expressions in braces: `{me.threads_on_me} comments wait on you` |
| May become a task | Only for actions that can be tasks. Whether a task is actually created also depends on the member's automation settings ([Tasks](06-tasks.md)) |

## The default rule set

Park ships with the rule set below, ported from its predecessor's decision tree, where it ran on a real team's pull requests. Every rule whose action can be a task is marked as able to become one. The numbers in it — 24 hours, 48 hours, position 3 — are ordinary values, edited like anything else.

### Author

| # | Rule | When | Ball | Action | Reason |
|---|---|---|---|---|---|
| 1 | Draft | `pr.draft` | nobody | | Draft |
| 2 | Conflicts | `pr.conflicting` | me | `resolve_conflicts` | Conflicts with the base branch |
| 3 | Checks failing | `pr.checks` is `failing` | me | `fix_checks` | {size(pr.failing_checks)} required checks failing |
| 4 | Checks running | `pr.checks` is `pending` | nobody | | Waiting for checks |
| 5 | Changes requested, all answered | `pr.blocked_by` is not empty and `me.threads_on_me` = 0 | me | `request_review` to `pr.blocked_by` | Every comment is answered; dismiss {pr.blocked_by}'s request for changes, saying what changed, and ask again |
| 6 | Changes requested | `pr.blocked_by` is not empty and `me.threads_on_me` > 0 | me | `address_comments` | {pr.blocked_by} requested changes; {me.threads_on_me} comments wait on you |
| 7 | Comments wait on you | `me.threads_on_me` > 0 | me | `address_comments` | {me.threads_on_me} comments wait on you |
| 8 | Replies not handed back | `pr.threads_on_reviewers` > 0 and not `pr.awaited_rerequested` | me | `request_review` to `pr.awaited_reviewers` | {pr.threads_on_reviewers} of your replies wait on {pr.awaited_reviewers}; finish what you promised, then ask them to look again |
| 9 | Replies wait on reviewers | `pr.threads_on_reviewers` > 0 | others: `pr.awaited_reviewers` | | {pr.threads_on_reviewers} replies wait for the reviewers |
| 10 | Approved, stacked | `pr.approvals` ≥ `pr.required_approvals`, `pr.blocked_by` is empty, and `chain.open_below` > 0 | nobody | | Approved; {chain.open_below} pull requests below it must merge first |
| 11 | Ready to merge | `pr.approvals` ≥ `pr.required_approvals` and `pr.blocked_by` is empty | me | `merge` | Approved and passing; ready to merge |
| 12 | No reviewer | `pr.requested` is empty | me | `request_review` | Nobody is asked to review |
| 13 | Nudged | `pr.hours_requested` ≥ 24 and `me.hours_since_nudge` < 48 | others: `pr.requested` | | Nudged; waiting for review |
| 14 | Time to nudge | `pr.hours_requested` ≥ 24 | me | `nudge` | No review since the request; remind the reviewers |
| 15 | Waiting for review | always | others: `pr.requested` | | Waiting for review |

Passed-over labels: 1 "not a draft", 2 "no conflicts", 3 "checks not failing", 7 "no comments wait on you", 11 "not approved yet".

### Reviewer

| # | Rule | When | Ball | Action | Reason |
|---|---|---|---|---|---|
| 1 | Draft | `pr.draft` | nobody | | Draft |
| 2 | Team request | not `me.reviewed` and not `me.requested` | nobody | | Requested from your team; optional |
| 3 | Approved without you | not `me.reviewed` and `pr.approvals` ≥ `pr.required_approvals` | nobody | | Already has the approvals it needs; optional |
| 4 | First review, deep in a chain | not `me.reviewed` and `chain.position` ≥ 3 | me, low | `review` | First review; {chain.position} of {chain.length} in its chain, not urgent |
| 5 | First review | not `me.reviewed` | me | `review` | First review |
| 6 | Your block is stale | `me.decision` is `changes_requested`, `me.reviewed_since_decision`, and `me.threads_open` = 0 | me | `review` | You reviewed again and your threads are resolved, but your request for changes still blocks; a comment does not lift it |
| 7 | Nudged about your block | `me.decision` is `changes_requested`, not `me.new_commits`, `me.hours_since_review` ≥ 24, and `me.hours_since_nudge` < 48 | others: `pr.author` | | Nudged; waiting for the author |
| 8 | Time to nudge about your block | `me.decision` is `changes_requested`, not `me.new_commits`, and `me.hours_since_review` ≥ 24 | me | `nudge` | Nothing new since you requested changes; remind the author |
| 9 | Changes requested | `me.decision` is `changes_requested` | others: `pr.author` | | You requested changes; the author dismisses your review when it is ready for another look |
| 10 | Asked again | `me.reviewed` and `me.requested` | me | `review` | The author asked you to review again |
| 11 | Approved, new commits | `me.decision` is `approved` and `me.new_commits` | others: `pr.author` | | You approved; waiting for it to merge |
| 12 | New commits elsewhere | `me.new_commits`, `me.threads_open` > 0, and `judgement.commits_address_my_comments` is `no` | others: `pr.author` | | New commits leave your {me.threads_open} comments untouched |
| 13 | New commits for you | `me.new_commits`, `me.threads_open` > 0, and `judgement.commits_address_my_comments` is `yes` | me | `review` | Re-review: new commits respond to your comments |
| 14 | New commits | `me.new_commits` | me | `review` | Re-review: new commits since your review |
| 15 | The author awaits you | `me.threads_on_me` > 0 and `judgement.author_awaits_me` is `yes` | me | `address_comments` | The author is waiting for your view on {me.threads_on_me} comments |
| 16 | Resolved without approval | `me.decision` is `none` and `me.threads_open` = 0 | me | `review` | None of your threads is open; review again |
| 17 | Approved, others block | `me.decision` is `approved` and `pr.blocked_by` is not empty | others: `pr.blocked_by` | | You approved; {pr.blocked_by} still request changes |
| 18 | Approved, more needed | `me.decision` is `approved` and `pr.approvals` < `pr.required_approvals` | others: `pr.requested` | | You approved; it needs another approval |
| 19 | Approved | `me.decision` is `approved` | nobody | | You approved |
| 20 | Nudged | `me.hours_since_review` ≥ 24 and `me.hours_since_nudge` < 48 | others: `pr.author` | | Nudged; waiting for the author |
| 21 | Time to nudge | `me.hours_since_review` ≥ 24 and `judgement.comments_blocking` is `yes` | me | `nudge` | No progress since your review, and your comments block it |
| 22 | Waiting for the author | always | others: `pr.author` | | Waiting for the author on {me.threads_open} comments |

Passed-over labels: 1 "not a draft", 5 "reviewed before", 9 "no request for changes", 10 "not asked again", 14 "no new commits", 19 "not approved".

### How to read them

- **Handing the ball back is explicit.** While a reviewer's request for changes stands, the pull request is the author's — with every comment answered, and even after asking again (author 5, reviewer 9). The author hands it back by dismissing the review, saying what changed, and asking again. With nothing to dismiss — the reviewer only commented — asking again hands it back (author 8, reviewer 10). A reply, including one that disagrees and leaves the thread open, never hands the ball back by itself.
- **An approval settles the approver's own threads.** Once a reviewer approves, their open threads no longer wait on anyone, and do not keep the author from merging.
- **Mechanical work comes first for authors.** Conflicts and failing checks come before comments: answering comments on a pull request that conflicts or fails means coming back after fixing it.
- **Chain position matters only where it must.** An approved pull request waits for the ones below it (author 10), and a first review deep in a chain can wait (reviewer 4). Answering comments, fixing checks, and resolving conflicts can happen anywhere in a chain, in parallel. Position 2 is not downgraded: it is usually next.
- **Judgements are all on the reviewer side**, and each sits in front of a rule that decides without it. Reviewer 12 and 13 share one question and fall through to 14; reviewer 15 and 21 fall through to the rules after them.
- **Without a model**, reviewer 12, 13, 15, and 21 never match: new commits always ask for a re-review (reviewer 14), an author's reply reaches the reviewer when the author asks again, and a reviewer who only commented is never told to nudge.
- **Someone always holds the ball.** For one author and one reviewer, in every combination of review, thread, new commits, and review request, with or without a model, at least one of them holds the ball, and they never wait on each other.

## Editing and publishing

1. **Draft.** A member with the rules permission starts a draft from the published version. Several drafts can exist at once.
2. **Edit.** Add, remove, and reorder rules; edit conditions in the structured editor or as expressions; edit conclusions.
3. **Check on save.** Every condition and reason template is compiled and type-checked against the field catalog, and errors are shown on the rule. A draft with errors can be saved, but not published.
4. **Preview.** The draft is evaluated against the current facts of every open pull request, using recorded judgements only — the preview never calls the model, and shows a judgement with no recorded answer as "would ask the model". The dashboard lists every verdict that would change, before and after.
5. **Examples.** Members can pin a past verdict as an example: its inputs, judgements, and conclusion. Before publishing, every pinned example is evaluated again; an example whose conclusion would change must be confirmed as intended, or the draft changed.
6. **Publish.** The draft becomes the next version, atomically. Every open pull request is evaluated again, and the new verdicts reference the new version. The publish records who did it, and when.
7. **Roll back** by publishing an earlier version's content as a new version.

A draft can be published only when:

- every condition and reason template compiles and type-checks;
- each role's list ends with a catch-all rule;
- every conclusion is complete — an action allowed for the role when the ball is `me`, someone to wait on when it is `others`, and "may become a task" only on actions that can be tasks;
- every judgement names a question asked for that role;
- every pinned example is confirmed.

## Rules

- **R1. First match wins.** Each role's list is ordered; the first rule whose condition matches decides, and later rules are not evaluated.
- **R2. Every list ends with a catch-all**, so every member with a role gets a verdict.
- **R3. One language.** Structured conditions compile to CEL, and there is one evaluator.
- **R4. Conditions read only the field catalog.** Not where facts came from, not task states, not other members' verdicts.
- **R5. Evaluation is total.** A comparison on an unset optional field is false; a run-time error, an exceeded cost limit, or an unknown judgement makes a condition not match. Errors are recorded, and the rule is flagged.
- **R6. Only drafts that pass every check are published.**
- **R7. Published versions are immutable.** Every verdict references the version that produced it.
- **R8. Expression rules are visibly marked.**
