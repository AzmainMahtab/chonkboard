// Package render turns templ components into responses and into strings.
// Fragments have to become strings because a broadcast over SSE happens after
// the request that caused it has already been answered — there is no
// ResponseWriter left to render into.
package render

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
)

// String renders a component outside a request, for an SSE payload.
func String(ctx context.Context, c templ.Component) (string, error) {
	var b bytes.Buffer
	if err := c.Render(ctx, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Page writes a full document or fragment. The component is rendered into a
// buffer first: a template that fails halfway would otherwise emit a 200 with
// truncated HTML, which looks to the user like a broken page rather than an error.
func Page(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var b bytes.Buffer
	if err := c.Render(r.Context(), &b); err != nil {
		slog.ErrorContext(r.Context(), "render failed", "error", err, "path", r.URL.Path)
		http.Error(w, "Something went wrong rendering this page.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// Fragment is Page with a 200, for an HTMX partial.
func Fragment(w http.ResponseWriter, r *http.Request, c templ.Component) {
	Page(w, r, http.StatusOK, c)
}
