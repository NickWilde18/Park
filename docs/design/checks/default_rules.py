"""Checks the default rule-set file against the rule tables of 05-rules.md, and the cases the Go engine is tested with.

internal/rules/default.json is the default rule set as Park ships it. Each of its rules must say what its row in the
tables says: its name, passed-over label, kind, priority, reminder, action, targets, whether it may become a task,
reason, and condition, term by term.

internal/rules/testdata/default_cases.json holds people's fields, drawn from the sweep's states, the regression cases,
and a few more for rules neither reaches, each with the rule the tables give it; the Go engine must reach the same
rule. Every rule is reached by some case. With --write, the file is written; otherwise the run fails if it differs
from what the tables give.
"""
import json
import math
import os
import re
import sys

import regress
import sweep
from placement import AUTHOR, REVIEWER, RULES, VERDICTS, evaluate, people, rows

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
RULESET = os.path.join(ROOT, "internal", "rules", "default.json")
CASES = os.path.join(ROOT, "internal", "rules", "testdata", "default_cases.json")
SECTIONS = [("author", "### Author\n", "### Reviewer\n"), ("reviewer", "### Reviewer\n", "### How to read them\n")]
OPS = {"=": "eq", "≠": "ne", "<": "lt", "≤": "le", ">": "gt", "≥": "ge"}
QUESTIONS = ("commits_address_my_comments", "comments_blocking")

# Every field of the catalog but judgements and me.role, at the value a case leaves out.
BASE = {
    "pr.repo": "api", "pr.title": "A change", "pr.labels": [], "pr.base": "main", "pr.author": "ana",
    "pr.draft": False, "pr.conflicting": False, "pr.checks": "passing", "pr.failing_checks": [],
    "pr.hours_checks_pending": None, "pr.approvals": 0, "pr.required_approvals": 1, "pr.blocked_by": [],
    "pr.blockers_rerequested": True, "pr.requested": [], "pr.hours_requested": None, "pr.reviewed": False,
    "pr.unanswered_reviews": 0, "pr.threads_on_reviewers": 0, "pr.awaited_reviewers": [],
    "pr.awaited_rerequested": True, "pr.mergeable_now": False, "pr.merge_blocked_by": [],
    "pr.code_owners_pending": [], "pr.author_can_dismiss": True,
    "me.threads_on_me": 0, "me.threads_open": 0, "me.requested": False, "me.code_owner": False, "me.reviewed": False,
    "me.decision": "none", "me.reviewed_since_decision": False, "me.new_commits": False,
    "me.hours_since_review": None, "me.hours_since_nudge": None,
    "chain.position": 1, "chain.length": 1, "chain.open_below": 0, "group.size": 0,
}

# Units for rules that neither the sweep nor the regression cases reach.
EXTRA = [
    ("conflicts", {"api#12": dict(author="ana", conflicting=True, direct={"ben"})}),
    ("approved, stacked on an open pull request",
     {"api#12": dict(author="ana", reviews={"ben": dict(decision="approved")}, position=2, length=2, open_below=1)}),
    ("a first review, third in a chain", {"api#12": dict(author="ana", direct={"ben"}, position=3, length=3,
                                                          open_below=2)}),
]


def table_terms(when):
    """A When cell's terms, as (field, op) or (field, op, value); a value naming a field is ("field", name)."""
    out = []
    for t in re.split(r",\s*and\s+|,\s*|\s+and\s+", when):
        t = t.strip()
        if t == "always":
            out.append(("always",))
        elif m := re.fullmatch(r"not `([\w.]+)`", t):
            out.append((m[1], "is_false"))
        elif m := re.fullmatch(r"`([\w.]+)`", t):
            out.append((m[1], "is_true"))
        elif m := re.fullmatch(r"`([\w.]+)` is (not )?empty", t):
            out.append((m[1], "is_not_empty" if m[2] else "is_empty"))
        elif m := re.fullmatch(r"`([\w.]+)` (is not|is|contains) `(\w+)`", t):
            out.append((m[1], m[2].replace(" ", "_"), m[3]))
        elif m := re.fullmatch(r"`([\w.]+)` (=|≠|<|≤|>|≥) (\d+)", t):
            out.append((m[1], OPS[m[2]], int(m[3])))
        elif m := re.fullmatch(r"`([\w.]+)` (=|≠|<|≤|>|≥) `([\w.]+)`", t):
            out.append((m[1], OPS[m[2]], ("field", m[3])))
        else:
            raise SystemExit(f"cannot read the term {t!r}")
    return out


def file_terms(when):
    """A rule-set file condition's terms, the same way; only always, a term, or all of terms are expected here."""
    if when == {"always": True}:
        return [("always",)]
    out = []
    for t in when["all"] if set(when) == {"all"} else [when]:
        if "field" not in t or set(t) - {"field", "op", "value"}:
            return [("not a term", json.dumps(t))]
        value = t.get("value")
        if isinstance(value, dict):
            value = ("field", value.get("field"))
        out.append((t["field"], t["op"]) + ((value,) if "value" in t else ()))
    return out


def can_be_task():
    """Each action's "Can be a task" in 04-verdicts.md."""
    body = VERDICTS.split("## Actions", 1)[1].split("\n## ", 1)[0]
    return {m[1]: m[2].startswith("Yes") for m in re.finditer(r"^\| `(\w+)` \|[^|]+\|[^|]+\| ([^|]+) \|$", body, re.M)}


def check_file(ruleset):
    """The ways the file differs from the tables."""
    problems = []
    tasks = can_be_task()
    for role, section, until in SECTIONS:
        body = RULES.split(section, 1)[1].split(until, 1)[0]
        label_line = next((line for line in body.splitlines() if line.startswith("Passed-over labels:")), "")
        passed = {int(n): text for n, text in re.findall(r'(\d+) "([^"]+)"', label_line)}
        table, rules = rows(section, until), ruleset[role]
        if len(table) != len(rules):
            problems.append(f"{role}: the file has {len(rules)} rules, the table {len(table)}")
        for row, rule in zip(table, rules):
            kind, *flags = [x.strip() for x in row["Kind"].split(",")]
            action = re.fullmatch(r"(?:`(\w+)`(?: to `([\w.]+)`)?(, not a task)?)?", row["Action"])
            want = dict(name=row["Rule"], passed_over=passed.get(int(row["#"])), kind=kind,
                        priority="low" if "low" in flags else None, remind="remind" in flags, action=action[1],
                        targets=action[2], task=bool(action[1]) and tasks[action[1]] and not action[3],
                        reason=row["Reason"], when=table_terms(row["When"]))
            then = rule.get("then", {})
            got = dict(name=rule.get("name"), passed_over=rule.get("passed_over"), kind=then.get("kind"),
                       priority=then.get("priority"), remind=then.get("remind", False), action=then.get("action"),
                       targets=then.get("targets"), task=then.get("task", False), reason=then.get("reason"),
                       when=file_terms(rule.get("when", {})))
            for key, value in want.items():
                if got[key] != value:
                    problems.append(f"{role} {row['#']} {row['Rule']}: {key} is {got[key]!r}; the table says {value!r}")
    return problems


def whole(v):
    """Numbers as the engine reads them: whole numbers, and whole hours."""
    return math.floor(v) if isinstance(v, float) else v


def case(source, role, fields):
    """A case: the fields that differ from BASE, the recorded judgements, and the rule the tables give."""
    fields = {k: whole(v) for k, v in fields.items()}
    judgements = {q: fields["judgement." + q] for q in QUESTIONS if fields.get("judgement." + q) is not None}
    full = dict(BASE)
    for k, v in fields.items():
        if k.startswith("judgement."):
            continue
        if k not in BASE:
            raise SystemExit(f"people() computes {k}, which BASE does not have")
        full[k] = v
    rule = evaluate(AUTHOR if role == "author" else REVIEWER, full | {"judgement." + q: judgements.get(q)
                                                                     for q in QUESTIONS})
    action = re.match(r"`(\w+)`", rule["action"])
    return {"from": source, "role": role, "fields": {k: v for k, v in full.items() if v != BASE[k]},
            "judgements": judgements, "rule": rule["n"], "kind": rule["kind"], "action": action[1] if action else ""}


def cases():
    """Two cases for each rule under each judgement answer the sweep reaches, and every regression and extra case."""
    out, seen, taken = [], set(), {}
    for _, unit in sweep.states():
        for answer in (None, "yes", "no"):
            pr = sweep.with_judgements(unit["pr"], answer) if answer else unit["pr"]
            for _, (role, fields) in sorted(people(pr).items()):
                if answer and role == "author":
                    continue
                key = (role, evaluate(AUTHOR if role == "author" else REVIEWER, fields)["n"], answer)
                if taken.get(key, 0) >= 2:
                    continue
                c = case("sweep", role, fields)
                if c["rule"] != key[1]:
                    raise SystemExit(f"whole numbers change the rule of a state: {c}")
                identity = json.dumps(c, sort_keys=True)
                if identity not in seen:
                    taken[key] = taken.get(key, 0) + 1
                    seen.add(identity)
                    out.append(c)
    units = [("regress " + title.split()[0], unit) for title, unit, *_ in regress.CASES]
    units += [("extra: " + title, unit) for title, unit in EXTRA]
    for source, unit in units:
        for _, pr in sorted(unit.items()):
            if pr.get("closed"):
                continue
            for _, (role, fields) in sorted(people(pr).items()):
                c = case(source, role, fields)
                identity = json.dumps(c, sort_keys=True)
                if identity not in seen:
                    seen.add(identity)
                    out.append(c)
    return out


def written(cs):
    about = ("Generated by docs/design/checks/default_rules.py from the rule tables of docs/design/05-rules.md; "
             "do not edit. A case's fields are BASE with its own on top, and me.role is its role.")
    lines = ",\n".join("    " + json.dumps(c, sort_keys=True, ensure_ascii=False) for c in cs)
    return (f'{{\n  "about": {json.dumps(about)},\n  "base": {json.dumps(BASE, sort_keys=True)},\n'
            f'  "cases": [\n{lines}\n  ]\n}}\n')


def main():
    with open(RULESET, encoding="utf-8") as f:
        ruleset = json.load(f)
    problems = check_file(ruleset)
    cs = cases()
    for role, rules in (("author", AUTHOR), ("reviewer", REVIEWER)):
        reached = {c["rule"] for c in cs if c["role"] == role}
        problems += [f"{role} {r['n']} {r['name']}: no case reaches it" for r in rules if r["n"] not in reached]
    text = written(cs)
    if "--write" in sys.argv:
        with open(CASES, "w", encoding="utf-8") as f:
            f.write(text)
    elif not os.path.exists(CASES) or open(CASES, encoding="utf-8").read() != text:
        problems.append(f"{os.path.relpath(CASES, ROOT)} is not what the tables give; run with --write")
    print(f"default rule set: {len(ruleset['author'])} author rules, {len(ruleset['reviewer'])} reviewer rules; "
          f"{len(cs)} cases")
    for p in problems:
        print("FAIL", p)
    print("all pass" if not problems else f"{len(problems)} failing")
    sys.exit(1 if problems else 0)


if __name__ == "__main__":
    main()
