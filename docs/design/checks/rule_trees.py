"""Draws the rule trees in 05-rules.md from the rule tables below them.

The tables are the source; each tree is regenerated from its table and the passed-over labels that follow it.
With --check, nothing is written, and the run fails if a tree differs from what its table gives.
"""
import os
import re
import sys

DOC = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "05-rules.md")
COLOURS = {"act": "fill:#fde2c8,stroke:#d9822b", "hold": "fill:#dbe8f7,stroke:#4a7ab8",
           "finish": "fill:#d8f0d8,stroke:#3b8a3b", "idle": "fill:#eeeeee,stroke:#9e9e9e"}
SECTIONS = [("### Author\n", "### Reviewer\n", "author", "a"),
            ("### Reviewer\n", "### How to read them\n", "reviewer", "r")]


def parse(body):
    """The rows of a section's table, and its passed-over labels by rule number."""
    rows = []
    for line in body.splitlines():
        if re.match(r"^\| \d+ \|", line):
            n, name, when, kind, action, _ = [c.strip() for c in line.strip().strip("|").split("|")]
            rows.append(dict(n=int(n), name=name, when=when, kind=kind, action=action))
    if [r["n"] for r in rows] != list(range(1, len(rows) + 1)):
        raise SystemExit("rule numbers must run 1, 2, 3, … in table order")
    line = next((line for line in body.splitlines() if line.startswith("Passed-over labels:")), "")
    passed = {int(n): text for n, text in re.findall(r'(\d+) "([^"]+)"', line)}
    return rows, passed


def label(row):
    """A conclusion's label: its kind, then its action without targets — or, for hold and finish without an action,
    the rule's name — then any flags of the kind, such as low or remind."""
    base, *flags = [x.strip() for x in row["kind"].split(",")]
    words = row["action"].replace("`", "").replace("_", " ").split(" to ")[0]
    if not words and base in ("hold", "finish"):
        words = row["name"].lower()
    return base + (f" · {words}" if words else "") + "".join(f" · {f}" for f in flags)


def tree(rows, passed, root, prefix):
    lines = ["```mermaid", "flowchart TD", f'    s(["{root}"]) --> {prefix}1{{"1 {rows[0]["name"]}"}}']
    classes = {k: [] for k in COLOURS}
    last = len(rows)
    for row in rows:
        i, k = row["n"], f"{prefix}k{row['n']}"
        classes[row["kind"].split(",")[0].strip()].append(k)
        if i == last:
            lines.append(f'    {k}["{i} {label(row)}"]')
            continue
        yes = '"yes (model)"' if "judgement." in row["when"] else "yes"
        lines.append(f'    {prefix}{i} -->|{yes}| {k}["{label(row)}"]')
        target = f'{prefix}{i + 1}{{"{i + 1} {rows[i]["name"]}"}}' if i + 1 < last else f"{prefix}k{last}"
        lines.append(f"    {prefix}{i} -->" + (f"|{passed[i]}| " if i in passed else " ") + target)
    lines += [f"    classDef {kind} {style}" for kind, style in COLOURS.items() if classes[kind]]
    lines += [f"    class {','.join(ids)} {kind}" for kind, ids in classes.items() if ids]
    lines.append("```")
    return "\n".join(lines)


text = open(DOC, encoding="utf-8").read()
new = text
for start, end, root, prefix in SECTIONS:
    head, rest = new.split(start, 1)
    body, tail = rest.split(end, 1)
    rows, passed = parse(body)
    table_at = body.index("| # |")
    drawn = tree(rows, passed, root, prefix)
    new = head + start + "\n" + drawn + "\n\n" + body[table_at:] + end + tail
    print(f"{root}: {len(rows)} rules, {len(passed)} passed-over labels")

if "--check" in sys.argv:
    if new != text:
        print("the rule trees in 05-rules.md do not match their tables; run rule_trees.py to redraw them")
        sys.exit(1)
    print("the rule trees match their tables")
elif new != text:
    open(DOC, "w", encoding="utf-8").write(new)
    print("redrew the rule trees in 05-rules.md")
else:
    print("the rule trees were already up to date")
