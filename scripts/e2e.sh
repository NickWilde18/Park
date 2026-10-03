#!/bin/sh
# The dashboard's end-to-end tests: Park serving a database seeded by cmd/seed, and Playwright against it. Park has
# the reference bridge plugin, cmd/park-bridge-file, in its plugin directory, and its public address is where it
# serves, so that connecting a bridge's account comes back to it.
#
# PARK_DATABASE_URL names an empty database, which the run fills. bin/park must be built first: make e2e does both.
# Arguments go to Playwright: sh scripts/e2e.sh settings runs only the tests in files matching settings.
set -eu
cd "$(dirname "$0")/.."
: "${PARK_DATABASE_URL:?set PARK_DATABASE_URL to an empty database}"
PARK_SECRET_KEY="${PARK_SECRET_KEY:-$(openssl rand -base64 32)}"
PARK_HTTP_ADDR="${PARK_HTTP_ADDR:-127.0.0.1:8090}"
PARK_URL="http://$PARK_HTTP_ADDR"
PARK_HTTP_PUBLIC_URL="$PARK_URL"
PARK_BRIDGES_PLUGIN_DIR="$(mktemp -d)"
export PARK_SECRET_KEY PARK_HTTP_ADDR PARK_URL PARK_HTTP_PUBLIC_URL PARK_BRIDGES_PLUGIN_DIR
pid=
trap 'if [ -n "$pid" ]; then kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi; rm -rf "$PARK_BRIDGES_PLUGIN_DIR"' EXIT

go run ./cmd/seed
go build -o "$PARK_BRIDGES_PLUGIN_DIR/park-bridge-file" ./cmd/park-bridge-file
log="$(mktemp)"
bin/park serve > "$log" 2>&1 &
pid=$!
for _ in $(seq 60); do
  curl -fsS -o /dev/null "$PARK_URL/readyz" 2>/dev/null && break
  sleep 0.5
done
curl -fsS -o /dev/null "$PARK_URL/readyz"
if ! (cd web && pnpm exec playwright test "$@"); then
  echo "--- park serve's log"
  cat "$log"
  exit 1
fi
