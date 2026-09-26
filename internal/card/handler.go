package card

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	boarddomain "github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/markdown"
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
	Labels(ctx context.Context, subj authz.Subject, projectUUID string) ([]*boarddomain.Label, error)
	ProjectOfLane(ctx context.Context, laneUUID string) (string, error)
}

// People resolves the display name behind a uuid. Satisfied by the auth service,
// because names are its data.
type People interface {
	DisplayName(ctx context.Context, userUUID string) string
}

// Assignees answers who may be given a card on a board. Satisfied by the project
// service, because membership is its data — which is why this is a second port rather
// than another method on People.
type Assignees interface {
	People(ctx context.Context, projectUUID string) ([]project.Person, error)
}

// Handler serves cards.
//
// Like the board handler, it leans on the project handler for access resolution,
// page chrome and the error path, so there is one definition of "which board is this
// and what may the caller do to it".
type Handler struct {
	svc       *Service
	projects  *project.Handler
	board     LaneReader
	people    People
	assignees Assignees
	hub       *sse.Hub
	log       *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(
	svc *Service, projects *project.Handler, board LaneReader,
	people People, assignees Assignees, hub *sse.Hub, log *slog.Logger,
) *Handler {
	return &Handler{
		svc: svc, projects: projects, board: board,
		people: people, assignees: assignees, hub: hub, log: log,
	}
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

	form, err := h.withChoices(r, access, view.CardForm{
		LaneUUID: lane.UUID,
		LaneName: lane.Name,
		Priority: string(domain.PriorityNone),
	})
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Fragment(w, r, components.CardForm(access.Project.Slug, form))
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

	in, form, err := cardInputFrom(r)
	form.LaneUUID = laneUUID
	if err != nil {
		h.cardFormFailure(w, r, access, form, err)
		return
	}

	card, err := h.svc.Create(r.Context(), access.Subject, access.Project.UUID, laneUUID, in.Title)
	if err != nil {
		h.cardFormFailure(w, r, access, form, err)
		return
	}

	// The rich fields land in a second write, because Create takes only a title.
	// The create form offers all of them, and asking somebody to save twice to set a
	// due date would be hostile.
	if hasRichFields(in) {
		card, err = h.svc.Edit(r.Context(), access.Subject, access.Project.UUID, card.UUID, in)
		if err != nil {
			h.cardFormFailure(w, r, access, form, err)
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

	labels, err := h.svc.LabelUUIDsForCards(r.Context(), []string{card.UUID})
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	form := view.CardForm{
		UUID:        card.UUID,
		LaneUUID:    card.LaneUUID,
		Title:       card.Title,
		Description: card.Description,
		Priority:    string(card.Priority),
		LabelUUIDs:  labels[card.UUID],
	}
	if card.AssigneeUUID != nil {
		form.Assignee = *card.AssigneeUUID
	}
	if card.DueAt != nil {
		form.DueAt = card.DueAt.Format(dueDateLayout)
	}

	form, err = h.withChoices(r, access, form)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	render.Fragment(w, r, components.CardForm(access.Project.Slug, form))
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

	in, form, err := cardInputFrom(r)
	form.UUID = card.UUID
	form.LaneUUID = card.LaneUUID
	if err != nil {
		h.cardFormFailure(w, r, access, form, err)
		return
	}

	updated, err := h.svc.Edit(r.Context(), access.Subject, access.Project.UUID, card.UUID, in)
	if err != nil {
		h.cardFormFailure(w, r, access, form, err)
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

	cardViews, err := h.toCardViews(r, access, cards)
	if err != nil {
		return view.Board{}, view.Lane{}, err
	}

	return h.boardChrome(access), view.Lane{
		UUID:     lane.UUID,
		Name:     lane.Name,
		Color:    string(lane.Colour),
		WIPLimit: lane.Limit(),
		IsDone:   lane.IsDone,
		Cards:    cardViews,
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

// toCardViews maps cards onto the UI's shape, labels and counts included.
//
// The labels and the two counts are one query each for the whole set, not one per
// card: a board with six lanes and forty cards would otherwise be a hundred and
// twenty round trips to render one page.
func (h *Handler) toCardViews(
	r *http.Request, access *project.Access, cards []*domain.Card,
) ([]view.Card, error) {
	if len(cards) == 0 {
		return nil, nil
	}

	uuids := make([]string, 0, len(cards))
	for _, c := range cards {
		uuids = append(uuids, c.UUID)
	}

	labelsByCard, err := h.svc.LabelUUIDsForCards(r.Context(), uuids)
	if err != nil {
		return nil, err
	}
	comments, err := h.svc.CommentCounts(r.Context(), uuids)
	if err != nil {
		return nil, err
	}
	files, err := h.svc.AttachmentCounts(r.Context(), uuids)
	if err != nil {
		return nil, err
	}

	projectLabels, err := h.labelsByUUID(r, access)
	if err != nil {
		return nil, err
	}

	out := make([]view.Card, 0, len(cards))
	for _, c := range cards {
		v := view.Card{
			UUID:      c.UUID,
			LaneUUID:  c.LaneUUID,
			Title:     c.Title,
			Priority:  string(c.Priority),
			DueAt:     c.DueAt,
			HasDesc:   strings.TrimSpace(c.Description) != "",
			Comments:  comments[c.UUID],
			Files:     files[c.UUID],
			UpdatedAt: c.UpdatedAt,
		}
		for _, labelUUID := range labelsByCard[c.UUID] {
			if l, ok := projectLabels[labelUUID]; ok {
				v.Labels = append(v.Labels, l)
			}
		}
		if c.AssigneeUUID != nil {
			v.Assignee = &view.CurrentUser{
				UUID:        *c.AssigneeUUID,
				DisplayName: h.people.DisplayName(r.Context(), *c.AssigneeUUID),
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// labelsByUUID is this board's labels, keyed for lookup.
func (h *Handler) labelsByUUID(
	r *http.Request, access *project.Access,
) (map[string]view.Label, error) {
	labels, err := h.board.Labels(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]view.Label, len(labels))
	for _, l := range labels {
		out[l.UUID] = view.Label{UUID: l.UUID, Name: l.Name, Color: string(l.Colour)}
	}
	return out, nil
}

// priorityLabel is the human form of a priority, or "" for none — which is the
// default and not worth a row in the facts list.
func priorityLabel(p domain.Priority) string {
	switch p {
	case domain.PriorityLow:
		return "Low"
	case domain.PriorityNormal:
		return "Normal"
	case domain.PriorityHigh:
		return "High"
	case domain.PriorityUrgent:
		return "Urgent"
	}
	return ""
}

// humanBytes renders a size for a person rather than for a machine.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 0, 64) + " KB"
	default:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MB"
	}
}

// detailView assembles the card modal.
//
// Several reads, deliberately one query each rather than a join: this runs once when a
// person opens a card, not once per card on a board.
func (h *Handler) detailView(
	r *http.Request, access *project.Access, card *domain.Card,
) (view.CardDetail, error) {
	lanes, err := h.board.Lanes(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return view.CardDetail{}, err
	}
	laneNames := make(map[string]string, len(lanes))
	for _, l := range lanes {
		laneNames[l.UUID] = l.Name
	}

	history, err := h.svc.History(r.Context(), access.Subject, access.Project.UUID, card.UUID, 20)
	if err != nil {
		return view.CardDetail{}, err
	}
	comments, err := h.svc.Comments(r.Context(), access.Subject, access.Project.UUID, card.UUID)
	if err != nil {
		return view.CardDetail{}, err
	}
	attachments, err := h.svc.Attachments(r.Context(), access.Subject, access.Project.UUID, card.UUID)
	if err != nil {
		return view.CardDetail{}, err
	}

	cards, err := h.toCardViews(r, access, []*domain.Card{card})
	if err != nil {
		return view.CardDetail{}, err
	}

	// Rendered and sanitised here rather than in the component: the component
	// receives trusted markup, and exactly one place decides what "trusted" means.
	descriptionHTML := markdown.Render(card.Description)

	detail := view.CardDetail{
		Card:            cards[0],
		LaneName:        laneNames[card.LaneUUID],
		DescriptionHTML: descriptionHTML,
		HasDescription:  strings.TrimSpace(descriptionHTML) != "",
		CreatedBy:       h.people.DisplayName(r.Context(), card.CreatedBy),
		CreatedAt:       card.CreatedAt,
		UpdatedAt:       card.UpdatedAt,
		IsArchived:      card.IsArchived(),
		CanDelete:       access.Subject.CanOn(authz.ActionCardDelete, card.CreatedBy),
		Priority:        priorityLabel(card.Priority),
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

	for _, c := range comments {
		detail.Comments = append(detail.Comments, view.Comment{
			UUID:      c.UUID,
			Author:    h.people.DisplayName(r.Context(), c.AuthorUUID),
			BodyHTML:  markdown.Render(c.Body),
			CreatedAt: c.CreatedAt,
			IsDeleted: c.IsDeleted(),
			CanDelete: !c.IsDeleted() &&
				access.Subject.CanOn(authz.ActionCommentDelete, c.AuthorUUID),
		})
	}

	for _, a := range attachments {
		detail.Attachments = append(detail.Attachments, view.Attachment{
			UUID:       a.UUID,
			Filename:   a.Filename,
			SizeHuman:  humanBytes(a.SizeBytes),
			MIMEType:   a.MIMEType,
			UploadedBy: h.people.DisplayName(r.Context(), a.UploadedBy),
			CreatedAt:  a.CreatedAt,
			CanDelete:  access.Subject.CanOn(authz.ActionAttachmentDelete, a.UploadedBy),
			IsImage:    strings.HasPrefix(a.MIMEType, "image/"),
		})
	}

	// Every lane but the one the card is in, for the keyboard move control.
	for _, l := range lanes {
		if l.UUID == card.LaneUUID {
			continue
		}
		detail.MoveTo = append(detail.MoveTo, view.Choice{Value: l.UUID, Label: l.Name})
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
		case "due_at":
			form.DueError = f.Message
		case "assignee":
			form.AssigneeError = f.Message
		}
	}
	if form.TitleError == "" && form.DescError == "" &&
		form.DueError == "" && form.AssigneeError == "" {
		form.Error = app.Message
	}

	// The choices have to come back with it, or a rejected form renders with empty
	// dropdowns and the person loses their label ticks.
	withChoices, buildErr := h.withChoices(r, access, form)
	if buildErr != nil {
		h.projects.Fail(w, r, buildErr)
		return
	}

	render.Page(w, r, app.Status(), components.CardForm(access.Project.Slug, withChoices))
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
		cardViews, err := h.toCardViews(r, access, byLane[l.UUID])
		if err != nil {
			return nil, view.Board{}, err
		}
		board.Lanes = append(board.Lanes, view.Lane{
			UUID:     l.UUID,
			Name:     l.Name,
			Color:    string(l.Colour),
			WIPLimit: l.Limit(),
			IsDone:   l.IsDone,
			Cards:    cardViews,
		})
	}
	return access, board, nil
}

// dueDateLayout is the format a `<input type="date">` submits and expects.
const dueDateLayout = "2006-01-02"

// cardInputFrom parses the card form.
//
// It returns the form state alongside the input, so a failure re-renders what was
// typed rather than an empty form. Every field is read every time: the form always
// submits all of them, which is what makes "clear the due date" expressible at all.
func cardInputFrom(r *http.Request) (EditInput, view.CardForm, error) {
	if err := r.ParseForm(); err != nil {
		return EditInput{}, view.CardForm{},
			apperrors.Invalid("That form could not be read.").Wrap(err)
	}

	form := view.CardForm{
		Title:       r.PostForm.Get("title"),
		Description: r.PostForm.Get("description"),
		Priority:    r.PostForm.Get("priority"),
		Assignee:    strings.TrimSpace(r.PostForm.Get("assignee")),
		DueAt:       strings.TrimSpace(r.PostForm.Get("due_at")),
		LabelUUIDs:  orderField(r.PostForm["labels"]),
	}

	in := EditInput{
		Title:       form.Title,
		Description: form.Description,
		Priority:    domain.Priority(form.Priority),
		LabelUUIDs:  form.LabelUUIDs,
	}

	// An empty select means nobody, which is a NULL column rather than an empty
	// string — the foreign key would refuse "".
	if form.Assignee != "" {
		assignee := form.Assignee
		in.AssigneeUUID = &assignee
	}

	// An empty date field means no due date. Parsed in UTC, so a card due "3 Nov" is
	// due on the 3rd wherever it is read.
	if form.DueAt != "" {
		due, err := time.ParseInLocation(dueDateLayout, form.DueAt, time.UTC)
		if err != nil {
			return in, form, apperrors.Validation("That is not a date.").
				WithField("due_at", "must be a date like 2026-11-03")
		}
		in.DueAt = &due
	}

	return in, form, nil
}

// hasRichFields reports whether anything beyond the title needs a second write after
// a create.
func hasRichFields(in EditInput) bool {
	return strings.TrimSpace(in.Description) != "" ||
		(in.Priority != "" && in.Priority != domain.PriorityNone) ||
		in.AssigneeUUID != nil ||
		in.DueAt != nil ||
		len(in.LabelUUIDs) > 0
}

// withChoices fills in the options a card form offers: priorities, the people who can
// see this board, and its labels.
func (h *Handler) withChoices(
	r *http.Request, access *project.Access, form view.CardForm,
) (view.CardForm, error) {
	form.Priorities = priorityChoices()

	people, err := h.assignees.People(r.Context(), access.Project.UUID)
	if err != nil {
		return form, err
	}
	for _, p := range people {
		form.People = append(form.People, view.Candidate{
			UUID: p.UUID, DisplayName: p.DisplayName, Email: p.Email,
		})
	}

	labels, err := h.board.Labels(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return form, err
	}
	for _, l := range labels {
		form.Labels = append(form.Labels, view.Label{
			UUID: l.UUID, Name: l.Name, Color: string(l.Colour),
		})
	}
	return form, nil
}

// priorityChoices is the priority select's options, in the order they mean something:
// none first, because it is the default.
func priorityChoices() []view.Choice {
	return []view.Choice{
		{Value: string(domain.PriorityNone), Label: "None"},
		{Value: string(domain.PriorityLow), Label: "Low"},
		{Value: string(domain.PriorityNormal), Label: "Normal"},
		{Value: string(domain.PriorityHigh), Label: "High"},
		{Value: string(domain.PriorityUrgent), Label: "Urgent"},
	}
}

// MoveToLane is the keyboard path for a drag: pick a lane from the card's own menu.
func (h *Handler) MoveToLane(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	affected, err := h.svc.MoveToLane(
		r.Context(), access.Subject, access.Project.UUID, card.UUID, r.PostForm.Get("to_lane"))
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.broadcast(r, access, affected)
	h.afterCardChange(w, r, access, card.LaneUUID)
}

// Comment adds a comment and returns the refreshed card panel.
func (h *Handler) Comment(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.projects.Fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	if _, err := h.svc.Comment(
		r.Context(), access.Subject, access.Project.UUID, card.UUID, r.PostForm.Get("body"),
	); err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	// The count on the card changed, so the lane behind the modal is stale in every
	// other tab.
	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardPanelChange(w, r, access, card.UUID)
}

// DeleteComment soft-deletes a comment.
func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	commentUUID := chi.URLParam(r, "comment")

	// The comment carries no project in its URL, so the project comes from the card
	// it is on. Same two-step as a card: look up, resolve access, then act scoped.
	comment, err := h.svc.commentForRouting(r.Context(), commentUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	projectUUID, err := h.svc.ProjectOf(r.Context(), comment.CardUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	access, err := h.projects.ResolveRef(r, projectUUID)
	if err != nil {
		h.projects.Fail(w, r, ErrNoSuchComment)
		return
	}

	if _, err := h.svc.DeleteComment(
		r.Context(), access.Subject, access.Project.UUID, commentUUID,
	); err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	card, err := h.svc.ByUUID(r.Context(), access.Subject, access.Project.UUID, comment.CardUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardPanelChange(w, r, access, card.UUID)
}

// Attach uploads a file onto a card.
func (h *Handler) Attach(w http.ResponseWriter, r *http.Request) {
	access, card, err := h.resolveCard(r)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	// The body was capped by middleware.LimitBody and already parsed by the CSRF
	// check, both of which cache — so this is a no-op on the happy path and the
	// error here is a body that never arrived whole.
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		h.projects.Fail(w, r, ErrUploadTooLarge)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		h.projects.Fail(w, r, apperrors.Validation("Choose a file to attach.").
			WithField("file", "is required"))
		return
	}
	defer func() { _ = file.Close() }()

	if _, err := h.svc.Attach(r.Context(), access.Subject, access.Project.UUID, card.UUID,
		UploadInput{
			Filename: header.Filename,
			MIMEType: header.Header.Get("Content-Type"),
			Content:  file,
		}); err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardPanelChange(w, r, access, card.UUID)
}

// multipartMemory is how much of an upload is buffered before it spills to a temporary
// file. Small, because the bytes are going to disk anyway.
const multipartMemory = 1 << 20

// ServeAttachment streams a file.
//
// Authorised per request against the card's project, so a leaked URL is not a leaked
// file. Every refusal is a 404 — a 403 would confirm the file exists.
func (h *Handler) ServeAttachment(w http.ResponseWriter, r *http.Request) {
	attachmentUUID := chi.URLParam(r, "attachment")

	projectUUID, err := h.svc.ProjectOfAttachment(r.Context(), attachmentUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	access, err := h.projects.ResolveRef(r, projectUUID)
	if err != nil {
		h.projects.Fail(w, r, ErrNoSuchAttachment)
		return
	}

	attachment, file, err := h.svc.OpenAttachment(
		r.Context(), access.Subject, access.Project.UUID, attachmentUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()

	// Always as a download, never rendered in place. Combined with nosniff, that
	// means even a file whose declared type is wrong cannot execute in this origin.
	// The filename is quoted and its quotes stripped, because it is the one piece of
	// user input that reaches a header.
	w.Header().Set("Content-Type", attachment.MIMEType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(attachment.Filename))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")

	http.ServeContent(w, r, attachment.Filename, attachment.CreatedAt, file)
}

// DeleteAttachment removes a file.
func (h *Handler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	attachmentUUID := chi.URLParam(r, "attachment")

	projectUUID, err := h.svc.ProjectOfAttachment(r.Context(), attachmentUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	access, err := h.projects.ResolveRef(r, projectUUID)
	if err != nil {
		h.projects.Fail(w, r, ErrNoSuchAttachment)
		return
	}

	attachment, err := h.svc.DeleteAttachment(
		r.Context(), access.Subject, access.Project.UUID, attachmentUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}

	card, err := h.svc.ByUUID(r.Context(), access.Subject, access.Project.UUID, attachment.CardUUID)
	if err != nil {
		h.projects.Fail(w, r, err)
		return
	}
	h.broadcast(r, access, []string{card.LaneUUID})
	h.afterCardPanelChange(w, r, access, card.UUID)
}

// afterCardPanelChange re-renders the card modal after something inside it changed,
// and redirects a plain form post back to the board.
func (h *Handler) afterCardPanelChange(
	w http.ResponseWriter, r *http.Request, access *project.Access, cardUUID string,
) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/projects/"+access.Project.Slug, http.StatusSeeOther)
		return
	}

	card, err := h.svc.ByUUID(r.Context(), access.Subject, access.Project.UUID, cardUUID)
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

// contentDisposition builds the header, defending the quoted-string it puts a
// user-supplied filename into.
//
// A filename containing a quote or a newline could otherwise close the quoted string
// early or inject a second header. Anything outside a conservative set is dropped, and
// a name that reduces to nothing falls back to a generic one.
func contentDisposition(filename string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f: // control characters, CR and LF included
			return -1
		case r == '"' || r == '\\' || r == ';' || r == '/':
			return -1
		case r > 0x7e: // non-ASCII: kept out of the quoted form, see below
			return -1
		}
		return r
	}, filename)
	safe = strings.TrimSpace(safe)
	if safe == "" {
		safe = "attachment"
	}

	// filename* carries the original in UTF-8 per RFC 5987, so a name with
	// non-ASCII characters still downloads correctly in a browser that understands
	// it, while filename= stays a safe ASCII fallback for one that does not.
	return `attachment; filename="` + safe + `"; filename*=UTF-8''` +
		url.PathEscape(filename)
}
