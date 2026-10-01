#!/bin/sh
# Runs Park on this machine against real repositories, to try it before it is deployed: make db's Postgres, a
# database of its own, park_local, and http://localhost:8080.
#
#   scripts/local.sh [--executor-token FILE] [--bridge NAME --bridge-token FILE] owner/name...
#
# The first run sets Park up. It asks for the admin's username and password, a GitHub token that can read the
# repositories, and your GitHub login, which it links to the admin. It adds the GitHub forge, the token as an
# installation, and the repositories, and makes a token for park-executor, scoped to read and tasks:work, in
# ~/.config/park-executor/token, or the file --executor-token names. With --bridge, it registers a bridge of that name
# and makes its token, scoped to assertions:write, in the file --bridge-token names. Later runs only serve Park;
# Ctrl-C stops it.
#
# The secret key that encrypts the credentials Park stores is kept in ~/.config/park-local/secret_key, so that a
# restarted Park can read them; Park's log is there too. $PARK_LOCAL_DIR, $PARK_LOCAL_DB, and $PARK_LOCAL_PORT choose
# another directory, database, and port.
set -eu
cd "$(dirname "$0")/.."

bridge=""
bridge_token=""
executor_token="$HOME/.config/park-executor/token"
while [ $# -gt 0 ]; do
  case "$1" in
    --bridge) bridge="$2"; shift 2 ;;
    --bridge-token) bridge_token="$2"; shift 2 ;;
    --executor-token) executor_token="$2"; shift 2 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
if [ $# -eq 0 ]; then
  echo "usage: scripts/local.sh [--executor-token FILE] [--bridge NAME --bridge-token FILE] owner/name..." >&2
  exit 2
fi
if [ -n "$bridge" ] && [ -z "$bridge_token" ]; then
  echo "--bridge needs --bridge-token, the file for its token" >&2
  exit 2
fi

state="${PARK_LOCAL_DIR:-$HOME/.config/park-local}"
db="${PARK_LOCAL_DB:-park_local}"
port="${PARK_LOCAL_PORT:-8080}"
mkdir -p "$state"
umask 077
[ -s "$state/secret_key" ] || openssl rand -base64 32 > "$state/secret_key"
URL="http://localhost:$port"
export PARK_DATABASE_URL="postgres://park:park@localhost:5432/$db?sslmode=disable"
export PARK_SECRET_KEY="$(cat "$state/secret_key")"
export PARK_HTTP_ADDR="127.0.0.1:$port"
export PARK_HTTP_PUBLIC_URL="$URL"
PARK_SETUP_CODE="$(openssl rand -hex 8)"
export PARK_SETUP_CODE

# make and docker compose read standard input, which holds the answers to come when it is not a terminal.
make db < /dev/null > /dev/null
docker compose exec -T db psql -U park -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'" < /dev/null | grep -q 1 ||
  docker compose exec -T db psql -U park -qc "CREATE DATABASE $db" < /dev/null > /dev/null
[ -x bin/park ] || make build < /dev/null > /dev/null

bin/park serve >> "$state/park.log" 2>&1 &
park=$!
trap 'kill $park 2>/dev/null || true' EXIT INT TERM
for _ in $(seq 60); do curl -fsS -o /dev/null "$URL/readyz" 2>/dev/null && break; sleep 0.5; done
curl -fsS -o /dev/null "$URL/readyz"

# field reads one field of a JSON answer.
field() { python3 -c "import json, sys; print(json.load(sys.stdin)$1)"; }

# ask reads a line; hidden asks without echoing it.
ask() {
  printf '%s' "$1" >&2
  read -r answer || { echo "no answer" >&2; exit 1; }
  printf '%s' "$answer"
}
hidden() {
  printf '%s' "$1" >&2
  if [ -t 0 ]; then stty -echo; fi
  read -r answer || answer=""
  if [ -t 0 ]; then stty echo; fi
  echo >&2
  [ -n "$answer" ] || { echo "no answer" >&2; exit 1; }
  printf '%s' "$answer"
}

if [ "$(curl -fsS "$URL/api/v1/setup" | field '["needed"]')" = "True" ]; then
  echo "Setting Park up."
  username="$(ask "The admin's username [$USER]: ")"
  username="${username:-$USER}"
  password="$(hidden "The admin's password, at least 12 characters: ")"
  body="$(python3 -c 'import json, sys; print(json.dumps({"code": sys.argv[1], "username": sys.argv[2], "password": sys.argv[3], "display_name": sys.argv[2]}))' \
    "$PARK_SETUP_CODE" "$username" "$password")"
  cookie="$(curl -fsS -D - -o /dev/null -H 'Content-Type: application/json' -d "$body" "$URL/api/v1/setup" |
    sed -n 's/^[Ss]et-[Cc]ookie: \(park_session=[^;]*\).*/\1/p')"
  me="$(curl -fsS -H "Cookie: $cookie" "$URL/api/v1/me")"
  csrf="$(printf '%s' "$me" | field '["csrf_token"]')"
  member="$(printf '%s' "$me" | field '["member"]["id"]')"
  call() {
    curl -sS --fail-with-body -X "$1" -H "Cookie: $cookie" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' -d "$3" "$URL/api/v1$2"
  }

  token="$(hidden "A GitHub token that can read the repositories: ")"
  forge="$(call POST /admin/forges '{"kind": "github", "name": "GitHub", "base_address": "https://github.com"}' | field '["id"]')"
  body="$(python3 -c 'import json, sys; print(json.dumps({"forge_id": int(sys.argv[1]), "name": "github", "kind": "token", "token": sys.argv[2]}))' \
    "$forge" "$token")"
  installation="$(call POST /admin/installations "$body" | field '["id"]')"
  for repo in "$@"; do
    owner="${repo%%/*}"
    name="${repo#*/}"
    if answer="$(call POST /admin/repos "{\"installation_id\": $installation, \"owner\": \"$owner\", \"name\": \"$name\"}")"; then
      echo "Watching $repo."
    else
      why="$(printf '%s' "$answer" | field '["error"]["message"]' 2>/dev/null || true)"
      echo "Park could not watch $repo. ${why:-Can the token read it?} Add it later at /api/v1/admin/repos." >&2
    fi
  done

  login="$(ask "Your GitHub login: ")"
  account="$(curl -fsS "https://api.github.com/users/$login" | field '["id"]')"
  call POST "/admin/members/$member/identities" "{\"forge_id\": $forge, \"account_id\": $account}" > /dev/null
  echo "Linked $login to $username."

  mkdir -p "$(dirname "$executor_token")"
  call POST /me/tokens '{"name": "park-executor", "scopes": ["read", "tasks:work"]}' | field '["token"]' > "$executor_token"
  echo "Made park-executor's token, in $executor_token."
  if [ -n "$bridge" ]; then
    id="$(call POST /admin/bridges "$(python3 -c 'import json, sys; print(json.dumps({"name": sys.argv[1]}))' "$bridge")" | field '["id"]')"
    mkdir -p "$(dirname "$bridge_token")"
    call POST /me/tokens "{\"name\": \"bridge\", \"scopes\": [\"assertions:write\"], \"bridge_id\": $id}" | field '["token"]' > "$bridge_token"
    echo "Registered the bridge $bridge, and made its token, in $bridge_token."
  fi
  echo "Turn on the actions park-executor should do under Settings, Automation."
fi
echo "Park is at $URL; its log is $state/park.log. Ctrl-C stops it."
wait $park
