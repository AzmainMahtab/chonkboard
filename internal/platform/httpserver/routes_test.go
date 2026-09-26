package httpserver_test

import (
	"bufio"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/admin"
	"github.com/AzmainMahtab/chonkboard/internal/auth"
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/board"
	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/platform/filestore"
	"github.com/AzmainMahtab/chonkboard/internal/platform/httpserver"
	"github.com/AzmainMahtab/chonkboard/internal/platform/middleware"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	projectdomain "github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
	"github.com/AzmainMahtab/chonkboard/internal/shared/password"
	"github.com/AzmainMahtab/chonkboard/internal/shared/ratelimit"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"
)

// These tests drive the assembled router over real HTTP, which is the only place
// the route boundary can actually be checked. The test plan says to verify it with
// curl; this is that, automated.

const (
	testPassword = "a good long password"
	// Mirrors the split in cmd/chonkboard: an address is shared and gets a wide
	// allowance, an account is one person and gets a tight one.
	loginBurstPerIP    = 30
	loginBurstPerEmail = 5
)

type harness struct {
	router         http.Handler
	store          *auth.Store
	service        *auth.Service
	projectService *project.Service
	boardService   *board.Service
	cardService    *card.Service
	hub            *sse.Hub
	loginByIP      *ratelimit.Limiter
	loginByEmail   *ratelimit.Limiter
	ctx            context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	_, tx := dbtest.NewWithTx(t)
	log := dbtest.Discard()

	store := auth.NewStore(tx)
	service, err := auth.NewService(store, tx, password.NewFake(),
		auth.ServiceConfig{SessionTTL: 24 * time.Hour}, log)
	require.NoError(t, err)

	boardService := board.NewService(board.NewStore(tx), tx, log)
	projectService := project.NewService(project.NewStore(tx), service, boardService, tx, log)

	hub := sse.NewHub(log)
	projectHandler := project.NewHandler(projectService, nil, hub,
		project.HandlerConfig{AssetSuffix: "?v=test"}, log)
	boardHandler := board.NewHandler(boardService, projectHandler, log)
	projectHandler.UseBoardShape(boardHandler)

	uploads, err := filestore.NewDisk(filepath.Join(t.TempDir(), "uploads"))
	require.NoError(t, err)
	cardService := card.NewService(card.NewStore(tx), boardService, uploads,
		card.ServiceConfig{MaxUploadBytes: 64 << 10}, tx, log)
	cardHandler := card.NewHandler(cardService, projectHandler, boardService,
		service, projectService, hub, log)
	cardService.UseMembers(projectService)

	adminHandler := admin.NewHandler(
		admin.NewService(service, projectService, log),
		admin.HandlerConfig{AssetSuffix: "?v=test"}, log)

	loginByIP := ratelimit.New(loginBurstPerIP, time.Minute)
	loginByEmail := ratelimit.New(loginBurstPerEmail, time.Minute)
	sessionCfg := middleware.SessionConfig{
		Secure:       false,
		LoginPath:    auth.LoginPath,
		PasswordPath: auth.PasswordPath,
		LogoutPath:   auth.LogoutPath,
	}

	router := httpserver.Router(httpserver.Deps{
		Log:         log,
		Hub:         hub,
		AssetSuffix: "?v=test",
		Auth: auth.NewHandler(service, loginByEmail, auth.HandlerConfig{
			CookieSecure:  false,
			SessionMaxAge: int((24 * time.Hour).Seconds()),
			AssetSuffix:   "?v=test",
		}, log),
		Projects:        projectHandler,
		BoardMgr:        boardHandler,
		Cards:           cardHandler,
		Admin:           adminHandler,
		Session:         service,
		SessionCfg:      sessionCfg,
		LoginByIP:       loginByIP,
		LoginByEmail:    loginByEmail,
		MaxRequestBytes: (64 << 10) + (1 << 20),
	})

	return &harness{
		router: router, store: store, service: service,
		projectService: projectService, boardService: boardService,
		cardService: cardService, hub: hub,
		loginByIP: loginByIP, loginByEmail: loginByEmail,
		ctx: context.Background(),
	}
}

func (h *harness) addUser(t *testing.T, email string, role authdomain.Role) *authdomain.User {
	t.Helper()
	hash, err := password.NewFake().Hash(testPassword)
	require.NoError(t, err)

	u, err := authdomain.NewUser(idgenerator.NewUUIDv7(), email, "Test Person",
		hash, role, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.store.CreateUser(h.ctx, u))
	return u
}

// addSuperAdmin creates the operator's account.
func (h *harness) addSuperAdmin(t *testing.T, email string) *authdomain.User {
	t.Helper()
	return h.addUser(t, email, authdomain.RoleSuperAdmin)
}

func (h *harness) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, r)
	return rec
}

func (h *harness) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return h.do(r)
}

func (h *harness) postForm(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return h.do(r)
}

// signIn logs a user in over HTTP and returns the session cookie plus the CSRF
// token the pages will carry.
func (h *harness) signIn(t *testing.T, email string) (*http.Cookie, string) {
	t.Helper()
	rec := h.postForm(auth.LoginPath, url.Values{
		"email":    {email},
		"password": {testPassword},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, "sign-in should redirect")

	cookie := sessionCookie(rec)
	require.NotNil(t, cookie, "sign-in should set a session cookie")

	_, session, err := h.service.Authenticate(h.ctx, cookie.Value)
	require.NoError(t, err)
	return cookie, session.CSRFToken
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

// bodyOf is body under another name, for the one place a local variable called body
// shadows it.
func bodyOf(rec *httptest.ResponseRecorder) string { return body(rec) }

// body returns the response body.
//
// Reads rec.Body directly rather than rec.Result().Body: the latter is a reader that is
// consumed, so calling this twice on one recorder returned "" the second time — which
// makes an assertion pass or fail depending on how many times the test happened to look.
func body(rec *httptest.ResponseRecorder) string { return rec.Body.String() }

func TestPublicRoutesNeedNoSession(t *testing.T) {
	// These are deliberately outside the session middleware, so they cannot come
	// to require the thing they exist to provide.
	h := newHarness(t)

	assert.Equal(t, http.StatusOK, h.get("/healthz", nil).Code)
	assert.Equal(t, http.StatusOK, h.get(auth.LoginPath, nil).Code)
	assert.Equal(t, http.StatusOK, h.get("/static/js/board.js", nil).Code)
}

func TestTheLoginPageRendersAForm(t *testing.T) {
	h := newHarness(t)
	rec := h.get(auth.LoginPath, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	page := body(rec)
	assert.Contains(t, page, `action="/login"`)
	assert.Contains(t, page, `name="email"`)
	assert.Contains(t, page, `name="password"`)
	assert.Contains(t, page, `autocomplete="current-password"`)
	assert.NotContains(t, page, "board.js",
		"the login page must not ship the drag-and-drop bundle")
}

func TestAnUnauthenticatedRequestIsSentToLogin(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/projects/demo", auth.AccountPath, auth.PasswordPath} {
		t.Run(path, func(t *testing.T) {
			rec := h.get(path, nil)
			assert.Equal(t, http.StatusSeeOther, rec.Code)
			assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
		})
	}
}

func TestAnUnauthenticatedHTMXRequestGetsHXRedirect(t *testing.T) {
	// A 303 to an HTMX request would be followed by the XMLHttpRequest and the
	// whole login page swapped into whatever element asked, so the board would
	// appear to dissolve into a login form.
	h := newHarness(t)

	r := httptest.NewRequest(http.MethodGet, "/projects/demo/board", nil)
	r.Header.Set("HX-Request", "true")
	rec := h.do(r)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, auth.LoginPath, rec.Header().Get("HX-Redirect"))
	assert.Empty(t, body(rec))
}

func TestSignInSetsAHardenedCookie(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)

	rec := h.postForm(auth.LoginPath, url.Values{
		"email": {"person@example.com"}, "password": {testPassword},
	}, nil)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))

	cookie := sessionCookie(rec)
	require.NotNil(t, cookie)
	assert.True(t, cookie.HttpOnly, "an injected script must not be able to read it")
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)
	assert.False(t, cookie.Secure, "off for plain HTTP locally; config drives this")
	assert.Equal(t, int((24 * time.Hour).Seconds()), cookie.MaxAge)
	assert.NotContains(t, cookie.Value, "@", "the cookie is a token, not an identifier")
}

func TestSignInFailureIsGenericAndSetsNoCookie(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "known@example.com", authdomain.RoleMember)

	// A slice, not a map: the comparison below depends on which response is which,
	// and map iteration order is random. As a map this passed or failed by luck.
	tests := []struct {
		name  string
		email string
		form  url.Values
	}{
		{
			name:  "wrong password",
			email: "known@example.com",
			form:  url.Values{"email": {"known@example.com"}, "password": {"wrong password here"}},
		},
		{
			name:  "unknown address",
			email: "nobody@example.com",
			form:  url.Values{"email": {"nobody@example.com"}, "password": {testPassword}},
		},
	}

	normalised := make([]string, 0, len(tests))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.postForm(auth.LoginPath, tc.form, nil)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Nil(t, sessionCookie(rec), "no cookie on a failed sign-in")

			page := body(rec)
			assert.Contains(t, page, "That email or password is wrong.")
			// Each response is normalised against its own address, so the two are
			// comparable whatever they were.
			normalised = append(normalised, strings.ReplaceAll(page, tc.email, "<ADDRESS>"))
		})
	}

	require.Len(t, normalised, 2)
	// Account enumeration: the two responses must be indistinguishable apart from
	// the address echoed back into the form.
	assert.Equal(t, normalised[0], normalised[1])
}

func TestASignedInUserReachesTheProjectList(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, csrf := h.signIn(t, "person@example.com")

	rec := h.get("/", cookie)
	require.Equal(t, http.StatusOK, rec.Code)

	page := body(rec)
	assert.Contains(t, page, "Projects")
	assert.Contains(t, page, csrf, "the page carries the session's CSRF token")
	assert.Contains(t, page, "Test Person", "and the signed-in user's name")
	assert.Contains(t, page, "board.js")
}

func TestTheLoginPageRedirectsSomebodyAlreadySignedIn(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, _ := h.signIn(t, "person@example.com")

	rec := h.get(auth.LoginPath, cookie)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
}

func TestAStateChangingRequestWithoutTheCSRFTokenIsRefused(t *testing.T) {
	// Cookie auth plus POSTs is exactly the shape SameSite=Lax does not fully
	// cover, so this check is not optional.
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, csrf := h.signIn(t, "person@example.com")

	tests := []struct {
		name string
		form url.Values
		hdr  string
	}{
		{name: "no token at all", form: url.Values{}},
		{name: "an empty token", form: url.Values{"csrf_token": {""}}},
		{name: "somebody else's token", form: url.Values{"csrf_token": {"forged-token-value"}}},
		{name: "a wrong header", hdr: "forged-token-value"},
		{name: "a truncated token", form: url.Values{"csrf_token": {csrf[:len(csrf)-1]}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := tc.form
			if form == nil {
				form = url.Values{}
			}
			r := httptest.NewRequest(http.MethodPost, auth.PasswordPath,
				strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.hdr != "" {
				r.Header.Set(middleware.CSRFHeader, tc.hdr)
			}
			r.AddCookie(cookie)

			assert.Equal(t, http.StatusForbidden, h.do(r).Code)
		})
	}
}

func TestTheCSRFTokenIsAcceptedFromEitherTheHeaderOrTheForm(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)

	t.Run("form field, for a plain form with no JavaScript", func(t *testing.T) {
		cookie, csrf := h.signIn(t, "person@example.com")
		rec := h.postForm(auth.PasswordPath, url.Values{
			"csrf_token":       {csrf},
			"current_password": {testPassword},
			"new_password":     {"a replacement password"},
		}, cookie)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body(rec), "Password changed")
	})

	t.Run("header, which is how HTMX sends it", func(t *testing.T) {
		h := newHarness(t)
		h.addUser(t, "other@example.com", authdomain.RoleMember)
		cookie, csrf := h.signIn(t, "other@example.com")

		form := url.Values{
			"current_password": {testPassword},
			"new_password":     {"a replacement password"},
		}
		r := httptest.NewRequest(http.MethodPost, auth.PasswordPath,
			strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set(middleware.CSRFHeader, csrf)
		r.AddCookie(cookie)

		assert.Equal(t, http.StatusOK, h.do(r).Code)
	})
}

func TestSafeMethodsAreExemptFromTheCSRFCheck(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, _ := h.signIn(t, "person@example.com")

	assert.Equal(t, http.StatusOK, h.get(auth.AccountPath, cookie).Code)
	assert.Equal(t, http.StatusOK, h.get("/", cookie).Code)
}

func TestAForcedPasswordChangeHoldsTheUserOnThatPage(t *testing.T) {
	// This is what makes credential handover safe: a password the operator chose
	// cannot remain in use.
	h := newHarness(t)
	user := h.addUser(t, "person@example.com", authdomain.RoleMember)
	user.MustChangePassword = true
	require.NoError(t, h.store.UpdateUser(h.ctx, user))

	rec := h.postForm(auth.LoginPath, url.Values{
		"email": {"person@example.com"}, "password": {testPassword},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, auth.PasswordPath, rec.Header().Get("Location"),
		"sign-in sends them straight there")

	cookie := sessionCookie(rec)
	require.NotNil(t, cookie)

	for _, path := range []string{"/", auth.AccountPath} {
		t.Run("blocked: "+path, func(t *testing.T) {
			rec := h.get(path, cookie)
			assert.Equal(t, http.StatusSeeOther, rec.Code)
			assert.Equal(t, auth.PasswordPath, rec.Header().Get("Location"))
		})
	}

	t.Run("the password page itself is reachable", func(t *testing.T) {
		rec := h.get(auth.PasswordPath, cookie)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body(rec), "Choose your own password")
	})

	t.Run("and so is signing out, or it is a loop with no way out", func(t *testing.T) {
		_, csrf := func() (*http.Cookie, string) {
			_, session, err := h.service.Authenticate(h.ctx, cookie.Value)
			require.NoError(t, err)
			return cookie, session.CSRFToken
		}()
		rec := h.postForm(auth.LogoutPath, url.Values{"csrf_token": {csrf}}, cookie)
		assert.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
	})
}

func TestChangingThePasswordLiftsTheGate(t *testing.T) {
	h := newHarness(t)
	user := h.addUser(t, "person@example.com", authdomain.RoleMember)
	user.MustChangePassword = true
	require.NoError(t, h.store.UpdateUser(h.ctx, user))
	cookie, csrf := h.signIn(t, "person@example.com")

	rec := h.postForm(auth.PasswordPath, url.Values{
		"csrf_token":       {csrf},
		"current_password": {testPassword},
		"new_password":     {"a replacement password"},
	}, cookie)

	// Somebody who was held here is let through rather than left looking at the form,
	// which reads as though nothing happened.
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))

	assert.Equal(t, http.StatusOK, h.get("/", cookie).Code,
		"the application is reachable now")
}

func TestSignOutRevokesTheSessionAndClearsTheCookie(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, csrf := h.signIn(t, "person@example.com")

	rec := h.postForm(auth.LogoutPath, url.Values{"csrf_token": {csrf}}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))

	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cleared = c
		}
	}
	require.NotNil(t, cleared)
	assert.Empty(t, cleared.Value)
	assert.Negative(t, cleared.MaxAge)
	assert.True(t, cleared.HttpOnly, "attributes must match, or some browsers keep the original")

	// The cookie value is dead even if somebody kept a copy of it.
	assert.Equal(t, http.StatusSeeOther, h.get("/", cookie).Code)
}

func TestARevokedSessionGetsItsCookieCleared(t *testing.T) {
	h := newHarness(t)
	user := h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, _ := h.signIn(t, "person@example.com")

	_, err := h.store.RevokeSessionsForUser(h.ctx, user.UUID, time.Now())
	require.NoError(t, err)

	rec := h.get("/", cookie)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value == "" {
			cleared = true
		}
	}
	assert.True(t, cleared, "a useless cookie should not be resent on every request")
}

func TestASuspendedUserIsRefusedOnEveryRequest(t *testing.T) {
	// Not only at sign-in: suspending somebody has to end the session they are
	// already using.
	h := newHarness(t)
	user := h.addUser(t, "person@example.com", authdomain.RoleMember)
	cookie, _ := h.signIn(t, "person@example.com")
	require.Equal(t, http.StatusOK, h.get("/", cookie).Code)

	user.Suspend(time.Now())
	require.NoError(t, h.store.UpdateUser(h.ctx, user))

	rec := h.get("/", cookie)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
}

func TestTheLoginRateLimiterTrips(t *testing.T) {
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)

	form := url.Values{"email": {"person@example.com"}, "password": {"wrong password here"}}
	for i := range loginBurstPerEmail {
		rec := h.postForm(auth.LoginPath, form, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i+1)
	}

	rec := h.postForm(auth.LoginPath, form, nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))

	// Even the correct password is refused while the bucket is empty, which is
	// the point: an attacker cannot tell that they found it.
	rec = h.postForm(auth.LoginPath, url.Values{
		"email": {"person@example.com"}, "password": {testPassword},
	}, nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestASuccessfulSignInClearsOnlyTheAccountsThrottle(t *testing.T) {
	// Somebody who eventually remembers their password must not still be limited.
	h := newHarness(t)
	h.addUser(t, "person@example.com", authdomain.RoleMember)

	for range loginBurstPerEmail - 1 {
		h.postForm(auth.LoginPath, url.Values{
			"email": {"person@example.com"}, "password": {"wrong password here"},
		}, nil)
	}
	require.Equal(t, 1, h.loginByEmail.Len())

	_, _ = h.signIn(t, "person@example.com")

	// The account's bucket is gone, so a full fresh run of failures is possible.
	for i := range loginBurstPerEmail {
		rec := h.postForm(auth.LoginPath, url.Values{
			"email": {"person@example.com"}, "password": {"wrong password here"},
		}, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i+1)
	}

	// The address's bucket is deliberately NOT reset: clearing it on any success
	// would let an attacker holding one valid credential reset their own throttle
	// and carry on guessing at everybody else's.
	assert.Equal(t, 1, h.loginByIP.Len(), "the address bucket must survive a success")
}

func TestTheRateLimiterKeysOnEmailAsWellAsAddress(t *testing.T) {
	// Per-IP alone lets a botnet spread one password across many addresses.
	h := newHarness(t)
	h.addUser(t, "target@example.com", authdomain.RoleMember)

	// Exhaust the account bucket from varying addresses.
	for i := range loginBurstPerEmail {
		r := httptest.NewRequest(http.MethodPost, auth.LoginPath,
			strings.NewReader(url.Values{
				"email": {"target@example.com"}, "password": {"wrong password here"},
			}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "203.0.113." + string(rune('1'+i)) + ":1234"
		require.Equal(t, http.StatusUnauthorized, h.do(r).Code)
	}

	r := httptest.NewRequest(http.MethodPost, auth.LoginPath,
		strings.NewReader(url.Values{
			"email": {"target@example.com"}, "password": {"wrong password here"},
		}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "198.51.100.77:1234"
	assert.Equal(t, http.StatusTooManyRequests, h.do(r).Code,
		"a fresh address must not get a fresh allowance against the same account")
}

func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{auth.LoginPath, "/healthz", "/projects/demo"} {
		t.Run(path, func(t *testing.T) {
			rec := h.get(path, nil)
			assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'self'")
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 3: projects, lanes, membership.
//
// The gate for this phase is a wall, and a wall is only proven from the outside.
// These drive the assembled router over real HTTP with a member's cookie, which is
// what the test plan means by "verify it with curl".
// ---------------------------------------------------------------------------

// boardHarness extends the auth harness with the project and board slices.
type boardHarness struct {
	*harness
	projects *project.Service
	board    *board.Service
}

func newBoardHarness(t *testing.T) *boardHarness {
	t.Helper()
	h := newHarness(t)
	return &boardHarness{
		harness:  h,
		projects: h.projectService,
		board:    h.boardService,
	}
}

// makeProject creates a board owned by the given super admin.
func (h *boardHarness) makeProject(t *testing.T, owner *authdomain.User, name string) *projectdomain.Project {
	t.Helper()
	p, err := h.projects.Create(h.ctx, owner, project.CreateInput{Name: name})
	require.NoError(t, err)
	return p
}

// grant gives somebody a role on a board.
func (h *boardHarness) grant(
	t *testing.T, owner *authdomain.User, p *projectdomain.Project,
	who *authdomain.User, role projectdomain.ProjectRole,
) {
	t.Helper()
	access, err := h.projects.Resolve(h.ctx, owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, h.projects.Grant(h.ctx, access, who.UUID, role))
}

// laneUUIDs reads a board's lanes in order.
func (h *boardHarness) laneUUIDs(t *testing.T, owner *authdomain.User, p *projectdomain.Project) []string {
	t.Helper()
	access, err := h.projects.Resolve(h.ctx, owner, p.Slug)
	require.NoError(t, err)
	lanes, err := h.board.Lanes(h.ctx, access.Subject, p.UUID)
	require.NoError(t, err)

	out := make([]string, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, l.UUID)
	}
	return out
}

// csrfFor reads a real CSRF token for a signed-in cookie, from the account page —
// which always renders one, whatever else the session can or cannot reach.
func (h *boardHarness) csrfFor(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	_, session, err := h.service.Authenticate(h.ctx, cookie.Value)
	require.NoError(t, err)
	return session.CSRFToken
}

// TestAMemberCannotChangeTheBoardsShape is the phase-3 gate.
//
// Every route that alters a board is exercised with a member's cookie and a VALID
// CSRF token, so a 403 here means authorization refused it — not the CSRF check.
// That distinction matters: a missing token would make every one of these pass for
// the wrong reason.
func TestAMemberCannotChangeTheBoardsShape(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)

	p := h.makeProject(t, owner, "Chonkboard Build")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, _ := h.signIn(t, "member@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	require.NotEmpty(t, lanes)
	lane := lanes[0]
	base := "/projects/" + p.Slug

	// The token really is accepted: this reaches the handler and fails on the
	// password, not on CSRF. Without this the 403s below prove nothing.
	t.Run("the CSRF token is valid", func(t *testing.T) {
		rec := h.postForm(auth.PasswordPath, url.Values{
			"csrf_token":       {csrf},
			"current_password": {"definitely not it"},
			"new_password":     {"a replacement password"},
		}, cookie)
		require.NotEqual(t, http.StatusForbidden, rec.Code,
			"the token must pass the CSRF check, or the rest of this test is meaningless")
	})

	writes := []struct {
		name string
		path string
		form url.Values
	}{
		{"create a lane", base + "/lanes", url.Values{"name": {"Sneaky"}, "color": {"rose"}}},
		{"rename a lane", base + "/lanes/" + lane, url.Values{"name": {"Renamed"}, "color": {"rose"}}},
		{"move a lane", base + "/lanes/" + lane + "/move", url.Values{"direction": {"down"}}},
		{"reorder every lane", base + "/lanes/reorder", url.Values{"order": lanes}},
		{"delete a lane", base + "/lanes/" + lane + "/delete", url.Values{"move_to": {""}}},
		{"rename the board", base + "/settings", url.Values{"name": {"Hijacked"}}},
		{"archive the board", base + "/archive", url.Values{}},
		{"delete the board", base + "/delete", url.Values{}},
		{"grant themselves manager", base + "/members", url.Values{
			"user_uuid": {memberUser.UUID}, "role": {"manager"},
		}},
		{"re-role somebody", base + "/members/" + owner.UUID, url.Values{"role": {"member"}}},
		{"revoke somebody", base + "/members/" + owner.UUID + "/revoke", url.Values{}},
		{"create a project", "/projects", url.Values{"name": {"Mine"}}},
	}

	for _, w := range writes {
		t.Run("POST: "+w.name, func(t *testing.T) {
			form := w.form
			form.Set("csrf_token", csrf)

			rec := h.postForm(w.path, form, cookie)

			assert.Equal(t, http.StatusForbidden, rec.Code,
				"a member must not be able to %s", w.name)
		})
	}

	reads := map[string]string{
		"the settings page":    base + "/settings",
		"the add-lane form":    base + "/lanes/new",
		"the edit-lane form":   base + "/lanes/" + lane + "/edit",
		"the delete-lane form": base + "/lanes/" + lane + "/delete",
		"the new-project form": "/projects/new",
	}
	for name, path := range reads {
		t.Run("GET: "+name, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, h.get(path, cookie).Code,
				"a member must not reach %s", name)
		})
	}

	t.Run("and nothing changed", func(t *testing.T) {
		after := h.laneUUIDs(t, owner, p)
		assert.Equal(t, lanes, after, "the lanes are exactly as they were")

		access, err := h.projects.Resolve(h.ctx, owner, p.Slug)
		require.NoError(t, err)
		assert.Equal(t, "Chonkboard Build", access.Project.Name)
		assert.False(t, access.Project.IsArchived())
	})
}

func TestAMemberSeesTheBoardWithNoControls(t *testing.T) {
	// The other half of the requirement: they see the same board.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Chonkboard Build")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, _ := h.signIn(t, "member@example.com")
	rec := h.get("/projects/"+p.Slug, cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	page := body(rec)

	assert.Contains(t, page, "Chonkboard Build")
	assert.Contains(t, page, "Backlog", "the lanes are real and visible")
	assert.Contains(t, page, "Stash")

	for _, control := range []string{
		"Add a lane", "/settings", "/lanes/new", "/edit", "Lane settings for",
	} {
		assert.NotContains(t, page, control,
			"a member must not be offered %q", control)
	}
}

func TestAManagerGetsEveryLaneControl(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	managerUser := h.addUser(t, "manager@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Chonkboard Build")
	h.grant(t, owner, p, managerUser, projectdomain.RoleManager)

	cookie, _ := h.signIn(t, "manager@example.com")
	csrf := h.csrfFor(t, cookie)
	base := "/projects/" + p.Slug

	page := body(h.get(base, cookie))
	assert.Contains(t, page, "Add a lane")
	assert.Contains(t, page, base+"/settings")

	assert.Equal(t, http.StatusOK, h.get(base+"/settings", cookie).Code)
	assert.Equal(t, http.StatusOK, h.get(base+"/lanes/new", cookie).Code)

	t.Run("add a lane", func(t *testing.T) {
		rec := h.postForm(base+"/lanes", url.Values{
			"csrf_token": {csrf}, "name": {"Blocked"}, "color": {"rose"},
		}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		lanes := h.laneUUIDs(t, owner, p)
		assert.Len(t, lanes, 6)
	})

	t.Run("rename, recolour and limit a lane", func(t *testing.T) {
		lane := h.laneUUIDs(t, owner, p)[1]
		rec := h.postForm(base+"/lanes/"+lane, url.Values{
			"csrf_token": {csrf}, "name": {"Doing now"},
			"color": {"teal"}, "wip_limit": {"3"},
		}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		settings := body(h.get(base+"/settings", cookie))
		assert.Contains(t, settings, "Doing now")
		assert.Contains(t, settings, "max 3")
	})

	t.Run("reorder by one place", func(t *testing.T) {
		before := h.laneUUIDs(t, owner, p)
		rec := h.postForm(base+"/lanes/"+before[0]+"/move", url.Values{
			"csrf_token": {csrf}, "direction": {"down"},
		}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		after := h.laneUUIDs(t, owner, p)
		assert.Equal(t, before[1], after[0])
		assert.Equal(t, before[0], after[1])
	})

	t.Run("delete a lane", func(t *testing.T) {
		before := h.laneUUIDs(t, owner, p)
		rec := h.postForm(base+"/lanes/"+before[0]+"/delete", url.Values{
			"csrf_token": {csrf}, "move_to": {""},
		}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Len(t, h.laneUUIDs(t, owner, p), len(before)-1)
	})

	t.Run("but not archive, delete, or mint a manager", func(t *testing.T) {
		for name, path := range map[string]string{
			"archive": base + "/archive",
			"delete":  base + "/delete",
		} {
			rec := h.postForm(path, url.Values{"csrf_token": {csrf}}, cookie)
			assert.Equal(t, http.StatusForbidden, rec.Code, name)
		}

		other := h.addUser(t, "other@example.com", authdomain.RoleMember)
		rec := h.postForm(base+"/members", url.Values{
			"csrf_token": {csrf}, "user_uuid": {other.UUID}, "role": {"manager"},
		}, cookie)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestTheProjectListIsScopedToGrants(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)

	granted := h.makeProject(t, owner, "Granted Board")
	h.makeProject(t, owner, "Private Board")
	h.grant(t, owner, granted, memberUser, projectdomain.RoleMember)

	t.Run("the operator sees every board", func(t *testing.T) {
		cookie, _ := h.signIn(t, "owner@example.com")
		page := body(h.get("/", cookie))
		assert.Contains(t, page, "Granted Board")
		assert.Contains(t, page, "Private Board")
		assert.Contains(t, page, "New project")
	})

	t.Run("a member sees only theirs, and cannot create", func(t *testing.T) {
		cookie, _ := h.signIn(t, "member@example.com")
		page := body(h.get("/", cookie))
		assert.Contains(t, page, "Granted Board")
		assert.NotContains(t, page, "Private Board")
		assert.NotContains(t, page, "New project")
	})
}

func TestAnUngrantedBoardIs404NotForbidden(t *testing.T) {
	// A 403 would confirm the board exists, which slowly enumerates every project
	// slug in the installation.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	h.addUser(t, "stranger@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Private Board")

	cookie, _ := h.signIn(t, "stranger@example.com")

	assert.Equal(t, http.StatusNotFound, h.get("/projects/"+p.Slug, cookie).Code)
	assert.Equal(t, http.StatusNotFound, h.get("/projects/no-such-board", cookie).Code,
		"a board that does not exist looks identical")
}

func TestABoardIsReachableBySlugOrUUID(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonkboard Build")
	cookie, _ := h.signIn(t, "owner@example.com")

	assert.Equal(t, http.StatusOK, h.get("/projects/"+p.Slug, cookie).Code)
	assert.Equal(t, http.StatusOK, h.get("/projects/"+p.UUID, cookie).Code)
}

func TestANewProjectStartsWithFiveLanes(t *testing.T) {
	h := newBoardHarness(t)
	h.addSuperAdmin(t, "owner@example.com")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)

	rec := h.postForm("/projects", url.Values{
		"csrf_token": {csrf}, "name": {"Chonkboard Build"},
		"description": {"The board for building the board"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/projects/chonkboard-build", rec.Header().Get("Location"),
		"the slug is derived from the name")

	page := body(h.get("/projects/chonkboard-build", cookie))
	for _, lane := range []string{"Backlog", "In progress", "Testing", "Done", "Stash"} {
		assert.Contains(t, page, lane)
	}
}

func TestABoardKeepsItsLastLane(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonkboard Build")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	base := "/projects/" + p.Slug

	for {
		lanes := h.laneUUIDs(t, owner, p)
		if len(lanes) == 1 {
			break
		}
		rec := h.postForm(base+"/lanes/"+lanes[0]+"/delete",
			url.Values{"csrf_token": {csrf}, "move_to": {""}}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)
	}

	lanes := h.laneUUIDs(t, owner, p)
	rec := h.postForm(base+"/lanes/"+lanes[0]+"/delete",
		url.Values{"csrf_token": {csrf}, "move_to": {""}}, cookie)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, body(rec), "at least one lane")
	assert.Len(t, h.laneUUIDs(t, owner, p), 1)
}

func TestAnInvalidWIPLimitComesBackOnTheForm(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonkboard Build")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)

	for name, value := range map[string]string{
		"zero":         "0",
		"negative":     "-3",
		"not a number": "lots",
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.postForm("/projects/"+p.Slug+"/lanes", url.Values{
				"csrf_token": {csrf}, "name": {"Broken"},
				"color": {"slate"}, "wip_limit": {value},
			}, cookie)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			page := body(rec)
			assert.Contains(t, page, "Broken", "what was typed comes back")
			assert.Contains(t, page, "wip_limit", "and the field is marked")
		})
	}
}

func TestArchivingTakesABoardOffEveryList(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Chonkboard Build")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	ownerCookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, ownerCookie)

	rec := h.postForm("/projects/"+p.Slug+"/archive",
		url.Values{"csrf_token": {csrf}}, ownerCookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	assert.NotContains(t, body(h.get("/", ownerCookie)), "Chonkboard Build")
	assert.Contains(t, body(h.get("/?archived=1", ownerCookie)), "Chonkboard Build")

	memberCookie, _ := h.signIn(t, "member@example.com")
	assert.NotContains(t, body(h.get("/", memberCookie)), "Chonkboard Build",
		"archived boards are hidden from members too")
}

// ---------------------------------------------------------------------------
// Phase 4: cards.
// ---------------------------------------------------------------------------

// addCard creates a card over HTTP, as the signed-in cookie's owner.
func (h *boardHarness) addCard(
	t *testing.T, cookie *http.Cookie, csrf string,
	p *projectdomain.Project, laneUUID, title string,
) string {
	t.Helper()
	rec := h.postForm("/projects/"+p.Slug+"/lanes/"+laneUUID+"/cards", url.Values{
		"csrf_token": {csrf}, "title": {title},
	}, cookie)
	require.Equal(t, http.StatusOK, rec.Code, "creating %q", title)

	// The response is the lane fragment; the new card is the last one in it.
	ids := cardUUIDsIn(body(rec))
	require.NotEmpty(t, ids)
	return ids[len(ids)-1]
}

// cardUUIDsIn reads the card order out of rendered HTML, which is the only place the
// board's actual order is observable to a client.
func cardUUIDsIn(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`data-card-uuid="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// laneOrder reads one lane's card titles in order, from the lane fragment.
func (h *boardHarness) laneOrder(t *testing.T, cookie *http.Cookie, laneUUID string) []string {
	t.Helper()
	rec := h.get("/lanes/"+laneUUID+"/fragment", cookie)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []string
	for _, m := range regexp.MustCompile(`leading-snug text-ink">([^<]*)<`).
		FindAllStringSubmatch(body(rec), -1) {
		out = append(out, m[1])
	}
	return out
}

// move posts a drag and returns the status.
func (h *boardHarness) move(
	cookie *http.Cookie, csrf, cardUUID string, form url.Values,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/cards/"+cardUUID+"/move",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(middleware.CSRFHeader, csrf)
	r.AddCookie(cookie)
	return h.do(r)
}

func TestACardSurvivesAReload(t *testing.T) {
	// The phase gate: the drag is persisted, not held in a page.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	a := h.addCard(t, cookie, csrf, p, lanes[0], "first")
	b := h.addCard(t, cookie, csrf, p, lanes[0], "second")

	rec := h.move(cookie, csrf, b, url.Values{
		"to_lane":    {lanes[1]},
		"to_order":   {b},
		"from_lane":  {lanes[0]},
		"from_order": {a},
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, body(rec), "the result reaches other tabs over SSE, not in this response")

	// Re-read the whole board from scratch, as a reload would.
	page := body(h.get("/projects/"+p.Slug, cookie))
	assert.Contains(t, page, "first")
	assert.Contains(t, page, "second")
	assert.Equal(t, []string{"first"}, h.laneOrder(t, cookie, lanes[0]))
	assert.Equal(t, []string{"second"}, h.laneOrder(t, cookie, lanes[1]))
}

func TestMoveRefusalsOverHTTP(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	a := h.addCard(t, cookie, csrf, p, lanes[0], "a")
	b := h.addCard(t, cookie, csrf, p, lanes[0], "b")

	// A second board, to borrow a foreign card and a foreign lane from.
	other := h.makeProject(t, owner, "Other Board")
	otherLanes := h.laneUUIDs(t, owner, other)
	foreign := h.addCard(t, cookie, csrf, other, otherLanes[0], "theirs")

	tests := []struct {
		name string
		card string
		form url.Values
		want int
	}{
		{
			name: "a stale from_lane", card: a,
			form: url.Values{
				"to_lane": {lanes[1]}, "to_order": {a},
				"from_lane": {lanes[2]}, // it is not in lane 2
			},
			want: http.StatusConflict,
		},
		{
			name: "a lane from another board", card: a,
			form: url.Values{"to_lane": {otherLanes[0]}, "to_order": {a}},
			want: http.StatusNotFound,
		},
		{
			// The card URL determines the board, so moving a foreign card is
			// scoped to *its* board — and the lane named then belongs to
			// somebody else's. Refused as an unknown lane rather than an
			// unknown card, which is the same 404 either way.
			name: "a card from another board", card: foreign,
			form: url.Values{"to_lane": {lanes[0]}, "to_order": {a, foreign}},
			want: http.StatusNotFound,
		},
		{
			// The snapshot is this board's, so a card from elsewhere is simply
			// not on it.
			name: "an order naming a card from another board", card: a,
			form: url.Values{"to_lane": {lanes[0]}, "to_order": {foreign, a}},
			want: http.StatusNotFound,
		},
		{
			name: "an order missing the moved card", card: a,
			form: url.Values{"to_lane": {lanes[0]}, "to_order": {b}},
			want: http.StatusBadRequest,
		},
		{
			name: "the same card twice", card: a,
			form: url.Values{"to_lane": {lanes[0]}, "to_order": {a, a}},
			want: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.move(cookie, csrf, tc.card, tc.form)

			assert.Equal(t, tc.want, rec.Code)
			assert.NotEmpty(t, body(rec), "a refusal carries a toast the client can show")
		})
	}

	t.Run("and nothing moved", func(t *testing.T) {
		assert.Equal(t, []string{"a", "b"}, h.laneOrder(t, cookie, lanes[0]))
		assert.Empty(t, h.laneOrder(t, cookie, lanes[1]))
		assert.Equal(t, []string{"theirs"}, h.laneOrder(t, cookie, otherLanes[0]))
	})
}

func TestAWIPBreachIs409AndChangesNothing(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	// A limit of one on the target.
	rec := h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[1], url.Values{
		"csrf_token": {csrf}, "name": {"Doing"}, "color": {"blue"}, "wip_limit": {"1"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	moving := h.addCard(t, cookie, csrf, p, lanes[0], "moving")
	resident := h.addCard(t, cookie, csrf, p, lanes[1], "resident")

	rec = h.move(cookie, csrf, moving, url.Values{
		"to_lane": {lanes[1]}, "to_order": {resident, moving},
		"from_lane": {lanes[0]},
	})

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, body(rec), "already at its limit")
	assert.Equal(t, []string{"moving"}, h.laneOrder(t, cookie, lanes[0]))
	assert.Equal(t, []string{"resident"}, h.laneOrder(t, cookie, lanes[1]))
}

func TestDraggingTheLastCardOutOfALane(t *testing.T) {
	// The source order is genuinely empty. A form that sends `from_order=` with no
	// value must be read as an empty lane, not as one card with an empty uuid.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	only := h.addCard(t, cookie, csrf, p, lanes[0], "only")

	cases := []struct {
		name string
		form url.Values
	}{
		{"from_order omitted entirely", url.Values{
			"to_lane": {lanes[1]}, "to_order": {only}, "from_lane": {lanes[0]},
		}},
		{"from_order present but empty", url.Values{
			"to_lane": {lanes[1]}, "to_order": {only},
			"from_lane": {lanes[0]}, "from_order": {""},
		}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if i > 0 {
				// Put it back. A cross-lane move names its source, so this reset
				// carries from_lane too — without it the planner correctly
				// refuses, because a card that is not in the lane being rewritten
				// means the client is working from a stale board.
				require.Equal(t, http.StatusNoContent, h.move(cookie, csrf, only, url.Values{
					"to_lane": {lanes[0]}, "to_order": {only}, "from_lane": {lanes[1]},
				}).Code)
			}

			rec := h.move(cookie, csrf, only, tc.form)

			require.Equal(t, http.StatusNoContent, rec.Code)
			assert.Empty(t, h.laneOrder(t, cookie, lanes[0]))
			assert.Equal(t, []string{"only"}, h.laneOrder(t, cookie, lanes[1]))
		})
	}
}

func TestAMemberMayWorkOnCardsButDeleteOnlyTheirOwn(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Chonk Cards")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)
	lanes := h.laneUUIDs(t, owner, p)

	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	ownersCard := h.addCard(t, ownerCookie, ownerCSRF, p, lanes[0], "the owner's card")

	cookie, _ := h.signIn(t, "member@example.com")
	csrf := h.csrfFor(t, cookie)

	t.Run("create", func(t *testing.T) {
		rec := h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[0]+"/cards", url.Values{
			"csrf_token": {csrf}, "title": {"the member's card"},
		}, cookie)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("edit somebody else's card — allowed, it is a shared board", func(t *testing.T) {
		rec := h.postForm("/cards/"+ownersCard, url.Values{
			"csrf_token": {csrf}, "title": {"edited by a member"},
		}, cookie)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("move somebody else's card — allowed", func(t *testing.T) {
		rec := h.move(cookie, csrf, ownersCard, url.Values{
			"to_lane": {lanes[1]}, "to_order": {ownersCard}, "from_lane": {lanes[0]},
		})
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("archive somebody else's card — allowed", func(t *testing.T) {
		rec := h.postForm("/cards/"+ownersCard+"/archive", url.Values{
			"csrf_token": {csrf}, "archived": {"1"},
		}, cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)
		// Put it back.
		h.postForm("/cards/"+ownersCard+"/archive", url.Values{
			"csrf_token": {csrf}, "archived": {"0"},
		}, cookie)
	})

	t.Run("delete somebody else's card — refused", func(t *testing.T) {
		rec := h.postForm("/cards/"+ownersCard+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, body(rec), "only delete your own cards")
	})

	t.Run("delete their own card — allowed", func(t *testing.T) {
		theirs := h.addCard(t, cookie, csrf, p, lanes[0], "to be deleted")
		rec := h.postForm("/cards/"+theirs+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie)
		assert.Equal(t, http.StatusSeeOther, rec.Code)
	})

	t.Run("a manager may delete anybody's card", func(t *testing.T) {
		rec := h.postForm("/cards/"+ownersCard+"/delete",
			url.Values{"csrf_token": {ownerCSRF}}, ownerCookie)
		assert.Equal(t, http.StatusSeeOther, rec.Code)
	})
}

func TestACardOnAnotherBoardIs404(t *testing.T) {
	// Card URLs carry no project, so the project is resolved from the card. An
	// ungranted card must look exactly like one that does not exist.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	h.addUser(t, "stranger@example.com", authdomain.RoleMember)

	p := h.makeProject(t, owner, "Private Board")
	lanes := h.laneUUIDs(t, owner, p)
	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	privateCard := h.addCard(t, ownerCookie, ownerCSRF, p, lanes[0], "private")

	cookie, _ := h.signIn(t, "stranger@example.com")
	csrf := h.csrfFor(t, cookie)

	for name, path := range map[string]string{
		"detail":        "/cards/" + privateCard,
		"edit form":     "/cards/" + privateCard + "/edit",
		"lane fragment": "/lanes/" + lanes[0] + "/fragment",
	} {
		t.Run("GET "+name, func(t *testing.T) {
			assert.Equal(t, http.StatusNotFound, h.get(path, cookie).Code)
		})
	}

	t.Run("POST edit", func(t *testing.T) {
		rec := h.postForm("/cards/"+privateCard, url.Values{
			"csrf_token": {csrf}, "title": {"hijacked"},
		}, cookie)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("POST delete", func(t *testing.T) {
		rec := h.postForm("/cards/"+privateCard+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("POST move", func(t *testing.T) {
		rec := h.move(cookie, csrf, privateCard, url.Values{
			"to_lane": {lanes[1]}, "to_order": {privateCard},
		})
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("and the card is untouched", func(t *testing.T) {
		assert.Equal(t, []string{"private"}, h.laneOrder(t, ownerCookie, lanes[0]))
	})
}

func TestAMoveWithoutTheCSRFTokenIsRefused(t *testing.T) {
	// The move is a fetch, not an HTMX request, so board.js sends the token
	// explicitly. Without it the move must not land.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	c := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	rec := h.postForm("/cards/"+c+"/move", url.Values{
		"to_lane": {lanes[1]}, "to_order": {c},
	}, cookie)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, []string{"a card"}, h.laneOrder(t, cookie, lanes[0]))
}

func TestTheCardModalAndItsHistory(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	c := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	require.Equal(t, http.StatusNoContent, h.move(cookie, csrf, c, url.Values{
		"to_lane": {lanes[1]}, "to_order": {c}, "from_lane": {lanes[0]},
	}).Code)
	require.Equal(t, http.StatusOK, h.postForm("/cards/"+c, url.Values{
		"csrf_token": {csrf}, "title": {"a card"}, "description": {"Now described."},
	}, cookie).Code)

	page := body(h.get("/cards/"+c, cookie))

	assert.Contains(t, page, "a card")
	assert.Contains(t, page, "Now described.")
	assert.Contains(t, page, "In progress", "it says which lane the card is in")
	assert.Contains(t, page, "added this card")
	assert.Contains(t, page, "moved it from Backlog to In progress")
	assert.Contains(t, page, "edited it")
}

func TestAnInvalidCardComesBackOnTheForm(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	t.Run("on create", func(t *testing.T) {
		rec := h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[0]+"/cards", url.Values{
			"csrf_token": {csrf}, "title": {"   "}, "description": {"kept"},
		}, cookie)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, body(rec), "kept", "what was typed comes back")
		assert.Empty(t, h.laneOrder(t, cookie, lanes[0]), "nothing was written")
	})

	t.Run("on edit", func(t *testing.T) {
		c := h.addCard(t, cookie, csrf, p, lanes[0], "original")
		rec := h.postForm("/cards/"+c, url.Values{
			"csrf_token": {csrf}, "title": {""}, "description": {"kept"},
		}, cookie)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, body(rec), "kept")
		assert.Equal(t, []string{"original"}, h.laneOrder(t, cookie, lanes[0]))
	})
}

func TestArchivingTakesACardOffTheBoard(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	keep := h.addCard(t, cookie, csrf, p, lanes[0], "kept")
	stash := h.addCard(t, cookie, csrf, p, lanes[0], "stashed")
	_ = keep

	rec := h.postForm("/cards/"+stash+"/archive", url.Values{
		"csrf_token": {csrf}, "archived": {"1"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	assert.Equal(t, []string{"kept"}, h.laneOrder(t, cookie, lanes[0]))
	assert.NotContains(t, body(h.get("/projects/"+p.Slug, cookie)), "stashed")

	// Still reachable, so it can be restored.
	assert.Equal(t, http.StatusOK, h.get("/cards/"+stash, cookie).Code)
	rec = h.postForm("/cards/"+stash+"/archive", url.Values{
		"csrf_token": {csrf}, "archived": {"0"},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Len(t, h.laneOrder(t, cookie, lanes[0]), 2)
}

func TestTheAddCardControlIsRenderedNow(t *testing.T) {
	// It was hidden through phase 3 because the route behind it did not exist.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Chonk Cards")
	cookie, _ := h.signIn(t, "owner@example.com")

	page := body(h.get("/projects/"+p.Slug, cookie))
	assert.Contains(t, page, "Add a card")
	assert.Contains(t, page, "/cards/new")
}

// ---------------------------------------------------------------------------
// Phase 5: the rich card.
// ---------------------------------------------------------------------------

// upload posts a multipart file onto a card.
//
// A real multipart request, because that is the shape the bug was in: the CSRF check
// has to find its token inside a multipart body, and r.ParseForm does not read one.
func (h *boardHarness) upload(
	cookie *http.Cookie, csrf, cardUUID, filename, contentType string, content []byte,
) *httptest.ResponseRecorder {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("csrf_token", csrf)

	head := make(textproto.MIMEHeader)
	head.Set("Content-Disposition",
		`form-data; name="file"; filename="`+filename+`"`)
	head.Set("Content-Type", contentType)
	part, _ := w.CreatePart(head)
	_, _ = part.Write(content)
	_ = w.Close()

	r := httptest.NewRequest(http.MethodPost, "/cards/"+cardUUID+"/attachments", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.AddCookie(cookie)
	return h.do(r)
}

// makeLabel creates a project label and returns its uuid.
func (h *boardHarness) makeLabel(
	t *testing.T, cookie *http.Cookie, csrf string, p *projectdomain.Project, name, colour string,
) string {
	t.Helper()
	rec := h.postForm("/projects/"+p.Slug+"/labels", url.Values{
		"csrf_token": {csrf}, "name": {name}, "color": {colour},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	access, err := h.projects.Resolve(h.ctx, h.mustUser(t, "owner@example.com"), p.Slug)
	require.NoError(t, err)
	labels, err := h.board.Labels(h.ctx, access.Subject, p.UUID)
	require.NoError(t, err)
	for _, l := range labels {
		if l.Name == name {
			return l.UUID
		}
	}
	t.Fatalf("label %q was not created", name)
	return ""
}

func (h *boardHarness) mustUser(t *testing.T, email string) *authdomain.User {
	t.Helper()
	u, err := h.store.UserByEmail(h.ctx, email)
	require.NoError(t, err)
	require.NotNil(t, u)
	return u
}

func TestEveryRichFieldRoundTripsOverHTTP(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	bug := h.makeLabel(t, cookie, csrf, p, "bug", "rose")
	chore := h.makeLabel(t, cookie, csrf, p, "chore", "teal")

	rec := h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[0]+"/cards", url.Values{
		"csrf_token":  {csrf},
		"title":       {"A rich card"},
		"description": {"## Heading\n\nSome **bold** text."},
		"priority":    {"urgent"},
		"due_at":      {"2026-11-03"},
		"assignee":    {owner.UUID},
		"labels":      {bug, chore},
	}, cookie)
	require.Equal(t, http.StatusOK, rec.Code)

	cardUUID := cardUUIDsIn(body(rec))[0]
	page := body(h.get("/cards/"+cardUUID, cookie))

	assert.Contains(t, page, "A rich card")
	assert.Contains(t, page, "Urgent")
	assert.Contains(t, page, "3 Nov 2026")
	assert.Contains(t, page, ">bug<")
	assert.Contains(t, page, ">chore<")
	assert.Contains(t, page, "<strong>bold</strong>", "markdown is rendered")
	assert.Contains(t, page, "<h2", "and its headings survive")

	t.Run("and the edit form comes back filled in", func(t *testing.T) {
		form := body(h.get("/cards/"+cardUUID+"/edit", cookie))
		assert.Contains(t, form, `value="2026-11-03"`)
		assert.Contains(t, form, `value="urgent" selected`)
		assert.Contains(t, form, owner.UUID)
	})

	t.Run("and the board shows the chips and the due date", func(t *testing.T) {
		board := body(h.get("/projects/"+p.Slug, cookie))
		assert.Contains(t, board, ">bug<")
		assert.Contains(t, board, "before:bg-danger", "urgent draws the priority rule")
		assert.Contains(t, board, "3 Nov")
	})
}

// TestMarkdownRendersInertOverHTTP is the gate. The CSP blocks script; none of these
// needs a script tag.
func TestMarkdownRendersInertOverHTTP(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	hostile := `<script>alert(1)</script>` + "\n" +
		`<img src=x onerror="alert(2)">` + "\n" +
		`[click](javascript:alert(3))` + "\n" +
		`<iframe src="https://evil.example"></iframe>` + "\n" +
		`<form action="https://evil.example"><input name="p"></form>` + "\n" +
		`<svg onload="alert(4)"></svg>`

	require.Equal(t, http.StatusOK, h.postForm("/cards/"+cardUUID, url.Values{
		"csrf_token": {csrf}, "title": {"a card"}, "description": {hostile},
	}, cookie).Code)
	require.Equal(t, http.StatusSeeOther, h.postForm("/cards/"+cardUUID+"/comments", url.Values{
		"csrf_token": {csrf}, "body": {hostile},
	}, cookie).Code)

	page := strings.ToLower(body(h.get("/cards/"+cardUUID, cookie)))

	for _, probe := range []string{
		"<script", "onerror", "onload", "javascript:", "<iframe", "<svg", "alert(",
	} {
		assert.NotContains(t, page, probe,
			"%q survived into the rendered card", probe)
	}

	// The only forms on the page are the application's own.
	for _, action := range regexp.MustCompile(`<form[^>]+action="([^"]*)"`).
		FindAllStringSubmatch(page, -1) {
		assert.True(t, strings.HasPrefix(action[1], "/"),
			"a form posting to %q was injected", action[1])
	}
}

func TestAttachmentUploadAndDownload(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	// A real multipart post: the CSRF token has to be found inside the body, which
	// r.ParseForm cannot do and which made every upload a 403.
	//
	// 303 because this is a plain form post — only an HTMX request gets the refreshed
	// panel back. What matters here is that it is not a 403.
	rec := h.upload(cookie, csrf, cardUUID, "notes.csv", "text/csv", []byte("a,b\n1,2\n"))
	require.Equal(t, http.StatusSeeOther, rec.Code,
		"a multipart post must pass the CSRF check")

	page := body(h.get("/cards/"+cardUUID, cookie))
	assert.Contains(t, page, "notes.csv")

	attachmentUUID := regexp.MustCompile(`/attachments/([0-9a-f-]{36})"`).
		FindStringSubmatch(page)
	require.Len(t, attachmentUUID, 2)

	t.Run("an HTMX upload returns the refreshed card panel", func(t *testing.T) {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		_ = w.WriteField("csrf_token", csrf)
		head := make(textproto.MIMEHeader)
		head.Set("Content-Disposition", `form-data; name="file"; filename="second.csv"`)
		head.Set("Content-Type", "text/csv")
		part, _ := w.CreatePart(head)
		_, _ = part.Write([]byte("x"))
		_ = w.Close()

		r := httptest.NewRequest(http.MethodPost, "/cards/"+cardUUID+"/attachments", &body)
		r.Header.Set("Content-Type", w.FormDataContentType())
		r.Header.Set("HX-Request", "true")
		r.AddCookie(cookie)

		rec := h.do(r)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, bodyOf(rec), "second.csv")
	})

	t.Run("download", func(t *testing.T) {
		rec := h.get("/attachments/"+attachmentUUID[1], cookie)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "a,b\n1,2\n", body(rec))
		assert.Equal(t, "text/csv", rec.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Contains(t, rec.Header().Get("Content-Disposition"), "attachment;",
			"always a download, never rendered in this origin")
		assert.Contains(t, rec.Header().Get("Content-Disposition"), `filename="notes.csv"`)
	})
}

func TestAnOversizedUploadIsRefusedWithAReadableMessage(t *testing.T) {
	// Not by aborting the connection: a person who picked a large file deserves to
	// be told which limit they hit.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	rec := h.upload(cookie, csrf, cardUUID, "big.png", "image/png",
		bytes.Repeat([]byte("x"), (64<<10)+1))

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, body(rec), "too large")

	assert.NotContains(t, body(h.get("/cards/"+cardUUID, cookie)), "big.png",
		"a refused upload leaves no row")
}

func TestADisallowedFileTypeIsRefused(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	for name, f := range map[string]struct{ filename, mime string }{
		"a shell script": {"x.sh", "application/x-sh"},
		"html":           {"x.html", "text/html"},
		"an svg":         {"x.svg", "image/svg+xml"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.upload(cookie, csrf, cardUUID, f.filename, f.mime, []byte("x"))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, body(rec), "cannot be attached")
		})
	}
}

// TestALeakedAttachmentURLIsNotALeakedFile is the gate. The refusal must be
// indistinguishable from a made-up id, or the response confirms the file exists.
func TestALeakedAttachmentURLIsNotALeakedFile(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	h.addUser(t, "stranger@example.com", authdomain.RoleMember)

	p := h.makeProject(t, owner, "Private Board")
	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, ownerCookie, ownerCSRF, p, lanes[0], "a card")

	require.Equal(t, http.StatusSeeOther,
		h.upload(ownerCookie, ownerCSRF, cardUUID, "secret.pdf", "application/pdf",
			[]byte("confidential")).Code)

	page := body(h.get("/cards/"+cardUUID, ownerCookie))
	match := regexp.MustCompile(`/attachments/([0-9a-f-]{36})"`).FindStringSubmatch(page)
	require.Len(t, match, 2)
	attachmentUUID := match[1]

	cookie, csrf := h.signIn(t, "stranger@example.com")

	leaked := h.get("/attachments/"+attachmentUUID, cookie)
	madeUp := h.get("/attachments/00000000-0000-7000-8000-000000000000", cookie)

	assert.Equal(t, http.StatusNotFound, leaked.Code, "404, never 403")
	assert.Equal(t, http.StatusNotFound, madeUp.Code)
	assert.Equal(t, body(madeUp), body(leaked),
		"the two responses must be indistinguishable")
	assert.NotContains(t, body(leaked), "confidential")
	assert.NotContains(t, body(leaked), "secret.pdf")

	t.Run("and they cannot delete it or upload to that card", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, h.postForm(
			"/attachments/"+attachmentUUID+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie).Code)
		assert.Equal(t, http.StatusNotFound,
			h.upload(cookie, csrf, cardUUID, "x.csv", "text/csv", []byte("x")).Code)
	})
}

func TestCommentsOverHTTP(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Rich Cards")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)
	lanes := h.laneUUIDs(t, owner, p)

	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	cardUUID := h.addCard(t, ownerCookie, ownerCSRF, p, lanes[0], "a card")
	require.Equal(t, http.StatusSeeOther, h.postForm("/cards/"+cardUUID+"/comments",
		url.Values{"csrf_token": {ownerCSRF}, "body": {"the owner's comment"}}, ownerCookie).Code)

	cookie, csrf := h.signIn(t, "member@example.com")
	require.Equal(t, http.StatusSeeOther, h.postForm("/cards/"+cardUUID+"/comments",
		url.Values{"csrf_token": {csrf}, "body": {"a **member's** comment"}}, cookie).Code)

	page := body(h.get("/cards/"+cardUUID, cookie))
	assert.Contains(t, page, "the owner&#39;s comment")
	assert.Contains(t, page, "<strong>member&#39;s</strong>", "markdown is rendered")

	comments := regexp.MustCompile(`id="comment-([0-9a-f-]{36})"`).
		FindAllStringSubmatch(page, -1)
	require.Len(t, comments, 2)

	t.Run("an empty comment is refused", func(t *testing.T) {
		rec := h.postForm("/cards/"+cardUUID+"/comments",
			url.Values{"csrf_token": {csrf}, "body": {"   "}}, cookie)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("a member deletes only their own", func(t *testing.T) {
		// The owner's is first, the member's second — oldest first is the order a
		// thread is read in.
		ownersComment, membersComment := comments[0][1], comments[1][1]

		assert.Equal(t, http.StatusForbidden, h.postForm(
			"/comments/"+ownersComment+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie).Code)

		assert.Equal(t, http.StatusSeeOther, h.postForm(
			"/comments/"+membersComment+"/delete",
			url.Values{"csrf_token": {csrf}}, cookie).Code)

		after := body(h.get("/cards/"+cardUUID, cookie))
		assert.Contains(t, after, "removed a comment",
			"a tombstone, so the thread keeps its shape")
	})
}

func TestTheMoveToLaneControlIsOfferedAndWorks(t *testing.T) {
	// The keyboard path for a drag. A board whose cards can only be dragged is
	// unusable without a pointer.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	page := body(h.get("/cards/"+cardUUID, cookie))
	assert.Contains(t, page, "/move-to")
	assert.Contains(t, page, "Move to…")
	assert.NotContains(t, page, `<option value="`+lanes[0]+`"`,
		"the lane the card is already in is not offered")
	assert.Contains(t, page, `<option value="`+lanes[1]+`"`)

	rec := h.postForm("/cards/"+cardUUID+"/move-to", url.Values{
		"csrf_token": {csrf}, "to_lane": {lanes[2]},
	}, cookie)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	assert.Empty(t, h.laneOrder(t, cookie, lanes[0]))
	assert.Equal(t, []string{"a card"}, h.laneOrder(t, cookie, lanes[2]))
}

func TestMoveToLaneRefusesAWIPBreach(t *testing.T) {
	// The same rules as a drag, because it is the same code path.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)

	require.Equal(t, http.StatusSeeOther, h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[1],
		url.Values{"csrf_token": {csrf}, "name": {"Doing"}, "color": {"blue"},
			"wip_limit": {"1"}}, cookie).Code)

	h.addCard(t, cookie, csrf, p, lanes[1], "resident")
	moving := h.addCard(t, cookie, csrf, p, lanes[0], "moving")

	rec := h.postForm("/cards/"+moving+"/move-to", url.Values{
		"csrf_token": {csrf}, "to_lane": {lanes[1]},
	}, cookie)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, body(rec), "already at its limit")
	assert.Equal(t, []string{"moving"}, h.laneOrder(t, cookie, lanes[0]))
}

func TestLabelManagementIsManagerOnly(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Rich Cards")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	bug := h.makeLabel(t, ownerCookie, ownerCSRF, p, "bug", "rose")

	cookie, csrf := h.signIn(t, "member@example.com")
	base := "/projects/" + p.Slug

	for name, form := range map[string]struct {
		path string
		vals url.Values
	}{
		"create": {base + "/labels", url.Values{"name": {"sneaky"}, "color": {"blue"}}},
		"rename": {base + "/labels/" + bug, url.Values{"name": {"renamed"}, "color": {"blue"}}},
		"delete": {base + "/labels/" + bug + "/delete", url.Values{}},
	} {
		t.Run(name+" is refused to a member", func(t *testing.T) {
			vals := form.vals
			vals.Set("csrf_token", csrf)
			assert.Equal(t, http.StatusForbidden, h.postForm(form.path, vals, cookie).Code)
		})
	}

	t.Run("but a member may attach one to a card", func(t *testing.T) {
		lanes := h.laneUUIDs(t, owner, p)
		rec := h.postForm(base+"/lanes/"+lanes[0]+"/cards", url.Values{
			"csrf_token": {csrf}, "title": {"labelled by a member"}, "labels": {bug},
		}, cookie)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body(rec), ">bug<")
	})

	t.Run("and a duplicate name is a conflict", func(t *testing.T) {
		rec := h.postForm(base+"/labels", url.Values{
			"csrf_token": {ownerCSRF}, "name": {"bug"}, "color": {"blue"},
		}, ownerCookie)
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
}

func TestDeletingALabelRemovesItFromEveryCard(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Rich Cards")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	bug := h.makeLabel(t, cookie, csrf, p, "bug", "rose")

	rec := h.postForm("/projects/"+p.Slug+"/lanes/"+lanes[0]+"/cards", url.Values{
		"csrf_token": {csrf}, "title": {"a card"}, "labels": {bug},
	}, cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, body(rec), ">bug<")

	require.Equal(t, http.StatusSeeOther, h.postForm(
		"/projects/"+p.Slug+"/labels/"+bug+"/delete",
		url.Values{"csrf_token": {csrf}}, cookie).Code)

	assert.NotContains(t, body(h.get("/projects/"+p.Slug, cookie)), ">bug<",
		"the assignment cascades with the label")
}

// ---------------------------------------------------------------------------
// Phase 6: the live board.
// ---------------------------------------------------------------------------

// watch subscribes to a board's stream the way a browser does, and returns the frames
// it receives plus a func to stop.
//
// Over a real httptest server rather than a ResponseRecorder. A recorder cannot be
// streamed: reading its Body while the handler still writes to it is a data race, which
// -race duly caught. A real server gives a real incremental reader, which is also what
// the thing under test actually faces.
func (h *boardHarness) watch(
	t *testing.T, cookie *http.Cookie, p *projectdomain.Project, clientID string,
) (frames <-chan string, stop func()) {
	t.Helper()

	server := httptest.NewServer(h.router)

	ctx, cancel := context.WithCancel(context.Background())
	r, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/projects/"+p.Slug+"/events?client="+url.QueryEscape(clientID), nil)
	require.NoError(t, err)
	r.AddCookie(cookie)

	resp, err := server.Client().Do(r)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out := make(chan string, 16)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(out)
		defer func() { _ = resp.Body.Close() }()

		// Frames are separated by a blank line. Comment lines (the heartbeat) start
		// with a colon and are skipped.
		reader := bufio.NewReader(resp.Body)
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				if strings.TrimRight(line, "\r\n") == "" {
					if f := strings.TrimSpace(frame.String()); f != "" &&
						!strings.HasPrefix(f, ":") {
						select {
						case out <- f:
						case <-ctx.Done():
							return
						}
					}
					frame.Reset()
				} else {
					frame.WriteString(line)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// The subscription registers inside the handler, so wait for it before the caller
	// broadcasts — otherwise the event is sent to an empty room.
	waitFor(t, func() bool { return h.hub.Subscribers(p.UUID) > 0 })

	return out, func() {
		cancel()
		server.Close()
		<-done
	}
}

// waitFor polls a condition, so a test never sleeps a fixed amount and hopes.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition was not met within two seconds")
}

// nextEvent waits for one frame.
func nextEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case ev, open := <-events:
		require.True(t, open, "the stream closed before an event arrived")
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived within two seconds")
		return ""
	}
}

func TestAMoveReachesAnotherTab(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	events, stop := h.watch(t, cookie, p, "watching-tab")
	defer stop()

	// Moved by a different tab, so the watcher is not the originator.
	r := httptest.NewRequest(http.MethodPost, "/cards/"+cardUUID+"/move",
		strings.NewReader(url.Values{
			"to_lane": {lanes[1]}, "to_order": {cardUUID}, "from_lane": {lanes[0]},
		}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(middleware.CSRFHeader, csrf)
	r.Header.Set("X-Client-Id", "moving-tab")
	r.AddCookie(cookie)
	require.Equal(t, http.StatusNoContent, h.do(r).Code)

	frame := nextEvent(t, events)
	assert.Contains(t, frame, "event: lane-updated")
	assert.Contains(t, frame, "hx-swap-oob",
		"a card change is broadcast as out-of-band lane fragments")
	assert.Contains(t, frame, "a card")
	assert.Contains(t, frame, lanes[0], "the lane it left")
	assert.Contains(t, frame, lanes[1], "and the lane it went to")
}

func TestATabNeverReceivesItsOwnChange(t *testing.T) {
	// Otherwise htmx would swap a card out from under the hand that just dropped it.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")

	events, stop := h.watch(t, cookie, p, "the-only-tab")
	defer stop()

	// The same client id the watcher subscribed with.
	r := httptest.NewRequest(http.MethodPost, "/cards/"+cardUUID+"/move",
		strings.NewReader(url.Values{
			"to_lane": {lanes[1]}, "to_order": {cardUUID}, "from_lane": {lanes[0]},
		}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(middleware.CSRFHeader, csrf)
	r.Header.Set("X-Client-Id", "the-only-tab")
	r.AddCookie(cookie)
	require.Equal(t, http.StatusNoContent, h.do(r).Code)

	select {
	case frame, open := <-events:
		if open && strings.Contains(frame, "lane-updated") {
			t.Fatalf("the originating tab received its own change: %s", frame)
		}
	case <-time.After(250 * time.Millisecond):
		// Nothing arrived, which is the point.
	}
}

func TestAStructuralChangeBroadcastsBoardDirty(t *testing.T) {
	// A lane arriving out of band cannot *relocate* an element, so an added or
	// reordered lane cannot be expressed as an out-of-band swap at all. The signal
	// tells the board to re-fetch itself whole.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)
	base := "/projects/" + p.Slug

	changes := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"add a lane", func(t *testing.T) {
			require.Equal(t, http.StatusSeeOther, h.postForm(base+"/lanes", url.Values{
				"csrf_token": {csrf}, "name": {"Blocked"}, "color": {"rose"},
			}, cookie).Code)
		}},
		{"rename a lane", func(t *testing.T) {
			lanes := h.laneUUIDs(t, owner, p)
			require.Equal(t, http.StatusSeeOther, h.postForm(base+"/lanes/"+lanes[0],
				url.Values{"csrf_token": {csrf}, "name": {"Renamed"}, "color": {"blue"}},
				cookie).Code)
		}},
		{"reorder lanes", func(t *testing.T) {
			lanes := h.laneUUIDs(t, owner, p)
			require.Equal(t, http.StatusSeeOther, h.postForm(
				base+"/lanes/"+lanes[0]+"/move",
				url.Values{"csrf_token": {csrf}, "direction": {"down"}}, cookie).Code)
		}},
		{"delete a lane", func(t *testing.T) {
			lanes := h.laneUUIDs(t, owner, p)
			require.Equal(t, http.StatusSeeOther, h.postForm(
				base+"/lanes/"+lanes[0]+"/delete",
				url.Values{"csrf_token": {csrf}, "move_to": {""}}, cookie).Code)
		}},
		{"add a label", func(t *testing.T) {
			require.Equal(t, http.StatusSeeOther, h.postForm(base+"/labels", url.Values{
				"csrf_token": {csrf}, "name": {"bug"}, "color": {"rose"},
			}, cookie).Code)
		}},
		{"rename the board", func(t *testing.T) {
			require.Equal(t, http.StatusSeeOther, h.postForm(base+"/settings", url.Values{
				"csrf_token": {csrf}, "name": {"Renamed Board"},
			}, cookie).Code)
		}},
	}

	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			events, stop := h.watch(t, cookie, p, "watching-tab")
			defer stop()

			c.do(t)

			frame := nextEvent(t, events)
			assert.Contains(t, frame, "event: board-dirty")
		})
	}
}

func TestClosingATabDropsItsSubscriber(t *testing.T) {
	// The phase gate: the room count returns to zero. A subscriber that is never
	// removed costs a goroutine and a channel and shows up nowhere.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")

	require.Zero(t, h.hub.Subscribers(p.UUID))

	_, stopA := h.watch(t, cookie, p, "tab-a")
	_, stopB := h.watch(t, cookie, p, "tab-b")
	waitFor(t, func() bool { return h.hub.Subscribers(p.UUID) == 2 })

	stopA()
	waitFor(t, func() bool { return h.hub.Subscribers(p.UUID) == 1 })

	stopB()
	waitFor(t, func() bool { return h.hub.Rooms() == 0 })
	assert.Zero(t, h.hub.Subscribers(p.UUID))
}

func TestTheStreamRequiresAClientId(t *testing.T) {
	// Without one there is no way to skip the originating tab, so a move would swap a
	// card out from under the hand that dropped it.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")

	rec := h.get("/projects/"+p.Slug+"/events", cookie)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, h.hub.Subscribers(p.UUID), "and nothing was subscribed")
}

func TestTheStreamNeedsAccessToTheBoard(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	h.addUser(t, "stranger@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Private Board")

	cookie, _ := h.signIn(t, "stranger@example.com")
	rec := h.get("/projects/"+p.Slug+"/events?client=tab-a", cookie)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Zero(t, h.hub.Subscribers(p.UUID))
}

func TestTheBoardCarriesBothDirtyTriggers(t *testing.T) {
	// The server's signal and the one board.js fires after a reconnect converge on one
	// re-fetch, rather than two code paths that can drift.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Live Board")
	cookie, _ := h.signIn(t, "owner@example.com")

	page := body(h.get("/projects/"+p.Slug, cookie))

	assert.Contains(t, page, `hx-trigger="sse:board-dirty, board-dirty"`)
	assert.Contains(t, page, `hx-get="/projects/`+p.Slug+`/board"`)
	assert.Contains(t, page, `sse-connect="/projects/`+p.UUID+`/events`)
}

func TestLiveStatusIsOperatorOnly(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Live Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	t.Run("a member gets 404, not 403", func(t *testing.T) {
		// A debug surface should not advertise itself.
		cookie, _ := h.signIn(t, "member@example.com")
		assert.Equal(t, http.StatusNotFound, h.get("/debug/live", cookie).Code)
	})

	t.Run("the operator sees the counts", func(t *testing.T) {
		cookie, _ := h.signIn(t, "owner@example.com")

		empty := body(h.get("/debug/live", cookie))
		assert.Contains(t, empty, `"rooms":0`)
		assert.Contains(t, empty, `"total_subscribers":0`)

		_, stop := h.watch(t, cookie, p, "tab-a")
		defer stop()

		withOne := body(h.get("/debug/live", cookie))
		assert.Contains(t, withOne, `"rooms":1`)
		assert.Contains(t, withOne, `"total_subscribers":1`)
		assert.Contains(t, withOne, p.UUID)
	})
}

// TestEveryRenderedFormCarriesACSRFToken is a regression guard for a bug that shipped
// in phase 2 and survived four phases.
//
// The sign-out form in the topbar had no `csrf_token` field. `hx-headers` on <body>
// only applies to HTMX requests, so a plain form post carried nothing and the CSRF
// middleware refused it — **clicking "Sign out" was a 403**. Every unit and route test
// passed, because every one of them sent the token explicitly.
//
// The check has to be on *rendered output*, not on source, and on every page: a form
// added without a token looks correct in the template and fails only when a person
// clicks it.
func TestEveryRenderedFormCarriesACSRFToken(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	p := h.makeProject(t, owner, "Forms Board")
	cookie, csrf := h.signIn(t, "owner@example.com")
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")
	label := h.makeLabel(t, cookie, csrf, p, "bug", "rose")
	base := "/projects/" + p.Slug

	pages := map[string]string{
		"the board":            base,
		"project settings":     base + "/settings",
		"the lane form":        base + "/lanes/new",
		"the lane edit form":   base + "/lanes/" + lanes[0] + "/edit",
		"the lane delete form": base + "/lanes/" + lanes[0] + "/delete",
		"the label edit form":  base + "/labels/" + label + "/edit",
		"the project list":     "/",
		"the new project form": "/projects/new",
		"the account page":     auth.AccountPath,
		"the card modal":       "/cards/" + cardUUID,
		"the card edit form":   "/cards/" + cardUUID + "/edit",
		"the admin accounts":   "/admin/users",
		"the admin projects":   "/admin/projects",
	}

	for name, path := range pages {
		t.Run(name, func(t *testing.T) {
			rec := h.get(path, cookie)
			require.Equal(t, http.StatusOK, rec.Code)

			forms := formsIn(body(rec))
			require.NotEmpty(t, forms, "expected at least one form on %s", path)

			for _, f := range forms {
				if !strings.Contains(strings.ToLower(f.attrs), `method="post"`) {
					continue
				}
				// An htmx-driven form sends the token as a header, via
				// hx-headers on <body>.
				if strings.Contains(f.attrs, "hx-") {
					continue
				}
				assert.Contains(t, f.inner, `name="csrf_token"`,
					"a plain POST form on %s has no CSRF field — clicking it would 403:\n<form%s>",
					path, f.attrs)
			}
		})
	}
}

// formBlock is one <form> element: its attributes and its contents.
type formBlock struct{ attrs, inner string }

// formsIn finds every form in rendered HTML.
//
// Scanned by hand rather than with a regexp: Go's RE2 has no negative lookahead, so
// there is no expression for "everything up to the *next* </form>" — and a greedy one
// would swallow several forms into a single match and then pass because one of them had
// a token.
func formsIn(html string) []formBlock {
	var out []formBlock
	rest := html
	for {
		open := strings.Index(rest, "<form")
		if open < 0 {
			return out
		}
		rest = rest[open+len("<form"):]

		endAttrs := strings.Index(rest, ">")
		if endAttrs < 0 {
			return out
		}
		attrs := rest[:endAttrs]
		rest = rest[endAttrs+1:]

		close := strings.Index(rest, "</form>")
		if close < 0 {
			// Unclosed: record what there is, so a truncated page still fails
			// loudly rather than silently passing.
			out = append(out, formBlock{attrs: attrs, inner: rest})
			return out
		}
		out = append(out, formBlock{attrs: attrs, inner: rest[:close]})
		rest = rest[close+len("</form>"):]
	}
}

func TestSigningOutFromTheTopbarWorks(t *testing.T) {
	// The specific case the bug broke: the form the person actually clicks, posted
	// exactly as a browser would — the token from the page, nothing in a header.
	h := newBoardHarness(t)
	h.addSuperAdmin(t, "owner@example.com")
	cookie, _ := h.signIn(t, "owner@example.com")

	page := body(h.get("/", cookie))
	match := regexp.MustCompile(
		`(?s)<form[^>]*action="/logout"[^>]*>.*?name="csrf_token" value="([^"]+)"`).
		FindStringSubmatch(page)
	require.Len(t, match, 2, "the sign-out form must carry a token")

	rec := h.postForm(auth.LogoutPath, url.Values{"csrf_token": {match[1]}}, cookie)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
	assert.Equal(t, http.StatusSeeOther, h.get("/", cookie).Code, "the session is gone")
}

// ---------------------------------------------------------------------------
// Phase 7: the admin console.
// ---------------------------------------------------------------------------

// revealedPassword pulls the one-time password out of a console response.
//
// It matches the rendered <code> block, because that is the only place the value ever
// exists — there is no API, no log line and no row to read it from.
var revealedPassword = regexp.MustCompile(`tracking-wide"\s*>([A-Za-z0-9]{20})</code>`)

func revealOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := revealedPassword.FindStringSubmatch(body(rec))
	require.Len(t, m, 2, "no one-time password was revealed")
	return m[1]
}

// TestTheOwnerFlowEndToEnd is the phase gate, in one test, in order.
//
// Create an account, note the one-time password, grant a project, watch that person sign
// in and be forced to change it, reset it, watch their old session die, suspend them,
// watch them locked out mid-session, reinstate them.
func TestTheOwnerFlowEndToEnd(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	ownerCookie, _ := h.signIn(t, "owner@example.com")
	ownerCSRF := h.csrfFor(t, ownerCookie)
	p := h.makeProject(t, owner, "Shared Board")

	// --- create ---
	rec := h.postForm("/admin/users", url.Values{
		"csrf_token":   {ownerCSRF},
		"display_name": {"A Team Member"},
		"email":        {"member@example.com"},
	}, ownerCookie)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body(rec), "copy this password now")
	firstPassword := revealOf(t, rec)

	t.Run("the password is revealed exactly once", func(t *testing.T) {
		// Not in the page on a fresh load, and not anywhere it could be recovered:
		// no redirect carries it, and nothing stores it.
		assert.NotContains(t, body(h.get("/admin/users", ownerCookie)), firstPassword)

		user, err := h.store.UserByEmail(h.ctx, "member@example.com")
		require.NoError(t, err)
		assert.NotEqual(t, firstPassword, user.PasswordHash,
			"the password went through a hasher")
		assert.True(t, user.MustChangePassword,
			"a handed-over credential cannot stay in use")

		// This harness uses password.NewFake, which encodes as "fake$<plaintext>" on
		// purpose — so "the stored value does not contain the plaintext" cannot be
		// asserted here and would pass for the wrong reason if it were. That property
		// belongs to the real hasher: internal/shared/password asserts the PHC form,
		// and the phase-7 walkthrough confirmed an Argon2id hash with the plaintext
		// present in no table.
	})

	memberUser, err := h.store.UserByEmail(h.ctx, "member@example.com")
	require.NoError(t, err)

	// --- grant ---
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	// --- they sign in, and are held ---
	loginRec := h.postForm(auth.LoginPath, url.Values{
		"email": {"member@example.com"}, "password": {firstPassword},
	}, nil)
	require.Equal(t, http.StatusSeeOther, loginRec.Code)
	assert.Equal(t, auth.PasswordPath, loginRec.Header().Get("Location"),
		"sign-in sends them straight to the password page")

	memberCookie := sessionCookie(loginRec)
	require.NotNil(t, memberCookie)

	t.Run("they cannot reach the board until they replace it", func(t *testing.T) {
		rec := h.get("/projects/"+p.Slug, memberCookie)
		assert.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, auth.PasswordPath, rec.Header().Get("Location"))
	})

	memberCSRF := h.csrfFor(t, memberCookie)
	changed := h.postForm(auth.PasswordPath, url.Values{
		"csrf_token":       {memberCSRF},
		"current_password": {firstPassword},
		"new_password":     {"their own password"},
	}, memberCookie)
	// Let straight through, because they were held here rather than choosing to come.
	require.Equal(t, http.StatusSeeOther, changed.Code)
	require.Equal(t, "/", changed.Header().Get("Location"))
	require.Equal(t, http.StatusOK, h.get("/projects/"+p.Slug, memberCookie).Code)

	// --- reset ---
	resetRec := h.postForm("/admin/users/"+memberUser.UUID+"/password",
		url.Values{"csrf_token": {ownerCSRF}}, ownerCookie)
	require.Equal(t, http.StatusOK, resetRec.Code)
	assert.Contains(t, body(resetRec), "Password reset")
	secondPassword := revealOf(t, resetRec)
	assert.NotEqual(t, firstPassword, secondPassword)

	t.Run("the reset kills every session they hold", func(t *testing.T) {
		// A reset is what an operator does when a credential may be compromised. A
		// session that outlived it would survive the very thing meant to end it.
		rec := h.get("/projects/"+p.Slug, memberCookie)
		assert.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, auth.LoginPath, rec.Header().Get("Location"))
	})

	t.Run("the old password no longer works, the new one does", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, h.postForm(auth.LoginPath, url.Values{
			"email": {"member@example.com"}, "password": {"their own password"},
		}, nil).Code)

		rec := h.postForm(auth.LoginPath, url.Values{
			"email": {"member@example.com"}, "password": {secondPassword},
		}, nil)
		require.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, auth.PasswordPath, rec.Header().Get("Location"),
			"and it forces a change again")
	})

	// Sign in properly again, so there is a live session to suspend.
	memberCookie, _ = h.signInWith(t, "member@example.com", secondPassword)
	memberCSRF = h.csrfFor(t, memberCookie)
	require.Equal(t, http.StatusSeeOther, h.postForm(auth.PasswordPath, url.Values{
		"csrf_token":       {memberCSRF},
		"current_password": {secondPassword},
		"new_password":     {"a third password"},
	}, memberCookie).Code)
	require.Equal(t, http.StatusOK, h.get("/projects/"+p.Slug, memberCookie).Code)

	// --- suspend ---
	t.Run("suspending locks them out mid-session", func(t *testing.T) {
		rec := h.postForm("/admin/users/"+memberUser.UUID+"/suspend",
			url.Values{"csrf_token": {ownerCSRF}}, ownerCookie)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		board := h.get("/projects/"+p.Slug, memberCookie)
		assert.Equal(t, http.StatusSeeOther, board.Code)
		assert.Equal(t, auth.LoginPath, board.Header().Get("Location"))

		assert.Equal(t, http.StatusForbidden, h.postForm(auth.LoginPath, url.Values{
			"email": {"member@example.com"}, "password": {"a third password"},
		}, nil).Code, "and they cannot sign in again")

		assert.Contains(t, body(h.get("/admin/users", ownerCookie)), ">Suspended<")
	})

	// --- reinstate ---
	t.Run("reinstating lets them back in", func(t *testing.T) {
		require.Equal(t, http.StatusSeeOther, h.postForm(
			"/admin/users/"+memberUser.UUID+"/reinstate",
			url.Values{"csrf_token": {ownerCSRF}}, ownerCookie).Code)

		rec := h.postForm(auth.LoginPath, url.Values{
			"email": {"member@example.com"}, "password": {"a third password"},
		}, nil)
		require.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, "/", rec.Header().Get("Location"))
	})
}

// signInWith is signIn for a password other than the harness default.
func (h *harness) signInWith(t *testing.T, email, password string) (*http.Cookie, string) {
	t.Helper()
	rec := h.postForm(auth.LoginPath, url.Values{
		"email": {email}, "password": {password},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	cookie := sessionCookie(rec)
	require.NotNil(t, cookie)
	_, session, err := h.service.Authenticate(h.ctx, cookie.Value)
	require.NoError(t, err)
	return cookie, session.CSRFToken
}

// TestTheConsoleIsInvisibleToAMember is the wall. 404 everywhere, never 403 — a member
// should not learn this surface exists, including from a redirect.
func TestTheConsoleIsInvisibleToAMember(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Shared Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, csrf := h.signIn(t, "member@example.com")

	reads := []string{
		"/admin",
		"/admin/users",
		"/admin/projects",
		"/admin/users/" + owner.UUID + "/edit",
		"/debug/live",
	}
	for _, path := range reads {
		t.Run("GET "+path, func(t *testing.T) {
			assert.Equal(t, http.StatusNotFound, h.get(path, cookie).Code,
				"404, never 403 and never a redirect that confirms the route exists")
		})
	}

	writes := map[string]url.Values{
		"/admin/users": {
			"display_name": {"Sneaky"}, "email": {"sneaky@example.com"},
			"is_operator": {"1"},
		},
		"/admin/users/" + owner.UUID:               {"display_name": {"X"}, "email": {"x@example.com"}},
		"/admin/users/" + owner.UUID + "/password": {},
		"/admin/users/" + owner.UUID + "/suspend":  {},
		"/admin/users/" + owner.UUID + "/sign-out": {},
		"/admin/users/" + memberUser.UUID:          {"display_name": {"Me"}, "email": {"member@example.com"}, "is_operator": {"1"}},
	}
	for path, form := range writes {
		t.Run("POST "+path, func(t *testing.T) {
			vals := form
			vals.Set("csrf_token", csrf)
			assert.Equal(t, http.StatusNotFound, h.postForm(path, vals, cookie).Code)
		})
	}

	t.Run("and nothing changed", func(t *testing.T) {
		users, err := h.store.ListUsers(h.ctx)
		require.NoError(t, err)
		assert.Len(t, users, 2, "no account was created")
		for _, u := range users {
			if u.Email == "member@example.com" {
				assert.False(t, u.IsSuperAdmin(), "they did not promote themselves")
			}
			assert.True(t, u.CanSignIn(), "nobody was suspended")
		}
	})

	t.Run("and the topbar offers no admin link", func(t *testing.T) {
		assert.NotContains(t, body(h.get("/", cookie)), `href="/admin"`)

		ownerCookie, _ := h.signIn(t, "owner@example.com")
		assert.Contains(t, body(h.get("/", ownerCookie)), `href="/admin"`,
			"but the operator's does")
	})
}

func TestTheOperatorCannotLockThemselvesOut(t *testing.T) {
	// Both of these would leave the installation with nobody able to administer it,
	// recoverable only by editing the database by hand.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)

	t.Run("suspending their own account", func(t *testing.T) {
		rec := h.postForm("/admin/users/"+owner.UUID+"/suspend",
			url.Values{"csrf_token": {csrf}}, cookie)

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, body(rec), "cannot suspend your own account")
	})

	t.Run("demoting the last owner", func(t *testing.T) {
		rec := h.postForm("/admin/users/"+owner.UUID, url.Values{
			"csrf_token": {csrf}, "display_name": {"Owner"},
			"email": {"owner@example.com"}, // is_operator absent = demote
		}, cookie)

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, body(rec), "only owner account")
	})

	t.Run("still an active operator afterwards", func(t *testing.T) {
		reloaded, err := h.store.UserByUUID(h.ctx, owner.UUID)
		require.NoError(t, err)
		assert.True(t, reloaded.IsSuperAdmin())
		assert.True(t, reloaded.CanSignIn())
	})

	t.Run("allowed once there is a second owner", func(t *testing.T) {
		rec := h.postForm("/admin/users", url.Values{
			"csrf_token": {csrf}, "display_name": {"Second Owner"},
			"email": {"second@example.com"}, "is_operator": {"1"},
		}, cookie)
		require.Equal(t, http.StatusOK, rec.Code)

		second, err := h.store.UserByEmail(h.ctx, "second@example.com")
		require.NoError(t, err)
		require.True(t, second.IsSuperAdmin())

		// Now demoting the first is fine.
		assert.Equal(t, http.StatusSeeOther, h.postForm("/admin/users/"+owner.UUID,
			url.Values{
				"csrf_token": {csrf}, "display_name": {"Owner"},
				"email": {"owner@example.com"},
			}, cookie).Code)
	})
}

func TestTheConsoleShowsTheStateOfEachAccount(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Shared Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, _ := h.signIn(t, "owner@example.com")
	page := body(h.get("/admin/users", cookie))

	assert.Contains(t, page, "owner@example.com")
	assert.Contains(t, page, "member@example.com")
	assert.Contains(t, page, ">Owner<", "the operator is marked")
	assert.Contains(t, page, "only one owner account",
		"and a single-owner installation is warned about — nobody could reset its password")

	t.Run("a handover that has not completed is visible", func(t *testing.T) {
		csrf := h.csrfFor(t, cookie)
		rec := h.postForm("/admin/users", url.Values{
			"csrf_token": {csrf}, "display_name": {"Fresh Account"},
			"email": {"fresh@example.com"},
		}, cookie)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body(rec), "Handover pending")
	})
}

func TestSignOutEverywhereEndsSessionsWithoutChangingThePassword(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Shared Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	memberCookie, _ := h.signIn(t, "member@example.com")
	require.Equal(t, http.StatusOK, h.get("/projects/"+p.Slug, memberCookie).Code)

	ownerCookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, ownerCookie)

	require.Equal(t, http.StatusSeeOther, h.postForm(
		"/admin/users/"+memberUser.UUID+"/sign-out",
		url.Values{"csrf_token": {csrf}}, ownerCookie).Code)

	assert.Equal(t, http.StatusSeeOther, h.get("/projects/"+p.Slug, memberCookie).Code,
		"their session is gone")

	// The password is untouched, so they can simply sign in again.
	rec := h.postForm(auth.LoginPath, url.Values{
		"email": {"member@example.com"}, "password": {testPassword},
	}, nil)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
}

func TestAdminUserValidation(t *testing.T) {
	h := newBoardHarness(t)
	h.addSuperAdmin(t, "owner@example.com")
	h.addUser(t, "taken@example.com", authdomain.RoleMember)
	cookie, _ := h.signIn(t, "owner@example.com")
	csrf := h.csrfFor(t, cookie)

	tests := map[string]struct {
		form url.Values
		want int
		body string
	}{
		"a duplicate email": {
			// Reported on the field, not as a headline: the person can fix it by
			// typing a different address.
			form: url.Values{"display_name": {"X"}, "email": {"taken@example.com"}},
			want: http.StatusConflict, body: "is already taken",
		},
		"a malformed email": {
			form: url.Values{"display_name": {"X"}, "email": {"not-an-address"}},
			want: http.StatusBadRequest, body: "",
		},
		"no name": {
			form: url.Values{"display_name": {"   "}, "email": {"new@example.com"}},
			want: http.StatusBadRequest, body: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			form := tc.form
			form.Set("csrf_token", csrf)

			rec := h.postForm("/admin/users", form, cookie)

			assert.Equal(t, tc.want, rec.Code)
			if tc.body != "" {
				assert.Contains(t, body(rec), tc.body)
			}
			// What was typed comes back, rather than being thrown away.
			assert.Contains(t, body(rec), tc.form.Get("email"))
			assert.NotRegexp(t, revealedPassword, body(rec),
				"a refused create must not reveal a password")
		})
	}
}

func TestTheAdminProjectsPageAnswersWhoCanReachWhat(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)

	granted := h.makeProject(t, owner, "Granted Board")
	h.makeProject(t, owner, "Owner Only Board")
	h.grant(t, owner, granted, memberUser, projectdomain.RoleMember)

	cookie, _ := h.signIn(t, "owner@example.com")
	page := body(h.get("/admin/projects", cookie))

	assert.Contains(t, page, "Granted Board")
	assert.Contains(t, page, "Owner Only Board")

	// Grants are listed against the board they are on. Both accounts share a display
	// name in this harness, so the address is what distinguishes them — which is why
	// the chip carries it as a title.
	assert.Contains(t, page, `title="member@example.com"`,
		"the granted member is listed against the board")
	assert.Contains(t, page, `title="owner@example.com"`)
	assert.Contains(t, page, ">manager<")
	assert.Contains(t, page, ">member<")

	// Every board has at least its creator, who is granted manager when it is
	// created — so there is no grant-less board to show the empty state for.
	assert.NotContains(t, page, "Nobody has been granted this board.")
}

// TestNoRenderedPageUsesAnInlineEventHandler guards a whole class of bug that only a
// browser would reveal.
//
// The CSP is `script-src 'self' 'unsafe-eval'` with no `'unsafe-inline'`, so an
// `onsubmit=` or `onchange=` attribute is blocked and silently never runs. Four delete
// confirmations never appeared, and the member role picker — an `onchange` that submitted
// its form — did nothing at all. None of it produced an error the server could see, so
// every server-side test passed.
//
// Behaviour of that kind belongs in board.js, which is `'self'` and therefore allowed.
func TestNoRenderedPageUsesAnInlineEventHandler(t *testing.T) {
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Handlers Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, csrf := h.signIn(t, "owner@example.com")
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")
	label := h.makeLabel(t, cookie, csrf, p, "bug", "rose")
	base := "/projects/" + p.Slug

	paths := map[string]string{
		"the board":            base,
		"project settings":     base + "/settings",
		"the lane form":        base + "/lanes/new",
		"the lane delete form": base + "/lanes/" + lanes[0] + "/delete",
		"the label edit form":  base + "/labels/" + label + "/edit",
		"the card modal":       "/cards/" + cardUUID,
		"the card edit form":   "/cards/" + cardUUID + "/edit",
		"the project list":     "/",
		"the account page":     auth.AccountPath,
		"the admin accounts":   "/admin/users",
		"the admin projects":   "/admin/projects",
	}

	// Any attribute whose name begins with "on" and is followed by "=" is an inline
	// handler. Matched loosely on purpose: a new one should fail this test whatever it
	// is called.
	inline := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			rec := h.get(path, cookie)
			require.Equal(t, http.StatusOK, rec.Code)
			assertNoInlineHandlers(t, path, inline, body(rec))
		})
	}

	t.Run("the login page", func(t *testing.T) {
		// Anonymously: an authenticated request to /login is redirected away.
		rec := h.get(auth.LoginPath, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assertNoInlineHandlers(t, auth.LoginPath, inline, body(rec))
	})
}

// assertNoInlineHandlers reports every inline handler on a page at once, so one run
// names them all rather than stopping at the first.
//
// Alpine's own directives (x-data, @click, :class) deliberately do not match: they are
// not `on*` attributes, and Alpine evaluates them with new Function — which is the one
// reason the CSP carries 'unsafe-eval'.
func assertNoInlineHandlers(t *testing.T, path string, inline *regexp.Regexp, page string) {
	t.Helper()
	found := inline.FindAllString(page, -1)
	assert.Empty(t, found,
		"%s carries inline event handler(s) %v — the CSP blocks them, so they silently "+
			"never run. Use a delegated listener in board.js.",
		path, found)
}

func TestTheCSPForbidsInlineScript(t *testing.T) {
	// The other half of the guard above: if this ever gains 'unsafe-inline', the test
	// above stops meaning anything, so the policy itself is asserted.
	h := newBoardHarness(t)
	h.addSuperAdmin(t, "owner@example.com")

	csp := h.get(auth.LoginPath, nil).Header().Get("Content-Security-Policy")

	require.Contains(t, csp, "script-src")
	assert.NotContains(t, csp, "unsafe-inline",
		"inline handlers and inline <script> must stay blocked")
	assert.Contains(t, csp, "form-action 'self'")
	assert.Contains(t, csp, "base-uri 'none'")
	assert.Contains(t, csp, "object-src 'none'")
}

func TestDelegatedBehavioursAreDeclaredWhereTheyAreNeeded(t *testing.T) {
	// The replacement for the inline handlers: a data attribute the delegated listener
	// in board.js picks up. Asserting the markup carries it is what stops the
	// behaviour being quietly dropped when a template is rewritten.
	h := newBoardHarness(t)
	owner := h.addSuperAdmin(t, "owner@example.com")
	memberUser := h.addUser(t, "member@example.com", authdomain.RoleMember)
	p := h.makeProject(t, owner, "Handlers Board")
	h.grant(t, owner, p, memberUser, projectdomain.RoleMember)

	cookie, csrf := h.signIn(t, "owner@example.com")
	lanes := h.laneUUIDs(t, owner, p)
	cardUUID := h.addCard(t, cookie, csrf, p, lanes[0], "a card")
	h.makeLabel(t, cookie, csrf, p, "bug", "rose")

	t.Run("destructive actions ask first", func(t *testing.T) {
		for name, path := range map[string]string{
			"card delete":   "/cards/" + cardUUID,
			"label delete":  "/projects/" + p.Slug + "/settings",
			"account reset": "/admin/users",
		} {
			assert.Contains(t, body(h.get(path, cookie)), "data-confirm=",
				"%s should ask before it happens", name)
		}
	})

	t.Run("the role picker submits on change", func(t *testing.T) {
		page := body(h.get("/projects/"+p.Slug+"/settings", cookie))
		assert.Contains(t, page, "data-autosubmit")
		assert.Contains(t, page, "<noscript>",
			"and there is a button for when the script has not loaded")
	})
}

// TestChangingAPasswordGoesWhereTheReasonWas distinguishes the two ways somebody reaches
// the password page, which want different answers.
//
// The original handler always rendered a confirmation. For somebody who chose to visit
// /account that is right — they should see that it worked. For somebody the gate *sent*
// there it is wrong: they have done the one thing it was waiting for, and being left
// looking at the same form reads as though nothing happened. That was a real report.
func TestChangingAPasswordGoesWhereTheReasonWas(t *testing.T) {
	t.Run("held there by the gate: let through to the application", func(t *testing.T) {
		h := newBoardHarness(t)
		user := h.addUser(t, "held@example.com", authdomain.RoleMember)
		user.MustChangePassword = true
		require.NoError(t, h.store.UpdateUser(h.ctx, user))

		cookie, csrf := h.signIn(t, "held@example.com")

		rec := h.postForm(auth.PasswordPath, url.Values{
			"csrf_token":       {csrf},
			"current_password": {testPassword},
			"new_password":     {"a brand new password"},
		}, cookie)

		require.Equal(t, http.StatusSeeOther, rec.Code)
		assert.Equal(t, "/", rec.Header().Get("Location"))
		assert.Equal(t, http.StatusOK, h.get("/", cookie).Code,
			"and the gate is lifted")
	})

	t.Run("there by choice: stays, and sees that it worked", func(t *testing.T) {
		h := newBoardHarness(t)
		h.addUser(t, "willing@example.com", authdomain.RoleMember)
		cookie, csrf := h.signIn(t, "willing@example.com")

		rec := h.postForm(auth.PasswordPath, url.Values{
			"csrf_token":       {csrf},
			"current_password": {testPassword},
			"new_password":     {"a brand new password"},
		}, cookie)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body(rec), "Password changed")
		assert.NotContains(t, body(rec), "Choose your own password",
			"the forced-change banner is gone")
	})

	t.Run("a rejected change keeps the banner while still held", func(t *testing.T) {
		h := newBoardHarness(t)
		user := h.addUser(t, "held@example.com", authdomain.RoleMember)
		user.MustChangePassword = true
		require.NoError(t, h.store.UpdateUser(h.ctx, user))

		cookie, csrf := h.signIn(t, "held@example.com")

		rec := h.postForm(auth.PasswordPath, url.Values{
			"csrf_token":       {csrf},
			"current_password": {"not the right one"},
			"new_password":     {"a brand new password"},
		}, cookie)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, body(rec), "Choose your own password",
			"they are still held, so the banner stays")
	})
}
