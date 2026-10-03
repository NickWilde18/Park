package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// call answers one operation for a request, through JSON as Park sends and reads it.
func call(t *testing.T, op string, req map[string]any) map[string]any {
	t.Helper()
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := answer(op, strings.NewReader(string(in)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// chatsFile writes a chats file with one chat, whose two threads are a day apart, and returns its credentials.
func chatsFile(t *testing.T, extra string) map[string]string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chats.json")
	content := `{` + extra + `"chats": [{"id": "c1", "name": "Reviews", "threads": [
		{"anchor": "m1", "created": "2026-10-01T12:00:00Z", "text": "one", "plain": "one", "links": []},
		{"anchor": "m2", "created": "2026-10-02T12:00:00Z", "text": "two", "plain": "two", "links": ["https://forge.test/o/r/pull/1"]}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"file": path}
}

// The plugin describes itself as protocol 1, kind file, with its one credential.
func TestDescribe(t *testing.T) {
	got := call(t, "describe", nil)
	if got["protocol"] != float64(1) || got["kind"] != "file" || got["name"] != "File" {
		t.Fatalf("got %v", got)
	}
}

// Signing in goes straight back to the redirect address, keeping its query, with the challenge as the code and the
// state; exchanging the code takes the verifier whose digest it is, and refuses another, and answers the file's
// account, or a reader's when it names none, with fresh tokens.
func TestSignIn(t *testing.T) {
	creds := chatsFile(t, "")
	verifier := "a verifier of enough length to be one"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	got := call(t, "authorize", map[string]any{"credentials": creds, "redirect_uri": "https://park.test/back?x=1", "state": "s1",
		"code_challenge": challenge})
	u, err := url.Parse(got["url"].(string))
	if err != nil || u.Host != "park.test" || u.Path != "/back" || u.Query().Get("x") != "1" || u.Query().Get("state") != "s1" ||
		u.Query().Get("code") != challenge {
		t.Fatalf("sent the browser to %v", got["url"])
	}
	got = call(t, "exchange", map[string]any{"credentials": creds, "code": challenge, "code_verifier": "another verifier"})
	if got["error"].(map[string]any)["code"] != "invalid_grant" {
		t.Fatalf("a wrong verifier: %v", got)
	}
	got = call(t, "exchange", map[string]any{"credentials": creds, "code": challenge, "code_verifier": verifier})
	account := got["account"].(map[string]any)
	if account["id"] != "reader" || account["name"] != "File reader" || got["tokens"].(map[string]any)["uses"] != float64(0) {
		t.Fatalf("exchanged: %v", got)
	}
	named := chatsFile(t, `"account": {"id": "u-1", "name": "Ana"}, `)
	got = call(t, "exchange", map[string]any{"credentials": named, "code": challenge, "code_verifier": verifier})
	if got["account"].(map[string]any)["id"] != "u-1" {
		t.Fatalf("the file's account: %v", got)
	}
}

// Each operation renews the tokens it was given; threads come from the chat asked for, from the time asked for, with
// how many messages the chat has; a chat the file does not have is an error.
func TestChatsAndThreads(t *testing.T) {
	creds := chatsFile(t, "")
	got := call(t, "chats", map[string]any{"credentials": creds, "tokens": map[string]int{"uses": 4}})
	if chats := got["chats"].([]any); len(chats) != 1 || chats[0].(map[string]any)["id"] != "c1" ||
		got["tokens"].(map[string]any)["uses"] != float64(5) {
		t.Fatalf("got %v", got)
	}
	got = call(t, "threads", map[string]any{"credentials": creds, "tokens": map[string]int{"uses": 5}, "chat": "c1",
		"since": "2026-10-02T00:00:00Z"})
	threads := got["threads"].([]any)
	if got["read"] != float64(2) || len(threads) != 1 || threads[0].(map[string]any)["anchor"] != "m2" ||
		got["tokens"].(map[string]any)["uses"] != float64(6) {
		t.Fatalf("got %v", got)
	}
	got = call(t, "threads", map[string]any{"credentials": creds, "tokens": map[string]int{"uses": 6}, "chat": "c9"})
	if got["error"].(map[string]any)["code"] != "no_chat" || got["tokens"].(map[string]any)["uses"] != float64(7) {
		t.Fatalf("an unknown chat: %v", got)
	}
}

// With an error in the file, every operation but describe answers it, with renewed tokens all the same; an unreadable
// file is an error too; an unknown operation is refused before anything is read.
func TestFailures(t *testing.T) {
	creds := chatsFile(t, `"error": {"code": "signin_lapsed", "message": "expired"}, `)
	got := call(t, "chats", map[string]any{"credentials": creds, "tokens": map[string]int{"uses": 1}})
	if got["error"].(map[string]any)["code"] != "signin_lapsed" || got["tokens"].(map[string]any)["uses"] != float64(2) {
		t.Fatalf("got %v", got)
	}
	got = call(t, "chats", map[string]any{"credentials": map[string]string{"file": filepath.Join(t.TempDir(), "missing.json")}})
	if got["error"].(map[string]any)["code"] != "unreadable" || got["tokens"] != nil {
		t.Fatalf("an unreadable file, with no tokens given: %v", got)
	}
	if _, err := answer("nope", strings.NewReader("{}")); err == nil || !strings.Contains(err.Error(), `unknown operation "nope"`) {
		t.Fatalf("got %v, want the operation refused", err)
	}
}
