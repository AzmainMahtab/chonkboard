package card

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	boarddomain "github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/render"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"
	"github.com/AzmainMahtab/chonkboard/web/components"
	"github.com/AzmainMahtab/chonkboard/web/pages"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// LaneReader is what this handler needs to render a lane fragment: the lane, and
// which project it belongs to.
type LaneReader interface {
	Lane(ctx context.Context, subj authz.Subject, projectUUID, laneUUID string) (*boarddomain.Lane, error)
	Lanes(ctx context.Context, subj authz.Subject, projectUUID string) ([]*boarddomain.Lane, error)
	ProjectOfLane(ctx context.Context, laneUUID string) (string, error)
}

// People resolves display names for the uuids a card carries.
type People interface {
	DisplayName(ctx context.Context, userUUID string) string
}

// Handler serves cards.
//
// Like the board handler, it leans on the project handler for access resolution,
// page chrome and the error path, so there is one definition of "which board is this
// and what may the caller do to it".
type Handler struct {
	svc      *Service
	projects *project.Handler
	board    LaneReader
	people   People
	hub      *sse.Hub
	log      *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(
	svc *Service, projects *project.Handler, board LaneReader,
	people People, hub *sse.Hub, log *slog.Logger,
) *Handler {
	return &Handler{svc: svc, projects: projects, board: board, people: people, hub: hub, log: log}
}

// NewCard renders the create form for one lane.
func (h *Handler) NewCard(w http.ResponseWriter, r *http.Request) {
	access, err := h.projects.Resolve(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	laneUUID := chi.URLParam(r, "lane")
	lane, err := h.board.Lane(r.Context(), access.Subject, access.Project.UUID, laneUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	render.Fragment(w, r, components.CardForm(access.Project.Slug, view.CardForm{
		LaneUUID: lane.UUID,
		LaneName: lane.Name,
	}))
}

// Create adds a card and returns the lane it landed in.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	access, err := h.projects.Resolve(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	laneUUID := chi.URLParam(r, "lane")
	title := r.PostForm.Get("title")
	description := r.PostForm.Get("description")

	card, err := h.svc.Create(r.Context(), access.Subject, access.Project.UUID, laneUUID, title)
	if err != nil {
		h.cardFormFailure(w, r, access, view.CardForm{
			LaneUUID: laneUUID, Title: title, Description: description,
		}, err)
		return
	}

	// The description is set in a second step because Create takes only a title:
	// the create form offers both, and a card with a description should not need
	// the person to save twice.
	if strings.TrimSpace(description) != "" {
		card, err = h.svc.Edit(r.Context(), access.Subject, access.Project.UUID, card.UUID,
			EditInput{Title: title, Description: description})
		if err != nil {
			h.cardFormFailure(w, r, access, view.CardForm{
				LaneUUID: laneUUID, Title: title, Description: description,
			}, err)
			return
		}
	}

	h.broadcast(r, access, []string{card.LaneUUID})
	h.renderLane(w, r, access, card.LaneUUID)
}

// Detail renders the card modal.
func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	detail, err := h.detailView(r, access, card)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Fragment(w, r, components.CardModal(access.Project.Slug, detail))
}

// EditForm renders the edit form.
func (h *Handler) EditForm(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	render.Fragment(w, r, components.CardForm(access.Project.Slug, view.CardForm{
		UUID:        card.UUID,
		LaneUUID:    card.LaneUUID,
		Title:       card.Title,
		Description: card.Description,
	}))
}

// Update saves a card and returns its lane.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	in := EditInput{
		Title:       r.PostForm.Get("title"),
		Description: r.PostForm.Get("description"),
	}

	updated, err := h.svc.Edit(r.Context(), access.Subject, access.Project.UUID, card.UUID, in)
	if err != nil {
		h.cardFormFailure(w, r, access, view.CardForm{
			UUID: card.UUID, LaneUUID: card.LaneUUID,
			Title: in.Title, Description: in.Description,
		}, err)
		return
	}

	h.broadcast(r, access, []string{updated.LaneUUID})
	h.renderLane(w, r, access, updated.LaneUUID)
}

// SetArchived archives or restores a card.
func (h *Handler) SetArchived(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	archived := r.PostForm.Get("archived") == "1"
	if _, err := h.svc.SetArchived(
		r.Context(), access.Subject, access.Project.UUID, card.UUID, archived,
	); err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardChange(w, r, access, card.LaneUUID)
}

// Delete removes a card permanently.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	if _, err := h.svc.Delete(r.Context(), access.Subject, access.Project.UUID, card.UUID); err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardChange(w, r, access, card.LaneUUID)
}

// Move applies a drag.
//
// The one endpoint with a hand-written client, so its contract is fixed: form-encoded
// lane and order fields in, **204 with no body** out, and the result reaching other
// tabs over SSE rather than in this response.
func (h *Handler) Move(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.moveFailure(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.moveFailure(w, r, apperrors.Invalid("That move could not be read.").Wrap(err))
		return
	}

	m := domain.Move{
		CardUUID:  card.UUID,
		ToLane:    r.PostForm.Get("to_lane"),
		ToOrder:   orderField(r.PostForm["to_order"]),
		FromLane:  r.PostForm.Get("from_lane"),
		FromOrder: orderField(r.PostForm["from_order"]),
	}

	affected, err := h.svc.Move(r.Context(), access.Subject, access.Project.UUID, m)
	if err != nil {
		h.moveFailure(w, r, err)
		return
	}

	h.broadcast(r, access, affected)
	w.WriteHeader(http.StatusNoContent)
}

// orderField reads a repeated order field, dropping empties.
//
// A form that submits `from_order=` with no value arrives as a slice holding one
// empty string, which the planner would reject as a card that is not on the board.
// That would refuse the perfectly ordinary move of dragging the last card out of a
// lane, whose source order is genuinely empty. board.js omits the field entirely in
// that case, but a plain form or a hand-made request need not.
func orderField(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// LaneFragment re-renders one lane. It is the revert target after a failed drag, so
// the board snaps back to whatever the database actually says rather than to a
// guessed undo.
func (h *Handler) LaneFragment(w http.ResponseWriter, r *http.Request) {
	laneUUID := chi.URLParam(r, "lane")

	projectUUID, err := h.board.ProjectOfLane(r.Context(), laneUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	access, err := h.projects.ResolveRef(r, projectUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.renderLane(w, r, access, laneUUID)
}

// resolveCard turns the {card} URL parameter into the board it is on and the card
// itself, refusing with 404 when the caller has no grant.
//
// Two reads by design: the first is unscoped and only yields a project uuid, the
// second is scoped to the access that uuid resolved to. See Service.ProjectOf.
func (h *Handler) resolveCard(r *http.Request) (*project.Access, *domain.Card, error) {
	cardUUID := chi.URLParam(r, "card")

	projectUUID, err := h.svc.ProjectOf(r.Context(), cardUUID)
	if err != nil {
		return nil, nil, err
	}
	access, err := h.projects.ResolveRef(r, projectUUID)
	if err != nil {
		// No grant on the board this card is on. Report it as a missing card, so
		// the response does not confirm the card exists.
		if errors.Is(err, project.ErrNoSuchProject) {
			return nil, nil, ErrNoSuchCard
		}
		return nil, nil, err
	}

	card, err := h.svc.ByUUID(r.Context(), access.Subject, access.Project.UUID, cardUUID)
	if err != nil {
		return nil, nil, err
	}
	return access, card, nil
}

// renderLane writes one lane as a fragment.
func (h *Handler) renderLane(
	w http.ResponseWriter, r *http.Request, access *project.Access, laneUUID string,
) {
	board, lane, err := h.laneView(r, access, laneUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Fragment(w, r, components.Lane(board, lane))
}

// afterCardChange answers a non-HTMX form post with a redirect to the board, and an
// HTMX one with the lane fragment.
//
// The archive and delete buttons live inside the card modal, which is HTMX-driven in
// practice but a plain form without it. A redirect is the honest answer for the plain
// case: the modal is gone, so there is nothing for a fragment to replace.
func (h *Handler) afterCardChange(
	w http.ResponseWriter, r *http.Request, access *project.Access, laneUUID string,
) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/projects/"+access.Project.Slug, http.StatusSeeOther)
		return
	}
	// Close the modal, and let the board re-fetch itself: the card has left the
	// lane it was in, and the modal it was shown in no longer has a subject.
	w.Header().Set("HX-Trigger", "card-gone")
	h.renderLane(w, r, access, laneUUID)
}

// laneView assembles one lane with its cards.
func (h *Handler) laneView(
	r *http.Request, access *project.Access, laneUUID string,
) (view.Board, view.Lane, error) {
	lane, cards, err := h.svc.ForLane(r.Context(), access.Subject, access.Project.UUID, laneUUID)
	if err != nil {
		return view.Board{}, view.Lane{}, err
	}

	return h.boardChrome(access), view.Lane{
		UUID:     lane.UUID,
		Name:     lane.Name,
		Color:    string(lane.Colour),
		WIPLimit: lane.Limit(),
		IsDone:   lane.IsDone,
		Cards:    h.toCardViews(r, cards),
	}, nil
}

// boardChrome is the little of the board a lane fragment needs: the slug for its
// links, and whether to draw the manager controls.
func (h *Handler) boardChrome(access *project.Access) view.Board {
	return view.Board{
		ProjectUUID: access.Project.UUID,
		ProjectSlug: access.Project.Slug,
		ProjectName: access.Project.Name,
		CanManage:   access.CanManage(),
		CanAddCards: true,
	}
}

// toCardViews maps cards onto the UI's shape.
func (h *Handler) toCardViews(r *http.Request, cards []*domain.Card) []view.Card {
	out := make([]view.Card, 0, len(cards))
	for _, c := range cards {
		v := view.Card{
			UUID:      c.UUID,
			LaneUUID:  c.LaneUUID,
			Title:     c.Title,
			Priority:  string(c.Priority),
			DueAt:     c.DueAt,
			HasDesc:   strings.TrimSpace(c.Description) != "",
			UpdatedAt: c.UpdatedAt,
		}
		if c.AssigneeUUID != nil {
			v.Assignee = &view.CurrentUser{
				UUID:        *c.AssigneeUUID,
				DisplayName: h.people.DisplayName(r.Context(), *c.AssigneeUUID),
			}
		}
		out = append(out, v)
	}
	return out
}

// detailView assembles the card modal.
func (h *Handler) detailView(
	r *http.Request, access *project.Access, card *domain.Card,
) (view.CardDetail, error) {
	lane, err := h.board.Lane(r.Context(), access.Subject, access.Project.UUID, card.LaneUUID)
	if err != nil {
		return view.CardDetail{}, err
	}

	history, err := h.svc.History(r.Context(), access.Subject, access.Project.UUID, card.UUID, 20)
	if err != nil {
		return view.CardDetail{}, err
	}

	// Lane names for the move entries, resolved once for the whole history rather
	// than once per row.
	laneNames, err := h.laneNames(r, access)
	if err != nil {
		return view.CardDetail{}, err
	}

	detail := view.CardDetail{
		Card:        h.toCardViews(r, []*domain.Card{card})[0],
		LaneName:    lane.Name,
		Description: card.Description,
		CreatedBy:   h.people.DisplayName(r.Context(), card.CreatedBy),
		CreatedAt:   card.CreatedAt,
		UpdatedAt:   card.UpdatedAt,
		IsArchived:  card.IsArchived(),
		CanDelete:   access.Subject.CanOn(authz.ActionCardDelete, card.CreatedBy),
	}
	for _, a := range history {
		entry := view.ActivityEntry{
			Kind:  string(a.Kind),
			Actor: h.people.DisplayName(r.Context(), a.ActorUUID),
			At:    a.CreatedAt,
		}
		if a.FromLaneUUID != nil {
			entry.From = laneNames[*a.FromLaneUUID]
		}
		if a.ToLaneUUID != nil {
			entry.To = laneNames[*a.ToLaneUUID]
		}
		detail.History = append(detail.History, entry)
	}
	return detail, nil
}

// laneNames maps lane uuids to names, for rendering move history.
func (h *Handler) laneNames(r *http.Request, access *project.Access) (map[string]string, error) {
	lanes, err := h.board.Lanes(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(lanes))
	for _, l := range lanes {
		names[l.UUID] = l.Name
	}
	return names, nil
}

// cardFormFailure re-renders the card form with what was typed and what was wrong.
func (h *Handler) cardFormFailure(
	w http.ResponseWriter, r *http.Request, access *project.Access, form view.CardForm, err error,
) {
	app := apperrors.From(err)
	if app.Status() >= 500 || app.Code == apperrors.CodeForbidden || app.Code == apperrors.CodeNotFound {
		h.projects.Fail(w, r, err)
		return
	}

	for _, f := range app.Fields {
		switch f.Field {
		case "title":
			form.TitleError = f.Message
		case "description":
			form.DescError = f.Message
		}
	}
	if form.TitleError == "" && form.DescError == "" {
		form.Error = app.Message
	}

	render.Page(w, r, app.Status(), components.CardForm(access.Project.Slug, form))
}

// moveFailure answers a refused drag.
//
// A toast fragment and a real status code, so board.js can tell the person why and
// then re-fetch both affected lanes — the board snaps back to what the database says
// rather than to a guessed undo.
func (h *Handler) moveFailure(w http.ResponseWriter, r *http.Request, err error) {
	app := apperrors.From(err)
	if app.Status() >= 500 {
		h.log.ErrorContext(r.Context(), "move failed",
			"path", r.URL.Path, "error", app.Err)
	}
	render.Page(w, r, app.Status(), components.Toast(app.Message, "error"))
}

// broadcast re-renders each rewritten lane as an out-of-band fragment and pushes them
// to every other tab on this board as one event.
//
// Rendering happens after the write, so what is broadcast is what was actually
// stored. The originating tab is excluded by its client id — otherwise htmx would
// swap a card out from under the hand still dragging it.
func (h *Handler) broadcast(r *http.Request, access *project.Access, lanes []string) {
	if len(lanes) == 0 || h.hub == nil {
		return
	}

	var payload strings.Builder
	for _, laneUUID := range lanes {
		board, lane, err := h.laneView(r, access, laneUUID)
		if err != nil {
			h.log.ErrorContext(r.Context(), "cannot re-render lane for broadcast",
				"lane", laneUUID, "error", err)
			continue
		}
		html, err := render.String(r.Context(), components.LaneOOB(board, lane))
		if err != nil {
			h.log.ErrorContext(r.Context(), "cannot render lane fragment",
				"lane", laneUUID, "error", err)
			continue
		}
		payload.WriteString(html)
	}
	if payload.Len() == 0 {
		return
	}

	h.hub.Broadcast(access.Project.UUID, sse.Event{
		Name:         "lane-updated",
		HTML:         payload.String(),
		ExceptClient: r.Header.Get("X-Client-Id"),
	})
}

// Events is the SSE stream for a board.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	access, err := h.projects.Resolve(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	clientID := r.URL.Query().Get("client")
	if clientID == "" {
		h.projects.Fail(w, r, apperrors.Invalid("Missing client id."))
		return
	}
	h.hub.Stream(w, r, access.Project.UUID, clientID)
}

// BoardPage renders a whole board. Any granted member may open it.
//
// It lives in this slice rather than in board because it renders cards, and the card
// mapping is this slice's. Putting it in board would mean a second cross-slice port
// and a duplicate of toCardViews — this handler already holds both halves.
func (h *Handler) BoardPage(w http.ResponseWriter, r *http.Request) {
	access, board, err := h.boardView(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Page(w, r, http.StatusOK,
		pages.BoardPage(h.projects.Page(r, access.Project.Name), board))
}

// BoardFragment re-renders the whole board, which is what a reconnect and a
// structural change ask for.
func (h *Handler) BoardFragment(w http.ResponseWriter, r *http.Request) {
	_, board, err := h.boardView(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Fragment(w, r, components.Board(board))
}

// boardView assembles every lane with its cards.
//
// Two queries for the whole board — one for the lanes, one for every live card on it
// — rather than one per lane.
func (h *Handler) boardView(r *http.Request) (*project.Access, view.Board, error) {
	access, err := h.projects.Resolve(r)
	if err != nil {
		return nil, view.Board{}, err
	}

	lanes, err := h.board.Lanes(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return nil, view.Board{}, err
	}
	byLane, err := h.svc.ForProject(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return nil, view.Board{}, err
	}

	board := h.boardChrome(access)
	board.Lanes = make([]view.Lane, 0, len(lanes))
	for _, l := range lanes {
		board.Lanes = append(board.Lanes, view.Lane{
			UUID:     l.UUID,
			Name:     l.Name,
			Color:    string(l.Colour),
			WIPLimit: l.Limit(),
			IsDone:   l.IsDone,
			Cards:    h.toCardViews(r, byLane[l.UUID]),
		})
	}
	return access, board, nil
}
