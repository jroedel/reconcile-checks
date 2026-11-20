package main

// The program's side of updating itself (docs/10-install-update-and-releases.md,
// "Updating itself"). foundation/selfupdate finds, verifies and swaps a
// release, and keeps probation; what only the program can do is here: the key
// it was built with, the update command, the check at start, and restarting
// into the new version, since nothing else will.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/foundation/logger"
	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// releaseKey is the public half of the release signing key, as the standard
// base64 of its 32 bytes, stamped by the build:
// -ldflags "-X main.releaseKey=…". A release build takes it from
// release-key.pub, which a person commits (docs/10, "The release signing
// key"); the self-update test builds with a key of its own. A build without
// one never updates (selfupdate.ErrNoKey).
//
// Stamped rather than embedded from a file so that a test can build the
// program unchanged with its own key, and so that a build from a checkout
// with no key file is a build that says so, not one that fails to compile.
var releaseKey = ""

// updateKey decodes releaseKey: nil when there is none.
func updateKey() (ed25519.PublicKey, error) {
	if releaseKey == "" {
		return nil, nil
	}

	b, err := base64.StdEncoding.DecodeString(releaseKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("this build's update key is not an Ed25519 public key, so it can't check updates")
	}

	return b, nil
}

// keyWord is the key's fingerprint as `version` prints it: "key:" and the
// first 16 hex digits of its SHA-256, "key:none" without one. A release gate
// compares it with release-key.pub's, so that a published binary is known to
// carry the real key and not a test one. One word, because the smoke test
// reads `version` word by word.
func keyWord() string {
	k, err := updateKey()

	switch {
	case err != nil:
		return "key:invalid"
	case k == nil:
		return "key:none"
	}

	sum := sha256.Sum256(k)

	return "key:" + hex.EncodeToString(sum[:8])
}

// newUpdater is the updater for this build.
func newUpdater(log *slog.Logger) (*selfupdate.Updater, error) {
	key, err := updateKey()
	if err != nil {
		return nil, err
	}

	return selfupdate.New(selfupdate.Config{Current: version, Key: key, Log: log})
}

// updateTimeout bounds `update`, download and smoke test included.
const updateTimeout = 15 * time.Minute

// updateCommand is `reconcile-checks update`: for support, and for the
// self-update test. A person never needs it; the program checks at start.
func updateCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reconcile-checks update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "say whether there is a newer release, and install nothing")
	forget := fs.Bool("forget", false, "let this computer install again the versions it gave up on")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	u, err := newUpdater(logger.New(quiet{stderr}, slog.LevelInfo))
	if err != nil {
		fmt.Fprintln(stderr, "reconcile-checks:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	if *forget {
		if err := u.Forget(); err != nil {
			fmt.Fprintln(stderr, "reconcile-checks:", err)
			return 1
		}

		fmt.Fprintln(stdout, "This computer will install again the versions it gave up on.")

		return 0
	}

	if *check {
		rel, ok, err := u.Latest(ctx)

		switch {
		case err != nil:
			fmt.Fprintln(stderr, "reconcile-checks:", err)
			return 1
		case !ok:
			fmt.Fprintf(stdout, "%s is the version to have: there is nothing newer to install.\n", version)
		default:
			fmt.Fprintf(stdout, "%s is available, and verified; this is %s.\n", rel.Version, version)
		}

		return 0
	}

	rel, ok, err := u.Apply(ctx)

	switch {
	case err != nil:
		fmt.Fprintln(stderr, "reconcile-checks:", err)
		return 1
	case !ok:
		fmt.Fprintf(stdout, "%s is the version to have: there is nothing newer to install.\n", version)
	default:
		fmt.Fprintf(stdout, "Installed %s. It starts the next time the program is opened.\n", rel.Version)
	}

	return 0
}

// checkEvery is how often the check at start asks GitHub: at most once a
// day (docs/10, "When").
const checkEvery = 24 * time.Hour

// checkedPath is the file whose time is the last check that reached an
// answer. In the data folder rather than beside the binary: it is this
// computer's, not this version's.
func checkedPath(data string) string { return filepath.Join(data, "update-checked") }

// checkDue reports whether a day has passed since the last check that
// reached an answer.
func checkDue(data string, now time.Time) bool {
	info, err := os.Stat(checkedPath(data))

	return err != nil || now.Sub(info.ModTime()) >= checkEvery
}

// checked records a check that reached an answer: something installed, or
// nothing to install. One that failed (offline, GitHub down) is not
// recorded, so the next start asks again.
func checked(data string, now time.Time) error {
	path := checkedPath(data)

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}

	return os.Chtimes(path, now, now)
}

// healthy reports whether the copy listening on port answers /healthz with
// want, its version. Any answer but "ok <want>" is no.
func healthy(ctx context.Context, port int, want string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/healthz", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	return resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok "+want
}

// waitHealthy asks portOf's /healthz until it answers with want, for up to
// within. portOf is asked again each time, because a restarted copy writes its
// port to runtime.json once it has one.
func waitHealthy(ctx context.Context, portOf func() int, want string, within time.Duration) bool {
	deadline := time.Now().Add(within)

	for time.Now().Before(deadline) && ctx.Err() == nil {
		if p := portOf(); p > 0 && healthy(ctx, p, want) {
			return true
		}

		time.Sleep(250 * time.Millisecond)
	}

	return false
}

// restartWithin is how long a restarted copy has to answer (docs/10, step 8).
const restartWithin = 30 * time.Second

// relaunch is what a process does after it has let go of the lock and the
// port: start another copy of the program, and for an update, watch it come
// up and go back if it doesn't.
type relaunch struct {
	updater *selfupdate.Updater
	data    string
	args    []string

	// want is the version the new copy must answer /healthz with, for an
	// update restart; "" for a start after a rollback, which nobody watches.
	want string
}

// relaunchArgs are the flags the next copy is started with: the same
// config, told to wait for this one's lock rather than hand over to it.
func relaunchArgs(opt options, noBrowser bool) []string {
	args := []string{"--wait-for-lock", restartWithin.String()}

	if opt.config != "" {
		args = append(args, "--config", opt.config)
	}

	if noBrowser {
		args = append(args, "--no-browser")
	}

	return args
}

// run starts the next copy, and for an update watches it. It is called once
// start has returned, so the lock, the port, the register and the log are
// all let go of.
func (r *relaunch) run(stderr io.Writer) int {
	logFile, err := openLog(r.data)
	if err != nil {
		fmt.Fprintln(stderr, "reconcile-checks:", err)
		return 1
	}
	defer logFile.Close()

	log := logger.New(io.MultiWriter(logFile, quiet{stderr}), slog.LevelInfo)

	cmd, err := startCopy(r.updater.Path(), r.args)
	if err != nil {
		log.Error("could not start the program again", "path", r.updater.Path(), "err", err)
		return 1
	}

	if r.want == "" {
		log.Info("started the program again", "path", r.updater.Path())
		_ = cmd.Process.Release()

		return 0
	}

	log.Info("restarting into the update", "version", r.want)

	port := func() int {
		rt, err := loadRuntime(r.data)
		if err != nil {
			return 0
		}

		return rt.Port
	}

	if waitHealthy(context.Background(), port, r.want, restartWithin) {
		log.Info("the update is running", "version", r.want)
		_ = cmd.Process.Release()

		return 0
	}

	// It didn't come up: stop it, put this version back, refuse that one,
	// and start this version again (docs/10, "Rollback, twice over").
	log.Warn("the update did not answer; going back", "version", r.want, "within", restartWithin)

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	restored, err := r.updater.Revert(ctx)
	if err != nil {
		log.Error("could not go back to the previous version", "err", err)
		return 1
	}

	again, err := startCopy(r.updater.Path(), r.args)
	if err != nil {
		log.Error("went back, but could not start the program again", "version", restored, "err", err)
		return 1
	}

	_ = again.Process.Release()

	log.Info("went back to the previous version and started it", "version", restored, "refused", r.want)

	return 0
}

// startCopy starts the program at path on its own, so that it outlives this
// process.
func startCopy(path string, args []string) (*exec.Cmd, error) {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = detached()

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return cmd, nil
}
