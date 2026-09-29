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

One feature, from first push to merge. Ana writes it across two repositories, `api` and `web`; Ben reviews it.

1. Ana opens `api#12` in the `api` repository, from branch `feat/schedule`, and requests Ben's review. The forge sends a webhook; Park refreshes the pull request and records its facts.
2. Park evaluates the rules for everyone involved. Ben, a reviewer: *me · review · "First review"*. Ana, the author: *others (Ben) · "Waiting for review"*.
3. Ana opens `web#7` in the `web` repository, from a branch with the same name, `feat/schedule`, and requests Ben's review there too. The same branch name in two repositories is a structural signal: the two belong together, and group G is created. From now on they are one unit of work.
4. Ben reviews by hand. On `api#12` he requests changes, with three threads, T1–T3. On `web#7` he only comments, with one thread, W1.
5. Ana's verdict on `api#12` becomes *me · address comments · "Ben requested changes; 3 comments wait on you"*, and on `web#7` the same action for W1. Ana has turned on automation for comments, so Park queues one task for her on G, with two items: answer T1–T3 on `api#12`, and W1 on `web#7`.
6. Ana's executor claims the task and sends heartbeats while it works. It answers T1 and T2 and pushes a fix to `api#12`, pushes a fix for W1 to `web#7` and replies there, and reports done.
7. Park refreshes both pull requests and checks each item's post-condition: every target thread must be resolved, or end with a reply from Ana. W1 passes; T3 has neither. The attempt fails — "1 of 3 threads unanswered: T3" — and the task is queued again as attempt 2, carrying that reason.
8. Ben's verdicts moved as the commits landed. On `web#7`, where he only commented, his rules reach a question they cannot settle: do the new commits respond to his comment? No answer is recorded for these inputs, so Park asks the model, records "yes, 0.9", and concludes *me · review · "Re-review: new commits respond to your comments"*. On `api#12` his request for changes stands, so his verdict stays *others (Ana) · "You requested changes; the author dismisses your review when it is ready for another look"*, however many commits or replies arrive.
9. The executor claims the task again, now holding only T3. It disagrees with T3's suggestion, so it replies with its reasons and leaves the thread open for Ben. A reply that disagrees counts: the facts agree this time, and the task is done.
10. Ana's verdict on `api#12` becomes *me · request review · "Every comment is answered; dismiss Ben's request for changes, saying what changed, and ask again"*. Until she does, the ball stays with her. She dismisses the review and asks Ben again; her verdict moves to *others (Ben) · "3 replies wait for the reviewers"*, and his to *me · review · "The author asked you to review again"*.
11. Ben approves both pull requests. Ana's verdicts become *me · merge · "Approved and passing; ready to merge"* — his approval settles his own open threads, so T3, which he left open, holds nothing up. She merges both — only people merge — and every verdict on them closes.

## Principles

- **P1. Facts are the only input.** Every stage reads facts, or the output of earlier stages. Model answers, grouping claims, and member actions are recorded as facts too.
- **P2. Every conclusion carries its reason.** A verdict stores its inputs, its path through the rules, and any judgement it used; a group membership stores the assertion that placed it.
- **P3. A claim is not a fact about the world.** An executor's report is recorded, but a task completes only when forge facts agree.
- **P4. Stable identifiers.** Keys are forge IDs, never URLs or usernames. URLs are built when a page or response is rendered.
- **P5. No vendor in the core.** Forge-specific code stays inside that forge's collector, which writes neutral facts; identity and model providers sit behind provider interfaces. A vendor's name is a value — `kind: github` — never part of an API path, a column, a configuration key, or the frontend.
- **P6. Read-only toward forges.** Park observes; executors act.
