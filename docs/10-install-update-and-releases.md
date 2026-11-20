# 10 — Install, update and releases

Two computers on two operating systems, kept by people who are not
programmers. So: **one file to download, nothing else to install, and it
keeps itself up to date.** Most of what follows was learned in
[sion-backup](https://github.com/jroedel/sion-backup), which ships a Go
program to Linux, Windows and macOS and updates it in place. Its pull
requests are cited as sion-backup#N.

## Installing

1. Download `reconcile-checks-windows-amd64.exe` (or `-linux-amd64`) from the
   latest GitHub release. Later, Reconcile's own page can link to it
   ([11](11-later.md)).
2. Run it. On the first run it **installs itself**: it copies itself to
   where its user can write ([02](02-architecture.md), "Where files live"),
   adds a Start menu shortcut (Windows) or a desktop entry (Linux), starts
   from there and opens the browser at Settings to sign in.

No installer, no administrator, no second program, no runtime.

**Where its user can write, and why.** sion-backup first installed under
`%ProgramFiles%`, where the program can't replace itself, and those machines
never updated again (sion-backup#69). It now installs under
`%LocalAppData%`, and so does this. A program run by its own user, never
elevated, gives up nothing by being in the user's own folder.

**Windows SmartScreen.** A downloaded program without a code-signing
certificate gets *"Windows protected your PC"* the first time it runs:
**More info → Run anyway**, once. Updates are downloaded by the program
itself, not by a browser, so they carry no "downloaded from the internet"
mark and don't ask again. **No certificate** (decided 2026-11-20): it would
remove a warning seen once per computer, for a fee every year.

## Updating itself

Automatic, because nobody will remember to update two computers.

**When.** At start, at most once a day, in the background: the page is
usable at once and never waits on GitHub. Also **Update now** in Settings,
and `reconcile-checks update`. Never while a print is being followed or
waiting on *"Did it print correctly?"*.

**How**, in sion-backup's order (`foundation/selfupdate`), which applies as
it is:

1. Ask GitHub for the latest release. Refuse a draft or a pre-release.
2. Accept only a strict `vX.Y.Z` newer than this one. A development build
   (`dev`, a bare commit) never updates and never downgrades. A version on
   the local *refused* list (step 8) is skipped.
3. **Check that the program's own folder is writable** by creating a file
   in it, not by reading permission bits, which ACLs and read-only mounts
   make lie.
4. Download the asset for this system to `reconcile-checks.new` beside the
   binary, so that the swap is a rename on one disk. Hash it while it
   streams.
5. **Verify**: its SHA-256 against `SHA256SUMS` from the same release, **and
   `SHA256SUMS`'s signature against a public key compiled into the
   program**. sion-backup checks only the hash and says plainly that this
   proves the download whole, not genuine; a signature is the fix it
   intended and never built. Here it is built from the start: an Ed25519
   key, its private half a GitHub Actions secret used only by the release
   job (set by a person, never an agent).
6. **Smoke-test it**: run `reconcile-checks.new version` and require the
   version expected. That catches a truncated download or the wrong
   architecture, which a matching hash can't (if the release itself is
   wrong).
7. **Swap**: rename the running binary to `.old`, then `.new` into its
   place; if the second rename fails, undo the first. Windows lets a running
   program be renamed but not overwritten or deleted, which is why it's two
   renames and not a copy.
8. **Restart into it** when no check is in progress: at once if the page is
   idle, or at the next start. The new process takes the lock and the same
   port, and the open tab reconnects by itself ([03](03-the-local-app.md)).

**Rollback.** sion-backup puts a new version on probation (sion-backup#22),
recorded in a small file beside the binary, not in the database, which is
the likeliest thing to be broken. Here: a new version passes probation when
it has served a page and the register opened. If it fails to three times in
a row, the program checks `.old` still runs, swaps back, and adds the
version to the refused list, so it is not fetched again. The first update
into a release that has probation is not protected by it; that is a reason
to ship probation in the first release.

**What was rejected**, with sion-backup's reasons: an update address
settable in the config, for testing (it is a setting that hands the program
a binary to run; tests fake GitHub's name and certificate instead); an
"update available, fetch it?" step (one more thing to ignore).

## Releases

As in sion-backup, hand-rolled with `make` and `gh`, no GoReleaser:

- **Builds**: static and stripped, cross-compiled from one Linux runner
  (`CGO_ENABLED=0 -trimpath -ldflags '-s -w'`), named
  `reconcile-checks-GOOS-GOARCH[.exe]`, with the Windows GUI flag on the
  Windows build ([03](03-the-local-app.md)). Pure Go only, so that a
  cross-built binary runs everywhere, and SQLite through
  `modernc.org/sqlite`.
- **Version** stamped with `-ldflags -X main.version=$(git describe --tags
  --always --dirty)`. The release checkout needs `fetch-depth: 0`, or every
  binary calls itself by a bare hash (sion-backup#4).
- **Tagging** with `make tag-revision | tag-minor | tag-major`
  (`scripts/tag`): it switches to `main` and fast-forwards, refuses a dirty
  tree, unpushed commits, a diverged history or an existing tag, sorts tags
  by version, pushes the one tag (not `--tags`), and asks for confirmation
  (sion-backup#15, #16, #18: v0.1.0 there was cut from a feature branch). A
  person tags; an agent doesn't.
- **A tag publishes a draft release first**, the gates run against it, and
  only then is it published (sion-backup#20). Drafts are invisible to
  `/releases/latest`, so a release that fails a gate never reaches a
  computer. The traps found there (sion-backup#23, #24): a draft has no tag
  yet, so it is found by id and `tag_name`, not `gh release view vX`; GitHub
  allows several drafts with one tag, so a re-run deletes stale drafts and
  refuses an already-published one; a re-run uses the workflow file from the
  tagged commit, so fixing the workflow means a new tag.
- **Upload everything in `dist/`** and compare what is attached with
  `ls dist`, rather than a list of patterns: sion-backup v0.6.3 shipped
  without its installers because a pattern missed them (sion-backup#50).
  `SHA256SUMS` covers every file but itself, and `SHA256SUMS.sig` signs it.
- **The release job runs the checks, not `make check`**, which formats files
  instead of failing on them, so it would pass a tree CI should have
  refused (sion-backup#4).
- **Gates exit 0 (pass), 1 (the release is wrong: block) or 2 (the gate
  couldn't run: warn and publish)**, and never pass silently when they could
  not look (sion-backup#23, #27).

## Checks on every pull request

- `make test` on **Linux and Windows** runners (`windows-latest` is a real
  Windows machine, and free here): `go test -race`, `go vet`, a gofmt check,
  and a `go mod tidy` diff. macOS is left out until someone prints from a
  Mac.
- `make cross`: the build for every target type-checks on each push.
- `govulncheck`. `permissions: contents: read`, a concurrency group that
  cancels a stale run, `go-version-file: go.mod`.
- Dependabot: Go modules weekly, grouped; Actions monthly.
- **Golden tests** of the renderers: the PNG of an invented check compared
  with one committed, and the PDF's placed text compared with its
  positions, so that a change which moves a box shows up in the pull
  request, not on a check.

## Testing on Windows, and what can't be

Not Docker: Windows programs don't run in Linux containers, and sion-backup
tests Windows on GitHub's hosted Windows runner instead. Docker is used for
the Linux half of the self-update tests.

**On a Windows runner, every pull request:**

1. Build the `.exe`, run it with `--config` pointing at a test config and
   `--no-browser`, and check it **installs itself** where expected and makes
   its shortcut.
2. Check the **single-instance lock**: a second start finds the first and
   exits cleanly.
3. Drive the **local web app** from a Go test client: the launch secret,
   the session cookie, a refused request with a wrong `Host` or a
   cross-site origin, the preview PNG.
4. **Print to a fake IPP printer**, a small IPP server in the test, and
   assert what it received: a PDF, `sides=one-sided`, the tray,
   `print-scaling=none`, the media type. The paper can't be tested, but the
   request that decides where the ink lands can, on both systems.
5. Send to a **stub of Reconcile's routes** ([09](09-reconcile-api.md)):
   sent, already there, number used, amount differs, offline then sent.
6. Check that it **closes** when no page has been connected for the grace
   period ([03](03-the-local-app.md)).

**Self-update, in Docker on every pull request** (sion-backup's
`scripts/selfupdate-e2e`): the real release binary against a fake
`api.github.com` (a Docker network alias and a test certificate authority
trusted in the container). Cases: bad hash, bad or missing signature,
truncated, corrupt, no sums, no asset, draft, pre-release, 403, a hung
connection, a binary that reports the wrong version, a downgrade, a dev
build, an unwritable folder, and a broken build that must roll back.

**Self-update on Windows**, by the same fake through a `hosts` entry and the
test authority imported into the runner's certificate store. sion-backup
planned this and never built it; here the rename swap, which is the part
that differs on Windows, is what most needs it. As sion-backup put it,
untested PowerShell in a release gate is worse than a named gap, so it is
written when it can be run, and until then listed here as the gap.

**Before a release**: the previous published release updates itself to the
candidate, on Linux and on Windows, and must come back up serving its page.

**What no test can do**: put ink on check stock. The first print of a new
layout or a change to the print options is a person with a calibration page
and then one real check ([04](04-stock-and-layout.md)).


## The release signing key

Made once, by a person, before the first release: an Ed25519 key pair. The
public half is committed and compiled into the program; the private half is
only in the GitHub Actions secret the release job reads, and in an offline
backup (a password manager). Losing it means a release whose new key the
old programs can't verify, so they would have to be updated by hand once.
Agents never make, read or set it ([`CLAUDE.md`](../CLAUDE.md)).
