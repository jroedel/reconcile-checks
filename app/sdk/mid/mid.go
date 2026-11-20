// Package mid is the request middleware that knows this app's idea of a
// session: who may use the pages, and whether a write came from one of them.
//
// There is one user, the person at this computer, and the proof of it is the
// launch secret (docs/03-the-local-app.md, "Security of a server on
// localhost"):
//
//   - The program opens the browser at /open?k=<token>. A token is made by the
//     running program, is good once, and only for two minutes. It is traded for
//     the session cookie.
//   - A second start proves it is the same person by presenting the launch
//     key, read from runtime.json, which only this user can read; the running
//     program then makes it a fresh token (app/domain/launchapp).
//   - The cookie and the form token are derived from the session key, also in
//     runtime.json, so they survive the program restarting into an update: the
//     tab that was open keeps working.
//
// Another user on the computer can't read runtime.json. A website can't guess
// a token or a key, and can't read the cookie (HttpOnly) or send it
// (SameSite=Strict).
package mid

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/jroedel/reconcile-checks/foundation/web"
)

// CookieName is the session cookie's name.
//
// No __Host- prefix, unlike drop-in forms: the prefix requires Secure, and this
// server speaks plain HTTP on 127.0.0.1, which browsers count as a secure
// context without TLS.
const CookieName = "reconcile_checks_session"

// FormField is the form field, and FormHeader the request header, that carry
// the form token. A page's script sends the header; a plain form, the field.
const (
	FormField  = "form_token"
	FormHeader = "X-Form-Token"
)

// tokenLife is how long a launch token may wait to be used. The browser opens
// it within a second or two; two minutes covers a slow computer, and a link
// found in a history later is no good to anybody.
const tokenLife = 2 * time.Minute

// Keys are the two secrets kept in runtime.json.
type Keys struct {
	Launch  []byte // presented by a second start
	Session []byte // signs the cookie and the form token
}

// NewKeys makes a pair of fresh keys, for the first run.
func NewKeys() Keys {
	return Keys{Launch: random(32), Session: random(32)}
}

// Sessions holds the keys and the launch tokens not yet used.
type Sessions struct {
	keys   Keys
	cookie string
	form   string
	now    func() time.Time

	mu     sync.Mutex
	tokens map[string]time.Time
}

// New builds the sessions for one run of the program.
func New(keys Keys) *Sessions {
	return &Sessions{
		keys:   keys,
		cookie: derive(keys.Session, "reconcile-checks session v1"),
		form:   derive(keys.Session, "reconcile-checks form token v1"),
		now:    time.Now,
		tokens: make(map[string]time.Time),
	}
}

// Mint makes a launch token, good once, for two minutes.
func (s *Sessions) Mint() string {
	token := base64.RawURLEncoding.EncodeToString(random(32))

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for t, expires := range s.tokens {
		if now.After(expires) {
			delete(s.tokens, t)
		}
	}
	s.tokens[token] = now.Add(tokenLife)

	return token
}

// Redeem uses a launch token up, reporting whether it was good.
func (s *Sessions) Redeem(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	expires, ok := s.tokens[token]
	if !ok {
		return false
	}
	delete(s.tokens, token)

	return !s.now().After(expires)
}

// LaunchKeyMatches reports whether key is this install's launch key.
func (s *Sessions) LaunchKeyMatches(key []byte) bool {
	return len(key) > 0 && subtle.ConstantTimeCompare(key, s.keys.Launch) == 1
}

// Cookie is the session cookie, to set once a launch token is redeemed.
func (s *Sessions) Cookie() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    s.cookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

// FormToken is the value every form carries.
func (s *Sessions) FormToken() string { return s.form }

func (s *Sessions) hasSession(r *http.Request) bool {
	c, err := r.Cookie(CookieName)

	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.cookie)) == 1
}

// --- the gates ----------------------------------------------------------------

type ctxKey int

const sessionKey ctxKey = 1

// Authenticate notes whether the request carries the session cookie, and
// refuses nobody, so that the page saying "open the program from its shortcut"
// lives in the same chain as everything it protects.
func Authenticate(s *Sessions) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.hasSession(r) {
				r = r.WithContext(context.WithValue(r.Context(), sessionKey, true))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// HasSession reports whether Authenticate found the session.
func HasSession(ctx context.Context) bool {
	ok, _ := ctx.Value(sessionKey).(bool)
	return ok
}

// Require answers a request without the session with refused, which says how
// to get in: open the program from its shortcut.
func Require(refused http.Handler) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasSession(r.Context()) {
				refused.ServeHTTP(w, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireSameOrigin refuses a write that no page of this server asked for: the
// browser's own word on where it came from (web.SameOriginOnly), and then the
// form token, which only a page of ours could have put in it.
func RequireSameOrigin(s *Sessions, port func() string) web.Middleware {
	origin := web.SameOriginOnly(port)

	return func(next http.Handler) http.Handler {
		token := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if safe(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			got := r.Header.Get(FormHeader)
			if got == "" {
				got = r.PostFormValue(FormField)
			}

			if subtle.ConstantTimeCompare([]byte(got), []byte(s.form)) != 1 {
				http.Error(w,
					"This page is from before Reconcile Checks last started. Reload it and try again.",
					http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})

		return origin(token)
	}
}

func safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// --- helpers --------------------------------------------------------------------

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b) // documented never to fail

	return b
}

func derive(key []byte, label string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))

	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
