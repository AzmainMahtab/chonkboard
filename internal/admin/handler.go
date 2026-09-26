package admin

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
	"github.com/AzmainMahtab/chonkboard/internal/shared/render"
	"github.com/AzmainMahtab/chonkboard/web/components"
	"github.com/AzmainMahtab/chonkboard/web/pages"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// Paths this slice owns.
const (
	BasePath     = "/admin"
	UsersPath    = "/admin/users"
	ProjectsPath = "/admin/projects"
)

// HandlerConfig is what the handler needs from application configuration.
type HandlerConfig struct {
	AssetSuffix string
}

// Handler serves the operator's console.
type Handler struct {
	svc *Service
	cfg HandlerConfig
	log *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(svc *Service, cfg HandlerConfig, log *slog.Logger) *Handler {
	return &Handler{svc: svc, cfg: cfg, log: log}
}

// Index sends /admin to the account list, which is what the operator came for.
//
// It checks first rather than letting the redirect land on a route that refuses. A bare
// redirect confirms that /admin exists, which is the one thing the 404-not-403 policy on
// this whole surface is for.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Dashboard(r.Context(), h.actor(r), false); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, UsersPath, http.StatusFound)
}

// Users lists every account.
func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	h.renderUsers(w, r, http.StatusOK, view.AccountForm{}, nil, r.URL.Query().Get("saved"))
}

// EditUser renders the account list with one account's details in the form.
func (h *Handler) EditUser(w http.ResponseWriter, r *http.Request) {
	user, err := h.svc.Account(r.Context(), h.actor(r), chi.URLParam(r, "user"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	h.renderUsers(w, r, http.StatusOK, view.AccountForm{
		UUID:        user.UUID,
		DisplayName: user.DisplayName,
		Email:       user.Email,
		IsOperator:  user.IsSuperAdmin(),
	}, nil, "")
}

// CreateUser makes an account and reveals its password once.
//
// The credential is rendered into *this* response and nowhere else. No redirect, because
// a redirect would have to carry the password in a query string or a flash — both of
// which persist it somewhere, which is the one thing that must not happen.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	form, in, err := accountInputFrom(r)
	if err != nil {
		h.renderUsers(w, r, apperrors.From(err).Status(), form, nil, "")
		return
	}

	credential, err := h.svc.CreateAccount(r.Context(), h.actor(r), auth.CreateUserInput{
		Email:        in.Email,
		DisplayName:  in.DisplayName,
		MakeOperator: in.IsOperator,
	})
	if err != nil {
		h.renderUsers(w, r, apperrors.From(err).Status(), formWithError(form, err), nil, "")
		return
	}

	h.renderUsers(w, r, http.StatusOK, view.AccountForm{}, &view.Reveal{
		Email:    credential.User.Email,
		Password: credential.Password,
	}, "")
}

// UpdateUser saves an account's name, address and global role.
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	form, in, err := accountInputFrom(r)
	form.UUID = chi.URLParam(r, "user")
	if err != nil {
		h.renderUsers(w, r, apperrors.From(err).Status(), form, nil, "")
		return
	}

	if _, err := h.svc.EditAccount(r.Context(), h.actor(r), form.UUID, in); err != nil {
		h.renderUsers(w, r, apperrors.From(err).Status(), formWithError(form, err), nil, "")
		return
	}
	h.redirect(w, r, UsersPath, "Account saved.")
}

// ResetPassword generates a new one-time password and ends every session that account
// holds.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	credential, err := h.svc.ResetPassword(r.Context(), h.actor(r), chi.URLParam(r, "user"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Rendered here for the same reason a create is: a redirect cannot carry this.
	h.renderUsers(w, r, http.StatusOK, view.AccountForm{}, &view.Reveal{
		Email:    credential.User.Email,
		Password: credential.Password,
		Reset:    true,
	}, "")
}

// Suspend locks an account out immediately.
func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	h.setSuspended(w, r, true, "Account suspended, and signed out everywhere.")
}

// Reinstate lets a suspended account sign in again.
func (h *Handler) Reinstate(w http.ResponseWriter, r *http.Request) {
	h.setSuspended(w, r, false, "Account reinstated.")
}

func (h *Handler) setSuspended(w http.ResponseWriter, r *http.Request, suspended bool, saved string) {
	if _, err := h.svc.SetSuspended(
		r.Context(), h.actor(r), chi.URLParam(r, "user"), suspended,
	); err != nil {
		// A refusal here is a conflict the operator needs to read — the last owner,
		// or their own account — so it belongs on the page rather than on an error
		// screen.
		if apperrors.From(err).Code == apperrors.CodeConflict {
			h.renderUsers(w, r, http.StatusConflict,
				view.AccountForm{Error: apperrors.From(err).Message}, nil, "")
			return
		}
		h.fail(w, r, err)
		return
	}
	h.redirect(w, r, UsersPath, saved)
}

// SignOut ends every session an account holds without changing its password.
func (h *Handler) SignOut(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.SignOutEverywhere(r.Context(), h.actor(r), chi.URLParam(r, "user"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.redirect(w, r, UsersPath, pluralisedSignOut(n))
}

// Projects lists every board and who can reach it.
func (h *Handler) Projects(w http.ResponseWriter, r *http.Request) {
	showArchived := r.URL.Query().Get("archived") == "1"

	overview, err := h.svc.Dashboard(r.Context(), h.actor(r), showArchived)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	page := view.AdminPage{
		Operators:    overview.Operators,
		ShowArchived: showArchived,
		Saved:        r.URL.Query().Get("saved"),
	}
	for _, p := range overview.Projects {
		row := view.AdminProject{
			UUID: p.UUID, Slug: p.Slug, Name: p.Name,
			IsArchived: p.IsArchived, Members: p.Members, UpdatedAt: p.UpdatedAt,
		}
		for _, m := range overview.Access[p.UUID] {
			row.Access = append(row.Access, view.Member{
				UserUUID: m.UserUUID, DisplayName: m.DisplayName, Email: m.Email,
				Role: string(m.Role), Suspended: m.Suspended,
			})
		}
		page.Projects = append(page.Projects, row)
	}

	render.Page(w, r, http.StatusOK,
		pages.AdminProjectsPage(h.page(r, "Projects"), page))
}

// renderUsers builds and renders the account list.
//
// Every write path comes back through here rather than redirecting, because two of them
// carry a one-time password that must not survive the response.
func (h *Handler) renderUsers(
	w http.ResponseWriter, r *http.Request, status int,
	form view.AccountForm, reveal *view.Reveal, saved string,
) {
	overview, err := h.svc.Dashboard(r.Context(), h.actor(r), false)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	page := view.AdminPage{
		Operators: overview.Operators,
		Reveal:    reveal,
		Form:      form,
		Saved:     saved,
	}
	for _, a := range overview.Accounts {
		page.Accounts = append(page.Accounts, view.Account{
			UUID:               a.User.UUID,
			DisplayName:        a.User.DisplayName,
			Email:              a.User.Email,
			IsOperator:         a.User.IsSuperAdmin(),
			Suspended:          !a.User.CanSignIn(),
			MustChangePassword: a.User.MustChangePassword,
			Sessions:           a.Sessions,
			Projects:           a.Projects,
			IsSelf:             a.IsSelf,
			CreatedAt:          a.User.CreatedAt,
		})
	}

	render.Page(w, r, status, pages.AdminUsersPage(h.page(r, "Accounts"), page))
}

// accountInputFrom parses the account form.
func accountInputFrom(r *http.Request) (view.AccountForm, auth.UpdateUserInput, error) {
	if err := r.ParseForm(); err != nil {
		return view.AccountForm{}, auth.UpdateUserInput{},
			apperrors.Invalid("That form could not be read.").Wrap(err)
	}

	form := view.AccountForm{
		DisplayName: strings.TrimSpace(r.PostForm.Get("display_name")),
		Email:       strings.TrimSpace(r.PostForm.Get("email")),
		IsOperator:  r.PostForm.Get("is_operator") != "",
	}
	return form, auth.UpdateUserInput{
		DisplayName: form.DisplayName,
		Email:       form.Email,
		IsOperator:  form.IsOperator,
	}, nil
}

// formWithError puts a failure's field messages onto the form.
func formWithError(form view.AccountForm, err error) view.AccountForm {
	app := apperrors.From(err)
	for _, f := range app.Fields {
		switch f.Field {
		case "display_name":
			form.NameError = f.Message
		case "email":
			form.EmailError = f.Message
		}
	}
	if form.NameError == "" && form.EmailError == "" {
		form.Error = app.Message
	}
	return form
}

// pluralisedSignOut words the confirmation honestly, including when there was nothing to
// sign out.
func pluralisedSignOut(n int64) string {
	switch n {
	case 0:
		return "That account had no live sessions."
	case 1:
		return "Signed out of one browser."
	default:
		return "Signed out everywhere."
	}
}

// redirect follows a successful POST with a redirect, so a refresh does not resubmit it.
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, path, saved string) {
	if saved != "" {
		path += "?saved=" + strings.ReplaceAll(saved, " ", "+")
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (h *Handler) actor(r *http.Request) *authdomain.User { return authctx.User(r.Context()) }

func (h *Handler) page(r *http.Request, title string) view.Page {
	p := view.Page{
		Title:       title,
		CSRFToken:   authctx.CSRFToken(r.Context()),
		AssetSuffix: h.cfg.AssetSuffix,
	}
	if u := authctx.User(r.Context()); u != nil {
		p.CurrentUser = view.CurrentUser{
			UUID: u.UUID, DisplayName: u.DisplayName,
			Email: u.Email, IsSuperUser: u.IsSuperAdmin(),
		}
	}
	return p
}

// fail is the single error path.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	app := apperrors.From(err)
	if app.Status() >= 500 {
		h.log.ErrorContext(r.Context(), "admin request failed",
			"path", r.URL.Path, "error", app.Err)
	}
	render.Page(w, r, app.Status(), components.Toast(app.Message, "error"))
}
