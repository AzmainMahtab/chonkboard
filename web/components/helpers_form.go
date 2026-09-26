package components

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/AzmainMahtab/chonkboard/web/view"
)

// fieldState returns the accessibility attributes an input needs when it has an
// error, and nothing when it does not.
//
// A spread rather than templ's `attr?={ }` form, because that takes a bool and
// toggles presence for boolean attributes — it cannot omit an attribute that
// carries a value, and `aria-invalid=""` reads as invalid to a screen reader.
//
// It owns aria-describedby entirely: HTML keeps only one of a duplicate attribute,
// so a hint in the markup plus one from a spread silently loses whichever the
// browser sees second. Hint ids are passed in so the error id can be appended.
func fieldState(id, errMessage string, hintIDs ...string) templ.Attributes {
	attrs := templ.Attributes{}

	describedBy := hintIDs
	if errMessage != "" {
		attrs["aria-invalid"] = "true"
		describedBy = append(describedBy, id+"-error")
	}
	if len(describedBy) > 0 {
		attrs["aria-describedby"] = strings.Join(describedBy, " ")
	}
	return attrs
}

// formCSRF reads the session's CSRF token off the context.
//
// Through view rather than shared/authctx, so this package never reaches a domain
// type — see the note in web/view/csrf.go.
func formCSRF(ctx context.Context) string { return view.CSRF(ctx) }

// pluralise picks a word form.
func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// returnURL resolves where a lane form's Save and Cancel should go.
//
// The path is built here from a known token rather than taken from the request: a form
// field holding a URL is an open redirect waiting to be found. Anything but "board" means
// the settings page, so an unrecognised value fails safe.
func returnURL(projectSlug, returnTo string) templ.SafeURL {
	if returnTo == "board" {
		return templ.SafeURL("/projects/" + projectSlug)
	}
	return settingsURL(projectSlug)
}

// settingsURL is a project's settings page.
func settingsURL(projectSlug string) templ.SafeURL {
	return templ.SafeURL("/projects/" + projectSlug + "/settings")
}

// laneFormAction is where a lane form posts: the collection to create in, or the
// lane itself to update. One function so the create and edit cases cannot drift
// apart from the routes.
func laneFormAction(projectSlug string, f view.LaneForm) templ.SafeURL {
	if f.IsNew() {
		return templ.SafeURL("/projects/" + projectSlug + "/lanes")
	}
	return templ.SafeURL("/projects/" + projectSlug + "/lanes/" + f.UUID)
}

// cardFormTitle names the card form for its heading and its aria-label.
func cardFormTitle(f view.CardForm) string {
	if f.IsNew() {
		return "Add a card"
	}
	return "Edit card"
}

// cardFormAction is where a card form posts: the lane to create in, or the card
// itself to update. One function so create and edit cannot drift from the routes.
func cardFormAction(projectSlug string, f view.CardForm) templ.SafeURL {
	if f.IsNew() {
		return templ.SafeURL("/projects/" + projectSlug + "/lanes/" + f.LaneUUID + "/cards")
	}
	return templ.SafeURL("/cards/" + f.UUID)
}

// activityVerb turns an activity entry into a phrase.
//
// Written out rather than assembled from the kind so the wording reads like English
// and a new kind has to be given one deliberately.
func activityVerb(e view.ActivityEntry) string {
	switch e.Kind {
	case "created":
		return "added this card"
	case "moved":
		if e.From != "" && e.To != "" {
			return "moved it from " + e.From + " to " + e.To
		}
		return "moved it"
	case "updated":
		return "edited it"
	case "archived":
		return "archived it"
	case "restored":
		return "restored it"
	case "commented":
		return "commented"
	case "attached":
		return "attached a file"
	case "assigned":
		return "changed the assignee"
	case "labelled":
		return "changed its labels"
	}
	return e.Kind
}

// itoa keeps strconv out of the templates.
func itoa(n int) string { return strconv.Itoa(n) }

// now is the clock the due-date colouring reads. A function so a template does not
// call time.Now() in three places and get three answers.
func now() time.Time { return time.Now() }

// liveComments counts the comments that are not tombstones, which is the number worth
// showing next to a heading.
func liveComments(comments []view.Comment) int {
	n := 0
	for _, c := range comments {
		if !c.IsDeleted {
			n++
		}
	}
	return n
}

// labelFormAction is where a label form posts: the collection to create in, or the
// label itself to update.
func labelFormAction(projectSlug string, f view.LabelForm) templ.SafeURL {
	if f.IsNew() {
		return templ.SafeURL("/projects/" + projectSlug + "/labels")
	}
	return templ.SafeURL("/projects/" + projectSlug + "/labels/" + f.UUID)
}

// accountFormAction is where an account form posts: the collection to create in, or the
// account itself to update.
func accountFormAction(f view.AccountForm) templ.SafeURL {
	if f.IsNew() {
		return templ.SafeURL("/admin/users")
	}
	return templ.SafeURL("/admin/users/" + f.UUID)
}
