package bridges

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Protocol is the version of the plugin protocol this Park speaks: docs/design/12-bridge-plugins.md.
const Protocol = 1

// Field is one of a plugin's credentials: its key, its label for admins, and whether it is a secret. Park never shows
// a credential's value again once it is entered; a secret field is only entered as a password is.
type Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

// Plugin is a plugin Park found in its plugin directory: where its executable is, and what it said of itself.
type Plugin struct {
	Kind   string
	Name   string
	Path   string
	Fields []Field
}

// Account is a platform account, by the platform's ID for it and the name it shows.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Chat is a chat an account can read, by the platform's ID for it and its name.
type Chat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Thread is one of a chat's root messages that links anything. Anchor is the platform's ID for the message. Text is
// the message as words, a paragraph a line, with each link's address after its label; Plain is the same with labels
// only, as people read it; Links are the links' addresses, in the order they appear.
type Thread struct {
	Created time.Time `json:"created"`
	Anchor  string    `json:"anchor"`
	Text    string    `json:"text"`
	Plain   string    `json:"plain"`
	Links   []string  `json:"links"`
}

// Error is what a plugin answered instead of a result: a code Park may act on, and a message for its log.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return "the plugin answered " + e.Code + ": " + e.Message }

// SignInLapsed is the code of a plugin's error saying that the account can no longer be used, and has to be
// connected again.
const SignInLapsed = "signin_lapsed"

const (
	// describeTimeout bounds a plugin's answer to what it is, callTimeout every other operation.
	describeTimeout = 10 * time.Second
	callTimeout     = 2 * time.Minute
	// maxOutput bounds what Park reads of a plugin's answer, maxStderr what it keeps of its standard error for the log.
	maxOutput = 16 << 20
	maxStderr = 1 << 10
)

// passed is the part of Park's environment a plugin sees: what finding programs and files, the time, certificates, and
// a network proxy need. Park's own variables — its secret key and its database among them — are never passed.
var passed = []string{"PATH", "HOME", "TMPDIR", "TZ", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"}

// environment is the environment a plugin runs with.
func environment() []string {
	var env []string
	for _, name := range passed {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// envelope is what any operation's answer may carry besides its result: an error, and tokens to keep.
type envelope struct {
	Error  *Error          `json:"error"`
	Tokens json.RawMessage `json:"tokens"`
}

// capped keeps up to limit bytes of what is written to it, and whether more came.
type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// run runs the plugin for one operation, with request as JSON on its standard input, and reads the one JSON object
// it writes to its standard output into result. It returns the tokens the plugin answered with, if it answered any —
// also alongside an error, since a plugin that renewed its tokens before it failed has spent the old ones. Neither
// the request nor the answer is ever logged: they carry credentials, tokens, and a chat's text.
func (p Plugin) run(ctx context.Context, op string, timeout time.Duration, request, result any) (json.RawMessage, error) {
	in, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	limited, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(limited, p.Path, op)
	cmd.Env = environment()
	cmd.Dir = filepath.Dir(p.Path)
	cmd.Stdin = bytes.NewReader(in)
	stdout, stderr := &capped{limit: maxOutput}, &capped{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	ran := cmd.Run()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limited.Err() != nil {
		return nil, fmt.Errorf("the plugin %s gave no answer to %s within %s", p.Kind, op, timeout)
	}
	if stdout.over {
		return nil, fmt.Errorf("the plugin %s answered %s with more than %d bytes", p.Kind, op, maxOutput)
	}
	var a envelope
	if err := json.Unmarshal(stdout.buf.Bytes(), &a); err != nil {
		if ran != nil {
			return nil, fmt.Errorf("the plugin %s failed at %s: %w%s", p.Kind, op, ran, said(stderr))
		}
		return nil, fmt.Errorf("the plugin %s answered %s with no JSON object: %w%s", p.Kind, op, err, said(stderr))
	}
	tokens := a.Tokens
	if len(tokens) == 0 || string(tokens) == "null" {
		tokens = nil
	}
	if a.Error != nil {
		return tokens, a.Error
	}
	if ran != nil {
		return tokens, fmt.Errorf("the plugin %s failed at %s: %w%s", p.Kind, op, ran, said(stderr))
	}
	if result != nil {
		if err := json.Unmarshal(stdout.buf.Bytes(), result); err != nil {
			return tokens, fmt.Errorf("the plugin %s answered %s with an unexpected shape: %w", p.Kind, op, err)
		}
	}
	return tokens, nil
}

// said is what a plugin wrote to its standard error, for an error's message.
func said(stderr *capped) string {
	text := strings.TrimSpace(strings.ToValidUTF8(stderr.buf.String(), "?"))
	if text == "" {
		return ""
	}
	if stderr.over {
		text += "…"
	}
	return "; it said: " + text
}

// description is a plugin's answer to describe.
type description struct {
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	Fields   []Field `json:"fields"`
	Protocol int     `json:"protocol"`
}

var (
	kindPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	fieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

// describe asks the executable at path what plugin it is, and checks the answer.
func describe(ctx context.Context, path string) (Plugin, error) {
	p := Plugin{Path: path, Kind: filepath.Base(path)}
	var d description
	if _, err := p.run(ctx, "describe", describeTimeout, struct{}{}, &d); err != nil {
		return Plugin{}, err
	}
	switch {
	case d.Protocol != Protocol:
		return Plugin{}, fmt.Errorf("%s speaks plugin protocol %d; this Park speaks %d", path, d.Protocol, Protocol)
	case !kindPattern.MatchString(d.Kind):
		return Plugin{}, fmt.Errorf("%s gave its kind as %q: a kind is a lower-case letter, then up to 39 lower-case letters, "+
			"digits, and hyphens", path, d.Kind)
	case !between(d.Name, 1, 100):
		return Plugin{}, fmt.Errorf("%s gave a name of %d characters: a name is 1 to 100", path, utf8.RuneCountInString(d.Name))
	case len(d.Fields) > 20:
		return Plugin{}, fmt.Errorf("%s has %d credential fields: a plugin has 20 at most", path, len(d.Fields))
	}
	keys := map[string]bool{}
	for _, f := range d.Fields {
		if !fieldPattern.MatchString(f.Key) || keys[f.Key] {
			return Plugin{}, fmt.Errorf("%s has a field %q: a field's key is unique, a lower-case letter, then up to 39 lower-case "+
				"letters, digits, and underscores", path, f.Key)
		}
		if !between(f.Label, 1, 100) {
			return Plugin{}, fmt.Errorf("%s has a field %q whose label is not 1 to 100 characters", path, f.Key)
		}
		keys[f.Key] = true
	}
	return Plugin{Kind: d.Kind, Name: d.Name, Path: path, Fields: d.Fields}, nil
}

// between reports whether s has from to to characters, once trimmed.
func between(s string, from, to int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= from && n <= to
}

// Load finds the plugins in dir: each regular, executable file in it, other than those whose names start with a
// dot, is asked what it is. A file that fails to answer, speaks another protocol, or claims a kind another file
// claims is an error, so that Park does not start with a plugin directory it cannot account for. No dir, no plugins.
func Load(ctx context.Context, dir string) ([]Plugin, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the plugin directory: %w", err)
	}
	var out []Plugin
	byKind := map[string]string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path, err := filepath.Abs(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		p, err := describe(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("load the plugin %s: %w", path, err)
		}
		if other, ok := byKind[p.Kind]; ok {
			return nil, fmt.Errorf("the plugins %s and %s both have the kind %q", other, path, p.Kind)
		}
		byKind[p.Kind] = path
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Plugin) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// authorize asks for the address of the platform's sign-in page, which sends the browser back to redirect with a
// code and state; challenge is the PKCE challenge, by S256, for the plugins of platforms that take one.
func (p Plugin) authorize(ctx context.Context, credentials map[string]string, redirect, state, challenge string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	_, err := p.run(ctx, "authorize", callTimeout, map[string]any{"credentials": credentials, "redirect_uri": redirect,
		"state": state, "code_challenge": challenge, "code_challenge_method": "S256"}, &out)
	if err != nil {
		return "", err
	}
	u, perr := url.Parse(out.URL)
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("the plugin %s answered authorize with %q, which is not an http or https address", p.Kind, out.URL)
	}
	return out.URL, nil
}

// exchange trades the code the platform sent the browser back with for the account it signed in, and its tokens.
func (p Plugin) exchange(ctx context.Context, credentials map[string]string, redirect, code, verifier string) (Account, json.RawMessage, error) {
	var out struct {
		Account Account `json:"account"`
	}
	tokens, err := p.run(ctx, "exchange", callTimeout, map[string]any{"credentials": credentials, "redirect_uri": redirect,
		"code": code, "code_verifier": verifier}, &out)
	if err != nil {
		return Account{}, tokens, err
	}
	switch {
	case tokens == nil:
		return Account{}, nil, fmt.Errorf("the plugin %s answered exchange with no tokens", p.Kind)
	case !between(out.Account.ID, 1, 200) || !between(out.Account.Name, 1, 200):
		return Account{}, tokens, fmt.Errorf("the plugin %s answered exchange with an account without an ID or a name of 1 to 200 "+
			"characters", p.Kind)
	}
	return out.Account, tokens, nil
}

// chats lists the chats the account the tokens belong to can read.
func (p Plugin) chats(ctx context.Context, credentials map[string]string, tokens json.RawMessage) ([]Chat, json.RawMessage, error) {
	var out struct {
		Chats []Chat `json:"chats"`
	}
	renewed, err := p.run(ctx, "chats", callTimeout, map[string]any{"credentials": credentials, "tokens": tokens}, &out)
	if err != nil {
		return nil, renewed, err
	}
	for _, c := range out.Chats {
		if !validChat(c) {
			return nil, renewed, fmt.Errorf("the plugin %s answered chats with a chat whose ID or name is not 1 to 200 characters, or "+
				"is a link", p.Kind)
		}
	}
	return out.Chats, renewed, nil
}

// validChat reports whether a chat can be kept: an ID and a name of 1 to 200 characters, neither of them a link.
func validChat(c Chat) bool {
	return between(c.ID, 1, 200) && between(c.Name, 1, 200) && !strings.Contains(c.ID, "://")
}
