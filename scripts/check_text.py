"""Checks the text of every file in the repository: English only, and no private names.

Park is public from its first commit, so no file may carry an organization's name, a colleague's username, an
internal project's name, or an internal pull request number. Those names are kept outside the repository, in a file
of regular expressions — one per line, matched regardless of case, with # starting a comment line. The file is
$PARK_PRIVATE_NAMES_FILE, or else ~/.config/park/private-names. Without it, only the language is checked.

File paths are checked as well as contents. Files that are not UTF-8 text are skipped.
"""
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, capture_output=True, text=True,
                      check=True).stdout.strip()
CJK = re.compile("[\u3000-\u303f\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uff00-\uffef]")


def private_names():
    path = os.environ.get("PARK_PRIVATE_NAMES_FILE") or os.path.expanduser("~/.config/park/private-names")
    if not os.path.isfile(path):
        print(f"private names: no list at {path}; only the language is checked")
        return []
    names = []
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.strip()
            if line and not line.startswith("#"):
                names.append((n, re.compile(line, re.IGNORECASE)))
    print(f"private names: {len(names)} from {path}")
    return names


names = private_names()
listed = subprocess.run(["git", "ls-files", "--cached", "--others", "--exclude-standard"], cwd=ROOT,
                        capture_output=True, text=True, check=True).stdout.splitlines()
files = sorted(f for f in listed if os.path.isfile(os.path.join(ROOT, f)))
problems = []
checked = 0
for f in files:
    for entry, pattern in names:
        if m := pattern.search(f):
            problems.append(f"{f}: the path has a private name, {m[0]!r} (entry {entry})")
    try:
        text = open(os.path.join(ROOT, f), encoding="utf-8").read()
    except UnicodeDecodeError:
        continue
    checked += 1
    for n, line in enumerate(text.splitlines(), 1):
        if CJK.search(line):
            problems.append(f"{f}:{n}: not English: {line.strip()[:60]}")
        for entry, pattern in names:
            for m in pattern.finditer(line):
                problems.append(f"{f}:{n}: private name {m[0]!r} (entry {entry})")

print(f"{checked} text files of {len(files)}")
print("\n".join(problems) or "no problems")
sys.exit(1 if problems else 0)
