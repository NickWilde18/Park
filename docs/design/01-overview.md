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
| Decide | Facts, groups, rules, judgements | One ball per unit, with each person's part | [Verdicts](04-verdicts.md), [Rules](05-rules.md) |
| Tasks | Changed verdicts, members' automation settings | Tasks | [Tasks](06-tasks.md) |
| Verify | Reports, facts | Done, retry, or needs a person | [Tasks](06-tasks.md) |

## End to end

One feature, from first push to merge. Ana writes it across two repositories, `api` and `web`; Ben reviews it.

1. Ana opens `api#12` in the `api` repository, from branch `feat/schedule`, and requests Ben's review. The forge sends a webhook; Park refreshes the pull request and records its facts.
2. Park evaluates everyone's part and places the ball of `api#12`, a unit on its own for now: the reviewer side, Ben — *review · "First review"*. Ana's part is idle: she waits.
3. Ana opens `web#7` in the `web` repository, from a branch with the same name, `feat/schedule`, and requests Ben's review there too. The same branch name in two repositories is a structural signal: the two belong together, and group G is created. From now on they share one ball: still the reviewer side, Ben — review both.
4. Ben reviews by hand. On `api#12` he requests changes, with three threads, T1–T3. On `web#7` he only comments, with one thread, W1.
5. The author side now has work, so G's ball moves there: Ana — *address comments* on `api#12` ("Ben requested changes; 3 items wait on you") and on `web#7` (W1). Ana has turned on automation for comments, so Park queues one task for her on G, with two items: answer T1–T3 on `api#12`, and W1 on `web#7`.
6. Ana's executor claims the task and sends heartbeats while it works. It answers T1 and T2 and pushes a fix to `api#12`, pushes a fix for W1 to `web#7` and replies there, and reports done.
7. Park refreshes both pull requests and checks each item's post-condition: every target thread must be resolved, or have a reply from Ana since the attempt started. W1 passes; T3 has neither. The attempt fails — "1 of 3 threads unanswered: T3" — and the task is queued again as attempt 2, carrying that reason. The ball stays with Ana throughout: T3 still waits on her.
8. The executor claims the task again, now holding only T3. It disagrees with T3's suggestion, so it replies with its reasons and leaves the thread open for Ben. A reply that disagrees counts: the facts agree this time, and the task is done.
9. The ball is still Ana's, now to hand the group back: on `api#12`, *request review · "Every comment is answered; dismiss Ben's request for changes, saying what changed, and ask again"*; on `web#7`, ask Ben to look again at her reply. Until she does both, nothing reaches Ben.
10. Ana dismisses the review on `api#12`, saying what changed, and asks Ben again on both. The author side has nothing left, so G's ball moves to the reviewer side: Ben — *review* both. For Ben's reasons, Park wants to know whether the new commits respond to his comments. No answer is recorded for these inputs, so it asks the model, records "yes, 0.9", and tells Ben "Asked again; the new commits respond to your comments". Without a model, the ball would be in the same place; only the reason would be plainer.
11. Ben approves both pull requests. Only the last step is left, so the ball goes back to Ana: *merge · "Approved and passing; ready to merge"* — his approval settles his own open threads, so T3, which he left open, holds nothing up. She merges both — only people merge — and G's verdict closes.

## Principles

- **P1. Facts are the only input.** Every stage reads facts, or the output of earlier stages. Model answers, grouping claims, and member actions are recorded as facts too.
- **P2. Every conclusion carries its reason.** A verdict stores its inputs, every person's path through the rules, and any judgement it used; a group membership stores the assertion that placed it.
- **P3. A claim is not a fact about the world.** An executor's report is recorded, but a task completes only when forge facts agree.
- **P4. Stable identifiers.** Keys are forge IDs, qualified by the forge they come from, never URLs or usernames. URLs are built when a page or response is rendered.
- **P5. No vendor in the core.** Forge-specific code stays inside that forge's collector, which writes neutral facts; identity and model providers sit behind provider interfaces. A vendor's name is a value — `kind: github` — never part of an API path, a column, a configuration key, or the frontend.
- **P6. Read-only toward forges.** Park observes; executors act.
- **P7. One ball per unit.** Whose turn it is exists once per unit of work, on exactly one side, and every person's view is derived from it. Two people can never be shown waiting on each other.
