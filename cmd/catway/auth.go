//go:build ghostty

package main

import (
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/gwauth"
)

// resolveSecret returns the shared access secret: the --password flag, else
// the CATS_PASSWORD env, else a freshly generated one (generated=true so the
// caller logs it for the operator to use).
func resolveSecret(flagVal string) (secret string, generated bool, err error) {
	if flagVal != "" {
		return flagVal, false, nil
	}
	if env := os.Getenv("CATS_PASSWORD"); env != "" {
		return env, false, nil
	}
	secret, err = gwauth.GenerateSecret()
	return secret, true, err
}

// resolvePushToken returns the push webhook's bearer credential, from the
// environment only. Like CATS_PASSWORD it is deliberately unreadable from
// config.yaml — and here the reason is sharper: config.set marshals the whole
// config struct back to disk, so a token field would write a secret the
// operator carefully kept in their environment into a file the first time they
// changed a theme colour. An empty result simply sends no Authorization header,
// which is correct for a plain ntfy topic.
func resolvePushToken() string { return os.Getenv("CATS_PUSH_TOKEN") }

// authGuard enforces WS10 access control for the catway: an unauthenticated
// browser is bounced to /login, where it exchanges the shared secret for an
// HMAC-signed session cookie; a headless client presents the secret as a
// bearer token. The WebSocket upgrade additionally requires a same-origin
// request. A nil *authGuard means auth is disabled (--auth none) and no
// middleware is installed.
type authGuard struct {
	a              *gwauth.Authenticator
	secure         bool     // set the session cookie Secure (server is serving TLS)
	allowedOrigins []string // extra WS Origins accepted beyond same-origin (gwauth.OriginOK)
}

// middleware gates every request. Public paths (/login, /favicon.ico) pass
// through; everything else needs a valid session cookie or bearer token, and
// /ws also needs a same-origin Origin. Browser navigations without auth are
// redirected to /login; API/WS calls get a 401 so they fail fast.
func (g *authGuard) middleware(ctx rweb.Context) error {
	path := ctx.Request().Path()
	if path == "/login" || path == "/favicon.ico" {
		return ctx.Next()
	}
	if path == "/ws" {
		origin := ctx.Request().Header("Origin")
		if !gwauth.OriginOK(origin, ctx.Request().Host(), g.allowedOrigins) {
			return ctx.Status(http.StatusForbidden).WriteText("forbidden: cross-origin websocket")
		}
	}
	if g.authed(ctx) {
		return ctx.Next()
	}
	if path == "/ws" {
		return ctx.Status(http.StatusUnauthorized).WriteText("unauthorized")
	}
	return ctx.Redirect(http.StatusFound, "/login")
}

// authed reports whether the request carries valid credentials: the shared
// secret as a bearer token, a session token as a bearer token, or a session
// cookie.
//
// The middle case is the paired device (pair.go). A native client has no cookie
// jar, and the session value it redeemed a pairing token for is the credential
// it holds — so it presents that in the Authorization header. Accepting it there
// grants nothing new: the same value is already accepted from the cookie, and
// ValidSession still bounds it by signature and expiry. What it avoids is the
// alternative, which would be handing phones the shared secret.
func (g *authGuard) authed(ctx rweb.Context) bool {
	authorization := ctx.Request().Header("Authorization")
	if g.a.CheckBearer(authorization) {
		return true
	}
	if token, ok := gwauth.BearerToken(authorization); ok && g.a.ValidSession(token, time.Now()) {
		return true
	}
	if cookie, err := ctx.GetCookie(gwauth.CookieName); err == nil {
		return g.a.ValidSession(cookie, time.Now())
	}
	return false
}

// handleLoginGet renders the login form (already authenticated → straight to
// the app).
func (g *authGuard) handleLoginGet(ctx rweb.Context) error {
	if g.authed(ctx) {
		return ctx.Redirect(http.StatusFound, "/")
	}
	return ctx.WriteHTML(loginPage(""))
}

// handleLoginPost checks the submitted credential and, on success, issues a
// session. Failures re-render the form with a 401 so a probe can distinguish
// them.
//
// The credential is either the shared password or a device-pairing token
// (`catctl pair`), which is why this is the redemption point: pairing needed no
// new endpoint, only permission to spend a grant where a password would do. The
// order matters — the short circuit means submitting the real password never
// consumes somebody's outstanding pairing token.
//
// A browser gets the session as a cookie and a redirect into the app. A native
// client asking for JSON gets the session value in the body instead: it has no
// cookie jar, and scraping Set-Cookie out of a 303 to rebuild it as a bearer
// header is a needless dance.
func (g *authGuard) handleLoginPost(ctx rweb.Context) error {
	form, _ := url.ParseQuery(string(ctx.Request().Body()))
	asJSON := wantsJSON(ctx.Request().Header("Accept"))

	out := g.authorizeLogin(form.Get("password"), time.Now())
	if !out.ok {
		if asJSON {
			return ctx.Status(http.StatusUnauthorized).
				WriteJSON(map[string]string{"error": "invalid password or pairing token"})
		}
		return ctx.Status(http.StatusUnauthorized).WriteHTML(loginPage("Incorrect password."))
	}
	if asJSON {
		return ctx.WriteJSON(map[string]any{
			"session":    out.session,
			"expires_at": out.expires.Unix(),
		})
	}
	cookie := &rweb.Cookie{
		Name:     gwauth.CookieName,
		Value:    out.session,
		Path:     "/",
		MaxAge:   int(g.a.TTL() / time.Second),
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: rweb.SameSiteStrictMode,
	}
	if err := ctx.SetCookieWithOptions(cookie); err != nil {
		return ctx.Status(http.StatusInternalServerError).WriteText("failed to set session")
	}
	return ctx.Redirect(http.StatusSeeOther, "/")
}

// loginOutcome is what a submitted credential bought. session is empty unless ok.
type loginOutcome struct {
	ok      bool
	session string
	expires time.Time
}

// authorizeLogin decides whether a submitted credential grants a session, and
// mints it if so. It is separated from handleLoginPost because this is the whole
// of the security decision — the caller above it only chooses between a cookie
// and a JSON body — and because rweb's synthetic-request helper cannot carry a
// POST body, so a test cannot reach it through the handler.
//
// The short circuit is load-bearing: submitting the real password must never
// consume somebody's outstanding pairing grant.
func (g *authGuard) authorizeLogin(credential string, now time.Time) loginOutcome {
	if !g.a.CheckSecret(credential) && !g.a.RedeemPairToken(credential, now) {
		return loginOutcome{}
	}
	return loginOutcome{ok: true, session: g.a.IssueSession(now), expires: now.Add(g.a.TTL())}
}

// wantsJSON reports whether the caller asked for a JSON response body. Matching
// on a substring rather than parsing the Accept header's q-values is deliberate:
// a browser's Accept always leads with text/html, and the only caller that names
// application/json here is one that constructed the header itself.
func wantsJSON(accept string) bool {
	return strings.Contains(accept, "application/json")
}

// loginPage renders the login form, optionally with an error banner. The page
// is self-contained (no external assets) so it works before any auth.
func loginPage(errMsg string) string {
	banner := ""
	if errMsg != "" {
		banner = `<p class="err">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>cats · sign in</title>
<style>
  html,body{margin:0;height:100%;background:#181818;color:#d4d4d4;
    font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;}
  form{background:#202020;border:1px solid #333;border-radius:8px;padding:28px 26px;
    width:300px;box-shadow:0 4px 20px rgba(0,0,0,.5);}
  h1{font-size:16px;margin:0 0 4px;color:#e8e8e8;}
  p.sub{font-size:12px;color:#888;margin:0 0 18px;}
  label{display:block;font-size:12px;color:#aaa;margin:0 0 6px;}
  input{width:100%;box-sizing:border-box;padding:9px 10px;font-size:14px;
    background:#141414;color:#e8e8e8;border:1px solid #3a3a3a;border-radius:5px;
    font-family:inherit;}
  input:focus{outline:none;border-color:#5b9dff;}
  button{margin-top:16px;width:100%;padding:9px;font-size:14px;cursor:pointer;
    background:#2f68c8;color:#fff;border:none;border-radius:5px;font-family:inherit;}
  button:hover{background:#3a78e0;}
  p.err{color:#ff6b6b;font-size:12px;margin:0 0 14px;}
</style></head><body>
<form method="post" action="/login">
  <h1>cats catway</h1>
  <p class="sub">Enter the access password to continue.</p>
  ` + banner + `
  <label for="password">Password</label>
  <input id="password" name="password" type="password" autofocus autocomplete="current-password"/>
  <button type="submit">Sign in</button>
</form>
</body></html>`
}
