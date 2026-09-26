package sse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEncodeSingleLine(t *testing.T) {
	got := encode(Event{Name: "card-moved", HTML: `<div id="card-1"></div>`})
	assert.Equal(t, "event: card-moved\ndata: <div id=\"card-1\"></div>\n\n", got)
}

func TestEncodePrefixesEveryLineOfAMultiLineFragment(t *testing.T) {
	// templ output is indented and multi-line. A bare newline inside data would
	// end the event early and the client would receive a truncated fragment.
	got := encode(Event{Name: "card-updated", HTML: "<article>\n\t<h3>Hi</h3>\n</article>"})
	assert.Equal(t,
		"event: card-updated\ndata: <article>\ndata: \t<h3>Hi</h3>\ndata: </article>\n\n",
		got)
}

func TestEncodeNormalisesCRLF(t *testing.T) {
	got := encode(Event{Name: "x", HTML: "a\r\nb"})
	assert.Equal(t, "event: x\ndata: a\ndata: b\n\n", got)
}

func TestEncodeSignalWithNoPayloadStillCarriesADataLine(t *testing.T) {
	// EventSource ignores a message with no data field, so board-dirty needs one.
	got := encode(Event{Name: "board-dirty"})
	assert.Equal(t, "event: board-dirty\ndata: -\n\n", got)
}
