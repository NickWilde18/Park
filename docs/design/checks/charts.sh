#!/bin/sh
# Renders every mermaid block in the repository's Markdown with mermaid-cli. A block that does not parse fails the
# run, since GitHub shows such a block as an error instead of a chart. Needs Node; the first run downloads
# mermaid-cli and the browser it renders with.
set -eu

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
status=0
for f in $(git -C "$root" ls-files --cached --others --exclude-standard -- '*.md'); do
	[ -f "$root/$f" ] && grep -q '^```mermaid' "$root/$f" || continue
	name=$(printf '%s' "$f" | tr '/' '_')
	if npx -y @mermaid-js/mermaid-cli@11 -q -i "$root/$f" -o "$out/$name" >"$out/log" 2>&1; then
		echo "ok   $f: $(ls "$out/${name%.md}"-*.svg | wc -l | tr -d ' ') charts"
	else
		echo "FAIL $f"
		sed 's/\x1b\[[0-9;]*m//g' "$out/log" | head -20
		status=1
	fi
done
exit $status
