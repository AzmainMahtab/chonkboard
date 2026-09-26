// Package markdown renders a card body or a comment to HTML, then strips anything
// dangerous out of the result.
//
// Server-side and sanitised, deliberately. A card body is user input that other
// people's browsers render, and the Content-Security-Policy does not save you from
// it: CSP blocks external and inline *script*, but injected markup needs no script
// to do damage — a form posting to another origin, an iframe, an anchor with a
// javascript: href, or an onerror handler on an image.
//
// Rendering client-side would be worse still: the sanitiser would then be the
// client's, and a client that skipped it would render whatever was stored.
package markdown

import (
	"bytes"
	"regexp"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// renderer and policy are built once. Both are safe for concurrent use, and
// bluemonday policy construction in particular is expensive enough to be worth not
// repeating per card.
var (
	once     sync.Once
	renderer goldmark.Markdown
	policy   *bluemonday.Policy
)

func initialise() {
	renderer = goldmark.New(
		goldmark.WithExtensions(
			// Tables, strikethrough, task lists and autolinks. What people
			// actually write in a card.
			extension.GFM,
		),
		goldmark.WithRendererOptions(
			// No html.WithUnsafe: raw HTML blocks in the source are escaped
			// rather than passed through. The sanitiser below is the second
			// line, not the only one.
			html.WithHardWraps(),
			html.WithXHTML(),
		),
	)

	// UGCPolicy is bluemonday's policy for user-generated content: a small tag
	// allowlist, no script, no style, no event handlers, and URLs restricted to
	// safe schemes.
	policy = bluemonday.UGCPolicy()

	// Links open in a new tab, and carry rel="noopener noreferrer" so the opened
	// page cannot reach back through window.opener.
	policy.AllowAttrs("target").Matching(bluemonday.Paragraph).OnElements("a")
	policy.AllowAttrs("rel").Matching(bluemonday.Paragraph).OnElements("a")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)

	// GFM task lists render a disabled checkbox. Without this a checklist loses its
	// meaning entirely, not merely its styling: bluemonday strips `checked`, and a
	// ticked item then renders identically to an unticked one.
	//
	// `checked` and `disabled` are boolean attributes, which goldmark emits as
	// `checked=""` in XHTML mode — an empty value, which is why the pattern has to
	// admit one. `type` is pinned to `checkbox` so no other input kind can appear.
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	policy.AllowAttrs("checked", "disabled").
		Matching(regexp.MustCompile(`^(|checked|disabled|true)$`)).
		OnElements("input")

	// Table markup, for the GFM table extension.
	policy.AllowTables()
}

// Render turns markdown into sanitised HTML.
//
// Returns a string rather than templ.Component or template.HTML so this package
// stays free of any view dependency; the caller wraps it in templ.Raw at the point
// of use, which is the one place that decision is visible.
func Render(source string) string {
	once.Do(initialise)

	if source == "" {
		return ""
	}

	var buf bytes.Buffer
	if err := renderer.Convert([]byte(source), &buf); err != nil {
		// Convert only fails on a writer error, and a bytes.Buffer does not
		// fail. Falling back to the escaped source means a body is never lost,
		// and never rendered unsanitised.
		return policy.Sanitize(source)
	}
	return policy.Sanitize(buf.String())
}
