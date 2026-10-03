// Command park-bridge-file is a bridge plugin that reads its chats from a JSON file: the reference for plugin authors,
// docs/design/12-bridge-plugins.md, and the plugin Park's own tests run. Its one credential is the file's path.
//
// The file names the account that signs in, and its chats, each with its threads:
//
//	{"account": {"id": "reader", "name": "File reader"},
//	 "chats": [{"id": "reviews", "name": "Reviews", "threads": [
//	   {"anchor": "m1", "created": "2026-10-01T12:00:00Z", "text": "…", "plain": "…", "links": ["…"]}]}],
//	 "error": {"code": "signin_lapsed", "message": "…"}}
//
// Signing in goes straight back to Park, with the PKCE challenge as the code, so that exchanging the code checks the
// verifier. Its tokens count the operations that used them, and every operation answers renewed ones, as a platform
// that rotates tokens does. With an error in the file, every operation but describe answers with it, and with renewed
// tokens all the same.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"
)

// request is every operation's request; each reads the part it needs.
type request struct {
	Since        time.Time         `json:"since"`
	Credentials  map[string]string `json:"credentials"`
	Tokens       *tokens           `json:"tokens"`
	RedirectURI  string            `json:"redirect_uri"`
	State        string            `json:"state"`
	Challenge    string            `json:"code_challenge"`
	Code         string            `json:"code"`
	CodeVerifier string            `json:"code_verifier"`
	Chat         string            `json:"chat"`
}

// tokens are this plugin's tokens: how many operations have used them.
type tokens struct {
	Uses int `json:"uses"`
}

type thread struct {
	Created time.Time `json:"created"`
	Anchor  string    `json:"anchor"`
	Text    string    `json:"text"`
	Plain   string    `json:"plain"`
	Links   []string  `json:"links"`
}

type chat struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Threads []thread `json:"threads"`
}

type failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// file is what the credential's file holds.
type file struct {
	Error   *failure `json:"error"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
	Chats []chat `json:"chats"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: park-bridge-file describe|authorize|exchange|chats|threads, with the request on standard input")
		os.Exit(2)
	}
	out, err := answer(os.Args[1], os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "park-bridge-file:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		os.Exit(1)
	}
}

// answer answers one operation.
func answer(op string, in io.Reader) (map[string]any, error) {
	switch op {
	case "describe":
		return map[string]any{"protocol": 1, "kind": "file", "name": "File",
			"fields": []map[string]any{{"key": "file", "label": "File", "secret": false}}}, nil
	case "authorize", "exchange", "chats", "threads":
	default:
		return nil, fmt.Errorf("unknown operation %q", op)
	}
	var req request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return nil, fmt.Errorf("read the request: %w", err)
	}
	var renewed *tokens
	if req.Tokens != nil {
		renewed = &tokens{Uses: req.Tokens.Uses + 1}
	}
	f, err := read(req.Credentials["file"])
	if err != nil {
		return failed(renewed, "unreadable", err.Error()), nil
	}
	if f.Error != nil {
		return failed(renewed, f.Error.Code, f.Error.Message), nil
	}
	switch op {
	case "authorize":
		back, err := url.Parse(req.RedirectURI)
		if err != nil {
			return nil, err
		}
		q := back.Query()
		q.Set("code", req.Challenge)
		q.Set("state", req.State)
		back.RawQuery = q.Encode()
		return map[string]any{"url": back.String()}, nil
	case "exchange":
		sum := sha256.Sum256([]byte(req.CodeVerifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != req.Code {
			return failed(nil, "invalid_grant", "the code verifier does not match the challenge"), nil
		}
		account := map[string]string{"id": f.Account.ID, "name": f.Account.Name}
		if account["id"] == "" {
			account = map[string]string{"id": "reader", "name": "File reader"}
		}
		return map[string]any{"account": account, "tokens": tokens{}}, nil
	case "chats":
		list := []map[string]string{}
		for _, c := range f.Chats {
			list = append(list, map[string]string{"id": c.ID, "name": c.Name})
		}
		return map[string]any{"chats": list, "tokens": renewed}, nil
	case "threads":
		for _, c := range f.Chats {
			if c.ID != req.Chat {
				continue
			}
			kept := []thread{}
			for _, t := range c.Threads {
				if !t.Created.Before(req.Since) {
					kept = append(kept, t)
				}
			}
			return map[string]any{"read": len(c.Threads), "threads": kept, "tokens": renewed}, nil
		}
		return failed(renewed, "no_chat", "the file has no chat "+req.Chat), nil
	}
	return nil, nil
}

// failed is an error's answer, with the renewed tokens, if any.
func failed(renewed *tokens, code, message string) map[string]any {
	out := map[string]any{"error": failure{Code: code, Message: message}}
	if renewed != nil {
		out["tokens"] = renewed
	}
	return out
}

// read reads the file a credential names.
func read(path string) (file, error) {
	var f file
	if path == "" {
		return f, fmt.Errorf("no file is named")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}
