package bridges

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// github is how pull requests' addresses read on github.com, as the forge kind's pattern has them.
var github = []pullLinks{newPullLinks(regexp.MustCompile(`https://github\.com/([\w.-]+)/([\w.-]+)/pull/(\d+)`))}

// A thread's title is the first line of its plain text that says what the work is: a pull request's address is
// dropped, and so is the opening of a request for review, in English or in Chinese; a title is cut at 60 characters
// as text; and a thread whose only words ask for review has none.
func TestTitle(t *testing.T) {
	for _, c := range []struct{ name, plain, want string }{
		{"paragraphs", "Invoice layout, ready for a look:\nhttps://github.com/acme/web/pull/3 https://github.com/acme/api/pull/4",
			"Invoice layout, ready for a look:"},
		{"a line that only asks for review", "review:\nStatement emails\nhttps://github.com/acme/api/pull/5", "Statement emails"},
		{"a request for review in Chinese", "\u5ba1\u6838\uff1afeat(web): statement emails", "feat(web): statement emails"},
		{"only a request for review, and links", "\u5ba1\u6838\uff1a\nhttps://github.com/acme/api/pull/12\nhttps://github.com/acme/web/pull/7", ""},
		{"longer than a title", "feat(web)!: archive the statement images and rewrite their links, with a new setting",
			"feat(web)!: archive the statement images and rewrite their l…"},
		{"angle brackets", "Map<K, V> lookup for exports https://github.com/acme/api/pull/3", "Map<K, V> lookup for exports"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := title(c.plain, github); got != c.want {
				t.Errorf("title %q\nwant  %q", got, c.want)
			}
		})
	}
}

// A thread's text, as the model reads it: a paragraph a line, no empty ones, and each pull request's address, with
// anything after its number, as its owner/name#number, beside its label.
func TestText(t *testing.T) {
	got := text("Two features, please review:\n\n1. Exports: export formats (https://github.com/acme/api/pull/15) "+
		"https://github.com/acme/web/pull/6/files#diff-2\n2. Retries:   retry payments (https://github.com/acme/api/pull/14)\n", github)
	want := "Two features, please review:\n1. Exports: export formats (acme/api#15) acme/web#6\n2. Retries: retry payments (acme/api#14)"
	if got != want {
		t.Fatalf("text %q\nwant %q", got, want)
	}
}

// A thread's pull requests are those its links name on a watched forge, each once, in the order the links name them;
// a link elsewhere, or to an issue, names none.
func TestRead(t *testing.T) {
	fs := read([]Thread{{Anchor: "m1", Created: roundNow, Plain: "Billing export", Text: "Billing export",
		Links: []string{"https://github.com/acme/web/pull/7", "https://github.com/acme/api/pull/12/files", "https://github.com/acme/web/pull/7",
			"https://github.com/acme/api/issues/3", "https://tracker.test/acme/api/pull/9"}}}, github)
	if len(fs) != 1 || fs[0].Title != "Billing export" || !slices.Equal(fs[0].PRs, []string{"acme/web#7", "acme/api#12"}) {
		t.Fatalf("read %+v", fs)
	}
}

// A plugin's threads must say how many messages it read, and give each thread an anchor that is an ID, never a link,
// and a time.
func TestThreadsAnswers(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct{ body, want string }{
		"no count":     {`echo '{"threads": []}'`, "without how many messages it read"},
		"a link":       {`echo '{"read": 1, "threads": [{"anchor": "https://chat.test/m/1", "created": "2026-10-03T10:00:00Z"}]}'`, "anchor"},
		"no time":      {`echo '{"read": 1, "threads": [{"anchor": "m1"}]}'`, "no time"},
		"a fair count": {`echo '{"read": 3, "threads": [{"anchor": "m1", "created": "2026-10-03T10:00:00Z", "links": []}]}'`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			p := Plugin{Kind: "chat", Path: script(t, dir, strings.ReplaceAll(name, " ", "-"), c.body)}
			threads, n, _, err := p.threads(context.Background(), nil, nil, "c1", roundNow.Add(-time.Hour))
			if c.want == "" {
				if err != nil || n != 3 || len(threads) != 1 {
					t.Fatalf("got %v, %d, %v", threads, n, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error saying %q", err, c.want)
			}
		})
	}
}
