// Package selfupdate replaces this program with a newer release of itself:
// find it, verify it, stage it beside the running binary and swap it in, then
// watch the new version start and put the old one back if it can't.
// docs/10-install-update-and-releases.md, "Updating itself", is the
// specification; this package is its steps 1–7 and its rollback. Restarting
// into the new binary (step 8) is the caller's, because only the caller knows
// whether a check is being printed.
//
// It is ported from sion-backup's foundation/selfupdate (sion-backup#11 and
// #22), which ships the same kind of program to Windows and Linux and updates
// it in place. The rules kept from there say so where they are; what is new
// here is said where it is.
//
// # Why a program updates itself here
//
// Two computers, on two operating systems, kept by people who are not
// programmers. Nobody will remember to update either of them, and a fix that
// reaches only one of the two computers that print from one stack of checks
// is half a fix.
//
// # What is checked before anything is replaced
//
//  1. The release is published (not a draft, not a pre-release), its version
//     is a strict vX.Y.Z newer than this one, and it is not one this computer
//     already gave up on. A build whose version is not a strict vX.Y.Z —
//     "dev", a bare commit — never updates, so a working copy cannot replace
//     itself out from under the person testing it.
//  2. SHA256SUMS carries a valid Ed25519 signature by the release key, whose
//     public half the program was built with ([Config].Key). Checked over the
//     exact bytes downloaded, before a single hash in the file is believed.
//  3. The binary hashes to what the signed SHA256SUMS says.
//  4. The binary runs, and reports exactly the version the release is tagged
//     with. That catches a truncated download or the wrong architecture,
//     which a matching hash can't when the release itself is wrong — and it is
//     also what ties the signature to a version: SHA256SUMS names files, not
//     versions, so an old signed binary republished under a newer tag still
//     reports the old version and is refused here.
//
// Only then is anything moved, and the old binary is kept beside the new one
// until the new one has proved it starts.
//
// # Who says which version is right
//
// The release key. sion-backup checked the hash alone, and said plainly that
// a hash published beside the binary it vouches for proves the download
// whole, not genuine: whoever can replace one can replace both. A signature
// is the fix it intended and never built. Here GitHub only carries the
// release; a release that was not signed with the key, by the release job
// that alone holds it, is not installed whatever GitHub says about it. An
// updater built without a key refuses to update at all ([ErrNoKey]) rather
// than skip the check, because a build that quietly installs unsigned
// releases is the one nobody notices.
//
// # The files beside the binary
//
// Everything this package keeps is a file next to the program, never in the
// register, which is the likeliest thing to be broken by a bad release:
//
//	reconcile-checks[.exe]          the program
//	reconcile-checks.new[.exe]      a download being verified; gone after Apply
//	reconcile-checks.old[.exe]      the previous version, until the new one passes
//	reconcile-checks.probation      the version being tried and how often it started
//	reconcile-checks.refused        versions this computer will not install again
//
// On Windows the runnable ones keep ".exe" last, so that each is still a
// program to Windows when it is run to be smoke-tested.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Release is one build that could be installed.
type Release struct {
	// Version is the tag, "v1.4.0".
	Version string

	// URL is where the binary for this computer's system is.
	URL string

	// SHA256 is what it must hash to, lower-case hex, read from a SHA256SUMS
	// whose signature has already been checked.
	SHA256 string
}

// Errors a person may see, from Update now in Settings or from
// `reconcile-checks update`. Each says what happens next, in words that don't
// name this package. Details are wrapped after them, so that errors.Is finds
// the kind and the log has the particulars.

// ErrNoKey is a build that was given no key to check releases with. It never
// updates; installing an unchecked release would be worse than not updating.
var ErrNoKey = errors.New("this copy of the program was built without the key that checks updates, " +
	"so it does not update itself; download the latest release by hand")

// ErrNotWritable is a program whose own folder it may not write to, so that
// it can't replace itself. A fact about the installation rather than a
// failure: it will still be true tomorrow (sion-backup#69 is the computer
// that never updated again because of it).
var ErrNotWritable = errors.New("the program can't update itself because it can't write to its own folder; " +
	"install it again from the latest release, where its user can write")

// ErrNoRelease is a release there is nothing installable in: no published
// release, a draft or pre-release where the latest should be, or no build for
// this system. Usually a state that passes; the next check tries again.
var ErrNoRelease = errors.New("there is no update this computer can install right now; it will look again later")

// ErrUnverified is a release that failed a check: its signature, its hashes,
// or the binary itself. Nothing was changed, and the next check tries again.
var ErrUnverified = errors.New("an update was found but did not pass its checks, so it was not installed " +
	"and nothing changed")

// ErrWaitForRestart is an update that can't be installed yet because the last
// one hasn't finished: it is installed and the program hasn't restarted into
// it, or the new version is still on probation, or the previous binary can't
// be removed. The last is Windows, where after an update the previous binary
// is the program still running and can't be deleted until it stops
// (docs/10, "Two updates without a restart between them"). Not a failure.
var ErrWaitForRestart = errors.New("an update is already installed and waits for the program to restart; " +
	"the next one follows after that")

// DefaultRepository and DefaultAPI are where releases come from.
//
// Parameters of [Config] rather than constants only so that the tests can
// point the updater at a fake. main gives the real ones and never reads either
// from the config file: docs/10 rejected an update address settable in the
// config, because it is a setting that hands the program a binary to run.
const (
	DefaultRepository = "jroedel/reconcile-checks"
	DefaultAPI        = "https://api.github.com"
)

// downloadTimeout bounds the whole fetch of a binary. Generous: these are
// tens of megabytes, and a slow connection is not a broken one. A hung
// connection still ends.
const downloadTimeout = 10 * time.Minute

// maxBinary bounds the download, so that a server sending without end fills
// neither the disk nor the ten minutes. Several times the real size.
const maxBinary = 256 << 20

// Config is what an updater needs.
type Config struct {
	// Current is this build's version, as stamped at link time.
	Current string

	// Key is the public half of the release signing key. Empty is allowed, so
	// that probation (Start, Passed) still works in a build without one, but
	// such an updater refuses to update ([ErrNoKey]). A key of the wrong
	// length is refused by New.
	Key ed25519.PublicKey

	// Repository is "owner/name"; empty is DefaultRepository.
	Repository string

	// API is GitHub's API address; empty is DefaultAPI.
	API string

	// Executable overrides where this program is, for the tests.
	Executable string

	HTTP *http.Client
	Log  *slog.Logger
}

// Updater replaces this program.
type Updater struct {
	current string
	key     ed25519.PublicKey
	repo    string
	api     string
	exe     string
	http    *http.Client
	log     *slog.Logger

	// mu makes Apply one at a time: Update now pressed during the check at
	// start would otherwise be two downloads into one .new.
	mu sync.Mutex
}

// Self is the file this program is running from, with symlinks resolved:
// an update replaces that file, not a name pointing at it. Exported, as in
// sion-backup#61, because installing and Settings ask where the program is
// without needing an Updater.
func Self() (string, error) {
	found, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("the program can't find its own file: %w", err)
	}

	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}

	return found, nil
}

// New checks the configuration and finds this program's own file.
//
// Made once, at start, and kept: after a swap, the running program's own file
// is .old, and on Linux os.Executable follows it there, so an updater made
// afterwards would take .old for the program.
func New(cfg Config) (*Updater, error) {
	// ed25519.Verify panics on a key of the wrong length, so a bad key is
	// refused here, once, rather than discovered by a crash at update time.
	if len(cfg.Key) != 0 && len(cfg.Key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the update key is %d bytes and must be %d", len(cfg.Key), ed25519.PublicKeySize)
	}

	exe := cfg.Executable

	if exe == "" {
		found, err := Self()
		if err != nil {
			return nil, err
		}

		exe = found
	}

	repo := cfg.Repository
	if repo == "" {
		repo = DefaultRepository
	}

	api := strings.TrimRight(cfg.API, "/")
	if api == "" {
		api = DefaultAPI
	}

	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}

	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Updater{
		current: cfg.Current,
		key:     cfg.Key,
		repo:    repo,
		api:     api,
		exe:     exe,
		http:    client,
		log:     log,
	}, nil
}

// Path is the program's own file, the one an update replaces.
func (u *Updater) Path() string { return u.exe }

// Apply installs a newer release if there is one.
//
// It reports the release it installed and true, or a zero release and false
// when there was nothing to do, which is the ordinary answer and not an
// error. Nothing here restarts anything: the caller restarts into the new
// binary when no check is in progress (docs/10, step 8).
func (u *Updater) Apply(ctx context.Context) (Release, bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Before the network: an update in progress makes any newer release moot
	// until the program has restarted.
	if err := u.pending(); err != nil {
		return Release{}, false, err
	}

	latest, ok, err := u.Latest(ctx)
	if err != nil || !ok {
		return Release{}, false, err
	}

	if err := u.Writable(); err != nil {
		return Release{}, false, err
	}

	// Beside the binary rather than in the temp directory, so that the swap
	// is a rename on one disk.
	staged := u.runnable(".new")

	if err := u.download(ctx, latest, staged); err != nil {
		_ = os.Remove(staged)

		return Release{}, false, err
	}

	defer os.Remove(staged)

	if err := smokeTest(ctx, staged, latest.Version); err != nil {
		return Release{}, false, err
	}

	if err := u.swap(staged); err != nil {
		return Release{}, false, err
	}

	// Before the caller hears of it, because the caller's next move is to
	// restart into the new binary, which reads this on the way up. A failure
	// is logged rather than returned (sion-backup#22): the swap has happened,
	// and undoing a working update because a bookkeeping file couldn't be
	// written is the wrong trade. The cost is a version nobody is watching.
	if err := u.begin(latest.Version, u.current); err != nil {
		u.log.Warn("installed a new version but could not put it on probation",
			"err", err, "consequence", "it will not be rolled back if it fails to start")
	}

	u.log.Info("installed a newer release", "was", u.current, "now", latest.Version, "path", u.exe)

	return latest, true, nil
}

// pending refuses to start a second update before the first has finished.
//
// Two ways to tell. A probation file means a version was installed and either
// hasn't been restarted into or hasn't passed yet; replacing it would make
// .old a version that never ran, and the way back would lead nowhere. And a
// .old that can't be removed is, on Windows, the program still running from
// it after an earlier swap: swapping again would have to rename over it, and
// Windows won't. On Linux the second can't happen, and the first is the
// reason it doesn't matter.
func (u *Updater) pending() error {
	if p, on := u.read(); on {
		return fmt.Errorf("%w (%s is installed and not yet proven)", ErrWaitForRestart, p.Installed)
	}

	if err := os.Remove(u.runnable(".old")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w (the previous version can't be removed yet: %w)", ErrWaitForRestart, err)
	}

	return nil
}

// Writable reports whether this program may replace its own file.
//
// Checked by creating a file beside it rather than by reading permission
// bits, which ACLs and read-only mounts make lie (docs/10, step 3). Exported
// so that Settings can say so before an update is due, rather than a computer
// that never updates looking like one that has nothing to update to.
func (u *Updater) Writable() error {
	probe := u.beside(".probe")

	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%w (%s: %w)", ErrNotWritable, filepath.Dir(u.exe), err)
	}

	f.Close()

	return os.Remove(probe)
}

// download fetches the release into to, hashing it as it streams, and
// refuses anything that does not match.
func (u *Updater) download(ctx context.Context, r Release, to string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return fmt.Errorf("%w (the download address %q is not one: %w)", ErrNoRelease, r.URL, err)
	}

	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)

	resp, err := u.http.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't download %s; the next check tries again: %w", r.Version, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't download %s (%s); the next check tries again", r.Version, resp.Status)
	}

	// 0700: it is about to be run, and until it has been verified nobody else
	// should be able to run it either.
	f, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("couldn't save the download of %s: %w", r.Version, err)
	}

	sum := sha256.New()

	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		f.Close()

		return fmt.Errorf("couldn't download %s; the next check tries again: %w", r.Version, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("couldn't save the download of %s: %w", r.Version, err)
	}

	if n > maxBinary {
		return fmt.Errorf("%w (the download of %s is larger than any build of this program)", ErrUnverified, r.Version)
	}

	if got := hex.EncodeToString(sum.Sum(nil)); got != r.SHA256 {
		return fmt.Errorf("%w (%s hashes to %s, and its signed SHA256SUMS says %s)",
			ErrUnverified, r.Version, got, r.SHA256)
	}

	return nil
}

// smokeTestTimeout bounds the "does it run" check. A build for the wrong
// architecture fails at once; one that hangs is as broken as one that
// crashes. Long enough for Windows Defender to scan a new program the first
// time it runs.
const smokeTestTimeout = 30 * time.Second

// smokeTest runs a binary as `<path> version` and requires it to report want.
//
// The last check before the swap, and after the hash and the signature,
// because running something that failed verification is exactly what this
// package exists not to do. `version` is answered at the very top of main,
// before the lock, the config or the register, so running it has no side
// effects; it must stay that way.
//
// The version must be one of the words of the output exactly. sion-backup
// looked for it as a substring, under which v1.2.3 is found in v1.2.30.
func smokeTest(ctx context.Context, path, want string) error {
	ctx, cancel := context.WithTimeout(ctx, smokeTestTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w (%s does not run: %w: %s)",
			ErrUnverified, filepath.Base(path), err, strings.TrimSpace(string(out)))
	}

	for field := range strings.FieldsSeq(string(out)) {
		if field == want {
			return nil
		}
	}

	return fmt.Errorf("%w (%s reports %q, and the release is %s)",
		ErrUnverified, filepath.Base(path), strings.TrimSpace(string(out)), want)
}

// swap moves the new binary into place, keeping the old one beside it.
//
// Two renames rather than one over the top (sion-backup#11): Windows lets the
// running program be renamed but not overwritten or deleted, and everywhere it
// leaves the previous version on the disk for probation to go back to. pending
// has already removed any earlier .old, or refused.
func (u *Updater) swap(staged string) error {
	old := u.runnable(".old")

	if err := rename(u.exe, old); err != nil {
		return fmt.Errorf("couldn't move the running program aside to install the update; nothing changed: %w", err)
	}

	if err := rename(staged, u.exe); err != nil {
		// Put it back. A computer with no program at all can't print and
		// can't update itself out of it.
		if back := rename(old, u.exe); back != nil {
			return fmt.Errorf("installing the update failed (%w) and the previous version could not be put back (%w); "+
				"it is at %s: rename it to %s to start the program again", err, back, old, filepath.Base(u.exe))
		}

		return fmt.Errorf("couldn't install the update; the previous version is back in place: %w", err)
	}

	return nil
}

// beside is a bookkeeping file next to the program: reconcile-checks.<x>,
// without the program's .exe.
func (u *Updater) beside(suffix string) string {
	return u.stem() + suffix
}

// runnable is a program next to the program: reconcile-checks.<x>, with the
// .exe kept last on Windows. Docs/10 calls the download
// "reconcile-checks.new"; sion-backup named it after the whole file, which on
// Windows is reconcile-checks.exe.new, a file Windows doesn't think of as a
// program — and this one is run, to be smoke-tested, and so is .old, when it
// is checked before going back to it.
func (u *Updater) runnable(suffix string) string {
	return u.stem() + suffix + exeSuffix(u.exe)
}

// stem is the program's path without a trailing .exe.
func (u *Updater) stem() string {
	return strings.TrimSuffix(u.exe, exeSuffix(u.exe))
}

// exeSuffix is the path's ".exe", in whatever case it is written, or "".
func exeSuffix(path string) string {
	if ext := filepath.Ext(path); strings.EqualFold(ext, ".exe") {
		return ext
	}

	return ""
}

// AssetName is what a release calls the binary for one system, as the
// release workflow builds it (docs/10, "How the release is built").
func AssetName(goos, goarch string) string {
	name := "reconcile-checks-" + goos + "-" + goarch

	if goos == "windows" {
		name += ".exe"
	}

	return name
}

// Newer reports whether candidate is a later version than current.
//
// Both must be strict release versions, vX.Y.Z. Anything else makes this
// false, in both directions and on purpose (sion-backup#11): an unparseable
// current version is a build somebody made by hand, and replacing it would
// throw away the change they are testing; an unparseable candidate is a
// release this build can't reason about.
func Newer(current, candidate string) bool {
	c, ok := parse(current)
	if !ok {
		return false
	}

	n, ok := parse(candidate)
	if !ok {
		return false
	}

	for i := range 3 {
		switch {
		case n[i] > c[i]:
			return true
		case n[i] < c[i]:
			return false
		}
	}

	return false
}

// parse reads "v1.4.0" into its three numbers.
//
// Stricter than sion-backup's, which accepted "1.4.0" without the v and, through
// strconv.Atoi, "v+1.4.0". The release workflow writes exactly vX.Y.Z, so
// anything else is not a release of this program.
func parse(v string) ([3]int, bool) {
	var out [3]int

	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return out, false
	}

	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return out, false
	}

	for i, p := range parts {
		if p == "" || len(p) > 9 || (len(p) > 1 && p[0] == '0') {
			return out, false
		}

		n := 0

		for _, r := range p {
			if r < '0' || r > '9' {
				return out, false
			}

			n = n*10 + int(r-'0')
		}

		out[i] = n
	}

	return out, true
}
