# 11. Open questions

What is still undecided. Each item is decided by the owner, tuned during implementation, or waits for a real need.

## To decide

- **Model grouping: automatic, or suggestions only.** At or above its threshold, a model placement puts a pull request in a group by itself, and so changes the unit its ball belongs to ([D38](10-design-log.md#d38-no-judgement-moves-a-ball)). Making every model placement a suggestion that a member accepts would keep the model out of ball placement entirely, at the cost of a click per placement.
- **Synchronous or asynchronous judgement.** Refinements now change only a reason or a reminder, never a part's kind or action. Asking the model in the background, and updating the reason when the answer arrives, would remove the synchronous call with its timeout and background completion ([D7](10-design-log.md#d7-model-judgements-are-synchronous-and-recorded)); the price is that a reason can change a moment after it is first shown.

## Tune during implementation

- **Leases, heartbeats, attempts, backoff, and verification deadlines per action.** [Tasks](06-tasks.md#defaults) lists starting values; they should be checked against how long real executors take, and how long checks run.
- **Model questions.** The prompts, and each question's confidence threshold, should be tuned against recorded cases before the defaults are published.
- **Default rule thresholds.** The 24 hours before a nudge, the 48 hours between nudges, and position 3 in a chain come from one team's practice.
- **Checking rule sets for the fallback.** The default rule set was checked by evaluating it over every state of one author and up to two reviewers, and over regression cases with groups, drafts, checks, code owners, written reviews, team requests, and authors who cannot dismiss ([D37](10-design-log.md#d37-one-ball-per-unit-on-exactly-one-side)): no state needs the fallback. The preview flags units a draft would leave to the fallback today; the same exhaustive check could run whenever a draft is published.
- **Stuck checks.** Two hours before pending checks become the author's to look at is a guess; long test suites may need more, per repository.

## Port from the predecessor

The predecessor's collector (about 1,600 lines), model client (about 1,000), and grouping and clustering code (about 1,000) are worth porting — about 3,600 lines. Its decision code is not ported, because rules move into the database, and neither is its session spawning, because Park does not execute, nor its spreadsheet integration. Ported code has to be rewritten as it moves: the predecessor's comments and tests refer to real repositories, pull request numbers, and colleagues' usernames, none of which belong in a public repository.

## Wait for a real need

- **Shared executors.** An executor that acts for several members, under a bot account, would fail post-conditions that expect the member's own accounts. Supporting it needs a way for members to delegate to an account.
- **Review tasks and the lock.** A `review` changes no code; it may not need to wait for a group's lock.
- **Team review requests.** A request to a team puts the ball with every member of that team, at low priority, until one of them reviews. Handing it to one member instead — by rotation, or by code ownership — is open.
- **Conversation comments.** A reviewer's comment in the pull request's conversation, outside any review, does not wait on the author today; only threads and written reviews do. Counting those comments too would catch requests made there, at the cost of every "LGTM" putting the ball back on the author.
- **Outgoing notifications.** Bridges can read the event stream today. Whether Park should also send outgoing webhooks is open.
- **The size of a conflict.** Forges report only whether a pull request conflicts. The predecessor ranked small conflicts after comments, but knowing the size needs a fact that only an executor could report.
- **Merge order within a group.** Nothing says a feature's backend must merge before its frontend. A group-level field, or a rule on one, could.
- **A second forge**: which one, and the adapter layer it brings ([D18](10-design-log.md#d18-no-forge-adapter-layer-until-a-second-forge-exists)).
