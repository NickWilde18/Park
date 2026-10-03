#!/bin/sh
# Runs Park on this machine against real repositories, to try it before it is deployed: make db's Postgres, a
# database of its own, park_local, and http://localhost:8080.
#
#   scripts/local.sh [--executor-token FILE] [--bridge NAME --bridge-token FILE] owner/name...
#
# The first run sets Park up. It asks for the admin's username and password, a classic GitHub token with the repo and
# read:org scopes, which must read the repositories, and your GitHub login, which it links to the admin. An answer
# Park or GitHub refuses is explained and asked for again. It adds the GitHub forge, the token as an installation, and
# the repositories, and makes a token for park-executor, scoped to read and tasks:work, in
# ~/.config/park-executor/token, or the file --executor-token names. With --bridge, it registers a bridge of that name
# and makes its token, scoped to assertions:write, in the file --bridge-token names. Later runs only serve Park;
# Ctrl-C stops it.
#
# The secret key that encrypts the credentials Park stores is kept in ~/.config/park-local/secret_key, so that a
# restarted Park can read them; Park's log is there too. The bridge plugins in ~/.config/park-local/plugins, when it
# exists, are Park's (bridges.plugin_dir), unless $PARK_BRIDGES_PLUGIN_DIR names another directory; Park finds them
# when it starts. $PARK_LOCAL_DIR, $PARK_LOCAL_DB, and $PARK_LOCAL_PORT choose another directory, database, and port,
# and $PARK_LOCAL_GITHUB_API another address for GitHub's API, which scripts/local_test.py fakes.
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
if [ -z "${PARK_BRIDGES_PLUGIN_DIR:-}" ] && [ -d "$state/plugins" ]; then
  export PARK_BRIDGES_PLUGIN_DIR="$state/plugins"
fi
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

# field reads one field of a JSON answer; reason reads the message of an error answer, as curl -i prints it.
field() { python3 -c "import json, sys; print(json.load(sys.stdin)$1)"; }
reason() { tr -d '\r' | sed '1,/^$/d' | field '["error"]["message"]' 2>/dev/null || echo "It gave no reason."; }

# ask reads a line; hidden asks without echoing it, and keeps the line as typed, spaces at its ends too.
ask() {
  printf '%s' "$1" >&2
  read -r answer || { echo "no answer" >&2; exit 1; }
  printf '%s' "$answer"
}
hidden() {
  printf '%s' "$1" >&2
  if [ -t 0 ]; then stty -echo; fi
  IFS= read -r answer || answer=""
  if [ -t 0 ]; then stty echo; fi
  echo >&2
  [ -n "$answer" ] || { echo "no answer" >&2; exit 1; }
  printf '%s' "$answer"
}

# The password and the token reach python3 and curl through the environment and standard input, never a command line,
# which every user of this machine can read.
github="${PARK_LOCAL_GITHUB_API:-https://api.github.com}"
if [ "$(curl -fsS "$URL/api/v1/setup" | field '["needed"]')" = "True" ]; then
  echo "Setting Park up."
  while :; do
    username="$(ask "The admin's username [$USER]: ")"
    username="${username:-$USER}"
    password="$(hidden "The admin's password, at least 12 characters: ")"
    reply="$(code="$PARK_SETUP_CODE" username="$username" password="$password" python3 -c 'import json, os; e = os.environ
print(json.dumps({"code": e["code"], "username": e["username"], "password": e["password"], "display_name": e["username"]}))' |
      curl -sS -i -H 'Content-Type: application/json' --data-binary @- "$URL/api/v1/setup")"
    cookie="$(printf '%s\n' "$reply" | sed -n 's/^[Ss]et-[Cc]ookie: \(park_session=[^;]*\).*/\1/p')"
    [ -z "$cookie" ] || break
    echo "Park refused that. $(printf '%s\n' "$reply" | reason)" >&2
  done
  # Park keeps a username in lower case.
  username="$(printf '%s' "$username" | tr '[:upper:]' '[:lower:]')"
  echo "Created the admin $username."
  me="$(curl -fsS -H "Cookie: $cookie" "$URL/api/v1/me")"
  csrf="$(printf '%s' "$me" | field '["csrf_token"]')"
  member="$(printf '%s' "$me" | field '["member"]["id"]')"
  call() {
    printf '%s' "$3" | curl -sS --fail-with-body -X "$1" -H "Cookie: $cookie" -H "X-CSRF-Token: $csrf" \
      -H 'Content-Type: application/json' --data-binary @- "$URL/api/v1$2"
  }

  # GitHub names a classic token's scopes in X-OAuth-Scopes; a fine-grained token's answer has no such header to check.
  while :; do
    token="$(hidden "A classic GitHub token with the repo and read:org scopes: ")"
    reply="$(printf 'Authorization: Bearer %s\n' "$token" | curl -sS -o /dev/null -D - -H @- "$github/user" | tr -d '\r')"
    status="$(printf '%s\n' "$reply" | sed -n '1s/^HTTP[^ ]* \([0-9]*\).*/\1/p')"
    case "$status" in
      200) ;;
      401) echo "GitHub refused that token." >&2; continue ;;
      "") echo "GitHub did not answer; is this machine online?" >&2; continue ;;
      *) echo "GitHub answered $status to that token." >&2; continue ;;
    esac
    printf '%s\n' "$reply" | grep -qi '^x-oauth-scopes:' || break
    scopes=", $(printf '%s\n' "$reply" | grep -i '^x-oauth-scopes:' | sed 's/^[^:]*: *//'),"
    missing=""
    case "$scopes" in *", repo,"*) ;; *) missing="repo" ;; esac
    case "$scopes" in *", read:org,"* | *", write:org,"* | *", admin:org,"*) ;; *) missing="${missing:+$missing and }read:org" ;; esac
    [ -n "$missing" ] || break
    case "$missing" in
      *" and "*) echo "That token lacks the $missing scopes." >&2 ;;
      *) echo "That token lacks the $missing scope." >&2 ;;
    esac
  done
  forge="$(call POST /admin/forges '{"kind": "github", "name": "GitHub", "base_address": "https://github.com"}' | field '["id"]')"
  body="$(forge="$forge" token="$token" python3 -c 'import json, os; e = os.environ
print(json.dumps({"forge_id": int(e["forge"]), "name": "github", "kind": "token", "token": e["token"]}))')"
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
  account="$(curl -fsS "$github/users/$login" | field '["id"]')"
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
