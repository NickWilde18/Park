"""Checks scripts/local.sh's questions, as a person answers them at a terminal.

It types into local.sh through a pseudo-terminal, against a fake GitHub API, in
databases of its own, named park_lsfix_..., on ports from 8110, and drops them after:

- a password with spaces at its ends signs in as it was typed, and the admin's
  username, $USER by default, is said;
- a password Park refuses is explained, and the username and password asked again;
- a token GitHub refuses, or one without the repo and read:org scopes, is explained,
  and the token asked again;
- a later run, with no arguments, serves the Park the first run set up;
- a first run without repositories says it needs them, and ends.

It needs make db's Postgres and bin/park, so it runs here rather than in CI:

    python3 scripts/local_test.py
"""

import http.server
import json
import os
import pty
import re
import select
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PROMPT = "The admin's username"
PASSWORD = "a long enough password"

# The fake GitHub's tokens, with the scopes it says each has; any other is refused.
SCOPES = {"repo-only-token": "repo", "full-token": "read:org, repo"}


class FakeGitHub(http.server.BaseHTTPRequestHandler):
    """GitHub's API as local.sh calls it: /user by token, and /users/{login}."""

    def do_GET(self):
        token = self.headers.get("Authorization", "").removeprefix("Bearer ")
        if self.path == "/user" and token in SCOPES:
            self.answer(200, {"login": "tester"}, SCOPES[token])
        elif self.path.startswith("/users/"):
            self.answer(200, {"id": 1, "login": self.path.removeprefix("/users/")})
        else:
            self.answer(401, {"message": "Bad credentials"})

    def answer(self, status, body, scopes=None):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        if scopes is not None:
            self.send_header("X-OAuth-Scopes", scopes)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


def free_port():
    """The first port from 8110 that nothing listens on."""
    for port in range(8110, 8120):
        with socket.socket() as s:
            if s.connect_ex(("127.0.0.1", port)) != 0:
                return port
    sys.exit("no free port from 8110 to 8119")


class LocalSh:
    """One run of scripts/local.sh, typed into through a pseudo-terminal.

    Its arguments are the executor token's file and one repository, unless args gives
    others; a run that follows another passes that run's database and directory."""

    def __init__(self, github, args=None, db=None, state=None):
        self.github = github
        self.db = db or "park_lsfix_" + os.urandom(4).hex()
        self.port = free_port()
        self.state = state or tempfile.mkdtemp(prefix="local-test-")
        self.out, self.seen, self.failures, self.done = "", 0, [], False
        self.stopped = False
        env = dict(os.environ, USER="tester", PARK_LOCAL_DIR=self.state,
                   PARK_LOCAL_DB=self.db, PARK_LOCAL_PORT=str(self.port),
                   PARK_LOCAL_GITHUB_API=github)
        if args is None:
            token = os.path.join(self.state, "executor-token")
            args = ["--executor-token", token, "owner-a/api"]
        args = ["sh", "scripts/local.sh", *args]
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(ROOT)
            os.execvpe("sh", args, env)

    def expect(self, text, timeout=10):
        """Waits for text after what was expected before; a miss is a failure."""
        deadline = time.time() + timeout
        while True:
            at = self.out.find(text, self.seen)
            if at >= 0:
                self.seen = at + len(text)
                return True
            if self.done or time.time() > deadline:
                why = "local.sh ended" if self.done else f"{timeout}s passed"
                self.failures.append(f"expected {text!r}, but {why}")
                return False
            ready, _, _ = select.select([self.fd], [], [], 0.2)
            if ready:
                try:
                    chunk = os.read(self.fd, 4096)
                except OSError:
                    chunk = b""
                self.done = not chunk
                self.out += chunk.decode(errors="replace")

    def type(self, line):
        os.write(self.fd, (line + "\r").encode())

    def check(self, ok, failure):
        if not ok:
            self.failures.append(failure)

    def sign_in(self, username, password):
        """The status of signing in to this run's Park."""
        body = json.dumps({"username": username, "password": password}).encode()
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/api/v1/auth/login", data=body,
            method="POST", headers={"Content-Type": "application/json"})
        try:
            return urllib.request.urlopen(req, timeout=10).status
        except urllib.error.HTTPError as e:
            return e.code

    def set_up(self, username, password):
        """Answers the admin's questions; true once Park asks for the token."""
        if not self.expect(PROMPT, 180):
            return False
        self.type(username)
        self.expect("password")
        self.type(password)
        return self.expect("GitHub token", 30)

    def ended(self, timeout):
        """Whether local.sh ends within timeout seconds; it is reaped if so."""
        deadline = time.time() + timeout
        while True:
            try:
                if os.waitpid(self.pid, os.WNOHANG) != (0, 0):
                    return True
            except ChildProcessError:
                return True
            if time.time() >= deadline:
                return False
            time.sleep(0.2)

    def stop(self):
        """Stops local.sh and its Park, and keeps the database and the directory."""
        if self.stopped:
            return
        self.stopped = True
        for sig in (signal.SIGTERM, signal.SIGKILL):
            if self.ended(0):
                break
            try:
                os.killpg(self.pid, sig)
            except (ProcessLookupError, PermissionError):
                pass  # it ended meanwhile: macOS refuses to signal a group of zombies
            if self.ended(10):
                break
        os.close(self.fd)
        subprocess.run(f"lsof -t -iTCP:{self.port} -sTCP:LISTEN | xargs kill",
                       shell=True, capture_output=True, check=False)

    def close(self):
        """Stops local.sh and its Park, then drops the database and the directory."""
        self.stop()
        subprocess.run(["docker", "compose", "exec", "-T", "db", "psql", "-U", "park",
                        "-qc", f"DROP DATABASE IF EXISTS {self.db} WITH (FORCE)"],
                       cwd=ROOT, stdin=subprocess.DEVNULL, capture_output=True,
                       check=False)
        shutil.rmtree(self.state, ignore_errors=True)


def padded_password(sh):
    """The default username is said, and a password keeps the spaces at its ends."""
    password = "  " + PASSWORD + "  "
    if not sh.expect(PROMPT, 180):
        return
    sh.type("")
    sh.expect("password")
    sh.type(password)
    sh.expect("Created the admin tester.")
    if sh.expect("GitHub token", 30):
        status = sh.sign_in("tester", password)
        sh.check(status == 200, f"signing in as typed answered {status}, not 200")


def refused_password(sh):
    """A password too short is explained, and the admin is asked for again."""
    if not sh.expect(PROMPT, 180):
        return
    sh.type("shorty")
    sh.expect("password")
    sh.type("short")
    sh.expect("A password is 12 to 256 characters long.")
    if not sh.expect(PROMPT):
        return
    sh.type("shorty")
    sh.expect("password")
    sh.type(PASSWORD)
    sh.expect("Created the admin shorty.")
    if sh.expect("GitHub token", 30):
        status = sh.sign_in("shorty", PASSWORD)
        sh.check(status == 200, f"signing in answered {status}, not 200")


def refused_tokens(sh):
    """A token refused, or without a scope, is explained and asked for again."""
    if not sh.set_up("tokens", PASSWORD):
        return
    sh.check("read:org" in sh.out[sh.out.rfind("\n"):],
             "the token's question does not name the read:org scope")
    for token, why in [("refused-token", "GitHub refused that token."),
                       ("repo-only-token", "That token lacks the read:org scope.")]:
        sh.type(token)
        if not (sh.expect(why, 15) and sh.expect("GitHub token")):
            return
    sh.type("full-token")
    sh.expect("Your GitHub login", 30)


def later_run(sh):
    """A later run, with no arguments, serves the Park the first run set up.

    The first run sets up the admin, and is stopped when it asks for the GitHub token:
    Park is set up once it has an admin. The second run, scripts/local.sh alone, on the
    same database and directory, says where Park is, and the admin signs in."""
    if not sh.set_up("later", PASSWORD):
        return
    sh.stop()
    again = LocalSh(sh.github, args=[], db=sh.db, state=sh.state)
    try:
        if again.expect("Park is at", 180):
            status = again.sign_in("later", PASSWORD)
            again.check(status == 200,
                        f"signing in to the later run answered {status}, not 200")
    finally:
        again.stop()
        sh.out += again.out
        sh.failures += again.failures


def first_run_needs_repositories(sh):
    """A first run without repositories says it needs them, and ends, asking nothing."""
    if sh.expect("needs the repositories to watch", 180):
        sh.check(sh.ended(15), "local.sh went on without repositories")
        sh.check(PROMPT not in sh.out,
                 "local.sh asked for the admin without repositories")


def main():
    github = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeGitHub)
    threading.Thread(target=github.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{github.server_address[1]}"
    failed = False
    cases = [(padded_password, None), (refused_password, None), (refused_tokens, None),
             (later_run, None),
             (first_run_needs_repositories, ["--executor-token", os.devnull])]
    for case, args in cases:
        sh = LocalSh(url, args=args)
        try:
            case(sh)
        finally:
            sh.close()
        if sh.failures:
            failed = True
            print(f"FAIL {case.__name__}: {case.__doc__}")
            for f in sh.failures:
                print(f"  {f}")
            print("  local.sh said:")
            said = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", sh.out.replace("\r", ""))
            for line in said.splitlines()[-12:]:
                print(f"    | {line}")
        else:
            print(f"ok   {case.__name__}")
    github.shutdown()
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
