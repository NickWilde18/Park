package bridges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NickWilde18/Park/internal/bridges/bridgestest"
)

// filePlugin is the reference plugin, cmd/park-bridge-file, built once for the package's tests.
var filePlugin string

func TestMain(m *testing.M) {
	path, done, err := bridgestest.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	filePlugin = path
	code := m.Run()
	done()
	os.Exit(code)
}

// script writes a shell script into dir as an executable named name, and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// describing is a script's body that describes a plugin of a kind, then exits.
func describing(kind string) string {
	return `[ "$1" = describe ] && echo '{"protocol": 1, "kind": "` + kind + `", "name": "Chat ` + kind + `", "fields": []}' && exit 0`
}

// A directory's plugins are its executable files, asked what they are: a file that is not executable, a hidden file,
// and a subdirectory are left alone, and the plugins come back by kind. No directory means no plugins.
func TestLoad(t *testing.T) {
	ctx := context.Background()
	if ps, err := Load(ctx, ""); err != nil || ps != nil {
		t.Fatalf("Load(\"\") = %v, %v; want no plugins", ps, err)
	}
	dir := bridgestest.Dir(t, filePlugin)
	script(t, dir, "chat-b", describing("chat-b"))
	script(t, dir, ".hidden", "exit 1")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a plugin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Kind != "chat-b" || ps[1].Kind != "file" {
		t.Fatalf("got %+v, want chat-b and file, by kind", ps)
	}
	if f := ps[1]; f.Name != "File" || len(f.Fields) != 1 || f.Fields[0] != (Field{Key: "file", Label: "File"}) || !filepath.IsAbs(f.Path) {
		t.Fatalf("the reference plugin loaded as %+v", f)
	}
}

// Park does not start with a plugin directory it cannot account for: a plugin that fails to describe itself, speaks
// another protocol, gives a malformed kind or field, or claims a kind another plugin has, is an error that names it.
func TestLoadRefuses(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"fails":     {`echo "nothing to say" >&2; exit 3`, "it said: nothing to say"},
		"no json":   {`echo hello`, "no JSON object"},
		"protocol":  {`echo '{"protocol": 2, "kind": "x", "name": "X", "fields": []}'`, "speaks plugin protocol 2"},
		"kind":      {`echo '{"protocol": 1, "kind": "Chat X", "name": "X", "fields": []}'`, `gave its kind as "Chat X"`},
		"name":      {`echo '{"protocol": 1, "kind": "x", "name": "", "fields": []}'`, "a name is 1 to 100"},
		"field key": {`echo '{"protocol": 1, "kind": "x", "name": "X", "fields": [{"key": "App ID", "label": "A"}]}'`, `has a field "App ID"`},
		"twice": {`echo '{"protocol": 1, "kind": "x", "name": "X", "fields": [{"key": "a", "label": "A"}, {"key": "a", "label": "B"}]}'`,
			`has a field "a"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := script(t, dir, "plugin", c.body)
			_, err := Load(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("got %v, want an error naming %s and saying %q", err, path, c.want)
			}
		})
	}
	t.Run("same kind", func(t *testing.T) {
		dir := t.TempDir()
		script(t, dir, "one", describing("x"))
		script(t, dir, "two", describing("x"))
		_, err := Load(context.Background(), dir)
		if err == nil || !strings.Contains(err.Error(), `both have the kind "x"`) {
			t.Fatalf("got %v, want the two plugins of one kind named", err)
		}
	})
}

// A plugin sees only the environment it needs: Park's own variables, its secret key and database among them, never
// reach it, while PATH, which finding programs needs, does.
func TestPluginEnvironment(t *testing.T) {
	t.Setenv("PARK_SECRET_KEY", "a secret key for the test")
	t.Setenv("PARK_DATABASE_URL", "postgres://secret@db/park")
	p := Plugin{Kind: "env", Path: script(t, t.TempDir(), "env",
		`printf '{"secret": "%s", "database": "%s", "path": "%s"}' "$PARK_SECRET_KEY" "$PARK_DATABASE_URL" "$PATH"`)}
	var out struct{ Secret, Database, Path string }
	if _, err := p.run(context.Background(), "op", time.Minute, struct{}{}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Secret != "" || out.Database != "" {
		t.Fatalf("the plugin saw Park's variables: %+v", out)
	}
	if out.Path == "" {
		t.Fatal("the plugin did not see PATH")
	}
}

// The request arrives on the plugin's standard input as JSON, and the operation as its one argument.
func TestPluginRequest(t *testing.T) {
	p := Plugin{Kind: "echo", Path: script(t, t.TempDir(), "echo", `printf '{"op": "%s", "request": %s}' "$1" "$(cat)"`)}
	var out struct {
		Request map[string]string `json:"request"`
		Op      string            `json:"op"`
	}
	if _, err := p.run(context.Background(), "chats", time.Minute, map[string]string{"chat": "c1"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Op != "chats" || out.Request["chat"] != "c1" {
		t.Fatalf("the plugin got %+v", out)
	}
}

// A plugin that renewed its tokens and then failed still hands them over: Park keeps tokens that come with an error,
// since the old ones are spent. The error is the plugin's own, with its code.
func TestTokensComeWithAnError(t *testing.T) {
	p := Plugin{Kind: "lapsed", Path: script(t, t.TempDir(), "lapsed",
		`echo '{"error": {"code": "signin_lapsed", "message": "the refresh token expired"}, "tokens": {"generation": 7}}'`)}
	tokens, err := p.run(context.Background(), "chats", time.Minute, struct{}{}, nil)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != SignInLapsed {
		t.Fatalf("got %v, want the plugin's signin_lapsed error", err)
	}
	var got struct{ Generation int }
	if err := json.Unmarshal(tokens, &got); err != nil || got.Generation != 7 {
		t.Fatalf("got tokens %s, want the renewed ones", tokens)
	}
}

// A plugin that does not answer in time is stopped, and the error says how long it had; one that exits without an
// answer has what it wrote to its standard error in the error, cut short.
func TestPluginFailures(t *testing.T) {
	dir := t.TempDir()
	slow := Plugin{Kind: "slow", Path: script(t, dir, "slow", "sleep 10")}
	started := time.Now()
	_, err := slow.run(context.Background(), "chats", 200*time.Millisecond, struct{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "gave no answer to chats within 200ms") {
		t.Fatalf("got %v, want the time limit", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("the slow plugin was not stopped: it ran %s", time.Since(started))
	}
	noisy := Plugin{Kind: "noisy", Path: script(t, dir, "noisy", `head -c 5000 /dev/zero | tr '\0' x >&2; exit 4`)}
	_, err = noisy.run(context.Background(), "chats", time.Minute, struct{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 4") || !strings.Contains(err.Error(), "xxx…") ||
		len(err.Error()) > maxStderr+200 {
		t.Fatalf("got %d characters: %.300v; want the exit status and the standard error, cut short", len(err.Error()), err)
	}
}

// A canceled request stops its plugin, and says it was canceled, not that the plugin was slow.
func TestPluginCanceled(t *testing.T) {
	p := Plugin{Kind: "slow", Path: script(t, t.TempDir(), "slow", "sleep 10")}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := p.run(ctx, "chats", time.Minute, struct{}{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancellation", err)
	}
}
