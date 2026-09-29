# 11. Open questions

What is still undecided. Each item is either tuned during implementation or waits for a real need.

## Tune during implementation

- **Leases, heartbeats, attempts, backoff, and verification deadlines per action.** [Tasks](06-tasks.md#defaults) lists starting values; they should be checked against how long real executors take, and how long checks run.
- **Model questions.** The prompts, and each question's confidence threshold, should be tuned against recorded cases before the defaults are published.
- **Default rule thresholds.** The 24 hours before a nudge, the 48 hours between nudges, and position 3 in a chain come from one team's practice.
- **Checking rule sets for the fallback.** The default rule set was checked by evaluating it over every state of one author and up to two reviewers ([D37](10-design-log.md#d37-one-ball-per-unit-on-exactly-one-side)): no state needs the fallback. The preview flags units a draft would leave to the fallback today; the same exhaustive check could run whenever a draft is published.

## Port from the predecessor

The predecessor's collector (about 1,600 lines), model client (about 1,000), and grouping and clustering code (about 1,000) are worth porting — about 3,600 lines. Its decision code is not ported, because rules move into the database, and neither is its session spawning, because Park does not execute, nor its spreadsheet integration. Ported code has to be rewritten as it moves: the predecessor's comments and tests refer to real repositories, pull request numbers, and colleagues' usernames, none of which belong in a public repository.

## Wait for a real need

- **Shared executors.** An executor that acts for several members, under a bot account, would fail post-conditions that expect the member's own accounts. Supporting it needs a way for members to delegate to an account.
- **Review tasks and the lock.** A `review` changes no code; it may not need to wait for a group's lock.
- **Team review requests.** A request to a team puts the ball with every member of that team, at low priority, until one of them reviews. Handing it to one member instead — by rotation, or by code ownership — is open.
- **Repositories where authors cannot dismiss.** Protected branches can limit dismissal to administrators and named people or teams. There the default hand-back cannot be done by the author, and the rules should hand back on another review request instead.
- **Outgoing notifications.** Bridges can read the event stream today. Whether Park should also send outgoing webhooks is open.
- **The size of a conflict.** Forges report only whether a pull request conflicts. The predecessor ranked small conflicts after comments, but knowing the size needs a fact that only an executor could report.
- **Merge order within a group.** Nothing says a feature's backend must merge before its frontend. A group-level field, or a rule on one, could.
- **A second forge**: which one, and the adapter layer it brings ([D18](10-design-log.md#d18-no-forge-adapter-layer-until-a-second-forge-exists)).
