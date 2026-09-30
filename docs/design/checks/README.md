# Design checks

Scripts that check the design chapters as they are written. The rule tables and the field catalog are read from [Rules](../05-rules.md), the placement order from [Verdicts](../04-verdicts.md), and the long-lived branches from [Grouping](../03-grouping.md), so an edit to a chapter is checked as it stands, not as someone remembers it. Run them after changing a chapter; CI runs them on every pull request.

They need Python 3.10 or later and nothing else, except `charts.sh`, which needs Node.

| Command | Checks |
|---|---|
| `python3 docs/design/checks/check_docs.py` | Links and anchors resolve and fenced blocks close, in every Markdown file of the repository. In the chapters: every field mentioned is in the catalog; the judgement fields are the questions; the default rule set passes the publish checks its tables can show; every action that can be a task has a post-condition |
| `python3 docs/design/checks/rule_trees.py --check` | The trees in Rules match the tables below them. Without `--check`, it redraws them: edit a table, then run it |
| `python3 docs/design/checks/regress.py` | Twenty units — groups, drafts, running and stuck checks, code owners, written reviews, team requests, authors who cannot dismiss — each with the side, holders, and action its ball must have |
| `python3 docs/design/checks/sweep.py` | Every state of one pull request with an author and two reviewers, 583,200 in all, in about half a minute: the fallback is never needed; a reviewer who approved holds the ball only when asked again, and one who requested changes only when the author cannot dismiss the request and asked again; no model answer changes anyone's part; "all answered" never holds while a written review is unanswered. It also writes, for every thousand states, a digest of the rule each person reaches, `internal/verdicts/testdata/sweep.json`, which the Go port of the sweep must match; with `--write`, it writes them |
| `python3 docs/design/checks/default_rules.py` | `internal/rules/default.json`, the default rule set Park ships, says what the tables say, rule by rule and term by term; `internal/rules/testdata/default_cases.json`, the cases the Go engine must agree on, is what the tables give — people's fields from the sweep, the regression cases, and three more, reaching every rule. With `--write`, it writes the cases |
| `python3 docs/design/checks/grouping.py` | Chain edges skip forks and long-lived branches and follow backports; resolution keeps claims from chaining into a union |
| `sh docs/design/checks/charts.sh` | Every mermaid block in the repository renders |

`placement.py`, shared by `regress.py`, `sweep.py`, and `default_rules.py`, evaluates the rule tables and places the ball. The rules and the placement order come from the chapters; the fields the rules read are computed by `people()`, a model of the field catalog, and `grouping.py` models the grouping flows the same way. A pull request that changes what a field means, or how chains or groups resolve, changes these models with it; one that changes a default rule changes its table, the rule-set file, the cases, and the sweep's digests together.

## Private names

`scripts/check_text.py` checks every file in the repository for text that is not English, and for names that must not appear in a public repository. The names are kept outside it: one regular expression per line, in `~/.config/park/private-names` or the file `PARK_PRIVATE_NAMES_FILE` names. In CI, the repository secret `PARK_PRIVATE_NAMES` holds the same list; without it, only the language is checked.
