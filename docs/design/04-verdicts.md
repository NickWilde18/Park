# 4. Verdicts

For every open pull request and every member involved in it, Park concludes who holds the ball, what should be done, and why. That conclusion is a **verdict**.

## Responsibilities

- Work out each member's role on each pull request.
- Evaluate the published rules for that role, asking the model when a rule depends on a question only a model can answer.
- Store each new conclusion with everything needed to explain it.
- Hand changed verdicts to tasks and to the dashboard.

## Data

| Record | Holds |
|---|---|
| `verdict` | One conclusion for one member on one pull request (below); current, or superseded by a newer one |
| `judgement` | A model's answer: question, fingerprint, answer, confidence, reason, provider kind, model name, time taken, time. Failed calls are recorded as failed attempts, not answers |
| `member_action` | Actions of members that rules can read — so far, marking a pull request as nudged |

## Roles

A role is a function of (member, pull request), computed from facts — not a column on the member. The same person is the author of one pull request and a reviewer of another.

| Role | The member holds it when |
|---|---|
| Author | One of their linked accounts opened the pull request |
| Reviewer | They are not the author, and a review is requested from them — directly or through a team they belong to — or they have submitted a review |

A member holds at most one role on a pull request: the author of a pull request never also gets a reviewer verdict on it. A member with neither role gets no verdict on that pull request, though team views still show it.

## What a verdict holds

| Field | Meaning |
|---|---|
| Ball | `me`: the member must act. `others`: waiting on other people. `nobody`: no one needs to act now — a draft, checks still running, an optional review |
| Action | What the member should do, when the ball is theirs (below) |
| Waiting on | The accounts the member is waiting for, when the ball is with others. They may be people outside Park |
| Priority | `normal`, or `low` for work that can wait |
| Reason | One sentence, rendered from the rule's template: "Ben requested changes; 3 comments wait on you" |
| Since | When the ball, action, and waiting-on first took their current values |
| Path | Every rule tried, in order, with its outcome: matched, not matched, unknown judgement, or error |
| Judgements | The judgements it used, with their answers and confidence |
| Inputs | The value of every field the rules could read, the evaluation time, and the fact sequence numbers they were computed from |
| Rule set | The published version that produced it |

## Actions

| Action | Role | Meaning | Can be a task |
|---|---|---|---|
| `resolve_conflicts` | Author | Make the pull request mergeable again | Yes |
| `fix_checks` | Author | Make the failing required checks pass | Yes |
| `address_comments` | Either | Answer or resolve the review threads that wait on you | Yes |
| `request_review` | Author | Ask for review, or ask blocking reviewers to review again | Yes |
| `review` | Reviewer | Review the pull request, for the first time or again | Yes |
| `merge` | Author | Everything is in place; merge it | No — only people merge |
| `nudge` | Either | Remind the people it is waiting on | No |

What makes each task-capable action done is defined in [Tasks](06-tasks.md).

## Flow: evaluate a pull request

Runs when, for this pull request: its facts change; its group or chain changes; a judgement about it is recorded; a member acts on it, such as marking it nudged; a new rule set is published; or an account link or team membership changes someone's role. An hourly pass also re-evaluates every open pull request, because rules can read elapsed time.

1. **Load** the pull request's facts, chain, and group, and fix the evaluation time.
2. **Find the members with a role**, and each one's role.
3. **Build each member's input**: the fields of the [field catalog](05-rules.md#the-field-catalog), computed from the facts for that member.
4. **Evaluate that role's rules in order.** For each rule:
   1. Evaluate the condition with every judgement left unknown.
   2. If it is true, the rule matches. If it is false, go to the next rule.
   3. If the result depends on a judgement, get the judgement (next flow) and evaluate again. A judgement that is still unknown makes the condition not match, however the condition is phrased.
   4. A condition that fails at run time, or exceeds its cost limit, does not match. The error is recorded in the path, and the rule is flagged on the dashboard.
5. **Conclude** from the first rule that matched. Each role's list ends with a catch-all, so one always does.
6. **Compare** with the member's current verdict. If ball, action, waiting-on, priority, and reason are all unchanged, store nothing.
7. **Otherwise store** a new verdict and make it current. It keeps the previous verdict's `since` when only the priority or reason changed. Queue work for tasks and the dashboard.
8. **Close out** the verdicts of members who no longer hold a role, with a final verdict: ball `nobody`, reason "No longer involved".

When a pull request is merged or closed, every current verdict on it is closed out the same way, with the reason "Merged" or "Closed".

## Model judgement

Some questions have no reliable structural answer. Rules name them as **judgements**; the model answers them, and Park records each answer as a fact.

### Questions

| Question | Asked for | Asks | Why a rule cannot decide |
|---|---|---|---|
| `commits_address_my_comments` | Reviewer | Do the commits pushed since my latest review respond to my unresolved comments? | Outdated markers mislead both ways: the author may touch my lines while fixing someone else's comment, or fix my concern in a caller |
| `author_awaits_me` | Reviewer | Where the author replied last, is the author waiting for my view, rather than agreeing to change it? | "Will do" and "I'd keep it; what do you think?" look the same in every structured field |
| `comments_blocking` | Reviewer | Does any of my unresolved comments raise a real problem, rather than a matter of taste? | A count cannot tell a naming nit from a concurrency bug |

Each question defines, in code, what it reads, the prompt it sends, and its answers — `yes` or `no` — each returned with a confidence between 0 and 1 and a one-sentence reason.

A question's **fingerprint** is a hash of the question and its version, the member it is asked for, and the identity of everything it reads: comment IDs with their last edit times, and commit IDs. Anything that could change the answer changes the fingerprint.

### Flow: get a judgement

1. **Compute the fingerprint.**
2. **If an answer is recorded** for it, use it. No model call.
3. **If no model provider is configured**, the judgement is unknown.
4. **If the same fingerprint is already being asked**, wait for that call, within the timeout.
5. **Ask.** Take a slot under the concurrency limit, fetch the texts the question reads from the forge, send the question, and wait up to the timeout. Time spent waiting for a slot counts toward it.
6. **Validate** the answer against the question's schema. An invalid answer counts as an error.
7. **Record** the judgement, and continue the evaluation with it.
8. **Below the question's confidence threshold**, the recorded answer counts as unknown. The path shows it — "new commits · model unsure (0.55)" — so a reader can tell the conclusion came from the rules, not from the model.
9. **On a timeout**, the evaluation continues with the judgement unknown. The call runs on in the background; when its answer arrives, it is recorded and the pull request is evaluated again. **On an error**, the judgement is unknown, and the question is retried after a backoff.

### What this gives

- **No flicker.** A verdict that needs a judgement is computed with the answer in hand; the dashboard never shows one conclusion and then switches to another.
- **Optional by construction.** With no provider, every judgement is unknown, and the rules after it decide. No feature flag is needed.
- **Reproducible.** Replaying a verdict reads the recorded judgements and never calls the model again, so the same inputs always give the same conclusion. Rule logic is tested with recorded judgements, without network mocks.
- **Bounded cost.** A judgement is paid for once per distinct input, not once per evaluation.
- **Visible provenance.** Every verdict lists the judgements it used, so the dashboard shows which conclusions came from a model, and with what confidence.

The cost: an evaluation can wait on the model, up to the timeout, and concurrency is capped so a burst of new questions cannot stall everything. During a model outage, verdicts that need a judgement come from the rules, and switch once answers arrive.

### What is sent

Before a model provider is enabled, the settings page lists each question and what it sends. Verdict questions send only the review threads and commits of the pull request they are about; grouping questions send titles, branch names, and references ([Grouping](03-grouping.md)). Nothing sent includes credentials.

## Rules

- **V1. Verdicts are pure functions** of their inputs, rule set version, and recorded judgements. Replaying one gives the same conclusion.
- **V2. One verdict per member per pull request**, from at most one role; the author role takes precedence.
- **V3. No role, no verdict.**
- **V4. Tasks are not an input.** Verdicts read facts only. While an executor works, the ball stays where the facts put it, and the task's state is shown beside the verdict, never folded into it.
- **V5. The model is asked only when a rule's outcome depends on the answer**, at most once per fingerprint, except for retries after errors.
- **V6. Unknown never matches.** A condition that depends on an unknown judgement — no provider, a timeout, an error, or low confidence — does not match.
- **V7. Verdicts are immutable.** A new one is stored only when the conclusion or its reason changes.
- **V8. Every verdict explains itself**, with its path, judgements, inputs, and rule set version.
- **V9. The member's queue is derived**: it is their current verdicts with the ball on them. Nothing else puts work in a queue.

## Defaults

| Setting | Default |
|---|---|
| Model call timeout | 20 seconds |
| Concurrent model calls | 4 |
| Confidence threshold | 0.7, per question |
| Re-evaluation for elapsed time | Hourly |
| Retry after a model error | 1 minute, doubling up to 1 hour |
