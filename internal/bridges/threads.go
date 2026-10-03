package bridges

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// threads reads a chat's root messages since a time, as the account the tokens belong to, and returns the ones that
// link anything, how many messages the plugin read, and the tokens it answered with.
func (p Plugin) threads(ctx context.Context, credentials map[string]string, tokens json.RawMessage, chat string,
	since time.Time) ([]Thread, int, json.RawMessage, error) {
	var out struct {
		Read    *int     `json:"read"`
		Threads []Thread `json:"threads"`
	}
	renewed, err := p.run(ctx, "threads", callTimeout, map[string]any{"credentials": credentials, "tokens": tokens, "chat": chat,
		"since": since.UTC().Format(time.RFC3339)}, &out)
	if err != nil {
		return nil, 0, renewed, err
	}
	if out.Read == nil || *out.Read < 0 {
		return nil, 0, renewed, fmt.Errorf("the plugin %s answered threads without how many messages it read", p.Kind)
	}
	for _, t := range out.Threads {
		if !between(t.Anchor, 1, 200) || strings.Contains(t.Anchor, "://") || t.Created.IsZero() {
			return nil, 0, renewed, fmt.Errorf("the plugin %s answered threads with one whose anchor is not an ID of 1 to 200 "+
				"characters, or that has no time", p.Kind)
		}
	}
	return out.Threads, *out.Read, renewed, nil
}

// pullLinks is how a pull request's address reads on one forge: the address alone, with the repository's owner, its
// name, and the number as its groups; and the address with whatever follows its number, as far as a space or a closing
// bracket, which a thread's text gives the model as owner/name#number instead.
type pullLinks struct {
	pr   *regexp.Regexp
	full *regexp.Regexp
}

func newPullLinks(pr *regexp.Regexp) pullLinks {
	return pullLinks{pr: pr, full: regexp.MustCompile(pr.String() + `[^\s)\]]*`)}
}

// key names a pull request as a round does, owner/name#number, from a match of pr; false if it is not one.
func key(m []string) (string, bool) {
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", false
	}
	return m[1] + "/" + m[2] + "#" + strconv.Itoa(n), true
}

// found is a thread as a round reads it: its anchor and time; its title; its text, as the model reads it; and the
// pull requests its links name, as owner/name#number, each once, in the order the links name them.
type found struct {
	Created time.Time
	Anchor  string
	Title   string
	Text    string
	PRs     []string
}

// read turns a plugin's threads into what a round reads, finding pull requests with the forges' patterns.
func read(threads []Thread, forges []pullLinks) []found {
	out := make([]found, 0, len(threads))
	for _, t := range threads {
		f := found{Created: t.Created, Anchor: t.Anchor, Title: title(t.Plain, forges), Text: text(t.Text, forges)}
		seen := map[string]bool{}
		for _, l := range t.Links {
			for _, p := range forges {
				m := p.pr.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				if k, ok := key(m); ok && !seen[k] {
					seen[k] = true
					f.PRs = append(f.PRs, k)
				}
				break
			}
		}
		out = append(out, f)
	}
	return out
}

// opening is the opening of a request for review, which says nothing about the work: in English, review; in Chinese,
// to review, please review, and please take a look, in their usual forms; each with or without a colon.
var opening = regexp.MustCompile(`^(\x{5ba1}\x{6838}|\x{5ba1}|\x{8bf7}\x{5ba1}|\x{5e2e}\x{5fd9}\x{770b}\x{770b}|\x{9ebb}\x{70e6}\x{770b}\x{770b}|review)` +
	`[:\x{ff1a}]?\s*`)

// title is the first line of a thread's plain text that says what the work is: without the addresses of pull
// requests, and without the opening of a request for review. It is at most 60 characters, and none when no line has
// at least four.
func title(plain string, forges []pullLinks) string {
	words := plain
	for _, p := range forges {
		words = p.pr.ReplaceAllString(words, "")
	}
	for _, line := range strings.Split(words, "\n") {
		line = strings.Join(strings.Fields(opening.ReplaceAllString(strings.TrimSpace(line), "")), " ")
		if utf8.RuneCountInString(line) < 4 {
			continue
		}
		if r := []rune(line); len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return line
	}
	return ""
}

// text is a thread's text as the model reads it: a paragraph a line, without empty ones, and each link to a pull
// request, with whatever follows its number, as owner/name#number.
func text(words string, forges []pullLinks) string {
	for _, p := range forges {
		words = p.full.ReplaceAllStringFunc(words, func(link string) string {
			if k, ok := key(p.pr.FindStringSubmatch(link)); ok {
				return k
			}
			return link
		})
	}
	var lines []string
	for _, line := range strings.Split(words, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
