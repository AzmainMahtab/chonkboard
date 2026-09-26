package sse

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// heartbeat keeps idle connections alive through proxies that cut a silent
// stream. 25s sits under the common 30s and 60s idle timeouts.
const heartbeat = 25 * time.Second

// Stream holds the request open and writes events for one room until the client
// goes away or the hub drops it. It is the one handler that must not sit behind a
// request timeout.
func (h *Hub) Stream(w http.ResponseWriter, r *http.Request, room, clientID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// Tell nginx not to buffer the stream into uselessness.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, cancel := h.Subscribe(room, clientID)
	defer cancel()

	// Nudge the client so EventSource fires onopen immediately rather than after
	// the first real event.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				return // the hub dropped us
			}
			if _, err := w.Write([]byte(encode(ev))); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// encode renders one event in the wire format. Every line of the payload needs
// its own data: prefix — an HTML fragment is multi-line, and a bare newline
// would terminate the event early.
func encode(ev Event) string {
	var b strings.Builder
	if ev.Name != "" {
		b.WriteString("event: ")
		b.WriteString(ev.Name)
		b.WriteByte('\n')
	}
	payload := ev.HTML
	if payload == "" {
		payload = "-" // the spec requires a data line; the client ignores it
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(payload, "\r\n", "\n"), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}
