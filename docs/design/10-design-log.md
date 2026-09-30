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

Groups come from several sources of different reliability — members, bridges, and structure, with the model only suggesting (D47) — and one claim wins per pull request, the highest authority first. Claims are never unioned: union is transitive, and a few weak links glue unrelated features together.

Set aside: structural signals plus manual grouping alone, which miss groupings that exist only in people's conversations — in the predecessor, the most accurate source of all; and model-first clustering corrected by people, because clustering output shifts from run to run, and every reshuffle ripples into verdicts, tasks, and notifications.

### D5. Group identity is independent of membership

The predecessor derived a group's ID from its member list, so that a membership change would be noticed. But every regrouping then looked like one group vanishing and another appearing: cooldowns reset, pull requests that were still open were announced as merged, and the same alert went out again and again. Park gives groups their own IDs and records membership changes explicitly.

### D6. The model answers for one pull request at a time, and never asks twice

The predecessor re-clustered every unexplained pull request together whenever that set changed: one new pull request re-rolled everyone's grouping, and the same question asked twice could get a different answer. Park asks one question for one pull request — which existing group, a new one, or none — records the answer by fingerprint, and never asks again because other pull requests came and went. Since D47, the answer is a suggestion that a member accepts.

### D7. Model judgements are recorded, and asked in the background

When a rule depends on a judgement with no recorded answer, the model is asked, and the answer is recorded against the exact inputs it saw. At first the call was synchronous, with a timeout: a two-pass scheme — show the rule-only conclusion at once, switch when the model answers — was rejected, because conclusions would flicker and every switch would ripple into tasks. Once a judgement could change only a reason or a reminder, never a part's kind or action (D38), that concern no longer applied, and the owner chose to ask in the background: a verdict is stored at once with the rules' reasons, and a reason sharpens when its answer arrives. The synchronous call, with its timeout and its completion in the background, is gone.

### D8. Model judgement is optional

Every judgement has a path that does not need it, so Park works fully without a model provider; configuring one only sharpens a few reasons and reminders, and offers grouping suggestions.

### D9. Roles are a function, not a column

The same person is the author of one pull request and a reviewer of another, so a member has no role column. Roles are derived per pull request, and each person's part on a pull request is computed from their role; the ball is then placed once per unit from those parts (D37).

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

A unit is a feature group, not a single pull request of one: the predecessor at first started work on single pull requests inside a feature group, and two sessions ended up editing one feature at once. A pull request in no group is its own unit — including each pull request of a chain, since they are reviewed separately — and a task still locks the whole chain it touches.

### D15. Executors initiate every exchange

Some executors cannot be reached: Agent Orchestrator's daemon binds to `127.0.0.1` by design. Executors claim, send heartbeats, and report; Park never calls them.

### D16. One member, many identities; local accounts included

Teams start in different places — a laptop, a forge organization, a company single sign-on — and must be able to add the others later without recreating people. Local accounts keep self-hosting free of dependencies. Forge links must be verified, or anyone could read anyone's queue.

### D17. Go and React, with one contract

Park is a long-running server with background workers, queues, and streams, and self-hosters are best served by one binary with the frontend embedded. The dashboard sits behind sign-in, so server-side rendering would buy little, and CEL's reference implementation is in Go. With code largely written with AI assistance, the choice turns on runtime and deployment rather than on how fast the code is written. Full-stack TypeScript, with Next.js, was the main alternative considered.

### D18. No forge adapter layer until a second forge exists

The schema and vocabulary are forge-neutral from the start, which is cheap; an abstraction layer designed around one implementation would be guesswork. It is introduced when a second forge arrives. This does not mean the fact layer imports the vendor's package: it calls its collector through a thin interface shaped by its own needs, which is not guesswork, and keeps the vendor's name out of the core.

### D35. A standing request for changes keeps the ball with the author

Until the author dismisses a reviewer's request for changes, the author is not ready for that reviewer to look again — whatever else has happened: every comment answered, another review requested, new commits pushed. The author hands the pull request back by dismissing the review, saying what changed, and asking again, and the reviewer waits for that. The predecessor's author rule already said as much: dismiss the request for changes, and ask again.

### D37. One ball per unit, on exactly one side

The ported rules computed "whose turn is it" once per person, from that person's point of view: the predecessor served one person, so its "waiting on others" never had to agree with anyone else's. With several people each deriving it, they disagreed. Evaluating one author and one reviewer over every state found 24 classes of state where the two waited on each other or nobody held the ball; once the lists were aligned to fix that, 31 classes remained where both held it.

So the ball exists once per unit — a group, or a pull request in no group — on exactly one side, held by the people on that side who must act. Each person's rules describe only their own part, and the ball is placed from the parts by precedence: author work first, so the author finishes the whole group before handing it back; then reviewer work; then the author's last step, merging. D41 places an author who only waits — on checks, or on a draft — after reviewer work. Disagreement between people cannot be expressed. The same evaluation, extended to two reviewers and to one or two required approvals, finds no state that needs the fallback, and none where a reviewer whose approval stands holds the ball unless the author asked them again.

Set aside: one ball per group held by exactly one person, which has to pick arbitrarily when several people have work — two reviewers, two authors — and hides the others' work; and one ball per pull request, which lets a group carry several balls at once, so a reviewer could start on one half of a feature while its author still changes the other.

### D47. The model only suggests groups

A model placement at or above its threshold used to put a pull request in a group by itself, which changed the unit — and so the ball — it belonged to. The owner chose suggestions only: a model answer opens a suggestion, and nothing changes until a member accepts it, which records a manual placement. Grouping stays stable, and the model moves no ball anywhere in Park; the price is a click for each placement a member wants.

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

### D27. Facts never end a lease

Facts can look done while the executor is still finishing — it has answered every thread, but not yet pushed the change it promised — and ending the lease early would let another task start on the same code. A lease ends only by a report, a release, or its own end — it expires, its token is revoked, its member is disabled — or by a person dismissing the task. When facts say the work is no longer needed, the task is marked to cancel after the attempt, which is then verified: a review task whose review landed is done, not cancelled. Every lease has its own ID, carried by every call after the claim, so a late report from an earlier lease is never taken for the current one. The dashboard shows the verdict and the task's state side by side.

### D28. Every report is verified, whatever its outcome

An executor's "failed" is as much a claim as its "done", and both are checked against the facts. Verification decides the outcome in every case.

### D29. A task bundles every action a member's unit needs

One task per member per unit holds items of different actions — answer these threads, fix those checks — so a single session sees the whole feature. An executor names the actions it can do, and a claim takes the items it can do, leaving the rest queued; a task nobody claims for too long goes to a person, so work no executor can do is never left waiting unseen.

### D30. Asking for review is a task; a nudge is a reminder; rules can read elapsed time

The predecessor's tree re-requested reviews and suggested nudges after a day without progress. `request_review` has a checkable post-condition, so it can be a task. A nudge is a message to a colleague: it stays with people, and it is a reminder offered to the person waiting, not a move — it never takes the ball from whoever holds it. The member marks it done on the dashboard, and the mark is a fact rules can read. Elapsed-time fields make verdicts depend on the evaluation time, which is recorded in their inputs to keep them replayable, and an hourly pass re-evaluates open units.

### D31. Group names follow the authority order

A manual rename outranks an external title, as manual outranks external everywhere else in grouping. Groups formed by an issue or by references take the issue's title, or the oldest pull request's.

### D32. Visibility follows the forge

Park shows a repository's pull requests only to members whose linked forge accounts can read it, so that watching a private repository never exposes it to everyone in Park. Because one group can span a public and a private repository, visibility is decided pull request by pull request, and nothing derived from a hidden pull request — its checks, reasons, holders, or a name taken from it — is shown. Writes are checked the same way, and a pull request the caller cannot see is refused exactly like one that does not exist.

### D33. Comment text is fetched when needed, not stored

Facts keep comment IDs, authors, and times, and for a review only whether it has a written body; the few questions that need text fetch it from the forge when asked, and fingerprint comments by ID and edit time, checking that the text fetched matches the edit time fingerprinted. Park holds less of other people's words, and nothing is lost: judgements are recorded, and never asked twice.

### D34. The first administrator needs a one-time setup code

Without it, whoever reaches a fresh server first becomes its administrator. The code is printed to the server's log, or taken from the environment where logs are shared more widely, and it expires after an hour.

### D36. Handing the ball back is explicit

Beyond D35: with nothing to dismiss — the reviewer only commented — asking again hands the pull request back, and until then the author has work on it. Neither a reply, nor new commits, hands the ball back by themselves. An approval settles the approver's own threads where the author replied last, so they never keep the author from merging; a thread where the approver spoke last still waits on the author, who answers it first.

Set aside: letting whoever spoke last hold the ball, which ignores the dismissal practice and turns every "will do" into the reviewer's work; and letting new commits bring a reviewer back, as the predecessor did, which pulls reviewers in before the author is done.

### D38. No model answer moves a ball

Placement treats every judgement as unknown, and a refinement changes nothing but a reason or a reminder, so every part has the same kind and action with or without a model; judgements only sharpen what a holder is told, or decide whether someone waiting is offered a reminder. With D47, the model does not place pull requests in groups either, so no model answer moves a ball anywhere. Explicit hand-offs made two of the predecessor's uses redundant: asking whether the author was waiting for the reviewer's view, and whether new commits should bring the reviewer back — asking again answers both. The first question was dropped; the second now only sharpens the reason of a reviewer who is asked again.

### D39. Merging takes the forge's go-ahead and the team's minimum

Counting approvals against the forge's requirement had two gaps. Where the forge requires no approval, a requested reviewer was told their review was optional, and the author to merge at once; where it requires code owners, or rulesets apply to some branches only, the count said "ready" while the forge refused. Park now reads the forge's own merge verdict — whether it can merge, and what blocks it — as a fact, and keeps its own minimum of approvals, one by default, as team policy. A code owner whose approval is required is never optional.

Set aside: re-implementing branch protection, rulesets, and code ownership inside Park, which would drift from the forge.

### D40. A review's written body is something to answer

Only inline threads counted, so a request for changes written only in a review's body read as "every comment answered" at once — and, with automation on, an executor would have dismissed a review nobody had answered. Park now keeps, for each review, whether it has a written body, and such a review waits on the author until the author comments on the pull request or pushes after it. Comments in the pull request's conversation are left out for now ([Open questions](11-open-questions.md)).

### D41. Waiting never outranks work

An author's `hold` — a draft, running checks — used to rank with `act`, ahead of reviewer work. A group whose backend waited for review, while its frontend stayed a draft until the backend merged, then waited on itself; and every push that restarted checks flipped a group's ball between the sides. Now reviewer work comes before an author who only waits. A reviewer still waits for green checks on the pull request they review, by a reviewer rule, and checks that stay pending for hours become the author's to look at.

### D42. Every external ID is qualified by its forge

A forge kind and a numeric account ID do not identify an account once Park talks to two forges of one kind — github.com and a company's own server — whose IDs overlap: the same pair would sign a stranger in as a member. Park keeps a record for each forge, and every external ID — of accounts, repositories, and teams alike — is keyed by it. Sign-in identities are keyed by their provider the same way, and OIDC identities store only their `sub`, with the issuer kept in the provider's settings rather than on every row.

### D43. Grouping resolves a component at once, in a canonical order

Resolving one pull request at a time, with a new anchor joining whichever group held most of its pull requests at that moment, made the outcome depend on the order events arrived in, and could chain two unrelated claims into one group through a pull request they shared. Now each component is resolved as one batch: every pull request's winning claim first, then each anchor, from the pull requests that chose it, in a canonical order.

### D44. Authors who may not dismiss hand back by asking again

A forge lets only people with write access — and, on a protected branch, only those allowed — dismiss reviews, so a contributor from a fork could never hand a pull request back under D35. Park reads whether the author may dismiss; if not, asking the reviewer again hands it back, and the reviewer then holds it, to review again and lift their request.

### D45. A team request asks for a first review

Whether a request to a team counted as a pending request for each of its members was undefined, and one reading sent the ball to a teammate who had never reviewed, or to nobody at all. Now a team request asks its members for a first review; handing back takes a request to the reviewer directly; and a team with no member other than the author counts as no request.

### D46. Chains skip forks and long-lived branches

Matching base branch names against head branch names alone made a release pull request from `develop` the parent of every pull request based on `develop`, and a contributor's fork, pushed from its own `main`, the parent of everything based on `main`. Chain edges now stay within one repository, never start from a fork's branch, and never hang from the default branch, a protected branch, or a configured long-lived name.

Set aside: treating any branch that several pull requests are based on as long-lived, which would break legitimate stacked trees.
