"""Checks the repository's Markdown, and the design chapters against each other.

Every Markdown file in the repository:

- relative links point to files that exist, and their anchors to headings in them;
- fenced blocks are closed.

The design chapters:

- every field they mention is in the field catalog of 05-rules.md;
- the judgement fields of the catalog are the questions of 04-verdicts.md;
- the default rule set passes the publish checks of 05-rules.md that can be read from its tables: each list ends
  with a catch-all; kinds are act, hold, finish, or idle, and reviewers conclude only act or idle; every act has an
  action allowed for the role, a finish has none or merge, and hold and idle have none; remind only on idle; targets
  name a list of accounts; every judgement is asked for the role;
- every action that can be a task has a post-condition in 06-tasks.md, and nothing else has one.
"""
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, capture_output=True, text=True,
                      check=True).stdout.strip()
DESIGN = os.path.relpath(os.path.dirname(HERE), ROOT)
listed = subprocess.run(["git", "ls-files", "--cached", "--others", "--exclude-standard", "--", "*.md"], cwd=ROOT,
                        capture_output=True, text=True, check=True).stdout.splitlines()
files = sorted(f for f in listed if os.path.isfile(os.path.join(ROOT, f)))
texts = {f: open(os.path.join(ROOT, f), encoding="utf-8").read() for f in files}
problems = []


def outside_fences(text):
    """(line number, line) for every line outside fenced blocks."""
    fence = False
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith("```"):
            fence = not fence
            continue
        if not fence:
            yield n, line


def anchors(text):
    """GitHub's heading anchors: lower case, punctuation dropped, spaces to hyphens, repeats numbered."""
    out, seen = set(), {}
    for _, line in outside_fences(text):
        if m := re.match(r"^#{1,6}\s+(.*)$", line):
            slug = re.sub(r"[^\w\- ]", "", m[1].strip().lower()).replace(" ", "-")
            out.add(slug if slug not in seen else f"{slug}-{seen[slug]}")
            seen[slug] = seen.get(slug, 0) + 1
    return out


def section(text, heading, until="\n## "):
    if heading not in text:
        raise SystemExit(f"missing section: {heading.strip()}")
    return text.split(heading, 1)[1].split(until, 1)[0]


def table_rows(body):
    """The cells of every row of the first table in body, header and separator excluded."""
    rows = [[c.strip() for c in line.strip().strip("|").split("|")] for line in body.splitlines()
            if line.startswith("|")]
    return rows[2:]


def code(cell):
    """The first code span in a cell."""
    m = re.search(r"`([^`]+)`", cell)
    return m[1] if m else ""


# Links, anchors, and fences, in every Markdown file.
anchor_map = {f: anchors(t) for f, t in texts.items()}
for f, t in texts.items():
    if sum(1 for line in t.splitlines() if line.startswith("```")) % 2:
        problems.append(f"{f}: a fenced block is not closed")
    for n, line in outside_fences(t):
        for m in re.finditer(r"\]\(([^)\s]+)\)", line):
            target = m[1]
            if re.match(r"^[a-z]+:", target):
                continue
            path, _, frag = target.partition("#")
            dest = os.path.normpath(os.path.join(os.path.dirname(f), path)) if path else f
            if not os.path.exists(os.path.join(ROOT, dest)):
                problems.append(f"{f}:{n}: broken link {target}")
            elif frag and dest in anchor_map and frag not in anchor_map[dest]:
                problems.append(f"{f}:{n}: no heading for the anchor in {target}")

# The chapters against each other.
chapters = {f: t for f, t in texts.items() if f.startswith(DESIGN + "/")}
rules = texts[f"{DESIGN}/05-rules.md"]
verdicts = texts[f"{DESIGN}/04-verdicts.md"]
tasks = texts[f"{DESIGN}/06-tasks.md"]

catalog = {}
for cells in re.findall(r"^\| `((?:pr|me|chain|group|judgement)\.[a-z_]+)` \| ([^|]+) \|",
                        section(rules, "## The field catalog"), re.M):
    catalog[cells[0]] = cells[1].strip()
for f, t in chapters.items():
    for n, line in enumerate(t.splitlines(), 1):
        for m in re.finditer(r"\b((?:pr|me|chain|group|judgement)\.[a-z_]+)\b", line):
            if m[1] not in catalog:
                problems.append(f"{f}:{n}: {m[1]} is not in the field catalog")

questions = {code(c[0]): c[1].lower() for c in table_rows(section(verdicts, "### Questions", "\n### "))}
judged = {k.split(".", 1)[1] for k in catalog if k.startswith("judgement.")}
if judged != set(questions):
    problems.append(f"judgement fields {sorted(judged)} differ from the questions {sorted(questions)}")

actions = {code(c[0]): dict(role=c[1].lower(), task=c[3].startswith("Yes"))
           for c in table_rows(section(verdicts, "## Actions"))}
post = {code(c[0]) for c in table_rows(section(tasks, "## Items and post-conditions"))}
if post != {a for a, v in actions.items() if v["task"]}:
    problems.append(f"post-conditions exist for {sorted(post)}, "
                    f"but the actions that can be tasks are {sorted(a for a, v in actions.items() if v['task'])}")

for heading, until, role in (("### Author\n", "### Reviewer\n", "author"),
                             ("### Reviewer\n", "### How to read them\n", "reviewer")):
    table = [c for c in table_rows(section(rules, heading, until)) if c[0].isdigit()]
    where = f"{DESIGN}/05-rules.md, {role}"
    if not table or table[-1][2] != "always":
        problems.append(f"{where}: the list does not end with a catch-all")
    for n, name, when, kind, action, _ in table:
        rule = f"{where} {n} ({name})"
        base, *flags = [x.strip() for x in kind.split(",")]
        allowed = {"act", "idle"} if role == "reviewer" else {"act", "hold", "finish", "idle"}
        if base not in allowed:
            problems.append(f"{rule}: a {role} cannot conclude {base}")
        if set(flags) - {"low", "remind"}:
            problems.append(f"{rule}: unknown flags {sorted(set(flags) - {'low', 'remind'})}")
        if "remind" in flags and base != "idle":
            problems.append(f"{rule}: remind only on idle")
        name_of_action = code(action)
        if base == "act" and not name_of_action:
            problems.append(f"{rule}: act without an action")
        if base in ("hold", "idle") and name_of_action:
            problems.append(f"{rule}: {base} with an action")
        if base == "finish" and name_of_action not in ("", "merge"):
            problems.append(f"{rule}: a finish can only merge")
        if name_of_action and name_of_action not in actions:
            problems.append(f"{rule}: {name_of_action} is not in the action table of 04-verdicts.md")
        elif name_of_action and actions[name_of_action]["role"] != role:
            problems.append(f"{rule}: {name_of_action} is not an action of the {role}")
        if m := re.search(r" to `([\w.]+)`", action):
            if not catalog.get(m[1], "").startswith("list of accounts"):
                problems.append(f"{rule}: the target {m[1]} is not a list of accounts")
        for q in re.findall(r"`judgement\.(\w+)`", when):
            if role not in questions.get(q, ""):
                problems.append(f"{rule}: the question {q} is not asked for the {role}")

print(f"{len(files)} Markdown files, {len(chapters)} in {DESIGN}; {len(catalog)} fields, {len(actions)} actions, "
      f"{len(questions)} questions")
print("\n".join(problems) or "no problems")
sys.exit(1 if problems else 0)
