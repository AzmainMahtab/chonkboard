package auth

import "net/http"

// CookieName is the session cookie. The chonk_ prefix keeps it distinguishable
// from anything else on a shared host.
//
// The cookie lives in this package rather than in platform/middleware because it
// *is* the session, and the session is this slice's. The middleware imports this;
// nothing here imports the middleware.
const CookieName = "chonk_session"

// SetSessionCookie writes the session cookie.
//
// HttpOnly so an injected script cannot read it. SameSite=Lax so it is not sent on
// a cross-site POST — which narrows CSRF without closing it, hence the token check
// as well. Path=/ so it covers every route. MaxAge matches the session's own
// expiry, so the browser discards it at the moment the server would refuse it.
//
// secure comes from configuration and is never hard-coded: it must be on behind
// HTTPS, and cannot be on for plain HTTP at a desk.
func SetSessionCookie(w http.ResponseWriter, token string, maxAgeSeconds int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie.
//
// Every attribute except Value and MaxAge has to match what was set, or some
// browsers keep the original alongside the expired one.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionCookieValue reads the raw token from a request, or "" when absent.
func SessionCookieValue(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
