package view

import "context"

// The CSRF token, carried on the context for components that render a form.
//
// A fragment is rendered without a Page, so it cannot take the token as a prop the
// way a full page does, and threading it through every component signature would
// put it in dozens of places that do not otherwise care.
//
// The carrier lives here rather than in shared/authctx because the UI layer must
// stay a leaf of the dependency graph: authctx exposes the auth domain's User, and
// a component that could reach a domain type eventually will. This package imports
// nothing but the standard library and templ.
//
// The session middleware populates it on every authenticated request.

type csrfKey struct{}

// WithCSRF returns a context carrying the session's CSRF token.
func WithCSRF(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey{}, token)
}

// CSRF returns the token, or "" when there is no session.
//
// An empty token renders an empty hidden field, which the CSRF middleware then
// refuses — the right outcome for a form that somehow reached an anonymous page.
func CSRF(ctx context.Context) string {
	token, _ := ctx.Value(csrfKey{}).(string)
	return token
}
