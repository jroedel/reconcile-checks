---
name: writing-go-web
description: How the local web app is built here — the layering, the middleware chain for a server on 127.0.0.1 and why it is in that order, the response-header policy, and the page conventions. Load before adding a route, a handler, a middleware, a template or a script.
---

# Writing the local web server here

Carried over from drop-in forms (`../dropin-forms`), which took it from
eumaeus, and changed for what this server is: **a server on `127.0.0.1` that
only this computer's browser should reach, holding a Reconcile key and
driving a check printer.** Where a rule differs from drop-in forms, the
difference is marked **Here:**. The design is `docs/03-the-local-app.md`.

## The layering, and the one rule that makes it worth having

```
cmd/reconcile-checks/               main: the lock, build the dependencies, mount the apps, serve, open the browser
app/domain/<x>app/                  handlers and view types. No business rules.
app/sdk/                            plumbing under the apps: muxer, mid, page
business/domain/<x>/<x>bus/         the rules. This is where behaviour is.
business/domain/<x>/stores/<x>db/   storage, per domain
foundation/                         no domain knowledge: web, ipp, render, selfupdate, sqldb, errs
```

**An App package never imports another App package, and nothing in
`foundation/` knows a domain word.** When two apps need the same request
middleware it goes to `foundation/web`; when two apps need the same page
chrome it goes to `app/sdk/page`.

A handler's job is to decide a status code and a page. It parses the
request, calls one `…bus` function, and turns the error type it gets back
into an answer. If a handler contains an `if` about checks, that `if` belongs
in `…bus`.

## The middleware chain

Assembled once, in `app/sdk/muxer`, and documented there in the package
comment. Adding a route should be one entry in a list, and the thing most
easily got wrong about a new route is what it sits behind.

```
RequestID              mints the id every line of this request will carry
Logging                writes the one "request" line, whatever answers
Panics                 recovers, logs it, answers 500
RequireLocalHost       refuses a Host that is not 127.0.0.1:{port} or localhost:{port}
Authenticate           reads the session cookie, and refuses nobody
  Require              refuses a request with no session
    RequireSameOrigin  refuses a write that no page of this server asked for
      the app
```

Why that order:

- **The three above `RequireLocalHost` refuse nothing**, so whatever the
  gates below decide is logged, and a panic anywhere is one `Error` line and a
  500.
- **Here: `RequireLocalHost` is first among the gates.** It is the defence
  against DNS rebinding: a website makes its own name resolve to `127.0.0.1`,
  and the browser then treats this server as that website, cookies aside. The
  `Host` header still carries the website's name, so it is refused before
  anything reads a cookie.
- **`Authenticate` never refuses**, so `/open` (which trades the launch
  secret for the session cookie) and the "open the program from its
  shortcut" page live in the same chain as everything they protect.
- **`RequireSameOrigin` comes before anything that acts**: printing, voiding,
  sending, saving settings, signing out. "This request did not come from
  you" is a prior question to anything else.

Not behind a session: the embedded CSS, scripts and fonts, and `/open`.
**Here:** also `/callback`, where Reconcile's sign-in returns; it is guarded
by OAuth's own `state` and PKCE, and the session it lands in is checked
before the key is kept.

## Writes must come from our own pages

Any website open in the same browser can send a request to
`127.0.0.1:{port}`. It cannot read the answer, but it does not need to:
printing a check is the whole attack.

Overlapping defences, on purpose:

1. **The launch secret and the session cookie.** The browser is opened at
   `/open?k=<secret>`; the secret is single-use and short-lived, and is
   traded for a cookie, `HttpOnly`, `SameSite=Strict`, `Path=/`. **Here:**
   no `__Host-` prefix, because that needs `Secure`, and this server speaks
   plain HTTP on loopback, which browsers treat as a secure context anyway.
2. **`Sec-Fetch-Site`**, checked in `foundation/web.SameOriginOnly`:
   `same-origin` is ours, `none` is a direct navigation; cross-site and
   same-site are refused. Where it is absent, compare `Origin` with `Host`.
3. **A per-session token in every form**, checked by
   `app/sdk/mid.RequireSameOrigin`.

## Response headers

Set once, at the root, in `foundation/web.SecureHeaders`:

```go
head.Set("Content-Security-Policy",
    "default-src 'none'; script-src 'self'; style-src 'self'; "+
        "img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; "+
        "form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
head.Set("X-Content-Type-Options", "nosniff")
head.Set("Referrer-Policy", "no-referrer")
head.Set("Cache-Control", "no-store")
```

**Here:** `script-src 'self'` and `connect-src 'self'`, because the preview,
payee completion and the page's open connection are scripts
(`docs/03-the-local-app.md`). Still nothing from the internet: the page never
talks to Reconcile or the printer; the server does. `frame-ancestors 'none'`
is decided: nothing embeds this app. `no-store` on everything, because every
page shows a payee or an amount, and the embedded assets are served with
their own long cache headers instead.

## JavaScript

As in sum-app and stewards: **plain ES modules, no build step, no npm**,
embedded in the binary and served by the app that owns them. Every form also
works as a plain post, so a script that fails costs the preview, not the
check. Unit tests are `*_test.mjs` beside the module, under Node's own test
runner; browser tests are `*_browser_test.mjs`, in headless Chrome against a
server started with `--no-browser` and a test config.

## The server itself

- **Here: listens on `127.0.0.1` only**, never `0.0.0.0` or `[::]`.
- `http.Server` with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` and
  `IdleTimeout` set; the server-sent events route sets its own write
  deadline per event, since it is meant to stay open.
- Graceful shutdown: on a signal, on **Quit**, and when no page has been
  connected for the grace period with nothing in flight. Never while a print
  job is being followed.
- `http.MaxBytesReader` on every body.
- Routing is `net/http`'s own mux, with method-and-wildcard patterns. No
  router dependency.
- Logging is `log/slog`, one line per request. **The query string is never
  logged**, nor any form value: a payee, an amount or a memo does not belong
  in a log file, and neither does the launch secret, which arrives in a
  query string.
- Templates are `html/template`, parsed once at startup and `//go:embed`-ed.
  A parse error is a startup failure, not a 500 in front of a person.

## Testing a handler

`net/http/httptest` against the mounted mux, not against a bare handler: the
thing most worth testing about a route is the chain in front of it. Each
route, reached with a wrong `Host`, without a session, cross-site, or with a
stale form token, is refused by the gate that should refuse it.

## What was left behind

- **Eumaeus's vault and credential scopes**, as in drop-in forms.
- **Drop-in forms' public-server concerns**: embedding, public blank forms,
  accounts and sign-up. This server has one user, the person at the
  computer, proven by the launch secret.
