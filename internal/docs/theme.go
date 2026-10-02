package docs

import (
	"regexp"
	"slices"
)

// themeScript lets a description's pages follow the dashboard's theme, which browsers do not carry into a frame's
// prefers-color-scheme. The dashboard names the theme in the page's address, ?theme=light or ?theme=dark, and posts
// it again, as {type: "park-theme", theme}, when it changes and whenever the frame loads a page. The script copies it
// to the root element: as data-theme, for the page's own styles, and as color-scheme, for the browser's default
// colors. It takes no value but those two, and messages from no window but the one framing the page.
const themeScript = `<script>(function(){var r=document.documentElement;` +
	`function show(t){if(t==="light"||t==="dark"){r.dataset.theme=t;r.style.colorScheme=t}}` +
	`show(new URLSearchParams(location.search).get("theme"));` +
	`addEventListener("message",function(e){if(e.source===parent&&e.data&&e.data.type==="park-theme")show(e.data.theme)})` +
	`})()</script>`

var (
	headTag = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlTag = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	doctype = regexp.MustCompile(`(?i)^\s*<!doctype[^>]*>`)
)

// themed is an HTML page with themeScript first in its head: after its <head> tag, or failing that its <html> tag,
// or failing both its doctype, so that a page with a doctype stays in standards mode; at its start when it has none
// of them. The stored page is not changed.
func themed(page []byte) []byte {
	at := 0
	for _, tag := range []*regexp.Regexp{headTag, htmlTag, doctype} {
		if m := tag.FindIndex(page); m != nil {
			at = m[1]
			break
		}
	}
	return slices.Concat(page[:at:at], []byte(themeScript), page[at:])
}
