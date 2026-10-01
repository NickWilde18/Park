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

Stacked pull requests reference each other constantly, yet are separate pieces of work, each reviewed and merged on its own. Grouped into one row, a group shows only its worst state and hides the rest. People can still group them when they are reviewed together. A stack that the forge itself records is grouped ([D63](#d63-a-stack-the-forge-records-is-a-group)).

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

### D48. The first administrator signs up with a local password

Sign-in providers are stored in the database and configured by an admin, so none exists before the first admin does; configuring one in the file as well would give providers a second home. The setup code therefore creates a local account, and the admin connects a forge account right after, from the settings.

Set aside: a sign-in provider read from the configuration file for the first start only, which the dashboard could then neither show nor change.

### D49. Collecting within the forge's limits

GitHub charges its hourly limit by what a query asks for, not by what comes back: a pull request's threads with every comment each would cost a hundred times what its threads with their first and latest comments cost. A thread therefore keeps its first comment, which names who started it, and its latest twenty, which name who spoke last and who else took part; a comment in between adds a participant only when it is the only one they wrote. Short polling looks only at the head, the checks, and mergeability, and refreshes fully when those changed. While the limit holds, every refresh through the installation waits for its reset, instead of webhook refreshes going first. Whether the author may dismiss comes from their access level, not from the dismissal restrictions of branch protection, which need administration access to read.

Set aside: paging through every comment of every thread, which would spend the limit on comments that rarely matter.

### D50. An anchor takes only a group no other claim keeps

Resolving claims in a canonical order kept one resolution from depending on the order pull requests were processed in, but not a sequence of resolutions from depending on the order assertions arrived in. A branch claim over `api#1` and `web#2`, and an issue claim over `web#2` and `mobile#3`, made one group of all three when the branch claim came first: the issue claim's anchor was new, followed `web#2` into the branch claim's group, and the branch claim kept `api#1` there. Arriving the other way round, they made two groups. An anchor not mapped yet now takes only a group that no other winning claim keeps; with that rule, every order of arrival gives the same groups for the cases in the checks, and a test runs them all. One resolution covers every open pull request at once, which contains every component and is cheap at the size of one team.

Set aside: re-mapping anchors whenever their winners change, which would move groups under members' feet.

### D51. An issue anchor is named after its oldest pull request

The issue's own title would need collection to fetch issues, which it does not yet. The oldest pull request that closes the issue usually says the same.

### D52. Numbers are whole, and hours whole hours

Conditions compare hours with 2, 24, and 48, and every open unit is evaluated again each hour, so a fraction of an hour changes nothing a rule can see. Whole numbers keep conditions and reasons plain: a reason says "running for 3 hours", and a comparison never mixes integers with decimals.

Set aside: decimal hours, which a threshold under an hour would need, and no rule has one.

### D53. A rule set is one JSON document

The default rule set, a rule-set file kept in git, a draft, and a published version are one document: each role's list, and each rule with its condition and conclusion. Importing a file is posting it. The design checks read it with Python's standard library, and hold the default to its tables rule by rule, term by term. It is read strictly: a key the document does not have is refused rather than dropped, so that a mistyped `remind` cannot quietly change a rule.

Set aside: YAML, which reads more easily, but which the checks could not read without a library.

### D54. Fact sequence numbers are kept pull request by pull request

Each pull request's facts are numbered on their own, so the numbers of two pull requests say nothing about each other, and a unit can hold several. A verdict therefore records the number it read of each pull request, and one is replaced only by a verdict that read no older facts of any of them.

### D55. Links and team changes wait for the hourly pass

Linking an account, or a team's members changing, can change people's roles. In the first release, those changes reach verdicts with the next hourly pass, which evaluates every open unit, rather than by finding the units each change touches.

Set aside: queuing the units of every pull request that names the account or the team, which the hourly pass makes unneeded at the size of one team.

### D56. Verification counts only facts fetched after the attempt

A report arrives while the facts Park holds still predate the work it reports: judged against them, every thread would still be unanswered, and a finished attempt would fail. Verification therefore waits, polling, until every pull request of the task has been fetched since the attempt ended, and only then judges; the deadline runs meanwhile, and pauses while the facts are stale.

### D57. Every failing action spends an attempt

The budget is kept per member, unit, and action, and a task can hold several actions. A failed attempt spends one attempt of each action that failed, and none of those that were satisfied. The task needs a person as soon as any of them has none left, since retrying it would retry that action too.

### D58. Dismissing a task closes it, not its work

A dismissed task is final, but the work behind it is the verdict's: when the unit's verdict next changes and its member still holds the action, it becomes a task again. A member who does not want an action done by an executor turns it off in their automation.

Set aside: remembering dismissals to hold back the same work, which would leave a member's ball with nobody to act on it.

### D59. Notices are written by the changes themselves

A trigger on each table the dashboard shows writes the event stream's notice in the transaction that makes the change. A notice is therefore sent only for a change that was committed, every change has one whichever code path made it, and a change that alters nothing a page shows — a refresh that found nothing new, a lease extended — has none. Streams read the notices table, so a stream on one process hears the changes made on another.

A stream resumes after the last notice it saw, which is sound only if notices become visible in the order they are numbered. Numbers taken as changes are made are not: a change made first and committed last becomes visible after a stream has passed its number. The triggers are therefore deferred to the commit, where each transaction takes one lock before it numbers its notices and holds it until its commit is visible. Committing transactions that write notices wait for one another, briefly; nothing else does. For the same reason a fresh stream begins after the newest notice committed, not the newest numbered, and the dashboard fetches again when its stream opens, since a change committed between its fetch and the stream's start has no notice it will be sent.

Set aside: waking streams with `LISTEN/NOTIFY`, as the work queue's workers are woken. Each stream reads once a second, which is soon enough for a page, and holds no connection of its own between reads. Also set aside: reading by snapshot — the notices whose transactions committed since the stream's last read — which needs no lock, but makes the resume point a snapshot rather than a number.

### D60. An executor fences its work with its own heartbeats

The work is an agent's session, which writes to the forge on its own: a heartbeat before each of those writes would need the agent to send it, or to be wrapped so that something else could. So the executor sends heartbeats on its own short rounds while the session works, and stops the session as soon as one says the lease is gone. Nothing is asked of the agent, so any agent will do. The cost is a round: a session can act for that long after its lease has ended, 15 seconds in park-executor, and verification still judges what it did.

Set aside: a heartbeat before each of the agent's writes, through a hook before each of its commands or wrappers for git and the forge's command-line tool. Each binds the executor to one agent's internals, and misses a write the agent makes another way.

### D61. A bridge proposes the groups it is unsure of

A thread that links many pull requests is sometimes one large feature and sometimes several features announced together. A cap on an assertion's size treats both alike: it skips a real unit as readily as it skips a progress report. A bridge can work out which, and the owner chose to have it ask a model; but a model's answer may be wrong, and since D47 no model answer groups anything by itself. So a bridge posts what it works out as a proposal — named groups of pull requests under the thread's anchor — which changes nothing until a member accepts it, as proposed or edited, and accepting records manual placements. The thread's text goes to the model from the bridge and never through Park, which keeps only the IDs the bridge gave. A thread the bridge finds to be one group within the cap is still an assertion, applied as before.

Set aside: Park asking the model, with the thread's text sent along with the assertion. That would put a chat's text through an API that takes only IDs. Also set aside: a proposal as an assertion left unapplied until a member applies it. That mixes claims and suggestions in one record, and a model that splits a thread differently on another round would re-anchor its groups.

### D62. The board is read in one request, as the rules read it

A table of every open unit needs, for each pull request, values that only its own facts hold: the threads waiting on the viewer, the open threads, the viewer's review. Joining the existing lists in the browser would ask for each pull request on its own. `GET /board` answers in one request instead, and reads those values from the current verdict's inputs, so that the board shows what the rules saw when they placed the ball. The table is plain, drawn by the dashboard's own code.

Set aside: joining the lists in the browser, which leaves those columns out or asks once per pull request. Also set aside: a table library, whose resizing, grouping, and multi-column sorting are more than the board needs, for a dependency.

### D63. A stack the forge records is a group

A chain is still not a group (D20): base branches that happen to line up may be refactors that are each reviewed on their own. A stack that the forge records is another matter. Its author made it, putting the pull requests in it in order as one series, and a member who made one expects its pull requests to be one unit of work. So a forge's stack is a structural signal, the strongest, and the only one that pairs pull requests of a chain. Members' placements and bridges' threads still outrank it, and a member can take a pull request out of it.

Joining a stack does not move a pull request's update time: a stack made after its pull requests were last updated leaves their update times as they were. Polling therefore lists each pull request's stack with its update time, and refreshes one whose stack differs from the stored one. A forge of the same kind may record no stacks — an older server's API has no such field, and asking for it fails the whole query — so whether a forge does is asked of it first.

Set aside: grouping every chain, which the predecessor came to do once its board gave each pull request a row of its own; a chain of separate refactors would become one unit, with one ball. Also set aside: binding a stack so that a claim on any of its pull requests moves all of them, which would make it the one claim that unions others.

### D64. A member can ask for a poll now

The scheduled poll comes every ten minutes, and a change that does not move a pull request's update time otherwise waits for the sweep. A member who has just made a stack, pushed, or retargeted a base wants Park to look now. So the dashboard asks for the scheduled poll early, of every watched repository or of one, and refreshes a unit's pull requests one by one. A poll costs one listing per repository. Asking again while one waits costs nothing more, since a waiting item only moves earlier; and one waiting out a failure's backoff keeps it, so repeated clicks do not press a forge that limits Park. Only sessions may ask: no token scope allows it.

Set aside: a sweep on demand, which refreshes every open pull request in full and costs a team's repositories hundreds of points of the hourly limit each time.

### D65. A member keeps a way in

Forge sign-in finds a member by the account they linked, and a member who joined through the forge has no password. Unlinking that account left them no way in: the next sign-in found no member for the account, and was refused, or, where admission allowed it, made a new member. This came to light when Settings gained an unlink button, which would have put it one click away. So the last way in cannot be unlinked, as the last admin cannot be demoted; a member connects another account first.

### D66. A group's description is served by its link, sandboxed

Members wanted a feature's page beside its group — what it is for, screenshots, a short video — made as a static site and hosted by Park. A page that runs scripts in Park's origin could act as whoever opens it: read the CSRF token, change groups, make tokens. The owner chose to let pages run scripts. So a description runs sandboxed, in an origin of its own and without Park's session. Its images and videos then cannot be authorized by the session either, so it is served to anyone who has its link, under an unguessable token that each upload replaces. It sends no referrer, so that the token does not reach the sites a page loads fonts from.

Set aside: serving descriptions in Park's origin with scripts forbidden, which keeps them behind sign-in but stops interactive pages working. Also set aside: a separate origin for descriptions, which allows both, at the cost of a second hostname, a certificate, and an address in the configuration.
