package card

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContentDispositionResistsHeaderInjection is an internal test because the
// function is not exported — and it is worth having because a filename is the one
// piece of user input that reaches a response header.
//
// A quote closes the quoted string early; a CR or LF ends the header and starts
// another. Either turns a download into header injection.
func TestContentDispositionResistsHeaderInjection(t *testing.T) {
	attacks := map[string]string{
		"a quote":                 `evil".csv`,
		"a quote and a parameter": `x";filename="pwned.exe`,
		"a carriage return":       "x\r\nSet-Cookie: a=b",
		"a line feed":             "x\nSet-Cookie: a=b",
		"a null byte":             "x\x00.csv",
		"a backslash":             `x\".csv`,
		"a semicolon":             "x;filename=y.csv",
		"a path":                  "../../etc/passwd",
		"only quotes":             `"""`,
		"empty":                   "",
		"only whitespace":         "   ",
	}

	for name, filename := range attacks {
		t.Run(name, func(t *testing.T) {
			got := contentDisposition(filename)

			// Exactly one header value, on one line.
			assert.NotContains(t, got, "\r")
			assert.NotContains(t, got, "\n")
			assert.NotContains(t, got, "\x00")

			// The quoted form must contain exactly two quotes: the ones that
			// open and close it.
			quoted := got[strings.Index(got, `filename="`)+len(`filename="`):]
			quoted = quoted[:strings.Index(quoted, `"`)]
			assert.NotContains(t, quoted, `"`)
			assert.NotContains(t, quoted, ";")
			assert.NotContains(t, quoted, "/")
			assert.NotContains(t, quoted, `\`)

			assert.True(t, strings.HasPrefix(got, "attachment; "),
				"always a download, never rendered in place")
		})
	}
}

func TestContentDispositionKeepsAnOrdinaryName(t *testing.T) {
	got := contentDisposition("Quarterly report (final).pdf")
	assert.Contains(t, got, `filename="Quarterly report (final).pdf"`)
}

func TestContentDispositionCarriesUnicodeInTheStarForm(t *testing.T) {
	// filename= must stay ASCII for a browser that does not understand filename*,
	// while filename* carries the real name per RFC 5987.
	got := contentDisposition("réport-中文.pdf")

	require.Contains(t, got, "filename*=UTF-8''")
	assert.Contains(t, got, "%C3%A9", "the star form percent-encodes UTF-8")

	quoted := got[strings.Index(got, `filename="`)+len(`filename="`):]
	quoted = quoted[:strings.Index(quoted, `"`)]
	for _, r := range quoted {
		assert.Less(t, r, rune(0x7f), "the quoted form must stay ASCII")
	}
}

func TestContentDispositionFallsBackWhenNothingSurvives(t *testing.T) {
	got := contentDisposition("\"\r\n\x00")
	assert.Contains(t, got, `filename="attachment"`)
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0 B", 512: "512 B", 1023: "1023 B",
		1024: "1 KB", 65536: "64 KB",
		1024 * 1024: "1.0 MB", 5 * 1024 * 1024: "5.0 MB",
	} {
		assert.Equal(t, want, humanBytes(in), "%d bytes", in)
	}
}

func TestOrderFieldDropsEmpties(t *testing.T) {
	// A repeated form field submitted with no value arrives as a one-element slice
	// holding "". Left in, it is a card uuid that is not on the board.
	assert.Empty(t, orderField([]string{""}))
	assert.Empty(t, orderField([]string{"  ", ""}))
	assert.Empty(t, orderField(nil))
	assert.Equal(t, []string{"a", "b"}, orderField([]string{"a", "", "b", "   "}))
}
