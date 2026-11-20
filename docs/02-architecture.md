# 02 — Architecture

## A local program, a client of Reconcile

**One file**: a Go binary with everything inside it (pages, scripts, fonts,
stock layouts), downloaded from GitHub Releases and run on the computer that
prints checks, on Linux or Windows. It serves its own pages to the browser on
`127.0.0.1` ([03](03-the-local-app.md)), keeps its data on that computer, and
reaches Reconcile only through its API, with a key limited to the `checks`
scope ([08](08-signing-in.md), [09](09-reconcile-api.md)). It updates itself
([10](10-install-update-and-releases.md)).

The split of responsibility:

| Lives here | Lives in Reconcile |
|---|---|
| The stock, its layout and its offsets, per account | The accounts, their names and last four |
| Rendering and printing (IPP) | The checks outstanding, and attaching an image when its check clears |
| The local register of every check printed or voided | The API contract ([reconcile#166](https://github.com/jroedel/reconcile/issues/166)) |
| The image, until it is sent | The image, once sent, and the payee names |

## The pieces

- **The local web app**: pages rendered by Go, a little plain JavaScript,
  served on `127.0.0.1` to this computer's browser ([03](03-the-local-app.md)).
- **Rendering**: one layout of the check, drawn into a PDF for the printer
  and a PNG for the preview and for Reconcile. Below.
- **Printing**: IPP straight from the program to the printer, with every
  option that matters in the request rather than left to the printer's
  defaults ([05](05-printing.md)). The same on Linux and Windows, and nothing
  in it about one make of printer.
- **The register**: a local SQLite database of every check printed, voided
  and sent. It refuses a number twice on this computer, and holds a check
  that has not reached Reconcile yet.
- **The Reconcile client**: sign-in through OAuth, its redirect landing on
  the app's own server, and the handful of `checks` routes.
- **The updater**: checks GitHub Releases, verifies, and replaces the binary
  ([10](10-install-update-and-releases.md)).

## Rendering: in Go, not Typst

Decided 2026-11-20, replacing Typst, because of what the program became:

- **One file to install.** Typst is a second program. It would be either
  downloaded beside the binary at a pinned version (as sion-backup does with
  restic) or carried inside it and unpacked at run time. Either way there
  are two programs to keep in step, on two operating systems.
- **The live preview** redraws on every change. Starting another program for
  each keystroke costs more than drawing in the same process.
- **The check is simple.** Text at fixed positions in one font, a few
  lines, a line filled with `*`, and a calibration page of crosses and
  rulers. Typst's strength is typesetting prose, and a check has none.

So the program holds **one layout per stock**: data, not code (positions,
widths and sizes in millimetres, [04](04-stock-and-layout.md)), embedded in
the binary. Go places the check's fields on that layout once. Two small
renderers then draw the same placed text:

- **PDF**, for the printer: with the offset, without the stock drawn in.
- **PNG**, for the preview and for Reconcile: without the offset, with the
  stock drawn in ([07](07-the-image.md)).

Both use **one font file embedded in the binary**, an openly licensed one:
Go Regular, from `golang.org/x/image`. Both renderers
measure text with the same font, so a payee that fits in the preview fits
on the paper. A golden test of each keeps them honest
([10](10-install-update-and-releases.md)). Which PDF and font libraries to
use, pure Go only, is decided when the code is written.

## Libraries and versions

Decided when the build began (2026-11-20). Pure Go only, so one Linux runner
builds for every system ([10](10-install-update-and-releases.md)).

| Need | Choice | Why |
|---|---|---|
| Go | 1.26, as the sibling projects; 1.27 before v1.0 | Matches the `writing-go` skill; 1.26 leaves support when 1.28 ships |
| SQLite | `modernc.org/sqlite` | As every sibling |
| PDF | Our own small writer in `foundation/render` | A check is text and lines. One TrueType font embedded as a simple font with WinAnsi encoding: deterministic bytes for golden tests, the simplest font for a printer's own PDF interpreter, and no dependency |
| Font and PNG | Go Regular, from `golang.org/x/image` | Openly licensed; the PDF's widths and the PNG's drawing come from the same font file. Proportional rather than Go Mono, which was the first choice: the stock's date box is 31 mm wide, and "November 20, 2026" in a monospaced font needs 36 |
| Config and layouts | TOML, `github.com/BurntSushi/toml` | Readable by hand. Settings rewrites the file, so comments written into it by hand don't survive; `config.example.toml` says so |
| IPP | `github.com/OpenPrinting/goipp` to encode, our own HTTP and TLS | The encoding is the fiddly part; the transport is a POST |
| mDNS | Our own, on `golang.org/x/net/dns/dnsmessage` | One-shot queries only ([05](05-printing.md)) |
| Keychain | `github.com/zalando/go-keyring`, with a file fallback | Windows Credential Manager and Linux Secret Service, pure Go |
| Windows shortcut | `WScript.Shell` through `github.com/go-ole/go-ole` | The ordinary way to make a `.lnk` |
| Windows icon | `go-winres`, a committed `.syso` | The exe carries its icon ([12](12-look-and-voice.md)) |

**Characters.** The check's font covers English and the accented letters of
Western European names (ñ, á, é, ü). A payee or memo with a character
outside it is refused in the form, which says which character.

**The Windows build is the same everywhere**: built as a GUI program (no
console window) in CI as in the release, so CI tests what ships. It attaches
to the terminal it was started from, if any, so `version` and `update
--check` print there, and writes its log and any crash to a file in the data
directory.

## Where files live

| What | Linux | Windows |
|---|---|---|
| The program | `~/.local/bin/reconcile-checks` | `%LocalAppData%\Programs\reconcile-checks\reconcile-checks.exe` |
| Settings | `~/.config/reconcile-checks/config.toml` | `%AppData%\reconcile-checks\config.toml` |
| The key | The desktop keyring, or `~/.config/reconcile-checks/key` (0600) | Windows Credential Manager |
| The register, the lock, the log | `~/.local/share/reconcile-checks/` | `%LocalAppData%\reconcile-checks\` |
| Each check's PDF and PNG | `…/checks/<account>/<number>.{pdf,png}` under the data directory | the same |

The program lives where its own user can write, so that it can update
itself without asking for an administrator ([10](10-install-update-and-releases.md)).

The data directory holds the only copy of a check not yet sent, and the
only copy of the PDF ever. It belongs in the computer's backup.

`--config <path>` points the program at another config, and the config names
its own data directory, so a development run never touches the real register
([05](05-printing.md), "Printing to a file").

## The config, sketched

Written by the Settings screen ([03](03-the-local-app.md)); readable and
editable by hand. `config.example.toml` in this repository is its
documentation.

```toml
reconcile = "https://reconcile.example"   # the site's address

[[printer]]
name = "Office M612"
url  = "ipps://NPI46CBDF.local:631/ipp/print"   # "file:<dir>" in development

[[account]]
id      = "…"            # Reconcile's id for the account
name    = "Checking A"   # as Reconcile names it
last4   = "1234"
stock   = "walmart-voucher-top"   # which layout
printer = "Office M612"
tray    = "tray-1"                # the printer's media-source for the checks
media_type = "stationery-preprinted"
feed    = "face up, top edge in first"   # settled with a marked sheet (05)
offset_mm = { x = 0.0, y = 0.0 }         # from calibration (04)
```

The real file holds no account or routing number, because the program needs
none ([04](04-stock-and-layout.md), "Account numbers").

## The code, expected

The layering of the sibling projects, with the web layer back, because there
is a web server again ([`CLAUDE.md`](../CLAUDE.md)).

```
cmd/reconcile-checks/                 main: start, the single-instance lock, open the browser, serve
app/domain/<x>app/                    handlers: writeapp, checksapp, settingsapp, previewapp, signinapp. No rules.
app/sdk/                              the plumbing under them: muxer, mid, page
business/domain/check/checkbus/       the rules: numbering, refusing a number twice, void and reprint
business/domain/check/stores/checkdb/ the register (SQLite)
business/domain/layout/layoutbus/     placing a check's fields on a stock's layout
business/sdk/reconcile/               the client for Reconcile's checks routes and its sign-in
business/types/money/                 an amount in cents, the figures, and the amount in words
foundation/ipp/                       an IPP client: printer attributes, print a PDF with options, follow the job
foundation/render/                    placed text and lines to PDF and to PNG; knows nothing of checks
foundation/selfupdate/                find, verify, stage and swap a release; knows nothing of checks
foundation/web/, sqldb/, errs/        as in the sibling projects
layouts/                              the stock layouts, as data, embedded
```

## On the command line

A person never needs it. For development and support only:

| Command | Does |
|---|---|
| `reconcile-checks` | Starts (or finds the running copy) and opens the browser |
| `reconcile-checks --no-browser` | Starts and prints the address instead; for tests and CI |
| `reconcile-checks --config <path>` | Uses another config and its data directory |
| `reconcile-checks version` | The version, OS and architecture |
| `reconcile-checks update [--check]` | Updates now, or only says whether there is one |

