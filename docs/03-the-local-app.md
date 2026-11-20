# 03 — The local web app

Decided 2026-11-20: the program is used **in the browser, served by itself**.
Opening Reconcile Checks starts a small web server on this computer, on
`127.0.0.1` only, and opens the browser at it. Nothing is served to the
network, and nothing is hosted anywhere.

Why not a terminal: the rector prints from Windows ([01](01-overview.md)),
and a form in a browser is what anyone already knows how to use. It also
makes possible what a terminal can't: a **live preview** of the check as it
is typed, and the payee completing from a list under the field.

## Opening it

- **On Windows**, from a Start menu shortcut (or by double-clicking the
  `.exe`). It runs without a console window: built as a Windows GUI program,
  it logs to a file instead ([02](02-architecture.md), "Where files live").
- **On Linux**, from its desktop entry, or `reconcile-checks` in a terminal,
  where it also logs to the terminal.

Either way the program starts its server and opens the default browser at it.
The shortcut and the desktop entry are made by the program itself the first
time it runs ([10](10-install-update-and-releases.md)).

## Only one at a time

The register is one SQLite file, and one program writes it. So the program
takes an **operating-system lock** on a file in its data directory when it
starts. Beside it, in a separate `runtime.json` readable only by this user,
it keeps the port it is serving on, a **launch key** (long-lived, for this
install) and the **session key** that signs the page's cookie. (On Linux the
file is mode 0600; on Windows it is protected by living in the user's own
`%LocalAppData%`.)

- **Opened again while running**: the lock is held, so the second start
  reads the launch key and asks the running copy, at `/launch`, for a fresh
  one-time address, opens the browser at it, and exits. A second click on
  the shortcut is never an error.
- **After a crash**: the operating system released the lock when the process
  died, so the next start simply takes it. No stale lock to delete. (On
  Windows the lock is on a byte range past the end of the file, because
  Windows locks are mandatory and would otherwise stop the file being read.)
- **After an update**, the new program waits for the old one's lock rather
  than handing over to it ([10](10-install-update-and-releases.md)). The
  session key is the same, so the open tab's cookie still holds.

## When it closes

The open page keeps one connection to the server (server-sent events), which
the server also uses to tell the page what is happening: the print job's
state, a check sent, an update waiting.

**The server exits when no page has been connected for two minutes**
(decided 2026-11-20), and
nothing is in flight: no print job being followed, no send under way. Two
minutes covers a reload, a tab moved between windows, and a short sleep.
There is also **Quit** in the menu.

When the page loses its server (closed, or the computer slept past the two
minutes), it says so: *"Reconcile Checks has closed. Open it again from its
shortcut."* It never shows a half-working form.

Checks waiting to be sent are in the register, not in memory, so closing
loses nothing ([07](07-the-image.md)).

## The port

A fixed port by default (any number above 1024 that nothing common uses,
chosen when coding starts, and settable in the config), so that an open tab reconnects by itself after the program restarts
for an update ([10](10-install-update-and-releases.md)). If something else
holds that port, any free port, and the browser is opened at that one.

Sign-in's OAuth redirect comes back to this same server
(`http://127.0.0.1:{port}/callback`); Reconcile allows any loopback port
([08](08-signing-in.md)).

## Security of a server on localhost

`127.0.0.1` is not private: every website open in the browser can send
requests to it, and so can every other user and program on the computer.
The server holds a Reconcile key and drives a check printer, so it is
treated as a web server like any other, with the defences of the sibling
projects ([`CLAUDE.md`](../CLAUDE.md), `.claude/skills/writing-go-web`):

1. **A launch secret.** The browser is opened at
   `/open?k=<one-time token>`, which the server trades for a session cookie
   (`HttpOnly`, `SameSite=Strict`, signed with the session key) and
   redirects away from. The token is made by the running program, for its
   own start or for a second start that proved it holds the launch key. A request without
   the cookie gets only a page saying to open the program from its shortcut.
   Another user on the computer can't read the secret; a website can't guess
   it.
2. **The `Host` header must be `127.0.0.1:{port}`** (or `localhost:{port}`).
   This stops DNS rebinding, where a website's own name is made to point at
   `127.0.0.1` so that the browser treats this server as that website.
3. **Writes come from our own pages**: `Sec-Fetch-Site` (or `Origin`) is
   checked, and every form carries a per-session token. Printing a check is a
   write.
4. **A strict Content-Security-Policy**: scripts, styles and fonts only from
   this server, nothing from the internet. The page makes no request beyond
   `127.0.0.1`; the server talks to Reconcile and to the printer, the page
   never does.
5. **Listening on `127.0.0.1` only**, never `0.0.0.0`. The rector's computer
   does not print from the other computer's server.

## The screens

**Write a check** — the main screen.

- The form on one side: account, number, date, pay to, amount, memo, stub
  note, with the rules of [06](06-writing-a-check.md). The amount in words
  appears under the amount as it is typed.
- **The live preview** on the other: the check, drawn on its stock, updated a
  moment after each change. It is made by the same code as the paper and the
  image for Reconcile ([02](02-architecture.md), "Rendering"), so a payee
  that runs into the amount box is seen before it is printed. A switch shows
  the whole page with both stubs.
- **Print**, with the summary on the button ("Print check 1043 to Payee A for
  $1,234.50"). Then, as the printer reports it, *"Did it print correctly?"*
  with **Yes** and **No — void it and reprint on 1044**.

**Checks** — the register: recent checks, with filters for outstanding,
void and not yet sent. Void a check from here; send what is waiting.

**Settings** — what this computer prints for, and how:

- the accounts this computer prints checks on, from those Reconcile offers;
- per account: the stock, the printer, the tray, the media type, the way up,
  and the calibration offsets, with **Print calibration page**
  ([04](04-stock-and-layout.md), [05](05-printing.md));
- printers: found on the network, or an address typed in; what each says it
  supports ([05](05-printing.md));
- signing in and out of Reconcile ([08](08-signing-in.md));
- the version, and updates ([10](10-install-update-and-releases.md)).

## The front end

**English only** (decided 2026-11-20), like the check and the bank.

As in sum-app: **pages rendered by Go** (`html/template`), and **plain ES
modules with no build step and no npm** for what a page can't do alone: the
preview, payee completion, the amount in words as it is typed, and the
connection that tells the server a page is open. The CSS, scripts and fonts
are embedded in the binary.

Every form also works as a plain form post, so a script that fails to load
costs the preview, not the check.

