package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/AzmainMahtab/chonkboard/internal/admin"
	"github.com/AzmainMahtab/chonkboard/internal/auth"
	"github.com/AzmainMahtab/chonkboard/internal/board"
	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/platform/middleware"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/ratelimit"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"

	"github.com/AzmainMahtab/chonkboard/web"
)

type Deps struct {
	Log         *slog.Logger
	Hub         *sse.Hub
	AssetSuffix string

	// Projects and Board own the board's shape; Cards own what is on it; Auth owns
	// who is asking.
	Projects *project.Handler
	BoardMgr *board.Handler
	Cards    *card.Handler
	Admin    *admin.Handler

	// Auth owns the session, so it owns everything about who is asking.
	Auth       *auth.Handler
	Session    middleware.Authenticator
	SessionCfg middleware.SessionConfig
	// LoginByIP is wide (an address is shared); LoginByEmail is tight (an
	// address is one person, and is what bounds a guessing attack).
	LoginByIP    *ratelimit.Limiter
	LoginByEmail *ratelimit.Limiter

	// MaxRequestBytes caps every request body. It has to exceed UPLOAD_MAX_BYTES by
	// enough for the multipart envelope, or a file at exactly the upload ceiling
	// would be refused by this instead.
	MaxRequestBytes int64
}

func Router(d Deps) http.Handler {
	// A missing handler is a wiring mistake, and it has to fail here rather than
	// on the first request. A method value on a nil pointer is legal Go, so the
	// panic would otherwise land inside a request as a 500 with a stack trace —
	// which is exactly how this was found.
	mustHaveDeps(d)

	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)
	r.Use(middleware.RequestLogger(d.Log))
	r.Use(chimw.Recoverer)
	r.Use(middleware.SecurityHeaders)
	// Before anything parses a body. The CSRF check has to read a multipart form to
	// find its token, so the ceiling has to exist by then.
	r.Use(middleware.LimitBody(d.MaxRequestBytes))

	// ---- Public: no session required, and deliberately not wrapped in the
	// session middleware, so these cannot come to require the thing they exist
	// to provide.
	r.Handle("/static/*", web.StaticHandler("/static"))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get(auth.LoginPath, d.Auth.LoginPage)
	r.With(middleware.LoginRateLimit(d.LoginByIP, d.LoginByEmail, d.Log)).
		Post(auth.LoginPath, d.Auth.LogIn)

	// ---- Signed in. Every route below this point has a user on the context.
	r.Group(func(ar chi.Router) {
		ar.Use(middleware.RequireSession(d.Session, d.SessionCfg, d.Log))
		ar.Use(middleware.VerifyCSRF(d.Log))

		// Sign-out sits above the forced-password-change gate so a user held on
		// that page can still leave.
		ar.Post(auth.LogoutPath, d.Auth.LogOut)

		ar.Group(func(pr chi.Router) {
			pr.Use(middleware.RequirePasswordChange(d.SessionCfg))

			// Operator-only observability. Not on /healthz: room counts say how
			// many projects are in use and who is watching them.
			pr.Get("/debug/live", liveStatus(d.Hub))

			// ---- The operator's console. Every handler refuses anybody else with
			// 404 rather than 403: a member should not learn this surface exists.
			pr.Route(admin.BasePath, func(ar chi.Router) {
				ar.Get("/", d.Admin.Index)
				ar.Get("/users", d.Admin.Users)
				ar.Post("/users", d.Admin.CreateUser)
				ar.Get("/users/{user}/edit", d.Admin.EditUser)
				ar.Post("/users/{user}", d.Admin.UpdateUser)
				ar.Post("/users/{user}/password", d.Admin.ResetPassword)
				ar.Post("/users/{user}/suspend", d.Admin.Suspend)
				ar.Post("/users/{user}/reinstate", d.Admin.Reinstate)
				ar.Post("/users/{user}/sign-out", d.Admin.SignOut)
				ar.Get("/projects", d.Admin.Projects)
			})

			pr.Get(auth.AccountPath, d.Auth.AccountPage)
			pr.Get(auth.PasswordPath, d.Auth.AccountPage)
			pr.Post(auth.PasswordPath, d.Auth.ChangePassword)

			// ---- The project list is the application's home.
			pr.Get("/", d.Projects.List)
			pr.Get("/projects/new", d.Projects.NewProject)
			pr.Post("/projects", d.Projects.Create)

			pr.Route("/projects/{project}", func(br chi.Router) {
				// Readable by any granted member.
				br.Get("/", d.Cards.BoardPage)
				br.Get("/board", d.Cards.BoardFragment)
				br.Get("/events", d.Cards.Events)

				// Cards: what a member is here to do.
				br.Get("/lanes/{lane}/cards/new", d.Cards.NewCard)
				br.Post("/lanes/{lane}/cards", d.Cards.Create)

				// Everything below changes the shape of the board. Each handler
				// resolves access and refuses a member with 403, and the service
				// behind it checks again — so a route added here without the
				// check still cannot let a member through.
				br.Get("/settings", d.Projects.Settings)
				br.Post("/settings", d.Projects.Update)
				br.Post("/archive", d.Projects.Archive)
				br.Post("/delete", d.Projects.Delete)

				br.Post("/members", d.Projects.AddMember)
				br.Post("/members/{user}", d.Projects.SetMemberRole)
				br.Post("/members/{user}/revoke", d.Projects.RemoveMember)

				br.Get("/lanes/new", d.BoardMgr.NewLane)
				br.Post("/lanes", d.BoardMgr.CreateLane)
				br.Post("/lanes/reorder", d.BoardMgr.ReorderLanes)
				br.Get("/lanes/{lane}/edit", d.BoardMgr.EditLane)
				br.Post("/lanes/{lane}", d.BoardMgr.UpdateLane)
				br.Post("/lanes/{lane}/move", d.BoardMgr.MoveLane)
				br.Get("/lanes/{lane}/delete", d.BoardMgr.ConfirmDeleteLane)
				br.Post("/lanes/{lane}/delete", d.BoardMgr.DeleteLane)

				// Labels: a manager defines them, and any member attaches them.
				br.Post("/labels", d.BoardMgr.CreateLabel)
				br.Get("/labels/{label}/edit", d.BoardMgr.EditLabel)
				br.Post("/labels/{label}", d.BoardMgr.UpdateLabel)
				br.Post("/labels/{label}/delete", d.BoardMgr.DeleteLabel)
			})

			// Addressed by uuid alone, because these appear in the DOM on every
			// card and in the move request board.js builds. Each resolves the
			// project from the card or lane and then scopes to it, so an
			// ungranted one is a 404.
			pr.Get("/lanes/{lane}/fragment", d.Cards.LaneFragment)
			pr.Get("/cards/{card}", d.Cards.Detail)
			pr.Get("/cards/{card}/edit", d.Cards.EditForm)
			pr.Post("/cards/{card}", d.Cards.Update)
			pr.Post("/cards/{card}/archive", d.Cards.SetArchived)
			pr.Post("/cards/{card}/delete", d.Cards.Delete)
			pr.Post("/cards/{card}/move", d.Cards.Move)
			pr.Post("/cards/{card}/move-to", d.Cards.MoveToLane)

			// The rich card. Comments and attachments are addressed by their own
			// uuid for the same reason cards are, and resolve their project the
			// same way.
			pr.Post("/cards/{card}/comments", d.Cards.Comment)
			pr.Post("/comments/{comment}/delete", d.Cards.DeleteComment)
			pr.Post("/cards/{card}/attachments", d.Cards.Attach)
			pr.Get("/attachments/{attachment}", d.Cards.ServeAttachment)
			pr.Post("/attachments/{attachment}/delete", d.Cards.DeleteAttachment)
		})
	})

	return r
}

// mustHaveDeps panics at construction on an unwired dependency.
func mustHaveDeps(d Deps) {
	missing := map[string]bool{
		"Log":          d.Log == nil,
		"Hub":          d.Hub == nil,
		"Auth":         d.Auth == nil,
		"Projects":     d.Projects == nil,
		"BoardMgr":     d.BoardMgr == nil,
		"Cards":        d.Cards == nil,
		"Admin":        d.Admin == nil,
		"Session":      d.Session == nil,
		"LoginByIP":    d.LoginByIP == nil,
		"LoginByEmail": d.LoginByEmail == nil,
	}
	for name, absent := range missing {
		if absent {
			panic("httpserver: Deps." + name + " is required")
		}
	}
}
