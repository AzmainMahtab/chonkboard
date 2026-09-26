package pages

import (
	"context"
	"strings"

	"github.com/a-h/templ"

	"github.com/AzmainMahtab/chonkboard/web/view"
)

// fieldAttrs returns the accessibility attributes for a form input: whether it is
// invalid, and which elements describe it.
//
// A spread rather than templ's `attr?={ }` form, because that form takes a bool and
// toggles presence for boolean attributes -- it cannot conditionally omit an
// attribute that carries a value, and `aria-invalid=""` is not the same as no
// aria-invalid to a screen reader.
//
// It also owns aria-describedby entirely, rather than letting the markup set one
// and this add another: HTML allows only one of a given attribute, and a duplicate
// silently loses whichever the browser sees second. Hint ids are passed in here so
// the error id can be appended to them.
//
// This lives in a .go file rather than a .templ because templ injects its own
// import of the templ package into generated code, and importing it inside a
// .templ gives "templ redeclared in this block".
func fieldAttrs(id, errMessage string, hintIDs ...string) templ.Attributes {
	attrs := templ.Attributes{}

	describedBy := hintIDs
	if errMessage != "" {
		attrs["aria-invalid"] = "true"
		describedBy = append(describedBy, errorID(id))
	}
	if len(describedBy) > 0 {
		attrs["aria-describedby"] = strings.Join(describedBy, " ")
	}
	return attrs
}

// errorID is the id of the element holding a field's error message. One function
// so the markup and fieldAttrs cannot drift apart.
func errorID(field string) string { return field + "-error" }

// csrf reads the session's CSRF token off the context, for a form rendered by a
// page rather than by a component.
//
// Through view rather than shared/authctx, so the UI layer never reaches a domain
// type — see the note in web/view/csrf.go.
func csrf(ctx context.Context) string { return view.CSRF(ctx) }
