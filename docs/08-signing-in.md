# 08 — Signing in

As planned on reconcile#166 (the review of 2026-11-20); not built yet. When
it is, this document is checked against what was built.

## Through Reconcile's OAuth, as a native program

Authorization code with PKCE, as a native app with a loopback redirect
(RFC 8252). Reconcile registers the program as a built-in client,
`client_id=reconcile-checks`, whose scope is always `checks`.

1. **Sign in to Reconcile** in Settings (or on first run) opens a new tab at
   Reconcile's `/oauth/authorize`, with this computer's name as
   `device_name` (its hostname, which the person can change on Reconcile's
   consent page). The person signs in there with their email and 6-digit
   code as always.
2. Reconcile sends the browser back to
   `http://127.0.0.1:{port}/callback`: **the app's own server**
   ([03](03-the-local-app.md)), which checks `state` and `iss` and trades the
   code for a key at `/oauth/token`.
3. The tab closes back into Settings, now signed in, which lists the
   accounts the key reaches.

The program never sees the sign-in code or a session cookie. The endpoints
come from Reconcile's `/.well-known/oauth-authorization-server`, not from
addresses written into the program.

## One key per computer

The key is named for the computer ("Reconcile Checks — Office") and listed
on Reconcile's keys screen, where it can be revoked. Signing in again from
the same computer, with the same `device_name`, replaces that computer's key
and no other. **So the two computers have two keys, and signing in on one
never signs the other out.** Each check sent records which key sent it, so
Reconcile's history says which computer.

A key lasts **a year**, like Reconcile's `upload` keys. The program says so
two weeks before it ends.

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
