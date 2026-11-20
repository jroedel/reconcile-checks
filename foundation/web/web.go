// Package web is the HTTP plumbing that has no domain knowledge: the
// middleware every request passes through, the response-header policy, and the
// two checks a server on 127.0.0.1 needs that a public one does not.
//
// Nothing here knows what a check or a printer is. When two apps need the same
// request middleware it belongs here; when they need the same page chrome it
// belongs one layer up in app/sdk/page.
//
// Carried over from drop-in forms, less what a public server needed (rate
// limits, the embed policy, form-encoded-only), and with the origin checks
// rewritten for a server reached as http://127.0.0.1:{port}: drop-in forms
// required https, which no request here will ever have. The Host check and the
// handling of a null Origin come from sion-backup's app/sdk/loopback, which
// learned both the hard way.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Middleware wraps a handler. Nothing more.
type Middleware func(http.Handler) http.Handler

// Wrap applies middleware so that the first argument is the outermost layer,
// which is the order the chain is written down in and therefore the order it
// should be read in.
func Wrap(h http.Handler, mw ...Middleware) http.Handler {
	for _, m := range slices.Backward(mw) {
		if m != nil {
			h = m(h)
		}
	}

	return h
}

// --- request id -------------------------------------------------------------

type ctxKey int

const requestIDKey ctxKey = iota + 1

// RequestIDFrom returns the id every log line for this request carries, or the
// empty string outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID mints the id the rest of the chain logs against.
//
// It is the outermost middleware so that the request line and a panic line
// carry the same id, which is the only thing that lets the two be matched up
// afterwards. The id is ours rather than a client-supplied header.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var b [8]byte
			rand.Read(b[:]) // documented never to fail

			ctx := context.WithValue(r.Context(), requestIDKey, hex.EncodeToString(b[:]))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- logging ----------------------------------------------------------------

// recorder remembers what was answered, because the request line is written
// after the handler has finished and the standard ResponseWriter does not
// report either of these back.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)

	return n, err
}

// Unwrap keeps http.ResponseController working through this wrapper, so a
// handler that needs to flush or extend a deadline -- the page's open
// connection does both -- still can.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// Logging writes exactly one line per request, whatever answers it.
//
// The query string is never logged, and nor is any form value. A payee, an
// amount or a memo does not belong in a log file, and neither does the
// one-time launch token, which arrives in a query string.
//
// It sits outside Panics so that a request answered by the panic recovery
// still gets its request line, with the 500 on it.
func Logging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			log.InfoContext(r.Context(), "request",
				"id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// --- panics -----------------------------------------------------------------

// Panics turns a panic into one error line and a 500.
//
// The sentence is deliberately not "try again". A panic while printing can
// happen after the printer has taken the job, and printing again is how a
// second check comes out of the tray.
func Panics(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				log.ErrorContext(r.Context(), "panic",
					"id", RequestIDFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"value", rec,
				)

				http.Error(w,
					"Something went wrong in Reconcile Checks. If you were printing a check, look at the printer before trying again: it may already have printed.",
					http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// --- response headers -------------------------------------------------------

// Policy is the set of response headers the server answers with. Building it
// is the caller's job, one layer up, so that this package states no policy of
// its own.
//
// An empty field is not set at all.
type Policy struct {
	ContentSecurityPolicy string
	ReferrerPolicy        string
	CacheControl          string
	FrameOptions          string
}

// SecureHeaders sets the response headers before the handler can write a body.
//
// Every header is Set and never Add: two enforced Content-Security-Policy
// headers are evaluated independently, so a second one arriving alongside ours
// would block everything while looking like ours was ignored. A handler that
// needs a different Cache-Control (the embedded assets, which may be cached
// for ever) sets its own, which replaces this one.
func SecureHeaders(p Policy) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			head := w.Header()

			set := func(name, value string) {
				if value != "" {
					head.Set(name, value)
				}
			}

			set("Content-Security-Policy", p.ContentSecurityPolicy)
			set("Referrer-Policy", p.ReferrerPolicy)
			set("Cache-Control", p.CacheControl)
			set("X-Frame-Options", p.FrameOptions)
			head.Set("X-Content-Type-Options", "nosniff")

			next.ServeHTTP(w, r)
		})
	}
}

// --- the Host a request was sent to -------------------------------------------

// RequireLocalHost refuses a request whose Host is not this server.
//
// It is the defence against DNS rebinding. A page on evil.example can make its
// own name resolve to 127.0.0.1 after it has loaded; the browser then treats
// http://evil.example:{port} as the attacker's own origin, and the attacker's
// script can read what this server answers. The browser still sends
// Host: evil.example, so the request is refused here, before anything reads a
// cookie or renders a page.
//
// port is a function because the server may fall back to a port chosen by the
// system, which is known only once the socket is bound.
func RequireLocalHost(port func() string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !LocalHost(r.Host, port()) {
				// Deliberately unhelpful. Whoever reads this is either a
				// person who typed the wrong address, who will be fine, or a
				// script that should learn nothing.
				http.Error(w, "This page is only reachable at http://127.0.0.1:"+port()+"/",
					http.StatusMisdirectedRequest)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// LocalHost reports whether host (a Host header, or an origin's host) names
// this computer's loopback on the given port. An empty port means any.
func LocalHost(host, port string) bool {
	name, got, err := net.SplitHostPort(host)
	if err != nil {
		// No port at all. That cannot be this server, which is never on 80.
		name, got = host, ""
	}

	if port != "" && got != port {
		return false
	}

	switch strings.ToLower(name) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// --- where a write came from ------------------------------------------------

// SameOriginOnly refuses a write that a page of this server did not make.
//
// Safe methods are not checked: reading is not the attack here, since any page
// that could read our answers is already refused by RequireLocalHost. Writing
// is the attack -- any website open in the same browser can make it POST to
// http://127.0.0.1:{port}, and printing a check is a write.
//
//   - Sec-Fetch-Site is sent by every current browser and page script cannot
//     forge it. same-origin is ours; none is an address typed or bookmarked.
//     same-site and cross-site are refused.
//   - Origin, when sent, must be this server. "null" is an opaque origin,
//     which a browser can produce for an ordinary page of ours (Firefox does,
//     under some referrer policies); it is accepted only when Sec-Fetch-Site
//     has separately vouched for the request, because the other thing that
//     produces it is a sandboxed frame on somebody else's site, which arrives
//     as cross-site and was refused already.
//   - A request with neither header is let through. That is a program on this
//     computer rather than a browser, and the per-session form token, checked
//     one layer up, still has to be right.
func SameOriginOnly(port func() string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !safeMethod(r.Method) && !sameOrigin(r, port()) {
				http.Error(w,
					"That request did not come from a page of Reconcile Checks. Open it again from its shortcut and try once more.",
					http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func sameOrigin(r *http.Request, port string) bool {
	site := r.Header.Get("Sec-Fetch-Site")

	switch site {
	case "", "none", "same-origin":
	default:
		return false
	}

	switch origin := r.Header.Get("Origin"); origin {
	case "":
		return true
	case "null":
		return site == "same-origin" || site == "none"
	default:
		u, err := url.Parse(origin)
		return err == nil && u.Scheme == "http" && LocalHost(u.Host, port)
	}
}

// --- body size --------------------------------------------------------------

// MaxBody caps what a request may send.
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}

			next.ServeHTTP(w, r)
		})
	}
}
