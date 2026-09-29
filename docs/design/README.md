# Park design

Park is the team-level source of truth for "whose turn is it, and to do what". It collects facts from forges, groups pull requests into units of work, decides for every member who holds the ball and what to do next, and hands executable work to executors as tasks that count as done only when the forge confirms them.

> Status: decided, except the entries under *Proposed* in the [design log](10-design-log.md#proposed), which await review.

The design is split into chapters, one per part of the system. Each chapter states what the part is responsible for, the data it owns, its flows step by step, and the rules it must keep. Rules are numbered so that other chapters can cite them: P for principles, F facts, G grouping, V verdicts, R rules, T tasks, I identity, A the API, and D for design-log entries. The reasons behind the design, and the alternatives set aside, are collected in the design log, so that the chapters can stay descriptive.

API paths are relative to `/api/v1`, except the webhook endpoint and the health checks.

## Chapters

| | Chapter | Covers |
|---|---|---|
| 1 | [Overview](01-overview.md) | Scope, boundaries, principles, and one feature from first push to merge |
| 2 | [Facts](02-facts.md) | Collecting from forges: webhooks, polling, normalization, the fact layer |
| 3 | [Grouping](03-grouping.md) | Chains and groups: sources, assertions, resolution, the model's placements |
| 4 | [Verdicts](04-verdicts.md) | Roles, evaluation, model judgement, the member's queue |
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

## Glossary

| Term | Meaning |
|---|---|
| Forge | A code hosting service. GitHub is the only one implemented at first |
| Installation | Park's connection to one forge: credentials, webhook secret, watched repositories |
| Member | A person who uses Park |
| Account | A member's identity on a forge, keyed by the forge's stable account ID |
| Fact | An immutable, attributed record: a forge state change, a model judgement, a grouping assertion, a member action |
| Chain | Pull requests stacked on one another: one's base branch is another's head branch |
| Group | Pull requests that form one piece of work, often across repositories |
| Assertion | A claim, with a source, that pull requests belong together, or that one does or does not belong to a group |
| Anchor | The stable key a co-membership assertion is filed under: a thread ID, a branch name, an issue ID |
| Exclusion | A member's statement that a pull request is not in a group; it holds against every source |
| Suggestion | A grouping change Park proposes, applied only if a member accepts it |
| Role | A member's relation to one pull request: author or reviewer. The same person can be the author of one and a reviewer of another |
| Ball | Who must act next on a pull request, from one member's point of view: me, others, or nobody |
| Rule | A condition and a conclusion. A role's rules are evaluated in order; the first match wins |
| Rule set | The rule lists of every role, published as immutable versions |
| Verdict | The conclusion for one member on one pull request: who holds the ball, what to do, and why |
| Judgement | A model's answer to a question a rule could not settle, recorded as a fact |
| Fingerprint | A hash of everything a model question reads; an answer is recorded against it and never asked for again |
| Task | Work that verdicts call for, leased to an executor, and complete only when forge facts confirm it |
| Unit | What one task works on: a pull request's group, or else its chain, or else the pull request alone |
| Item | One action on one pull request within a task, with its own post-condition |
| Post-condition | What forge facts must show for an item to count as done |
| Executor | A program or person that claims tasks and does the work. Park never executes |
| Bridge | A program outside Park that feeds it signals, such as review threads from a chat tool |
