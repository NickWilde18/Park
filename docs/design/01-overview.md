# 1. Overview

## What Park is

The team-level source of truth for "whose turn is it, and to do what" — plus a dashboard, a verified task queue for executors, and a contract.

| Park does | Park does not |
|---|---|
| Collect facts from forges, webhooks first and polling as a backstop | Start AI sessions, manage worktrees, or manage agent lifecycles |
| Group pull requests into units of work | Ship integrations for chat tools or issue trackers |
| Decide, by rules, who holds the ball and what should happen next | Write to forges, or carry out any work on a pull request |
| Turn verdicts into tasks, lease them to executors, and verify every outcome against forge facts | Care how an executor does the work — an AI session, another tool, or a person |
| Identify members and link their forge accounts | Merge or close pull requests — only people do that |
| Serve a web dashboard and an open API | |

Park owns the **task** lifecycle; executors own the **agent** lifecycle. Executors already exist — local tools such as ballpark, Park's predecessor, or a bridge driving Agent Orchestrator — each managing the worktrees and sessions on its own machine. Park adds what they lack: many members, roles, a server, a verified task queue, and a contract.

## Who talks to Park

| Party | Through | Sends Park | Gets from Park |
|---|---|---|---|
| Forge | Webhook deliveries, and Park's polling | Changes | — |
| Member | The dashboard | Grouping actions, rule edits, settings | Queues, verdicts with reasons, groups, tasks |
| Executor | The task API | Claims, heartbeats, reports | Tasks |
| Bridge | The assertion API | Grouping assertions | The event stream, if it reads one |

Executors and bridges always initiate. Park never pushes to them, and could not reach some of them if it tried: Agent Orchestrator's daemon, for example, binds to `127.0.0.1` by design and treats that as load-bearing.

Park itself calls out to three kinds of provider: forges (read-only), identity providers for sign-in, and optionally a model provider.

## Where Park differs from forge inboxes

GitHub's own pull request dashboard (generally available since July 2026, open to every user) already covers the personal inbox: review requests, split into direct and team requests; pull requests that need fixing; pull requests ready to merge. Park does not compete on that list. It does what the inbox does not:

- **Whose turn it really is**, reasoned from thread state: the author answered every comment but never re-requested review; a blocking review was dismissed; comments were left unaddressed.
- **Work that spans repositories**: a backend and a frontend pull request are one feature.
- **Stacked chains**: an approved pull request cannot merge while the one below it is open, and a first review deep in a chain can wait.
- **The team view**: what each member should do, not only one person's inbox.
- **Rules that can be seen and edited**, and a reason for every verdict.
- **A task contract any executor can implement**, with every reported outcome checked against the forge.

## The stages

| Stage | Reads | Produces | Chapter |
|---|---|---|---|
| Collect | Webhook deliveries, polling | Refreshes of pull requests | [Facts](02-facts.md) |
| Facts | Forge state | Fact events and current pull request facts | [Facts](02-facts.md) |
| Group | Facts, assertions | Groups and chains | [Grouping](03-grouping.md) |
| Decide | Facts, groups, rules, judgements | One verdict per member per pull request | [Verdicts](04-verdicts.md), [Rules](05-rules.md) |
| Tasks | Changed verdicts, members' automation settings | Tasks | [Tasks](06-tasks.md) |
| Verify | Reports, facts | Done, retry, or needs a person | [Tasks](06-tasks.md) |

## End to end

One feature, from first push to merge. Ana writes it; Ben reviews it.

1. Ana opens `api#12` on branch `feat/schedule` and requests Ben's review. The forge sends a webhook; Park refreshes the pull request and records its facts.
2. Park evaluates the rules for everyone involved. Ben, a reviewer: *me · review · "First review"*. Ana, the author: *others (Ben) · "Waiting for review"*.
3. Ana opens `web#7` on the same branch and requests Ben's review there too. The structural source asserts that the two belong together, and group G is created. From now on they are one unit of work.
4. Ben reviews by hand: he requests changes on `api#12` with three threads, T1–T3, and approves `web#7`.
5. Ana's verdict on `api#12` becomes *me · address comments · "Ben requested changes; 3 comments wait on you"*. Ana has turned on automation for comments, so Park queues a task for her on G, with one item: answer T1–T3 on `api#12`.
6. Ana's executor claims the task, sends heartbeats while it works, answers T1 and T2, pushes a commit, and reports done.
7. Park refreshes `api#12` and checks the task's post-condition: T3 has no answer. The attempt fails — "1 of 3 threads unanswered: T3" — and the task is queued again as attempt 2, carrying that reason.
8. The executor claims it again, answers T3, and reports. This time the facts agree, and the task is done.
9. Ana's verdict becomes *me · request review · "Every comment is answered; ask Ben to review again"*. She re-requests his review, and her verdict moves to *others (Ben) · "3 replies wait for the reviewers"*.
10. Ben's rules reach a question they cannot settle: do the new commits respond to his comments? No answer is recorded for these inputs, so Park asks the model, records "yes, 0.9", and concludes *me · review · "Re-review: new commits respond to your comments"*.
11. Ben approves `api#12`. Ana's verdict on it becomes *me · merge · "Approved and passing; ready to merge"* — as her verdict on `web#7` has been since Ben approved that one. She merges both — only people merge — and every verdict on them closes.

## Principles

- **P1. Facts are the only input.** Every stage reads facts, or the output of earlier stages. Model answers, grouping claims, and member actions are recorded as facts too.
- **P2. Every conclusion carries its reason.** A verdict stores its inputs, its path through the rules, and any judgement it used; a group membership stores the assertion that placed it.
- **P3. A claim is not a fact about the world.** An executor's report is recorded, but a task completes only when forge facts agree.
- **P4. Stable identifiers.** Keys are forge IDs, never URLs or usernames. URLs are built when a page or response is rendered.
- **P5. No vendor in the core.** Forge-specific code stays inside that forge's collector, which writes neutral facts; identity and model providers sit behind provider interfaces. A vendor's name is a value — `kind: github` — never part of an API path, a column, a configuration key, or the frontend.
- **P6. Read-only toward forges.** Park observes; executors act.
