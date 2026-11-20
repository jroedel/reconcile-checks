# Working in this repository

`reconcile-checks` is a local Go program that prints checks on pre-printed
stock and sends the image of each to Reconcile, where it waits as a check
outstanding until the statement that pays it is imported. It is one binary,
for Linux and Windows, that serves its own pages to this computer's browser
on `127.0.0.1`, prints over IPP, updates itself from GitHub Releases, and is a
client of Reconcile's API
([reconcile#166](https://github.com/jroedel/reconcile/issues/166)).

**What the program does is decided in [`docs/`](docs/README.md).** A
question about behaviour is answered there, not by guessing; when the docs
don't answer it, add it to that doc's **Open questions** rather than
inventing a specific. What Reconcile does is decided in Reconcile:
[`docs/09-reconcile-api.md`](docs/09-reconcile-api.md) is this program's
view of that contract, and the issue wins when they differ.

**This project is in its documentation phase. Do not write code yet** — no
`go.mod`, no `.go` files, no Makefile, no workflows, no layouts. The work right
now is the design documents; the coding rules below are here so they are
settled before the first line is written, not because they apply today.

The rules below were imported from
[drop-in forms](https://github.com/jroedel/dropin-forms), which took them
from eumaeus. Where a rule is about this repository rather than about Go, it
is marked as such. Sibling projects are named by repository and, on the
development machine, checked out beside this one as `../<name>`:
`../reconcile`, `../dropin-forms`, `../sum-app`.

**How to write Go here** is section 0 below and
`.claude/skills/writing-go/SKILL.md`, following
[ardanlabs/kronk](https://github.com/ardanlabs/kronk). **How to build the
local web server** is `.claude/skills/writing-go-web/SKILL.md`: the layering,
the middleware chain for a server on `127.0.0.1`, the headers and the
JavaScript rules, from drop-in forms and changed for a local server.

**How it is released and updated** is
[`docs/10-install-update-and-releases.md`](docs/10-install-update-and-releases.md),
which carries over what [sion-backup](https://github.com/jroedel/sion-backup)
learned (`../sion-backup`): read the pull request it cites before changing a
rule that came from one.

**How to move around this repository** is sections 1–5. They close loops
that are a property of Go and of agents rather than of any one repository: a
compiler diagnostic already knows where the problem is, and gopls already
knows where a symbol is declared. When one of them stops paying here, delete
it rather than obey it out of habit.

**Who does what** is section 6, and it is the part that is enforced rather
than asked for.

## 0. Touching Go: load the skill, then run the check

**Before reading, writing or modifying any `.go` file**, load
`.claude/skills/writing-go/SKILL.md` — the modern-Go rules for the version in
`go.mod`. This is kronk's mandatory-skill rule and it is the one directive here
that is not negotiable. Before adding a route, a handler, a middleware, a
template or a script, load `.claude/skills/writing-go-web/SKILL.md` too.

**After modifying any `.go` file**, on the package you changed:

```sh
make go-check PKG=./business/domain/check/checkbus
```

which is `gofmt -s -w`, `go vet`, `staticcheck`, `go build ./...` and the
package's tests. All must pass. **Fix the code; do not suppress the
diagnostic.**

One package rather than `./...`, deliberately. A new module starts with
`staticcheck ./...` clean; keep it that way and the bound costs nothing, and
the day it is not clean the rule is already in place.

`make dev-tools` installs `staticcheck` and `gopls`, both pinned.

Three things kronk says that are worth repeating verbatim: be concise; verify an
API from the live toolchain or the docs rather than from recall; double-check
the arguments of a tool call before submitting it.

## 1. A diagnostic is an address. Go to it.

`go build`, `go vet`, `go test` and `gofmt` all report `file.go:line:col`. That
is the answer to "where", and no search is needed to find it. Read the region
around the reported line directly — `sed -n '<line-8>,<line+8>p'` on the file it
named — rather than grepping for the identifier it mentions.

## 2. Find a Go symbol with `make sym`, not with grep

```sh
make sym NAME=Check
make sym NAME=Register.ByNumber
make outline FILE=business/domain/check/checkbus/checkbus.go
```

Both are `gopls`, which answers from the same type information the compiler
uses: it knows which `Register` is a method on which type, and it brings the
doc comment with it. There is also a gopls MCP server in some setups
(`mcp__gopls__go_search`, `go_symbol_references`, `go_file_context`); when it is
available it is the same answer without the shell.

**grep is still right** for everything that is not a Go symbol: a config key, a
SQL column, a string in a template, a word in `docs/`. The rule is about the
declaration of an identifier, which is what gopls is exact about.

## 3. One verify command, and its output is already filtered

```sh
make test-unit     # the tests, with -race. No network, no printer.
make lint          # go vet + gofmt check
make test          # both, plus the golden tests and the JavaScript tests
make vuln-check    # govulncheck. Needs the network.
make go-check PKG= # the after-editing-Go loop, on one package
make test-js       # the JavaScript, under Node's own test runner
make test-browser  # the pages in headless Chrome, against a server on a test config
make cross         # builds for every target, so a Windows-only break shows here
make selfupdate-e2e # the self-update cases in Docker, against a fake GitHub
```

`make test-unit` counts the passing packages instead of listing them, so **there
is nothing to pipe it through**. A filter written in a hurry is one that
eventually hides a `FAIL`; if pass-noise ever comes back, fix `scripts/go-test`
rather than the command line.

**Here:** the golden tests compare the PNG and the PDF of an invented check
against ones committed ([`docs/10`](docs/10-install-update-and-releases.md)).
When one fails because a layout or a renderer was changed on purpose,
regenerate it with the make target for that and say in the PR what moved and
by how much. Never
regenerate a golden to make an unexplained failure go away: a moved box is
a misprinted check.

## 4. Formatting is part of verifying, not a step of its own

`make lint` fails when something is not gofmt-clean and names the files.
`make fmt` fixes them. Running `gofmt -l` speculatively is not useful.

## 5. Where things live

Expected, from [`docs/02-architecture.md`](docs/02-architecture.md): the
Ardan Labs layering of the sibling projects.

```
cmd/reconcile-checks/                 main: the lock, wiring, serve, open the browser
app/domain/<x>app/                    handlers and view types. No business rules.
app/sdk/                              the plumbing under them: muxer, mid, page
business/domain/check/checkbus/       the rules: numbering, refusing a number twice, void and reprint
business/domain/check/stores/checkdb/ the register (SQLite)
business/domain/layout/layoutbus/     placing a check's fields on a stock's layout
business/sdk/reconcile/               the client for Reconcile's checks routes and its sign-in
business/types/money/                 an amount in cents, the figures, and the amount in words
foundation/                           no domain knowledge: web, ipp, render, selfupdate, sqldb, errs
layouts/                              the stock layouts, as data, embedded
docs/                                 the reasoning that does not fit in a comment
scripts/                              how this is built, checked and released
```

The rule that makes the layering worth having: **an App package never
imports another App package, and nothing in `foundation/` knows a domain
word.** `foundation/ipp` sends a PDF to a printer with options; it does not
know what a check or a tray for checks is. Nothing anywhere knows one make
of printer ([`docs/05`](docs/05-printing.md)).

A question about *behaviour* starts in `business/domain/check/checkbus`. A
question about *what a page says or accepts* starts in `app/domain/…app`. A
question about *where ink lands* starts in `layouts/` and the account's
offsets, never in Go.

## 6. Who does what

**Here, and not negotiable.**

- **The human in the loop is the pull request, at a minimum.** Everything in
  `git` is a pull request on a branch. An agent opens it; a person reviews and
  merges it. Nothing is committed or pushed to `main` directly, and an agent
  never merges its own PR. GitHub enforces it: `main` is protected, and a
  pull request is the only way in, for everyone.
- **A merge to `main` is a deploy.** Push to deploy is essential here
  ([`docs/10`](docs/10-install-update-and-releases.md)): the release workflow
  turns every merge that changes the program into a release, and both
  computers update to it by themselves. So the review is the last gate
  before a check is printed with the change, and a PR description says what
  the change does to the paper and to what reaches Reconcile, not only to the
  code. A change to a layout, a renderer or the print options says whether
  it was tried on real stock.
- **Agents never print on a real printer.** No `lp`, `lpr`, `lpadmin`,
  `lpoptions -o` (which changes a queue's defaults), and no `ipptool` test
  that sends a job; the program is only ever run with a development config
  whose printers are `file:` directories, and its IPP client is tested
  against a fake server ([`docs/05-printing.md`](docs/05-printing.md)).
  Asking a printer for its attributes (`ipptool … get-printer-attributes.test`)
  is read-only and allowed. Check stock costs money and
  a misprint can't be taken back. When a task needs a real print, say what to
  print and why, and let the person run it and report what came out.
- **Agents never touch the real register, key or config.** Nothing under
  `~/.config/reconcile-checks/` or `~/.local/share/reconcile-checks/` is
  read, written or copied. Development uses `--config` pointing at a config
  in a temporary directory, which names its own data directory.
- **Agents never call the real Reconcile with a real key.** Development
  against Reconcile uses a Reconcile running locally from `../reconcile`.
  Reconcile's side of #166 is changed in that repository, by its own pull
  request, under its own `CLAUDE.md`; not as a side effect of a task here.
- **Nobody tags or publishes a release by hand**, agents least of all.
  Releases are made by the workflow, from a merge; `git tag`, pushing a tag,
  `gh release` and `gh workflow run` are not how anything reaches the
  computers. The self-update tests run against a fake GitHub, never the
  real one.
- **Agents never set GitHub secrets or variables**, and never read
  `secrets.env` if one ever exists. The release signing key is a person's
  to make and keep.

`.claude/settings.json` denies these commands and paths, so the rule holds
even when it is forgotten. It can deny `lp` and a print job from `ipptool`,
but not the program sending one itself; that half of the rule is kept by the
development config. Do not work around a denial — a denied command is the
answer.

## When there is a database

The register is SQLite on the person's computer, and it outlives every
version of the program, so these storage conventions from the sibling
projects apply the day the first store lands (`modernc.org/sqlite`, as in
the siblings).

- **No migration tool.** Each store owns `Init(ctx, db)` holding idempotent
  `CREATE TABLE IF NOT EXISTS … STRICT` DDL, and exports
  `Expected sqldb.Expected`. `main` calls every `Init` in foreign-key order and
  then `sqldb.CheckSchema` once. A later column arrives as an `ALTER … DEFAULT`
  beside the `CREATE`, so a fresh database gets it from one and an existing one
  from the other.
- **Nothing that mentions a later column may sit in the `CREATE` block.** An
  index on one goes in a second `Exec` *after* the `AddColumn` call. On a fresh
  database it builds and every test passes; on a database that predates the
  column it runs against a column that is not there yet. This took
  mass-intentions' production down through four deploys, and no test could see
  it, because every test starts from an empty directory.
- **A constraint removed from a `CREATE` block is still there on every database
  that already exists.** SQLite has no `ALTER` that drops one; removing a
  `CHECK` means rebuilding the table, guarded by a `sqlite_master` lookup.
- **A store with a later column owns a test that runs `Init` over the schema as
  it stood before** — the old DDL written out literally, not derived from the
  new one.
- **Timestamps are Unix milliseconds in INTEGER columns**, never text. An
  amount is whole cents in an INTEGER, never a float.
- **A single-use claim is one statement**, `INSERT … ON CONFLICT DO NOTHING`
  or `UPDATE … WHERE x IS NULL`, never a read followed by a write. Taking a
  check number is one.

## This repository is public

**Nothing private about an account, an organization or a payee is ever
committed**: no account or routing number, not even a real last four; no
real payee, amount or memo; no register, config or key; and **no scan or
photograph of real check stock**, whose MICR line carries the full account
and routing numbers. The stock is described in millimetres, not pictures.
Test fixtures are invented. Paths on the development machine are written
relative to the repository (`../reconcile`), never absolute.

## House style, in one paragraph

Comments explain **why**, at length, and in prose — including what was tried
and rejected, and what a reader would otherwise assume. Match the density of
the file being edited rather than the density of a tutorial. Error sentences
that reach a person say what to do about it and never name a Go package; a
sentence from Reconcile is shown as Reconcile wrote it.

## What was not imported from drop-in forms

Named here so that nobody goes looking for a rule that was left out by
decision rather than by oversight. **This is a web server for one person on
one computer, not a public one.**

- **Deploy, production and its secrets**: there is no server to deploy to,
  so no `deploy/`, no `make prod-*`. A release is how the program reaches a
  computer, and it is guarded in its own way (section 6, `docs/10`).
- **Accounts, sign-up and sessions that outlive a launch**: the one user is
  the person at the computer, proven by the launch secret
  ([`docs/03`](docs/03-the-local-app.md)).
- **The OAuth server side** (`foundation/oauth`, `oauthapp`): here the
  program is the OAuth *client*. The server is Reconcile's.
