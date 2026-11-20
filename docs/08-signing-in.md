# 08 — Signing in

Checked against what Reconcile built (reconcile#171, merged 2026-11-20):
its `docs/checks.md`, "Signing in", and `app/domain/oauthapp`.

## Through Reconcile's OAuth, as a native program

Authorization code with PKCE (`S256`), as a native app with a loopback
redirect (RFC 8252). Reconcile knows the program as a built-in client,
`client_id=reconcile-checks`, whose scope is always `checks`, whatever else
the person may do. Its only redirects are `http://127.0.0.1/callback` and
`http://[::1]/callback`, on any port; **`localhost` is refused**, so the
program always sends `http://127.0.0.1:{port}/callback`.

1. **Sign in to Reconcile** in Settings (or on first run) opens a new tab at
   Reconcile's `/oauth/authorize`, with this computer's name as
   `device_name` (its hostname, which the person can change on Reconcile's
   consent page; at most 40 characters, and "this computer" if none). The
   person signs in there with their email and 6-digit code as always.
2. Reconcile sends the browser back to
   `http://127.0.0.1:{port}/callback`: **the app's own server**
   ([03](03-the-local-app.md)), with `code`, `state` and `iss`. It checks
   that `state` is its own and that `iss` is the `issuer` of the metadata,
   and trades the code at `/oauth/token` (a form: `grant_type`, `code`,
   `client_id`, the same `redirect_uri`, `code_verifier`) for `{access_token,
   token_type: "Bearer", expires_in, scope: "checks"}`. The key starts
   `rcn_`.
3. The tab closes back into Settings, now signed in, which lists the
   accounts the key reaches.

The program never sees the sign-in code or a session cookie. The endpoints
come from Reconcile's `/.well-known/oauth-authorization-server`, not from
addresses written into the program.

## One key per computer

The key is named for the computer ("Reconcile Checks — Office") and listed
on Reconcile's keys screen ("connected from the check printer"), where it
can be revoked. Signing in again from
the same computer, with the same `device_name`, replaces that computer's key
and no other. **So the two computers have two keys, and signing in on one
never signs the other out.** Each check sent records which key sent it, so
Reconcile's history says which computer.

A key lasts **a year**, like Reconcile's `upload` keys; `expires_in` says
exactly. The program says so two weeks before it ends.

A person may hold **at most five live keys** in Reconcile, of every kind
(Claude's, an upload script's, this program's). A sixth is refused there,
on Reconcile's page, until one is revoked.

The rector signs in **as the rector**, with their own Reconcile login, and
needs a role on each account that lets them attach receipts. Each person's
checks are their own in the list of checks
([09](09-reconcile-api.md)); asking about a number sees both.

A key made by hand on Reconcile's keys screen, for "A program that prints
checks", works the same way. It is for a computer with no browser, and this
program doesn't take one yet.

## The accounts this computer prints for

The key reaches every checking account where the person may attach receipts
(not reference accounts, not archived ones). **Which of them this computer
prints checks on is a setting here**, not something ticked on Reconcile's
consent page: Settings lists the accounts the key reaches, and each one
ticked gets its stock, printer, tray and offsets ([02](02-architecture.md)).

The scope is narrow enough that this is safe: a `checks` key can't read
transactions, statements or receipts beyond checks, and can't change the
books ([09](09-reconcile-api.md)).

## The key, kept

- In the operating system's keychain: **Windows Credential Manager**, or the
  Secret Service keyring on a Linux desktop. Where Linux has no keyring
  running, a file readable only by its user, in the config directory.
- Never shown, never logged, never in an error message, never in a page.
- **Any `401` from Reconcile** (expired, revoked): the page says *"Sign in to
  Reconcile again"*, with the button. Printing never waits on Reconcile; the
  check is saved and sent once signed in ([07](07-the-image.md)).
- **Sign out** in Settings forgets the key here. Revoking it is on
  Reconcile's keys screen, which the page links to.
