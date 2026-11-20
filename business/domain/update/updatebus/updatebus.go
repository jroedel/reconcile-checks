// Package updatebus is when this program updates itself, and what Settings
// says about it (docs/10-install-update-and-releases.md, "Updating itself"):
// the check at start, at most once a day; Update now; waiting for no check to
// be printing before restarting; and the state of all that, for the page.
//
// foundation/selfupdate does the updating: finding, verifying, staging and
// swapping a release, and probation. Restarting into it is main's, which
// alone holds the lock and the port; this package asks for it through
// Config.Restart once nothing is printing.
package updatebus

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode"

	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// Updater is the part of foundation/selfupdate this package uses.
type Updater interface {
	Apply(ctx context.Context) (selfupdate.Release, bool, error)
	Writable() error
	Refused() []string
	Forget() error
}

// Config is what the rules need.
type Config struct {
	// Updater is nil when this build can't update: its key couldn't be read.
	Updater Updater

	Version string // this build's
	HasKey  bool   // whether it was built with the release key
	Data    string // the data folder, where the day's check is recorded

	// Busy reports whether a check is printing or waiting on "Did it print
	// correctly?" (checkbus.Business.Busy). No restart happens while it is.
	Busy func(ctx context.Context) (bool, error)

	// Restart is main's: let go of the lock and the port and start the
	// version just installed (cmd/reconcile-checks, relaunch).
	Restart func(version string)

	Log *slog.Logger
	Now func() time.Time
}

// State is where updating is.
type State string

const (
	Idle       State = "idle"       // nothing happening
	Checking   State = "checking"   // asking GitHub, downloading, verifying
	Waiting    State = "waiting"    // installed; waiting for printing to finish
	Restarting State = "restarting" // letting go, to start the new version
)

// Status is what Settings shows.
type Status struct {
	Version string
	State   State

	// CantUpdate is why this copy doesn't update itself, in a sentence; ""
	// when it does.
	CantUpdate string

	Checked   time.Time // the last check that reached an answer; zero for never
	Installed string    // the version installed, while waiting or restarting
	Problem   string    // why the last check failed, if it did; the next tries again
	Latest    bool      // the last check found nothing newer
	Refused   []string  // versions this computer gave up on
}

// Business is the rules.
type Business struct {
	cfg Config

	mu        sync.Mutex
	state     State
	installed string
	problem   string
	latest    bool
}

// New makes one.
func New(cfg Config) *Business {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &Business{cfg: cfg, state: Idle}
}

// checkEvery is how often the check at start asks: at most once a day.
const checkEvery = 24 * time.Hour

// checkedPath is the file whose time is the last check that reached an
// answer. In the data folder: it is this computer's, not this version's.
func (b *Business) checkedPath() string { return filepath.Join(b.cfg.Data, "update-checked") }

// Status is where things stand.
func (b *Business) Status() Status {
	b.mu.Lock()
	s := Status{Version: b.cfg.Version, State: b.state, Installed: b.installed, Problem: b.problem, Latest: b.latest}
	b.mu.Unlock()

	if info, err := os.Stat(b.checkedPath()); err == nil {
		s.Checked = info.ModTime()
	}

	s.CantUpdate = b.cantUpdate()

	if b.cfg.Updater != nil {
		s.Refused = b.cfg.Updater.Refused()
	}

	return s
}

// cantUpdate is why this copy doesn't update itself, or "".
func (b *Business) cantUpdate() string {
	switch {
	case !selfupdate.Newer("v0.0.0", b.cfg.Version):
		return "This is a development build (" + b.cfg.Version + "), which never updates itself."
	case !b.cfg.HasKey || b.cfg.Updater == nil:
		return "This copy was built without the key that checks updates, so it doesn't update itself. Download the latest release by hand."
	}

	if err := b.cfg.Updater.Writable(); err != nil {
		return sentence(err)
	}

	return ""
}

// CheckIfDue is the check at start: once a day at most, and only for a copy
// that can update. It returns once the check is over, and when it installed
// something, once the restart has been asked for.
func (b *Business) CheckIfDue(ctx context.Context) {
	if info, err := os.Stat(b.checkedPath()); err == nil && b.cfg.Now().Sub(info.ModTime()) < checkEvery {
		return
	}

	if b.cantUpdate() != "" {
		return
	}

	b.check(ctx)
}

// CheckNow is Update now: it starts a check in the background, unless one
// is already under way, and returns at once. ctx is the program's own, not
// the request's, which ends with the page.
func (b *Business) CheckNow(ctx context.Context) error {
	if why := b.cantUpdate(); why != "" {
		return errors.New(why)
	}

	b.mu.Lock()
	busy := b.state != Idle
	if !busy {
		b.state = Checking
	}
	b.mu.Unlock()

	if !busy {
		go b.run(ctx)
	}

	return nil
}

// Forget lets this computer install again the versions it gave up on.
func (b *Business) Forget() error {
	if b.cfg.Updater == nil {
		return nil
	}

	return b.cfg.Updater.Forget()
}

// check takes the state from Idle and runs, unless something else already
// is.
func (b *Business) check(ctx context.Context) {
	b.mu.Lock()
	if b.state != Idle {
		b.mu.Unlock()
		return
	}
	b.state = Checking
	b.mu.Unlock()

	b.run(ctx)
}

// run checks, installs, waits for printing to finish and asks for the
// restart. The state is Checking when it starts.
func (b *Business) run(ctx context.Context) {
	rel, ok, err := b.cfg.Updater.Apply(ctx)

	if err != nil {
		b.cfg.Log.Warn("the check for an update failed", "err", err)
		b.set(Idle, "", sentence(err), false)

		return
	}

	b.recordCheck()

	if !ok {
		b.set(Idle, "", "", true)
		return
	}

	b.set(Waiting, rel.Version, "", false)

	// Never while a check is printing or waiting on "Did it print
	// correctly?": that is the state printing, until it is answered.
	for {
		busy, err := b.cfg.Busy(ctx)
		if err == nil && !busy {
			break
		}

		select {
		case <-ctx.Done():
			// Closing anyway: the next start is the new version.
			return
		case <-time.After(5 * time.Second):
		}
	}

	b.set(Restarting, rel.Version, "", false)
	b.cfg.Restart(rel.Version)
}

func (b *Business) set(s State, installed, problem string, latest bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.state, b.installed, b.problem, b.latest = s, installed, problem, latest
}

// recordCheck marks a check that reached an answer: something installed, or
// nothing to install. One that failed (offline, GitHub down) is not, so the
// next start asks again.
func (b *Business) recordCheck() {
	path := b.checkedPath()
	now := b.cfg.Now()

	if err := os.WriteFile(path, nil, 0o600); err == nil {
		err = os.Chtimes(path, now, now)
		if err == nil {
			return
		}
	}

	b.cfg.Log.Warn("could not record the check for an update", "path", path)
}

// sentence is an error as a person reads it: a capital first letter and a
// full stop.
func sentence(err error) string {
	r := []rune(err.Error())
	if len(r) == 0 {
		return ""
	}

	r[0] = unicode.ToUpper(r[0])

	if last := r[len(r)-1]; last != '.' && last != '?' && last != '!' {
		r = append(r, '.')
	}

	return string(r)
}
