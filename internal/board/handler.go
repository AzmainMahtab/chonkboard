package board

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/render"
	"github.com/AzmainMahtab/chonkboard/web/pages"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// Handler serves lane management.
//
// It leans on the project handler for access resolution, page chrome and the error
// path, so there is exactly one definition of "which board is this and what may the
// caller do to it" rather than a second copy that can drift.
type Handler struct {
	svc      *Service
	projects *project.Handler
	log      *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(svc *Service, projects *project.Handler, log *slog.Logger) *Handler {
	return &Handler{svc: svc, projects: projects, log: log}
}

// LaneSummaries implements project.BoardShape: the lanes of one board.
//
// The subject is passed in rather than resolved here. Resolving it from the request
// would read the {project} URL parameter, which does not exist on the project list —
// so every board in the list would come back as "no such project".
//
// Card contents are not loaded; phase 4 owns cards.
func (h *Handler) LaneSummaries(
	ctx context.Context, subj authz.Subject, projectUUID string,
) ([]view.Lane, error) {
	lanes, err := h.svc.Lanes(ctx, subj, projectUUID)
	if err != nil {
		return nil, err
	}
	return toLaneViews(lanes), nil
}

// NewLane renders the create form.
func (h *Handler) NewLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}
	h.renderLaneForm(w, r, access, http.StatusOK, view.LaneForm{
		Colour:  string(domain.ColourSlate),
		Colours: colourNames(),
	})
}

// CreateLane adds a lane.
func (h *Handler) CreateLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}

	in, form, err := laneInputFrom(r)
	if err != nil {
		h.renderLaneForm(w, r, access, apperrors.From(err).Status(), formWithError(form, err))
		return
	}

	if _, err := h.svc.CreateLane(r.Context(), access.Subject, access.Project.UUID, in); err != nil {
		if isFormError(err) {
			h.renderLaneForm(w, r, access, apperrors.From(err).Status(), formWithError(form, err))
			return
		}
		h.projects.Fail(w, r, err)
		return
	}

	h.redirectToSettings(w, r, access.Project.Slug, "Lane added.")
}

// EditLane renders the edit form for one lane.
func (h *Handler) EditLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}

	lane, err := h.svc.Lane(r.Context(), access.Subject, access.Project.UUID, chi.URLParam(r, "lane"))
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.renderLaneForm(w, r, access, http.StatusOK, view.LaneForm{
		UUID:     lane.UUID,
		Name:     lane.Name,
		Colour:   string(lane.Colour),
		WIPLimit: wipField(lane.WIPLimit),
		IsDone:   lane.IsDone,
		Colours:  colourNames(),
	})
}

// UpdateLane saves a lane's name, colour, WIP limit and done flag.
func (h *Handler) UpdateLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}

	laneUUID := chi.URLParam(r, "lane")
	in, form, err := laneInputFrom(r)
	form.UUID = laneUUID
	if err != nil {
		h.renderLaneForm(w, r, access, apperrors.From(err).Status(), formWithError(form, err))
		return
	}

	_, err = h.svc.UpdateLane(r.Context(), access.Subject, access.Project.UUID, laneUUID, in)
	if err != nil {
		if isFormError(err) {
			h.renderLaneForm(w, r, access, apperrors.From(err).Status(), formWithError(form, err))
			return
		}
		h.projects.Fail(w, r, err)
		return
	}

	h.redirectToSettings(w, r, access.Project.Slug, "Lane saved.")
}

// MoveLane shifts one lane up or down by a place.
//
// This is the keyboard and no-JavaScript path for reordering. It builds the whole
// new order server-side and hands it to the same ReorderLanes the drag will use in a
// later phase, so there is one reorder implementation rather than two.
func (h *Handler) MoveLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	lanes, err := h.svc.Lanes(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	order, err := shifted(lanes, chi.URLParam(r, "lane"), r.PostForm.Get("direction"))
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	if err := h.svc.ReorderLanes(r.Context(), access.Subject, access.Project.UUID, order); err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	h.redirectToSettings(w, r, access.Project.Slug, "Lanes reordered.")
}

// ReorderLanes accepts a whole new order, which is what a drag sends.
func (h *Handler) ReorderLanes(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	err := h.svc.ReorderLanes(r.Context(), access.Subject, access.Project.UUID, r.PostForm["order"])
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ConfirmDeleteLane asks where the lane's cards should go.
func (h *Handler) ConfirmDeleteLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}

	lane, others, err := h.laneAndSiblings(r, access, chi.URLParam(r, "lane"))
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	render.Page(w, r, http.StatusOK, pages.LaneDeletePage(
		h.projects.Page(r, "Delete lane"),
		access.Project.Slug, access.Project.Name, lane, others, ""))
}

// DeleteLane removes a lane, moving its cards where the form said.
func (h *Handler) DeleteLane(w http.ResponseWriter, r *http.Request) {
	access, ok := h.manageable(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	laneUUID := chi.URLParam(r, "lane")
	err := h.svc.DeleteLane(r.Context(), access.Subject,
		access.Project.UUID, laneUUID, r.PostForm.Get("move_to"))
	if err != nil {
		// A refused delete goes back to the same question with the reason on it,
		// because the answer is a choice the person still has to make.
		if isFormError(err) {
			lane, others, buildErr := h.laneAndSiblings(r, access, laneUUID)
			if buildErr != nil {
				h.projects.Fail(w, r, buildErr)
				return
			}
			render.Page(w, r, apperrors.From(err).Status(), pages.LaneDeletePage(
				h.projects.Page(r, "Delete lane"),
				access.Project.Slug, access.Project.Name, lane, others,
				apperrors.From(err).Message))
			return
		}
		h.projects.Fail(w, r, err)
		return
	}

	h.redirectToSettings(w, r, access.Project.Slug, "Lane deleted.")
}

// manageable resolves access and refuses anybody who may not change the board.
//
// Every mutating route starts here. The service checks again — that is what makes
// the rule structural rather than a convention — but this is where the 403 a member
// sees is produced.
func (h *Handler) manageable(w http.ResponseWriter, r *http.Request) (*project.Access, bool) {
	access, err := h.projects.Resolve(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return nil, false
	}
	if !access.CanManage() {
		h.projects.Fail(w, r, ErrNotManager)
		return nil, false
	}
	return access, true
}

// laneAndSiblings loads one lane plus the others it could send cards to.
func (h *Handler) laneAndSiblings(
	r *http.Request, access *project.Access, laneUUID string,
) (view.Lane, []view.Lane, error) {
	lanes, err := h.svc.Lanes(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return view.Lane{}, nil, err
	}

	views := toLaneViews(lanes)
	var target view.Lane
	others := make([]view.Lane, 0, len(views))
	found := false
	for _, l := range views {
		if l.UUID == laneUUID {
			target, found = l, true
			continue
		}
		others = append(others, l)
	}
	if !found {
		return view.Lane{}, nil, ErrNoSuchLane
	}
	return target, others, nil
}

func (h *Handler) renderLaneForm(
	w http.ResponseWriter, r *http.Request, access *project.Access, status int, form view.LaneForm,
) {
	if form.Colours == nil {
		form.Colours = colourNames()
	}
	render.Page(w, r, status, pages.LanePage(
		h.projects.Page(r, laneFormTitle(form)),
		access.Project.Slug, access.Project.Name, form))
}

func (h *Handler) redirectToSettings(w http.ResponseWriter, r *http.Request, slug, saved string) {
	target := "/projects/" + slug + "/settings"
	if saved != "" {
		target += "?saved=" + strings.ReplaceAll(saved, " ", "+")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// laneInputFrom parses the lane form.
//
// It returns the form state alongside the input so a failure can re-render what the
// person typed rather than an empty form.
func laneInputFrom(r *http.Request) (LaneInput, view.LaneForm, error) {
	if err := r.ParseForm(); err != nil {
		return LaneInput{}, view.LaneForm{},
			apperrors.Invalid("That form could not be read.").Wrap(err)
	}

	form := view.LaneForm{
		Name:     r.PostForm.Get("name"),
		Colour:   r.PostForm.Get("color"),
		WIPLimit: strings.TrimSpace(r.PostForm.Get("wip_limit")),
		IsDone:   r.PostForm.Get("is_done") != "",
		Colours:  colourNames(),
	}

	in := LaneInput{
		Name:   form.Name,
		Colour: domain.Colour(form.Colour),
		IsDone: form.IsDone,
	}
	if form.Colour == "" {
		in.Colour = domain.ColourSlate
		form.Colour = string(domain.ColourSlate)
	}

	// An empty field means no limit, which is a NULL column rather than a zero.
	if form.WIPLimit != "" {
		limit, err := strconv.Atoi(form.WIPLimit)
		if err != nil {
			return in, form, apperrors.Validation("That WIP limit is not a number.").
				WithField("wip_limit", "must be a whole number")
		}
		in.WIPLimit = &limit
	}

	return in, form, nil
}

// formWithError puts a failure's field messages onto the lane form.
func formWithError(form view.LaneForm, err error) view.LaneForm {
	app := apperrors.From(err)
	for _, f := range app.Fields {
		switch f.Field {
		case "name":
			form.NameError = f.Message
		case "wip_limit":
			form.WIPError = f.Message
		case "colour":
			// The palette is a closed radio group, so this can only happen to a
			// hand-made request. Report it on the form rather than as a 400 page.
			form.Error = "That is not one of the available colours."
		}
	}
	if form.NameError == "" && form.WIPError == "" && form.Error == "" {
		form.Error = app.Message
	}
	return form
}

// shifted returns the lane order with one lane moved a single place.
func shifted(lanes []*domain.Lane, laneUUID, direction string) ([]string, error) {
	order := make([]string, 0, len(lanes))
	at := -1
	for i, l := range lanes {
		if l.UUID == laneUUID {
			at = i
		}
		order = append(order, l.UUID)
	}
	if at < 0 {
		return nil, ErrNoSuchLane
	}

	swapWith := at - 1
	if direction == "down" {
		swapWith = at + 1
	}
	if swapWith < 0 || swapWith >= len(order) {
		// Already at the end. Not an error — the button should not have been
		// offered, and a redirect to an unchanged board is the honest outcome.
		return order, nil
	}

	order[at], order[swapWith] = order[swapWith], order[at]
	return order, nil
}

// toLaneViews maps lanes onto the UI's shape. Cards are left empty; phase 4 fills
// them.
func toLaneViews(lanes []*domain.Lane) []view.Lane {
	out := make([]view.Lane, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, view.Lane{
			UUID:     l.UUID,
			Name:     l.Name,
			Color:    string(l.Colour),
			WIPLimit: l.Limit(),
			IsDone:   l.IsDone,
		})
	}
	return out
}

// colourNames is the palette as the form offers it. Derived from the domain's list
// so the two cannot disagree.
func colourNames() []string {
	out := make([]string, 0, len(domain.Colours))
	for _, c := range domain.Colours {
		out = append(out, string(c))
	}
	return out
}

func laneFormTitle(f view.LaneForm) string {
	if f.IsNew() {
		return "Add a lane"
	}
	return "Edit lane"
}

// isFormError reports whether a failure belongs on the form rather than an error
// page: the person can fix it by choosing something different.
func isFormError(err error) bool {
	switch apperrors.From(err).Code {
	case apperrors.CodeValidation, apperrors.CodeInvalid, apperrors.CodeConflict:
		return true
	}
	return false
}

// wipField renders a lane's limit for the form: an empty string for no limit, so an
// empty input round-trips to NULL rather than to zero.
func wipField(limit *int) string {
	if limit == nil {
		return ""
	}
	return strconv.Itoa(*limit)
}
