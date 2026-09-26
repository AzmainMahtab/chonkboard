package project

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/render"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"
	"github.com/AzmainMahtab/chonkboard/web/components"
	"github.com/AzmainMahtab/chonkboard/web/pages"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// BoardShape is what the project handler needs from the board slice to render a
// project list and a settings page: the lanes of a board.
//
// Declared here, by the consumer. It takes the already-resolved subject rather than
// the request, because the caller has always resolved access before asking — and an
// implementation that re-resolved it from the URL would find no project on a route
// like GET /, where there is no project parameter at all.
type BoardShape interface {
	LaneSummaries(ctx context.Context, subj authz.Subject, projectUUID string) ([]view.Lane, error)
	LabelSummaries(ctx context.Context, subj authz.Subject, projectUUID string) ([]view.Label, error)
	// Colours is the palette both lane and label forms offer.
	Colours() []string
}

// HandlerConfig is what the handler needs from application configuration.
type HandlerConfig struct {
	AssetSuffix string
}

// Handler serves the project list, project settings, and membership.
type Handler struct {
	svc   *Service
	board BoardShape
	hub   *sse.Hub
	cfg   HandlerConfig
	log   *slog.Logger
}

// NewHandler wires the handler.
//
// board may be nil at construction: the board handler needs this one for access
// resolution, so the two are genuinely mutually dependent and one of the two edges
// has to be set afterwards. UseBoardShape is that edge.
func NewHandler(
	svc *Service, board BoardShape, hub *sse.Hub, cfg HandlerConfig, log *slog.Logger,
) *Handler {
	return &Handler{svc: svc, board: board, hub: hub, cfg: cfg, log: log}
}

// Dirty tells every tab on a board to re-fetch it. Exported so the board handler
// signals through the same path rather than holding a second reference to the hub.
func (h *Handler) Dirty(access *Access) {
	if h.hub == nil {
		return
	}
	h.hub.Broadcast(access.Project.UUID, sse.Event{Name: "board-dirty"})
}

// UseBoardShape supplies the board slice's lane reader. Called once, at wiring.
func (h *Handler) UseBoardShape(board BoardShape) { h.board = board }

// List is the application's home.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	user := authctx.User(r.Context())
	showArchived := r.URL.Query().Get("archived") == "1"

	summaries, err := h.svc.List(r.Context(), user, showArchived)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Whether the archived toggle is worth showing at all. Asked separately
	// because the scoped list deliberately excludes them.
	hasArchived := showArchived
	if !showArchived {
		all, err := h.svc.List(r.Context(), user, true)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		hasArchived = len(all) > len(summaries)
	}

	list := view.ProjectList{
		CanCreate:    canCreateProject(user),
		ShowArchived: showArchived,
		HasArchived:  hasArchived,
	}
	for _, s := range summaries {
		summary, err := h.summarise(r.Context(), user, s)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		list.Projects = append(list.Projects, summary)
	}

	render.Page(w, r, http.StatusOK, pages.ProjectsPage(h.page(r, "Projects"), list))
}

// NewProject renders the create form.
func (h *Handler) NewProject(w http.ResponseWriter, r *http.Request) {
	if !canCreateProject(authctx.User(r.Context())) {
		h.fail(w, r, apperrors.Forbidden("Only the owner can create a project."))
		return
	}
	render.Page(w, r, http.StatusOK,
		pages.NewProjectPage(h.page(r, "New project"), view.ProjectForm{}))
}

// Create makes a project and sends the creator to its board.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	in := CreateInput{
		Name:        r.PostForm.Get("name"),
		Slug:        r.PostForm.Get("slug"),
		Description: r.PostForm.Get("description"),
	}

	project, err := h.svc.Create(r.Context(), authctx.User(r.Context()), in)
	if err != nil {
		// A validation or conflict failure re-renders the form with the values
		// still in it, rather than throwing the person's typing away.
		if isFormError(err) {
			render.Page(w, r, apperrors.From(err).Status(),
				pages.NewProjectPage(h.page(r, "New project"), projectFormFor(in, err)))
			return
		}
		h.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/projects/"+project.Slug, http.StatusSeeOther)
}

// Settings renders the manager's page for one board.
func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The whole page is a board setting, so reaching it at all requires manage.
	// This is why a member gets 403 rather than a page of disabled controls.
	if !access.CanManage() {
		h.fail(w, r, apperrors.Forbidden("You cannot change this board's settings."))
		return
	}

	settings, err := h.settingsView(r, access, "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	render.Page(w, r, http.StatusOK,
		pages.ProjectSettingsPage(h.page(r, access.Project.Name+" settings"), settings))
}

// Update saves a project's name and description.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	name := r.PostForm.Get("name")
	description := r.PostForm.Get("description")

	if err := h.svc.Rename(r.Context(), access, name, description); err != nil {
		if isFormError(err) {
			h.renderSettingsError(w, r, access, err, CreateInput{
				Name: name, Description: description, Slug: access.Project.Slug,
			})
			return
		}
		h.fail(w, r, err)
		return
	}

	// The board's name is in its header, so a rename is a structural change.
	h.Dirty(access)
	h.redirectToSettings(w, r, access.Project.Slug, "Details saved.")
}

// Archive hides or restores a project.
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if access.Project.IsArchived() {
		err = h.svc.Restore(r.Context(), access)
	} else {
		err = h.svc.Archive(r.Context(), access)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Archiving takes the board out of the list, so there is nothing useful to
	// return to; restoring leaves you on the settings page.
	if access.Project.IsArchived() {
		http.Redirect(w, r, "/?archived=1", http.StatusSeeOther)
		return
	}
	h.redirectToSettings(w, r, access.Project.Slug, "Project restored.")
}

// Delete removes a project and everything on it.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), access); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// AddMember grants somebody access.
func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	role := domain.ProjectRole(r.PostForm.Get("role"))
	if role == "" {
		role = domain.RoleMember
	}

	if err := h.svc.Grant(r.Context(), access, r.PostForm.Get("user_uuid"), role); err != nil {
		h.failOnSettings(w, r, access, err)
		return
	}
	h.redirectToSettings(w, r, access.Project.Slug, "Access granted.")
}

// SetMemberRole changes an existing member's role.
func (h *Handler) SetMemberRole(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, apperrors.Invalid("That form could not be read.").Wrap(err))
		return
	}

	err = h.svc.Grant(r.Context(), access,
		chi.URLParam(r, "user"), domain.ProjectRole(r.PostForm.Get("role")))
	if err != nil {
		h.failOnSettings(w, r, access, err)
		return
	}
	h.redirectToSettings(w, r, access.Project.Slug, "Role updated.")
}

// RemoveMember revokes access.
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	access, err := h.resolve(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Revoke(r.Context(), access, chi.URLParam(r, "user")); err != nil {
		h.failOnSettings(w, r, access, err)
		return
	}
	h.redirectToSettings(w, r, access.Project.Slug, "Access removed.")
}

// Resolve turns the {project} URL parameter into an Access.
//
// Exported so the board handler and, later, the card handler share exactly one
// definition of "which board is this and what may the caller do to it".
func (h *Handler) Resolve(r *http.Request) (*Access, error) { return h.resolve(r) }

func (h *Handler) resolve(r *http.Request) (*Access, error) {
	return h.svc.Resolve(r.Context(), authctx.User(r.Context()), chi.URLParam(r, "project"))
}

// ResolveRef resolves an explicit project reference rather than the URL parameter.
//
// Card and lane fragment routes carry no project in their path — they are addressed
// by the card or lane uuid alone — so their handlers look the project up first and
// resolve access against it here.
func (h *Handler) ResolveRef(r *http.Request, ref string) (*Access, error) {
	return h.svc.Resolve(r.Context(), authctx.User(r.Context()), ref)
}

// SettingsView builds the settings page's data. Exported so the board handler can
// re-render the page after a lane change fails.
func (h *Handler) SettingsView(r *http.Request, access *Access, saved string) (view.ProjectSettings, error) {
	return h.settingsView(r, access, saved)
}

func (h *Handler) settingsView(r *http.Request, access *Access, saved string) (view.ProjectSettings, error) {
	summary, err := h.summarise(r.Context(), access.Subject.User,
		Summary{Project: access.Project, Role: access.Role})
	if err != nil {
		return view.ProjectSettings{}, err
	}

	lanes, err := h.board.LaneSummaries(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return view.ProjectSettings{}, err
	}
	labels, err := h.board.LabelSummaries(r.Context(), access.Subject, access.Project.UUID)
	if err != nil {
		return view.ProjectSettings{}, err
	}

	members, err := h.svc.Members(r.Context(), access)
	if err != nil {
		return view.ProjectSettings{}, err
	}
	candidates, err := h.svc.Grantable(r.Context(), access)
	if err != nil {
		return view.ProjectSettings{}, err
	}

	self := authctx.User(r.Context())
	settings := view.ProjectSettings{
		Project:         summary,
		Lanes:           lanes,
		Labels:          labels,
		LabelForm:       view.LabelForm{Colours: h.board.Colours()},
		CanGrantManager: access.Subject.Can(authz.ActionMemberGrantManager),
		CanArchive:      access.Subject.Can(authz.ActionProjectArchive),
		CanDelete:       access.Subject.Can(authz.ActionProjectDelete),
		Saved:           saved,
		Form: view.ProjectForm{
			Name:        access.Project.Name,
			Slug:        access.Project.Slug,
			Description: access.Project.Description,
			Open:        true,
		},
	}
	for _, m := range members {
		settings.Members = append(settings.Members, view.Member{
			UserUUID:    m.User.UUID,
			DisplayName: m.User.DisplayName,
			Email:       m.User.Email,
			Role:        string(m.Membership.Role),
			Suspended:   !m.User.CanSignIn(),
			IsSelf:      self != nil && self.UUID == m.User.UUID,
		})
	}
	for _, c := range candidates {
		settings.Candidates = append(settings.Candidates, view.Candidate{
			UUID: c.UUID, DisplayName: c.DisplayName, Email: c.Email,
		})
	}
	return settings, nil
}

// summarise maps a project plus its board shape onto the list/settings view model.
//
// The subject is built here from the user and the grant the caller already holds,
// rather than resolved again from the URL: on the project list there is no project
// in the URL to resolve from.
func (h *Handler) summarise(
	ctx context.Context, user *authdomain.User, s Summary,
) (view.ProjectSummary, error) {
	lanes, err := h.board.LaneSummaries(ctx, authz.NewSubject(user, s.Role), s.Project.UUID)
	if err != nil {
		return view.ProjectSummary{}, err
	}

	cards := 0
	for _, l := range lanes {
		cards += len(l.Cards)
	}

	summary := view.ProjectSummary{
		UUID:        s.Project.UUID,
		Slug:        s.Project.Slug,
		Name:        s.Project.Name,
		Description: s.Project.Description,
		IsArchived:  s.Project.IsArchived(),
		Lanes:       len(lanes),
		Cards:       cards,
		UpdatedAt:   s.Project.UpdatedAt,
	}
	if s.Role != nil {
		summary.Role = string(*s.Role)
	}
	return summary, nil
}

// renderSettingsError re-renders the settings page with a failed details form.
func (h *Handler) renderSettingsError(
	w http.ResponseWriter, r *http.Request, access *Access, err error, in CreateInput,
) {
	settings, buildErr := h.settingsView(r, access, "")
	if buildErr != nil {
		h.fail(w, r, buildErr)
		return
	}
	settings.Form = projectFormFor(in, err)
	settings.Form.Open = true

	render.Page(w, r, apperrors.From(err).Status(),
		pages.ProjectSettingsPage(h.page(r, access.Project.Name+" settings"), settings))
}

// failOnSettings reports a membership failure on the settings page it came from,
// rather than replacing the whole page with a toast.
func (h *Handler) failOnSettings(w http.ResponseWriter, r *http.Request, access *Access, err error) {
	app := apperrors.From(err)
	if app.Status() >= 500 {
		h.fail(w, r, err)
		return
	}

	settings, buildErr := h.settingsView(r, access, "")
	if buildErr != nil {
		h.fail(w, r, buildErr)
		return
	}
	settings.Form.Error = app.Message

	render.Page(w, r, app.Status(),
		pages.ProjectSettingsPage(h.page(r, access.Project.Name+" settings"), settings))
}

// redirectToSettings follows a successful POST with a redirect, so a refresh does
// not resubmit it.
func (h *Handler) redirectToSettings(w http.ResponseWriter, r *http.Request, slug, saved string) {
	target := "/projects/" + slug + "/settings"
	if saved != "" {
		target += "?saved=" + urlQueryEscape(saved)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Page builds the chrome. Exported so the board handler renders the same one.
func (h *Handler) Page(r *http.Request, title string) view.Page { return h.page(r, title) }

func (h *Handler) page(r *http.Request, title string) view.Page {
	p := view.Page{
		Title:       title,
		CSRFToken:   authctx.CSRFToken(r.Context()),
		AssetSuffix: h.cfg.AssetSuffix,
	}
	if u := authctx.User(r.Context()); u != nil {
		p.CurrentUser = view.CurrentUser{
			UUID:        u.UUID,
			DisplayName: u.DisplayName,
			Email:       u.Email,
			IsSuperUser: u.IsSuperAdmin(),
		}
	}
	return p
}

// Fail is the single error path, shared with the board handler.
func (h *Handler) Fail(w http.ResponseWriter, r *http.Request, err error) { h.fail(w, r, err) }

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	app := apperrors.From(err)
	if app.Status() >= 500 {
		h.log.ErrorContext(r.Context(), "request failed",
			"path", r.URL.Path, "error", app.Err)
	}
	render.Page(w, r, app.Status(), components.Toast(app.Message, "error"))
}

// projectFormFor maps a failed submission back onto the form, field errors included.
func projectFormFor(in CreateInput, err error) view.ProjectForm {
	form := view.ProjectForm{
		Name:        in.Name,
		Slug:        in.Slug,
		Description: in.Description,
		Open:        true,
	}
	app := apperrors.From(err)

	for _, f := range app.Fields {
		switch f.Field {
		case "name":
			form.NameError = f.Message
		case "slug":
			form.SlugError = f.Message
		case "description":
			form.DescError = f.Message
		}
	}
	if form.NameError == "" && form.SlugError == "" && form.DescError == "" {
		form.Error = app.Message
	}
	return form
}

// isFormError reports whether a failure belongs on the form rather than on an error
// page: the person can fix it by typing something different.
func isFormError(err error) bool {
	switch apperrors.From(err).Code {
	case apperrors.CodeValidation, apperrors.CodeInvalid, apperrors.CodeConflict:
		return true
	}
	return false
}

// canCreateProject is the operator-only check the list page needs before it offers
// the button. The service refuses regardless; this only decides what is rendered.
func canCreateProject(user *authdomain.User) bool {
	return user != nil && user.CanSignIn() && user.IsSuperAdmin()
}

// urlQueryEscape is a minimal escape for the one confirmation message we put in a
// query string.
func urlQueryEscape(s string) string {
	return strings.NewReplacer(" ", "+", "&", "%26", "?", "%3F", "#", "%23").Replace(s)
}
