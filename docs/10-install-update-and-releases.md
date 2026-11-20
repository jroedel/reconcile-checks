# 10 — Install, update and releases

Two computers on two operating systems, kept by people who are not
programmers. So: **one file to download, nothing else to install, it keeps
itself up to date, and every merge to `main` is the update it gets.** Most of what follows was learned in
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

The copy is made by reading and writing the file, not by the system's copy,
so the "downloaded from the internet" mark stays on the download and
doesn't follow the installed program: SmartScreen asks once, not again at
the Start menu.

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

**Smart App Control**, on some Windows 11 computers, is stricter: when it is
**On**, it blocks an unsigned program with no "Run anyway" at all. Check
*Windows Security → App & browser control → Smart App Control* on each
Windows computer before installing; if it is On, the no-certificate
decision has to be revisited.

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
   program** (set at build time: the release has the real key, the tests a
   test key, and a release gate checks the published binary carries the
   real one). sion-backup checks only the hash and says plainly that this
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
8. **Restart into it** when no check is in progress (none `printing`, none
   waiting on *"Did it print correctly?"*): at once if the page is idle, or
   at the next start. There is no service manager to restart a program a
   person opened, so **the old process does it**: it gives up the lock and
   the port, starts the new binary told to wait for the lock, and watches it
   for about 30 seconds. The new one takes the same port, and the open tab
   reconnects with its cookie still good ([03](03-the-local-app.md)).

**Rollback, twice over.**

- **At the restart**: if the new version doesn't answer within those 30
  seconds, the old process swaps back, adds the version to the refused list,
  so it is not fetched again, and starts itself again.
- **Later**, by sion-backup's probation (sion-backup#22), recorded in a
  small file beside the binary, not in the database, which is the likeliest
  thing to be broken: a new version passes probation when it has served a
  page and the register opened. If it fails to three times in a row, the
  program checks `.old` still runs, swaps back, and refuses the version.

**Two updates without a restart between them** can't swap: `.old` is then
the running program, which Windows won't let be removed. The second update
waits for the next start. A rename that fails because Defender is still
scanning the new file is retried. The first update
into a release that has probation is not protected by it; that is a reason
to ship probation in the first release.

**What was rejected**, with sion-backup's reasons: an update address
settable in the config, for testing (it is a setting that hands the program
a binary to run; tests fake GitHub's name and certificate instead); an
"update available, fetch it?" step (one more thing to ignore).

## Push to deploy: a merge to `main` is a release

**Essential** (decided 2026-11-20). Every merge to `main` that changes the
program becomes a release, and every computer picks it up by itself
(above). Nobody tags, nobody uploads, nobody remembers.

Why it matters here more than anywhere:

- **Two computers, two operating systems, kept by people who aren't
  programmers.** A release that waits on someone to cut it is a fix that
  sits on `main` for weeks. With push to deploy, a fix merged is a fix on
  both computers by their next start.
- **One version everywhere.** The two computers print from one stack of
  checks and talk to one Reconcile; they should be running the same code
  within a day of each other, not whichever release someone last remembered
  to make.
- **It is how the sibling projects work** (sum-app, mass-intentions,
  drop-in forms): a merge to `main` deploys. Here "deploy" means "publish
  the release the computers update to".

### The path from a pull request to the computers

1. **A pull request**, opened by an agent or a person. `main` is protected
   on GitHub: a pull request is the only way in, for everyone including the
   owner; no force push; no deleting the branch; conversations resolved
   before merging. Once CI exists, the Linux and Windows test jobs become
   required checks too.
2. **The checks** run on Linux and Windows (below).
3. **A person reviews and merges.** This is the last human gate, so the
   pull request says what the change does to the paper and to what reaches
   Reconcile, not only to the code, and a change to a layout, a renderer or
   the print options says whether it was tried on real stock. An agent never
   merges.
4. **The release workflow**, on the push to `main`:
   - works out the next version: the latest release's `vX.Y.Z` with `Z + 1`,
     or `Y + 1` / `X + 1` when the merged pull request carries the label
     `release: minor` or `release: major`;
   - runs the checks again on the merged commit;
   - builds every target, writes `SHA256SUMS`, signs it;
   - creates a **draft** release for that version at that commit;
   - runs the release gates against the draft (below);
   - **publishes** it, which creates the tag.
5. **The computers** find it at their next start (at most a day), or at
   **Update now**, and swap to it when no check is in progress.

**Merges that change no program don't release**: a pull request touching
only `docs/`, `*.md` or `.claude/` builds nothing a computer could receive.
The workflow's `paths-ignore` says so.

**One release at a time, in order.** The workflow's concurrency group queues
rather than cancels (`cancel-in-progress: false`), so two quick merges become
two releases, in the order merged.

**A failed gate leaves a draft**, invisible to `/releases/latest`, and
nothing reaches a computer. The merge stays on `main`; the next merge that
fixes it releases both.

**Undoing a release is forward**: the computers never downgrade, so a bad
release is undone by a revert pull request, which becomes the next, higher
version. A release that won't even start is caught on the computer by
probation and rolled back there (above), and its version refused.

**Pausing releases**, if ever needed: a person disables the release workflow
in GitHub's Actions page, and enables it again after. Merges then wait on
`main` and go out together with the next one.

### How the release is built

As in sion-backup, hand-rolled with `make` and `gh`, no GoReleaser:

- **Builds**: static and stripped, cross-compiled from one Linux runner
  (`CGO_ENABLED=0 -trimpath -ldflags '-s -w'`), named
  `reconcile-checks-GOOS-GOARCH[.exe]`, with the Windows GUI flag on the
  Windows build ([03](03-the-local-app.md)). Pure Go only, so that a
  cross-built binary runs everywhere, and SQLite through
  `modernc.org/sqlite`.
- **Version** stamped with `-ldflags -X main.version=vX.Y.Z`, the version the
  workflow worked out. A build anywhere else stamps `git describe --tags
  --always --dirty`, which needs `fetch-depth: 0` in CI or every build calls
  itself by a bare hash (sion-backup#4), and is never a strict `vX.Y.Z`, so
  it never updates itself.
- **Tags are made only by publishing a release**, from the workflow. Nobody
  tags by hand, so the hand-tagging script sion-backup needed
  (sion-backup#15, #16, #18) has no place here; its lessons that remain are
  that versions sort by version, not date, and that a release is cut from
  `main` and nothing else.
- **Draft first, then gates, then publish** (sion-backup#20). The traps found
  there (sion-backup#23, #24): a draft has no tag yet, so it is found by id
  and `tag_name`, not `gh release view vX`; GitHub allows several drafts with
  one tag, so a re-run deletes stale drafts and refuses an already-published
  one; a re-run uses the workflow file from its own commit, so a fix to the
  workflow is a new merge, which push to deploy makes the natural way anyway.
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
- Dependabot opens pull requests for Go modules and Actions, weekly,
  grouped, and `go-version.yml` one for each Go patch release. **A person
  merges them**, like any other: there is no auto-merge here, because a merge
  is a release to both computers, and a merge made with GitHub's own token
  would not start the release workflow anyway.
- **Golden tests** of the renderers: the PNG of an invented check compared
  with one committed, and the PDF's placed text compared with its
  positions, so that a change which moves a box shows up in the pull
  request, not on a check.
- **The IPP client against a real IPP implementation** (`make ipp-interop`),
  on Linux: CUPS's `ippeveprinter`, on loopback and never announced, which
  spools what it receives. The fake printer in the unit tests decodes with
  the same library the client encodes with, so it can't catch an encoding
  another implementation rejects ([05](05-printing.md)).

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

## The signature, as built

`SHA256SUMS.sig` is one line: the standard base64, with padding, of the raw
64-byte Ed25519 signature over the exact bytes of `SHA256SUMS`. The release
job can make it with OpenSSL alone:

```sh
openssl pkeyutl -sign -rawin -inkey key.pem -in SHA256SUMS | base64 -w0 > SHA256SUMS.sig
```

(tried with a throwaway key, and accepted by Go's `ed25519.Verify`). The
updater checks it before reading any hash out of the file, and an updater
with no key refuses to update at all (`foundation/selfupdate`).

## Open questions

Found while building `foundation/selfupdate`; each has a provisional answer
in the code, to be confirmed or changed when it is wired in.

1. **How the public key gets into the program**: a committed file embedded
   with `//go:embed`, or set with `-ldflags` at build time? Either way the
   tests use a key of their own, and a release gate checks the published
   binary carries the real one. Its encoding (base64 of the 32 raw bytes
   is the obvious one) is decided with it.
2. **What "fails three times in a row" counts.** The code counts starts: a
   start on probation that never calls `Passed` has failed, and the fourth
   rolls back. With no service manager, a failed start is a person opening
   the program again; after a rollback the program starts the restored
   version and exits. The start the old process launches at the restart is
   the first.
3. **No update while on probation.** The code waits for the next start not
   only when `.old` can't be removed, but also while the running version
   hasn't passed. So the check at start belongs after `Passed`.
4. **The signature doesn't name the version.** `SHA256SUMS` lists files, so
   what ties a binary to its tag is the smoke test (the binary must report
   the version it was offered as). An old signed release republished under
   a new tag is refused only by that. Should the signed file also carry the
   version?
5. **A draft or pre-release at `/latest`** is an error today; "nothing to
   do" would also be defensible.
6. **Taking a version off the refused list.** `Forget` exists; no button or
   command calls it yet (sion-backup has `update --forget`). The list keeps
   the last 10.
7. **On Windows**, the staged file is `reconcile-checks.new.exe` (it is run
   for the smoke test, so `.exe` stays last), and a rename is retried on
   access denied as well as on a sharing violation.
