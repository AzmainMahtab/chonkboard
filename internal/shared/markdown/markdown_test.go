package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRendersOrdinaryMarkdown(t *testing.T) {
	got := Render("# Heading\n\nSome **bold** and _italic_ text.\n\n- one\n- two")

	assert.Contains(t, got, "<h1")
	assert.Contains(t, got, "<strong>bold</strong>")
	assert.Contains(t, got, "<em>italic</em>")
	assert.Contains(t, got, "<li>one</li>")
}

func TestRendersGFMExtensions(t *testing.T) {
	t.Run("tables", func(t *testing.T) {
		got := Render("| a | b |\n|---|---|\n| 1 | 2 |")
		assert.Contains(t, got, "<table")
		assert.Contains(t, got, "<td")
	})
	t.Run("strikethrough", func(t *testing.T) {
		assert.Contains(t, Render("~~gone~~"), "<del>gone</del>")
	})
	t.Run("task lists keep which items are ticked", func(t *testing.T) {
		// The thing that matters is that a ticked item differs from an unticked
		// one. bluemonday strips `checked` by default, which renders the two
		// identically and quietly destroys the meaning of a checklist.
		got := Render("- [x] done\n- [ ] not done")

		require.Contains(t, got, "<input", "a checklist must keep its shape")
		assert.Contains(t, got, "checked", "a ticked item must stay ticked")
		assert.Equal(t, 1, strings.Count(got, "checked"),
			"exactly one of the two items is ticked")
		assert.Contains(t, got, "disabled",
			"the checkbox is a rendering of the text, not a control")
		assert.NotContains(t, got, `type="text"`)
	})
	t.Run("autolinks", func(t *testing.T) {
		assert.Contains(t, Render("see https://example.com/x"), `href="https://example.com/x"`)
	})
}

func TestEmptyInput(t *testing.T) {
	assert.Empty(t, Render(""))
}

// TestScriptIsInert is the gate. Every one of these is a way to run code or reach
// another origin without needing a <script> tag, which is why the CSP is not
// sufficient on its own.
func TestScriptIsInert(t *testing.T) {
	attacks := map[string]string{
		"a script tag":              `<script>alert(1)</script>`,
		"a script tag in a list":    "- item\n- <script>alert(1)</script>",
		"an img onerror handler":    `<img src=x onerror="alert(1)">`,
		"a javascript: href":        `<a href="javascript:alert(1)">click</a>`,
		"a markdown javascript url": `[click](javascript:alert(1))`,
		"an iframe":                 `<iframe src="https://evil.example"></iframe>`,
		"an object":                 `<object data="x.swf"></object>`,
		"an embed":                  `<embed src="x">`,
		"a form posting elsewhere":  `<form action="https://evil.example"><input name="p"></form>`,
		"an svg onload":             `<svg onload="alert(1)"></svg>`,
		"a style block":             `<style>body{display:none}</style>`,
		"an inline style":           `<p style="position:fixed;inset:0">covered</p>`,
		"a meta refresh":            `<meta http-equiv="refresh" content="0;url=https://evil.example">`,
		"a base tag":                `<base href="https://evil.example/">`,
		"an onclick attribute":      `<p onclick="alert(1)">text</p>`,
		"a data: url image":         `<img src="data:text/html,<script>alert(1)</script>">`,
		"an uppercase script tag":   `<SCRIPT>alert(1)</SCRIPT>`,
		"a broken script tag":       `<scr<script>ipt>alert(1)</script>`,
	}

	for name, source := range attacks {
		t.Run(name, func(t *testing.T) {
			got := Render(source)
			lower := strings.ToLower(got)

			assert.NotContains(t, lower, "<script", "a script tag survived")
			assert.NotContains(t, lower, "javascript:", "a javascript: url survived")
			assert.NotContains(t, lower, "onerror", "an event handler survived")
			assert.NotContains(t, lower, "onload")
			assert.NotContains(t, lower, "onclick")
			assert.NotContains(t, lower, "<iframe")
			assert.NotContains(t, lower, "<object")
			assert.NotContains(t, lower, "<embed")
			assert.NotContains(t, lower, "<form")
			assert.NotContains(t, lower, "<style")
			assert.NotContains(t, lower, "http-equiv")
			assert.NotContains(t, lower, "<base")
			assert.NotContains(t, lower, `style="`, "an inline style survived")
		})
	}
}

func TestRawHTMLIsEscapedNotPassedThrough(t *testing.T) {
	// goldmark is configured without WithUnsafe, so raw HTML in the source is
	// escaped at render time. The sanitiser is the second line, not the only one.
	got := Render("Text with <b>bold html</b> in it.")
	assert.NotContains(t, got, "<b>bold html</b>")
	assert.Contains(t, got, "bold html")
}

func TestLinksAreHardened(t *testing.T) {
	got := Render("[a link](https://example.com)")

	require.Contains(t, got, `href="https://example.com"`)
	assert.Contains(t, got, `rel=`)
	assert.Contains(t, got, "noopener", "the opened page must not reach window.opener")
	assert.Contains(t, got, "noreferrer")
	assert.Contains(t, got, "nofollow")
}

func TestRelativeAndMailtoLinksSurvive(t *testing.T) {
	assert.Contains(t, Render("[mail](mailto:person@example.com)"), "mailto:person@example.com")
	assert.Contains(t, Render("[card](/cards/abc)"), `href="/cards/abc"`)
}

func TestLongAndUnicodeInput(t *testing.T) {
	long := strings.Repeat("A paragraph of text. ", 2000)
	got := Render(long)
	assert.Contains(t, got, "A paragraph of text.")

	unicode := Render("Héllo — ünïcødé, emoji 🔐, and 中文.")
	assert.Contains(t, unicode, "ünïcødé")
	assert.Contains(t, unicode, "🔐")
	assert.Contains(t, unicode, "中文")
}

func TestConcurrentUseIsSafe(t *testing.T) {
	// Run with -race: two people opening two cards render at the same time, and
	// the renderer and policy are shared singletons.
	done := make(chan struct{})
	for i := range 20 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 20 {
				Render("# Heading\n\n<script>alert(1)</script>\n\n- item")
				_ = i
			}
		}()
	}
	for range 20 {
		<-done
	}
}

func BenchmarkRender(b *testing.B) {
	source := strings.Repeat("Some **text** with a [link](https://example.com).\n\n", 20)
	for range b.N {
		Render(source)
	}
}
