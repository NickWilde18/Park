# 4. Verdicts

Every unit of work has one ball. A **unit** is a group, or a pull request in no group. At any moment its ball is on exactly one side — the author side or the reviewer side — held by the people on that side who must act next. Park's conclusion for a unit — where its ball is, who holds it, what each holder should do, and why — is the unit's **verdict**.

## Responsibilities

- Work out each person's role on each pull request.
- Evaluate the published rules for each person's part.
- Place each unit's ball from those parts, the same way every time.
- Store each new verdict with everything needed to explain it, and hand changes to tasks and to the dashboard.

## Data

| Record | Holds |
|---|---|
| `verdict` | One conclusion for one unit (below); current, or superseded by a newer one |
| `judgement` | A model's answer: question, fingerprint, answer, confidence, reason, provider kind, model name, time taken, time. Failed calls are recorded as failed attempts, not answers |
| `member_action` | Actions of members that rules can read — so far, marking a pull request as nudged |

## Roles

A role is a function of (person, pull request), computed from facts — not a column on the member. People here are forge accounts, members of Park or not, so an outside reviewer can hold a ball too.

| Role | The person holds it when |
|---|---|
| Author | Their account opened the pull request |
| Reviewer | They are not the author, and a review is requested from them — directly or through a team they belong to — or they have submitted a review |

A person holds at most one role on a pull request. People with neither role have no part in it.

## Parts

For each pull request of a unit, Park evaluates the author list for its author, and the reviewer list for each of its reviewers ([Rules](05-rules.md)). Each evaluation gives that person's **part** on that pull request: a kind, and with it an action, targets, priority, reason, and possibly a reminder.

| Kind | Means |
|---|---|
| `act` | This person has something to do |
| `hold` | The ball is this person's, but they wait on something that is not a person: their own draft, running checks. Authors only |
| `finish` | Only the last step is left: merging, or waiting for the pull requests below to merge. Authors only |
| `idle` | Nothing for this person now |

A part says what one person has to do. It never says who else must act: that follows from placing the ball.

## Placing the ball

Once per unit, from every part in it:

1. **The author side**, if any author has an `act` or `hold` part. The work is the author's until it is handed back as a whole.
2. Otherwise, **the reviewer side**, if any reviewer has an `act` part.
3. Otherwise, **the author side**, if any author has a `finish` part: only merging, or waiting for the pull requests below, is left.
4. Otherwise, **the fallback**: the author side, with the verdict flagged, because the rules found nothing for anyone to do. The default rule set never needs it.

The **holders** are the people on that side whose parts placed the ball, and their parts are their actions, pull request by pull request. Being on the holding side is not holding the ball: a reviewer who has approved, or has nothing to do, holds nothing, even while the ball is on the reviewer side. Parts on the other side are recorded, but hold nothing either: a reviewer's first review of `web#7` waits while the author still has work on `api#12`.

Placement treats every judgement as unknown, so it never waits on the model, and no model answer can move a ball.

| State of a unit | Its ball |
|---|---|
| Ana fixes failing checks on `api#12`; `web#7` waits for Ben's first review | Author side: Ana |
| `api#12` asks Ben and Carol for a first review | Reviewer side: Ben and Carol |
| Two approvals required: Ben approved; Carol has not reviewed yet | Reviewer side: Carol only. Ben's approval is his last word; he waits too |
| Ben approved, and Ana asks him to review again | Reviewer side: Ben, because the author asked him |
| `api#12` by Ana and `web#7` by Carol both have comments to answer | Author side: Ana and Carol |
| Ben approved both | Author side: Ana, to merge |

## What a verdict holds

| Field | Meaning |
|---|---|
| Unit | The group, or the pull request |
| Side | `author` or `reviewer` |
| Holders | The people holding the ball, each with their actions, targets, priorities, and reasons, pull request by pull request |
| Since | For each holder, when they started holding it |
| Parts | Every person's part on every pull request, with its path through the rules: each rule tried, and its outcome — matched, not matched, unknown judgement, or error |
| Reminders | Reminders offered to people waiting on the holders |
| Fallback | Whether placement needed the fallback |
| Judgements | The judgements used, with their answers and confidence |
| Inputs | The value of every field for every person, the evaluation time, and the fact sequence numbers they were computed from |
| Rule set | The published version that produced it |

## Actions

| Action | Role | Meaning | Can be a task |
|---|---|---|---|
| `resolve_conflicts` | Author | Make the pull request mergeable again | Yes |
| `fix_checks` | Author | Make the failing required checks pass | Yes |
| `address_comments` | Author | Answer or resolve the review threads that wait on you | Yes |
| `request_review` | Author | Hand the pull request back — dismiss a standing request for changes, saying what changed, and ask again — or ask for a first review | Yes |
| `review` | Reviewer | Review the pull request, for the first time or again | Yes |
| `merge` | Author | Everything is in place; merge it | No — only people merge |

What makes each task-capable action done is defined in [Tasks](06-tasks.md).

A **reminder** is not an action. A part can offer one to a person who is waiting on the holders: nudge them. The member marks it done on the dashboard, and the mark is a member action that rules can read. A reminder never moves the ball.

## Flow: evaluate a unit

Runs when, for this unit: the facts of any of its pull requests change; its membership changes; a judgement about it is recorded; a member acts on it, such as marking it nudged; a new rule set is published; or an account link or team membership changes someone's role. An hourly pass also re-evaluates every open unit, because rules can read elapsed time. When a pull request moves between groups, both units are evaluated.

1. **Load** the unit's pull requests with their facts and chains, and fix the evaluation time.
2. **Find each pull request's people** and their roles.
3. **Build each person's input**: the fields of the [field catalog](05-rules.md#the-field-catalog), computed from the facts for that person.
4. **Evaluate each person's list** with every judgement unknown. For each rule:
   1. If the condition is true, the rule matches. If it is false, go to the next rule.
   2. A condition that depends on a judgement does not match now; the path records it as "unknown judgement".
   3. A condition that fails at run time, or exceeds its cost limit, does not match. The error is recorded in the path, and the rule is flagged on the dashboard.
5. **Place the ball** (above).
6. **Refine.** For each holder, and for each waiting person whose path passed over a reminder rule on an unknown judgement, get those judgements (next flow) and evaluate that person's list again. A refined part is kept only if its kind is unchanged. Otherwise the part from step 4 stands, and the rule is flagged.
7. **Compare** with the unit's current verdict. If the side, the holders with their actions and reasons, and the reminders are all unchanged, store nothing.
8. **Otherwise store** a new verdict and make it current, and queue work for tasks and the dashboard.

When every pull request of a unit is merged or closed, its verdict is closed out, with the reason "Merged" or "Closed".

## Model judgement

Some questions have no reliable structural answer. Rules name them as **judgements**; the model answers them, and Park records each answer as a fact. Judgements only sharpen what a holder is told, or decide whether someone waiting is offered a reminder.

### Questions

| Question | Asked for | Asks | Used to | Why a rule cannot decide |
|---|---|---|---|---|
| `commits_address_my_comments` | Reviewer | Do the commits pushed since my latest review respond to my unresolved comments? | Tell a reviewer who is asked again where to look | Outdated markers mislead both ways: the author may touch my lines while fixing someone else's comment, or fix my concern in a caller |
| `comments_blocking` | Reviewer | Does any of my unresolved comments raise a real problem, rather than a matter of taste? | Decide whether a waiting reviewer is offered a reminder | A count cannot tell a naming nit from a concurrency bug |

Each question defines, in code, what it reads, the prompt it sends, and its answers — `yes` or `no` — each returned with a confidence between 0 and 1 and a one-sentence reason.

A question's **fingerprint** is a hash of the question and its version, the person it is asked for, and the identity of everything it reads: comment IDs with their last edit times, and commit IDs. Anything that could change the answer changes the fingerprint.

### Flow: get a judgement

1. **Compute the fingerprint.**
2. **If an answer is recorded** for it, use it. No model call.
3. **If no model provider is configured**, the judgement is unknown.
4. **If the same fingerprint is already being asked**, wait for that call, within the timeout.
5. **Ask.** Take a slot under the concurrency limit, fetch the texts the question reads from the forge, send the question, and wait up to the timeout. Time spent waiting for a slot counts toward it.
6. **Validate** the answer against the question's schema. An invalid answer counts as an error.
7. **Record** the judgement, and continue the refinement with it.
8. **Below the question's confidence threshold**, the recorded answer counts as unknown. The path shows it — "asked again · model unsure (0.55)" — so a reader can tell the reason came from the rules, not from the model.
9. **On a timeout**, the refinement continues with the judgement unknown. The call runs on in the background; when its answer arrives, it is recorded and the unit is evaluated again. **On an error**, the judgement is unknown, and the question is retried after a backoff.

### What this gives

- **The model never moves a ball.** Placement ignores judgements, and a refinement cannot change a part's kind. With no provider, every ball is exactly where it would be with one; reasons are plainer, and no reminder depends on a judgement.
- **No flicker.** A verdict is stored with its refinements done; the dashboard does not show one reason and then switch to another, except when a call timed out.
- **Reproducible.** Replaying a verdict reads the recorded judgements and never calls the model again. Rule logic is tested with recorded judgements, without network mocks.
- **Bounded cost.** Questions are asked only for holders and possible reminders, once per distinct input.
- **Visible provenance.** Every verdict lists the judgements it used, so the dashboard shows which reasons came from a model, and with what confidence.

The cost: storing a verdict can wait on the model, up to the timeout, and concurrency is capped so a burst of new questions cannot stall everything.

### What is sent

Before a model provider is enabled, the settings page lists each question and what it sends. Verdict questions send only the review threads and commits of the pull request they are about; grouping questions send titles, branch names, and references ([Grouping](03-grouping.md)). Nothing sent includes credentials.

## Rules

- **V1. Verdicts are pure functions** of their inputs, rule set version, and recorded judgements. Replaying one gives the same conclusion.
- **V2. One ball per unit**, on exactly one side, with at least one holder. There is no other record of whose turn it is.
- **V3. Precedence places the ball**: author work, then reviewer work, then the author's last step, then the fallback.
- **V4. Parts are per person per pull request**, from at most one role; the author role takes precedence. No role, no part.
- **V5. The model never moves a ball.** Placement treats every judgement as unknown; a refinement may change a part's action, reason, priority, or reminder, never its kind.
- **V6. Unknown never matches.** A condition that depends on an unknown judgement — no provider, a timeout, an error, or low confidence — does not match.
- **V7. Tasks are not an input.** Verdicts read facts only. While an executor works, the ball stays where the facts put it, and the task's state is shown beside it.
- **V8. Verdicts are immutable.** A new one is stored only when something a person sees changes.
- **V9. Every verdict explains itself**, with every part's path, its judgements, its inputs, and its rule set version.
- **V10. Queues are derived.** A member's queue is the units whose ball they hold; their waiting list is the units they have a part in, where the ball is with someone else.

## Defaults

| Setting | Default |
|---|---|
| Model call timeout | 20 seconds |
| Concurrent model calls | 4 |
| Confidence threshold | 0.7, per question |
| Re-evaluation for elapsed time | Hourly |
| Retry after a model error | 1 minute, doubling up to 1 hour |
