# 10. Design log

Why the design is the way it is, and what was set aside. The chapters describe how Park works; this log keeps the reasons.

Entries fall into three kinds:

- **Decided** — settled while the design was discussed.
- **Ported** — lessons carried over from the predecessor, ballpark, where they were learned on a real team's pull requests.
- **Proposed** — decisions made while the chapters were written in detail. They are reflected in the chapters, but have not been reviewed yet.

## Decided

### D1. Verdicts are computed from facts alone

Most of the predecessor's confusion came from four layers reading each other: verdicts read raw collection results, a state machine read verdicts, the display read the state machine. When one lagged, the board contradicted itself — a session marked done while its state still said "in progress". In Park a verdict is a pure function of facts and rules, and stores the inputs it was computed from, so "why was this judged that way three days ago?" needs no replay. Agent Orchestrator reaches the same principle independently: it stores durable facts and computes display status when it is read.

Set aside: keeping only current facts, which is simpler but cannot answer "why" — the question people ask most; and pure event sourcing, which answers it but makes every verdict walk history, and leaves out-of-order delivery, duplicates, and snapshots to be handled by hand.

### D2. Webhooks and polling share one refresh path

Forges guarantee neither delivery nor ordering. Both paths therefore fetch the full current state and diff it against the stored facts, instead of trusting a payload: a duplicate delivery then costs nothing, and a late one at most an empty diff. Nothing downstream can tell the two paths apart.

### D3. Stable identifiers only

Usernames can be renamed; a stored login silently stops matching, while a numeric account ID does not. URLs change with hosts and routes. Facts store forge IDs; URLs are built when rendered.

### D4. Grouping resolves assertions by authority

Groups come from several sources of different reliability — members, bridges, structure, and the model — and one claim wins per pull request, the highest authority first. Claims are never unioned: union is transitive, and a few weak links glue unrelated features together.

Set aside: structural signals plus manual grouping alone, which miss groupings that exist only in people's conversations — in the predecessor, the most accurate source of all; and model-first clustering corrected by people, because clustering output shifts from run to run, and every reshuffle ripples into verdicts, tasks, and notifications.

### D5. Group identity is independent of membership

The predecessor derived a group's ID from its member list, so that a membership change would be noticed. But every regrouping then looked like one group vanishing and another appearing: cooldowns reset, pull requests that were still open were announced as merged, and the same alert went out again and again. Park gives groups their own IDs and records membership changes explicitly.

### D6. The model places one pull request at a time, and never asks twice

The predecessor re-clustered every unexplained pull request together whenever that set changed: one new pull request re-rolled everyone's grouping, and the same question asked twice could get a different answer. Park asks one question for one pull request — which existing group, a new one, or none — records the answer by fingerprint, and never re-decides a placement because other pull requests came and went.

### D7. Model judgements are synchronous and recorded

When a rule depends on a judgement with no recorded answer, the model is called there and then, with a timeout, and the answer is recorded against the exact inputs it saw. A two-pass scheme — show the rule-only conclusion at once, switch when the model answers — was rejected: conclusions would flicker, and every switch would ripple into tasks.

### D8. Model judgement is optional

Every judgement has a path that does not need it, so Park works fully without a model provider; configuring one only sharpens a few conclusions.

### D9. Roles are a function, not a column

The same person is the author of one pull request and a reviewer of another, so a member has no role column. Roles are derived per pull request, and verdicts are computed per member.

### D10. Structured conditions, with CEL for the rest

Every branch of the predecessor's decision tree had the shape `(field, operator, value)`, so that is the default, edited with drop-downs. Expressions cover the rest, and structured conditions compile to them, so there is one engine. CEL is a published language designed for user-written policy conditions: it type-checks when a rule is saved, not when it first runs, and enforces a cost limit, so a slow rule is cut off instead of stalling an evaluation. It brings protobuf-related dependencies, acceptable for a library used in one place.

An escape hatch, once open, tends to be used for everything; the structured editor must cover the common fields well, and expression rules are visibly marked.

### D11. Rules are an ordered list, and the first match wins

The predecessor's proven semantics. They draw naturally as a tree, and explain naturally why later rules were never reached. Rule sets are versioned, so that after a bad edit past verdicts can still be explained, and anyone can see who changed what.

### D12. No free-form pipeline editor

A low-code canvas for wiring collectors, judgement steps, and executors was considered and set aside. Each stage keeps invariants — facts are the only input, a task is verified before it counts, grouping resolves deterministically — that a free-form graph would let anyone break, and what actually varies from team to team — sources, rules, thresholds, automation — is already editable. The dashboard draws the fixed pipeline and lets each box be configured.

### D13. Tasks are a queue with verified outcomes

Soft claims with optional reports keep Park stateless, but leave it unable to tell apart an executor still working, one that finished but skipped part of the job, and one that died. In practice an agent occasionally reports completion while leaving comments unanswered, and the pull request then sits in "needs work" with nothing saying why; the goal is for that never to go unnoticed. A plain task queue is not enough either: it records the executor's word, and a false "done" becomes a green checkmark on a stuck pull request. So every action declares a post-condition over forge facts, and a task is done only when the facts satisfy it.

### D14. One task per unit, one active task per group

A unit is a feature group, or a stacked chain, not a single pull request: the predecessor at first started work on single pull requests inside a feature group, and two sessions ended up editing one feature at once.

### D15. Executors initiate every exchange

Some executors cannot be reached: Agent Orchestrator's daemon binds to `127.0.0.1` by design. Executors claim, send heartbeats, and report; Park never calls them.

### D16. One member, many identities; local accounts included

Teams start in different places — a laptop, a forge organization, a company single sign-on — and must be able to add the others later without recreating people. Local accounts keep self-hosting free of dependencies. Forge links must be verified, or anyone could read anyone's queue.

### D17. Go and React, with one contract

Park is a long-running server with background workers, queues, and streams, and self-hosters are best served by one binary with the frontend embedded. The dashboard sits behind sign-in, so server-side rendering would buy little, and CEL's reference implementation is in Go. With code largely written with AI assistance, the choice turns on runtime and deployment rather than on how fast the code is written. Full-stack TypeScript, with Next.js, was the main alternative considered.

### D18. No forge adapter layer until a second forge exists

The schema and vocabulary are forge-neutral from the start, which is cheap; an abstraction layer designed around one implementation would be guesswork. It is introduced when a second forge arrives.

## Ported

### D19. The forge's summary review decision is not used

It stays at "changes requested" after the author has answered every comment but not yet asked for another review, keeping the ball on the author while it is really with the reviewer; and it goes back to "review required" on every push, pulling reviewers who already reviewed back into their queues. Individual reviews and thread state are stable.

### D20. Stacked pull requests are never grouped structurally

Stacked pull requests reference each other constantly, yet are separate pieces of work, each reviewed and merged on its own. Grouped into one row, a group shows only its worst state and hides the rest. People can still group them when they are reviewed together.

### D21. External assertions listing many pull requests are not applied

In the predecessor's data, messages that linked pull requests for review linked two (front and back end) or three (three repositories); messages with more — sixteen, six — were progress reports or epic overviews. Applying those glued whole chains into one row.

### D22. The model sees only true residue, and no descriptions

The first version asked the model about every pull request without a chat thread; most of those the structural rules could already group, and the model only repeated them. Measured on 63 open pull requests: asked about 24, it formed 3 groups, none new; asked only about the 31 that nothing else grouped, it formed 1 group — and that one was new. Descriptions are left out: thousands of characters are costly, and pull the model toward the wrong details.

### D23. The author never gets a reviewer verdict on their own pull request

The predecessor showed authors a reviewer verdict on their own pull requests, which read as work that was not theirs to do.

### D24. Mechanical work first; chain position only where it must

Authors fix conflicts and failing checks before answering comments, or they answer twice. Only the root of a chain can merge: the predecessor once reported non-root pull requests as ready to merge, and they jumped the queue ahead of the one that could actually move. A first review from the third position on can wait; the second is usually next.

### D25. Bare references stay in their repository

A title saying "redo #57" meant a closed pull request in the same repository; an early version, not finding it among open pull requests, looked elsewhere, found an unrelated #57 in another repository, and drew a dependency that did not exist. Bare numbers are only ever resolved in their own repository, and only the start of a description is parsed, because later text is often logs full of `#N`.

## Proposed

### D26. A judgement is an unknown in a condition

Instead of each judgement rule carrying its own fallback branch, a judgement is an unknown value in a CEL condition: the engine evaluates without it, asks only if the outcome depends on it, and a condition that depends on an unknown judgement does not match, so the next rule decides. This is how the predecessor's tree already worked — "new commits · model unsure" was a branch of its own — and it guarantees the model is never asked a question whose answer would not change anything.

### D27. Only a report, a release, or an expiry ends a lease

Facts can look done while the executor is still finishing — it has answered every thread, but not yet pushed the change it promised — and ending the lease early would let another task start on the same code. The dashboard shows the verdict and the task's state side by side instead.

### D28. Every report is verified, whatever its outcome

An executor's "failed" is as much a claim as its "done", and both are checked against the facts. Verification decides the outcome in every case.

### D29. A task bundles every action a member's unit needs

One task per member per unit holds items of different actions — answer these threads, fix those checks — so a single session sees the whole feature. An executor names the actions it can do, and only claims tasks made of those.

### D30. Asking for review is a task; nudging is a person's action; rules can read elapsed time

The predecessor's tree re-requested reviews and suggested nudges after a day without progress. `request_review` has a checkable post-condition, so it can be a task. A nudge is a message to a colleague, so it stays with people; the member marks it done on the dashboard, and the mark is a fact rules can read. Elapsed-time fields make verdicts depend on the evaluation time, which is recorded in their inputs to keep them replayable, and an hourly pass re-evaluates open pull requests.

### D31. Group names follow the authority order

A manual rename outranks an external title, as manual outranks external everywhere else in grouping.

### D32. Visibility follows the forge

Park shows a repository's pull requests only to members whose linked forge accounts can read it, so that watching a private repository never exposes it to everyone in Park.

### D33. Comment text is fetched when needed, not stored

Facts keep comment IDs, authors, and times; the few questions that need text fetch it from the forge when asked, and fingerprint comments by ID and edit time. Park holds less of other people's words, and nothing is lost: judgements are recorded, and never asked twice.

### D34. The first administrator needs a one-time setup code

Without it, whoever reaches a fresh server first becomes its administrator. The code is printed to the server's log, which only its operator can read.
