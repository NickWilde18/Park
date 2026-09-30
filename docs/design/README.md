# Park design

Park is the team-level source of truth for "whose turn is it, and to do what". It collects facts from forges, groups pull requests into units of work, decides for every member who holds the ball and what to do next, and hands executable work to executors as tasks that count as done only when the forge confirms them.

> Status: decided, except the entries under *Proposed* in the [design log](10-design-log.md#proposed), which await review.

The design is split into chapters, one per part of the system. Each chapter states what the part is responsible for, the data it owns, its flows step by step, and the rules it must keep. Rules are numbered so that other chapters can cite them: P for principles, F facts, G grouping, V verdicts, R rules, T tasks, I identity, A the API, and D for design-log entries. The reasons behind the design, and the alternatives set aside, are collected in the design log, so that the chapters can stay descriptive.

API paths are relative to `/api/v1`, except the webhook endpoint and the health checks.

Examples name pull requests as `repository#number`: `api#12` is pull request 12 of the `api` repository, and `web#7` is pull request 7 of `web`, a different repository. When examples say two pull requests share a branch, they mean branches with the same name in different repositories.

## Chapters

| | Chapter | Covers |
|---|---|---|
| 1 | [Overview](01-overview.md) | Scope, boundaries, principles, and one feature from first push to merge |
| 2 | [Facts](02-facts.md) | Collecting from forges: webhooks, polling, normalization, the fact layer |
| 3 | [Grouping](03-grouping.md) | Chains and groups: sources, assertions, resolution, the model's suggestions |
| 4 | [Verdicts](04-verdicts.md) | Roles, each person's part, placing the one ball of a unit, model judgement |
| 5 | [Rules](05-rules.md) | Rule shape, the field catalog, the default rule set, editing and publishing |
| 6 | [Tasks](06-tasks.md) | The executor contract: units, post-conditions, leases, verification |
| 7 | [Identity](07-identity.md) | Sign-in, account linking, permissions, visibility, API tokens |
| 8 | [Dashboard and API](08-dashboard-and-api.md) | Views, live updates, API conventions and endpoints |
| 9 | [Architecture](09-architecture.md) | Stack, runtime, how stages hand off, layout, deployment, operations |
| 10 | [Design log](10-design-log.md) | Decisions and their reasons, and what was set aside |
| 11 | [Open questions](11-open-questions.md) | What is still undecided |

## The pipeline

```
 collect ─▶ facts ─▶ group ─▶ decide ─▶ tasks ─▶ verify
    ▲                                              │
    └──── the executor's work lands on the forge ◀─┘
```

A change enters as a fact and flows forward. Each stage reads facts and the output of the stages before it, never the state of a later one. Nothing flows backward except through the forge.

## Phases

The chapters describe the whole design. The first release builds its core loop in full and leaves the rest for later. Everything later already has its place in the data model, so adding it needs no migration of facts, groups, verdicts, or tasks.

| Part | First release | Later |
|---|---|---|
| Facts | GitHub: webhooks, polling, short polling, redelivery | A second forge |
| Grouping | Chains, structural signals, manual grouping, bridge assertions | Model suggestions |
| Verdicts | One ball per unit, with the default rule set | Model judgements |
| Rules | Stored and versioned; shown as the rule trees; changed by importing a rule-set file, kept and reviewed in git, through the publish API, with every publish check | The rule editor: structured conditions, expressions, preview, pinned examples |
| Tasks | The whole contract: leases, locks, post-conditions, verification, retries | — |
| Identity | Local accounts, forge sign-in, API tokens, the setup code | OIDC single sign-on; admission by forge organization or OIDC group |
| Visibility | Every member sees every watched repository — fit only for one team whose members all have the same access | Visibility by forge access, pull request by pull request; required before a second team or an open-source release |
| Dashboard | My queue, the unit view with rule paths, tasks, the rule trees, manual grouping | The team view, the suggestions inbox, the full settings |
| Operations | One server process; the Postgres work queue as designed; structured logs | Several processes side by side; OpenTelemetry traces and metrics |

## Glossary

| Term | Meaning |
|---|---|
| Forge | A code hosting service at one address — github.com, or a company's own server. GitHub is the only kind implemented at first |
| Installation | Park's connection to a forge, such as one organization's app: credentials, webhook secret, watched repositories |
| Member | A person who uses Park |
| Account | A person's identity on a forge, keyed by the forge and its stable account ID. A member links their accounts; other people's accounts appear too |
| Fact | An immutable, attributed record: a forge state change, a model judgement, a grouping assertion, a member action |
| Chain | Pull requests stacked on one another: one's base branch is another's head branch |
| Group | Pull requests that form one piece of work, often across repositories |
| Assertion | A claim, with a source, that pull requests belong together, or that one does or does not belong to a group |
| Anchor | The stable key a co-membership assertion is filed under: a thread ID, a branch name, an issue ID |
| Exclusion | A member's statement that a pull request is not in a group; it holds against every source |
| Suggestion | A grouping change Park proposes, applied only if a member accepts it |
| Role | A person's relation to one pull request: author or reviewer. The same person can be the author of one and a reviewer of another |
| Unit | What a ball and a task belong to: a group, or a pull request in no group |
| Ball | Who must act next on a unit. Each unit has exactly one, on one side — the author side or the reviewer side — held only by the people on that side who must act: a reviewer who approved and waits for another approval holds nothing |
| Part | One person's share of one pull request, from their role's rules: `act`, `hold`, `finish`, or `idle` |
| Reminder | An offer to nudge the holders, made to someone waiting on them. It never moves the ball |
| Rule | A condition and a conclusion about one person's part. A role's rules are evaluated in order; the first match wins |
| Rule set | The rule lists of every role, published as immutable versions |
| Verdict | Park's conclusion for one unit: where its ball is, who holds it, what each holder should do, and every part behind it |
| Judgement | A model's answer to a question a rule could not settle, recorded as a fact |
| Fingerprint | A hash of everything a model question reads; an answer is recorded against it and never asked for again |
| Task | Work a ball's holder has to do, leased to an executor, and complete only when forge facts confirm it |
| Item | One action on one pull request within a task, with its own post-condition |
| Post-condition | What forge facts must show for an item to count as done |
| Executor | A program or person that claims tasks and does the work. Park never executes |
| Bridge | A program outside Park, registered by a member, that feeds it signals, such as review threads from a chat tool |
