package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
	"github.com/AzmainMahtab/chonkboard/internal/shared/ratelimit"
	"github.com/AzmainMahtab/chonkboard/internal/shared/render"
	"github.com/AzmainMahtab/chonkboard/web/pages"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// Paths this slice owns. Named constants because the session middleware and the
// router both need to agree on them.
const (
	LoginPath    = "/login"
	LogoutPath   = "/logout"
	AccountPath  = "/account"
	PasswordPath = "/account/password"
)

// HandlerConfig is what the handler needs from application configuration.
type HandlerConfig struct {
	CookieSecure  bool
	SessionMaxAge int // seconds; matches SESSION_TTL
	AssetSuffix   string
}

// Handler serves the login page, sign-in, sign-out, and the account page.
type Handler struct {
	svc *Service
	// emailBucket is the per-account sign-in throttle, cleared on success.
	emailBucket *ratelimit.Limiter
	cfg         HandlerConfig
	log         *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(svc *Service, emailBucket *ratelimit.Limiter, cfg HandlerConfig, log *slog.Logger) *Handler {
	return &Handler{svc: svc, emailBucket: emailBucket, cfg: cfg, log: log}
}

// LoginPage renders the sign-in form.
//
// An already-signed-in visitor is sent to the board rather than shown the form
// again: arriving here with a live session usually means a stale bookmark.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	if _, _, err := h.svc.Authenticate(r.Context(), SessionCookieValue(r)); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// The login form's CSRF token cannot come from a session, because there is
	// none yet. It is a plain same-origin form with SameSite=Lax on nothing, so
	// the field is present for shape and the real protection for this one route
	// is the rate limiter plus the generic failure.
	render.Page(w, r, http.StatusOK, pages.LoginPage(h.page("Sign in", nil, ""), view.LoginForm{}))
}

// LogIn verifies a credential and starts a session.
func (h *Handler) LogIn(w http.ResponseWriter, r *http.Request) {
	// ParseForm is cached, so the rate-limit middleware having read it first is
	// fine.
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, r, http.StatusBadRequest, view.LoginForm{
			Error: "That form could not be read.",
		})
		return
	}

	email := r.PostForm.Get("email")
	result, err := h.svc.LogIn(r.Context(), LoginInput{
		Email:     email,
		Password:  r.PostForm.Get("password"),
		IP:        r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.renderLoginFailure(w, r, email, err)
		return
	}

	// A successful sign-in clears this account's throttle, so somebody who
	// eventually remembers their password is not still limited afterwards.
	//
	// Only the account's bucket, never the address's: clearing the IP bucket on
	// any success would let an attacker who holds one valid credential reset
	// their own throttle and carry on guessing at everybody else's.
	h.emailBucket.Reset(domain.NormaliseEmail(email))

	SetSessionCookie(w, result.Token, h.cfg.SessionMaxAge, h.cfg.CookieSecure)

	destination := "/"
	if result.User.MustChangePassword {
		destination = PasswordPath
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

// renderLoginFailure maps a failed sign-in onto the form.
func (h *Handler) renderLoginFailure(w http.ResponseWriter, r *http.Request, email string, err error) {
	form := view.LoginForm{Email: email}
	app := apperrors.From(err)

	switch {
	case errors.Is(err, ErrInvalidCredentials):
		// One message for a wrong address and a wrong password. Two would let
		// anyone test whether an address has an account here.
		form.Error = app.Message
	case errors.Is(err, ErrAccountSuspended):
		form.Error = app.Message
	default:
		h.log.ErrorContext(r.Context(), "sign-in failed", "error", app.Err)
		form.Error = "Something went wrong signing you in. Please try again."
	}

	h.renderLogin(w, r, app.Status(), form)
}

func (h *Handler) renderLogin(w http.ResponseWriter, r *http.Request, status int, form view.LoginForm) {
	render.Page(w, r, status, pages.LoginPage(h.page("Sign in", nil, ""), form))
}

// LogOut revokes the session and clears the cookie.
//
// It is a POST, so a link cannot sign somebody out, and it always succeeds from
// the browser's point of view: whatever the state of the session row, the cookie
// goes and the person ends up at the login page.
func (h *Handler) LogOut(w http.ResponseWriter, r *http.Request) {
	if session := authctx.Session(r.Context()); session != nil {
		if err := h.svc.LogOut(r.Context(), session.UUID); err != nil {
			h.log.ErrorContext(r.Context(), "cannot revoke session",
				"session", session.UUID, "error", err)
		}
	}
	ClearSessionCookie(w, h.cfg.CookieSecure)
	http.Redirect(w, r, LoginPath, http.StatusSeeOther)
}

// AccountPage renders the change-password form.
func (h *Handler) AccountPage(w http.ResponseWriter, r *http.Request) {
	user := authctx.User(r.Context())
	h.renderAccount(w, r, http.StatusOK, view.PasswordForm{
		MustChange: user.MustChangePassword,
	})
}

// ChangePassword replaces the signed-in user's password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user := authctx.User(r.Context())
	session := authctx.Session(r.Context())

	// Captured before the service runs: ChangeOwnPassword mutates the user in place,
	// so by the time it returns this is already false and there is no way to tell
	// whether the person came here by choice or was sent here by the gate.
	wasHeld := user.MustChangePassword

	if err := r.ParseForm(); err != nil {
		h.renderAccount(w, r, http.StatusBadRequest, view.PasswordForm{
			MustChange: wasHeld,
			Error:      "That form could not be read.",
		})
		return
	}

	err := h.svc.ChangeOwnPassword(
		r.Context(), user,
		r.PostForm.Get("current_password"),
		r.PostForm.Get("new_password"),
		session.UUID,
	)
	if err != nil {
		h.renderAccount(w, r, apperrors.From(err).Status(),
			passwordFormFor(wasHeld, err))
		return
	}

	// Somebody who was *held* here is let through: RequirePasswordChange put them on
	// this page and they have now done the one thing it was waiting for, so leaving
	// them looking at the form reads as though it did not work.
	if wasHeld {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Somebody who came here by choice stays, and sees that it worked. Rendered
	// rather than redirected so the confirmation survives; the user object was
	// mutated by the service, so the forced-change banner is already gone.
	h.renderAccount(w, r, http.StatusOK, view.PasswordForm{Done: true})
}

// passwordFormFor maps a change-password failure onto the form's fields.
func passwordFormFor(mustChange bool, err error) view.PasswordForm {
	form := view.PasswordForm{MustChange: mustChange}
	app := apperrors.From(err)

	for _, field := range app.Fields {
		switch field.Field {
		case "current_password":
			form.CurrentError = field.Message
		case "password":
			form.NewError = field.Message
		}
	}
	// Only surface the headline when nothing landed on a field, so the message
	// is not said twice.
	if form.CurrentError == "" && form.NewError == "" {
		form.Error = app.Message
	}
	return form
}

func (h *Handler) renderAccount(w http.ResponseWriter, r *http.Request, status int, form view.PasswordForm) {
	user := authctx.User(r.Context())
	render.Page(w, r, status,
		pages.AccountPage(h.page("Your account", user, authctx.CSRFToken(r.Context())), form))
}

// page builds the chrome. The CSRF token comes from the session, so it is
// per-session and reaches the browser only in a server-rendered response.
func (h *Handler) page(title string, user *domain.User, csrfToken string) view.Page {
	p := view.Page{
		Title:       title,
		CSRFToken:   csrfToken,
		AssetSuffix: h.cfg.AssetSuffix,
	}
	if user != nil {
		p.CurrentUser = view.CurrentUser{
			UUID:        user.UUID,
			DisplayName: user.DisplayName,
			Email:       user.Email,
			IsSuperUser: user.IsSuperAdmin(),
		}
	}
	return p
}
