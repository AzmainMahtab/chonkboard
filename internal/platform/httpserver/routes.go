package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

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

	// Auth owns the session, so it owns everything about who is asking.
	Auth       *auth.Handler
	Session    middleware.Authenticator
	SessionCfg middleware.SessionConfig
	// LoginByIP is wide (an address is shared); LoginByEmail is tight (an
	// address is one person, and is what bounds a guessing attack).
	LoginByIP    *ratelimit.Limiter
	LoginByEmail *ratelimit.Limiter
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
