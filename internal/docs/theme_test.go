package docs

import (
	"bytes"
	"testing"
)

// The theme script goes first in a page's head: after <head>, whatever its case or attributes; after <html> for a
// page with no head — a <header> is not one; after the doctype for a page with neither, so that it stays in
// standards mode; and at the start of a page with none of them. The page it is given is left as it was.
func TestThemed(t *testing.T) {
	for _, c := range []struct{ name, page, before string }{
		{"a head", `<!doctype html><html lang="en"><head><meta charset="utf-8"></head><body>x</body></html>`,
			`<!doctype html><html lang="en"><head>`},
		{"a head in capitals, with attributes", "<!DOCTYPE html>\n<HTML>\n<HEAD data-x=\"1\">\n<title>t</title>", "<!DOCTYPE html>\n<HTML>\n<HEAD data-x=\"1\">"},
		{"a header but no head", `<html><body><header>h</header></body></html>`, `<html>`},
		{"a doctype only", "\n<!DOCTYPE html><p>hi</p>", "\n<!DOCTYPE html>"},
		{"none of them", `<p>hi</p>`, ``},
	} {
		page := []byte(c.page)
		got := themed(page)
		want := c.before + themeScript + c.page[len(c.before):]
		if string(got) != want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
		if !bytes.Equal(page, []byte(c.page)) {
			t.Errorf("%s: the page given was changed to %q", c.name, page)
		}
	}
}
