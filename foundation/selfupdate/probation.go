package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// # Why a new version is on probation
//
// Everything in Apply happens before the new binary has ever run as the
// program. The signature proves it is a release; the smoke test proves it is
// the right architecture and answers `version`. Neither proves it works, and
// `version` is answered before the config is read or the register opened — so
// a release whose register can't open passes every check and then fails at
// the one thing a person does with it (sion-backup#22, where the same gap was
// a broken migration).
//
// So a swap is not the end of an update. The new version starts on probation,
// and if it fails three times in a row, the previous one is put back and the
// failed version refused, so that it is not fetched again.
//
// # What counts as working
//
// Having served a page and opened the register (docs/10, "Rollback, twice
// over"). The caller knows when that is, and says so with [Updater.Passed].
// sion-backup used "stayed up ten minutes", because a backup daemon may serve
// nothing for days; this program serves a page within a second of starting,
// or something is wrong.
//
// # Why the state is a file beside the binary
//
// Because the register is the likeliest thing a bad release breaks. Nothing
// that depends on the register, or on the config having been read, can be
// part of the way back from a release that broke them.

// probationStarts is how many starts a new version gets.
//
// Three: a person opening a program that doesn't appear tries again, and a
// second try that also fails is still possibly a bad moment (the network at
// boot, a slow disk); a third is not. The fourth start goes back.
const probationStarts = 3

// maxRefused bounds the refused list. Nothing beside the program grows
// without limit.
const maxRefused = 10

// probation is the state a swap leaves behind, as JSON beside the binary.
//
// Readable on purpose: somebody looking at a computer that is a version
// behind should be able to see why in a text editor.
type probation struct {
	// Installed is the version being tried.
	Installed string `json:"installed"`

	// Previous is what it replaced, and what .old should report.
	Previous string `json:"previous"`

	// Starts counts starts of Installed, including the one in progress. Zero
	// is a swap that hasn't been restarted into yet.
	Starts int `json:"starts"`

	Began time.Time `json:"began"`
}

func (u *Updater) probationPath() string { return u.beside(".probation") }
func (u *Updater) refusedPath() string   { return u.beside(".refused") }

// begin records that a swap just happened.
func (u *Updater) begin(installed, previous string) error {
	return u.write(probation{Installed: installed, Previous: previous, Began: time.Now()})
}

// write saves the probation state.
func (u *Updater) write(p probation) error {
	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("recording the update's probation: %w", err)
	}

	if err := writeFile(u.probationPath(), append(blob, '\n')); err != nil {
		return fmt.Errorf("recording the update's probation: %w", err)
	}

	return nil
}

// read loads the probation state, or reports that there is none.
func (u *Updater) read() (probation, bool) {
	blob, err := os.ReadFile(u.probationPath())
	if err != nil {
		return probation{}, false
	}

	var p probation

	if err := json.Unmarshal(blob, &p); err != nil {
		// Unreadable is treated as absent (sion-backup#22). A corrupt file
		// here would otherwise be a computer whose program never starts again,
		// which is worse than the failure probation guards against.
		u.log.Warn("the update's probation file is unreadable; ignoring it",
			"path", u.probationPath(), "err", err)

		return probation{}, false
	}

	return p, p.Installed != ""
}

// Outcome is what a start found.
type Outcome int

const (
	// Nothing is the ordinary start: no update is being watched.
	Nothing Outcome = iota

	// OnProbation is a version that was installed and has not yet passed.
	// The caller calls [Updater.Passed] once it has served a page and opened
	// the register.
	OnProbation

	// RolledBack is a version given up on. The program on the disk is now
	// the previous one, and this process is the failed one: the caller starts
	// [Updater.Path] and exits, since there is no service manager to do it.
	RolledBack

	// Stuck is a version that failed probation and could not be rolled back,
	// because the previous binary is gone or does not run. Nothing was
	// changed, and the caller carries on: a program that starts badly is
	// still better than none, and can still be updated out of it.
	Stuck
)

func (o Outcome) String() string {
	switch o {
	case Nothing:
		return "nothing"
	case OnProbation:
		return "on probation"
	case RolledBack:
		return "rolled back"
	case Stuck:
		return "stuck"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// Start is what [Updater.Start] found.
type Start struct {
	Outcome Outcome

	// Version is the version now on the disk: the one being tried, or the one
	// restored.
	Version string

	// Failed is the version given up on. Set for RolledBack and Stuck.
	Failed string

	// Starts is how many times the version on probation has been started.
	Starts int
}

// Start decides what to do about the last update. It is called at the top of
// a start that will serve the app, before the config and the register, whose
// failure is what it is there to survive; it needs nothing but the path to
// this program. The context bounds the smoke test of the binary it would go
// back to, and nothing else here blocks.
func (u *Updater) Start(ctx context.Context) Start {
	p, on := u.read()

	if !on {
		// No update is being watched, so the previous binary — left by an
		// update that passed or by a rollback — is no longer needed. Removed
		// here rather than at the swap because on Windows it was the running
		// program then. It may still be, for a few seconds, while the process
		// that restarted into this one watches it; then it waits for the next
		// start, or the next update removes it (pending).
		u.removeOld()

		return Start{Outcome: Nothing}
	}

	p.Starts++

	if p.Starts <= probationStarts {
		if err := u.write(p); err != nil {
			u.log.Warn("could not record this start of a version on probation", "err", err)
		}

		u.log.Info("this version is on probation",
			"version", p.Installed, "start", p.Starts, "of", probationStarts)

		return Start{Outcome: OnProbation, Version: p.Installed, Starts: p.Starts}
	}

	restored, err := u.rollBack(ctx, p)

	// Either way probation is over: a program that tried to roll back at
	// every start would be its own kind of broken.
	_ = os.Remove(u.probationPath())

	if err != nil {
		u.log.Error("gave up on this version and could not go back", "version", p.Installed, "err", err)

		return Start{Outcome: Stuck, Version: p.Installed, Failed: p.Installed, Starts: p.Starts}
	}

	u.log.Warn("gave up on a version that would not start; went back",
		"gave_up_on", p.Installed, "now", restored, "starts", p.Starts)

	return Start{Outcome: RolledBack, Version: restored, Failed: p.Installed, Starts: p.Starts}
}

// Passed records that the version on probation works: it has served a page
// and opened the register. Only now is the previous binary removed, because
// until now it was the way back.
//
// Safe to call when nothing is on probation, and safe to call twice, so the
// caller need not remember which start this is.
func (u *Updater) Passed() {
	p, on := u.read()
	if !on {
		return
	}

	if err := os.Remove(u.probationPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.log.Warn("could not end the update's probation", "err", err)

		return
	}

	u.removeOld()

	u.log.Info("this version passed probation; keeping it", "version", p.Installed)
}

// Revert puts the previous version back after an update that did not come up
// at the restart, refuses the version that didn't, and ends probation
// (docs/10, "Rollback, twice over": at the restart).
//
// Called by the process that restarted into the update, which is the
// previous version, running from .old, once it has stopped the new one. It
// reports the version restored, which is its own.
func (u *Updater) Revert(ctx context.Context) (string, error) {
	p, on := u.read()
	if !on {
		return "", errors.New("there is no update to undo")
	}

	restored, err := u.rollBack(ctx, p)
	if err != nil {
		return "", err
	}

	if err := os.Remove(u.probationPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.log.Warn("could not end the update's probation", "err", err)
	}

	u.log.Warn("the update did not start; went back", "gave_up_on", p.Installed, "now", restored)

	return restored, nil
}

// rollBack puts the previous binary back, and reports which version that is.
//
// It refuses rather than tries if the previous binary is missing or does not
// run. A computer on a version that starts badly is bad; one with no working
// program at all can't even be updated out of it.
func (u *Updater) rollBack(ctx context.Context, p probation) (string, error) {
	old := u.runnable(".old")

	if _, err := os.Stat(old); err != nil {
		return "", fmt.Errorf("there is no previous version to go back to: %w", err)
	}

	// The same check the new binary had to pass. The old one passed it once,
	// but the disk it is on is the disk that just produced a version which
	// would not start.
	if err := smokeTest(ctx, old, p.Previous); err != nil {
		return "", fmt.Errorf("the previous version does not run: %w", err)
	}

	// Before the renames, so that a crash among them can't leave a computer
	// that installs the same bad version again at its next check.
	if err := u.refuse(p.Installed); err != nil {
		u.log.Warn("could not record the refused version", "version", p.Installed, "err", err)
	}

	// Three renames rather than two, for swap's reason: on Windows the
	// failed binary may be a running image, which can be renamed but not
	// overwritten. It becomes the new .old and goes at the next start that is
	// not on probation.
	back := u.runnable(".back")

	if err := rename(old, back); err != nil {
		return "", fmt.Errorf("couldn't move the previous version back: %w", err)
	}

	if err := rename(u.exe, old); err != nil {
		_ = rename(back, old)

		return "", fmt.Errorf("couldn't move the failed version aside: %w", err)
	}

	if err := rename(back, u.exe); err != nil {
		// Put the failed one back rather than leave nothing: it starts badly,
		// but it is what the shortcut points at.
		if undo := rename(old, u.exe); undo != nil {
			return "", fmt.Errorf("putting the previous version back failed (%w) and there is now no program at %s (%w); "+
				"the previous version is at %s: rename it to %s", err, u.exe, undo, back, u.exe)
		}

		return "", fmt.Errorf("couldn't put the previous version back: %w", err)
	}

	return p.Previous, nil
}

// removeOld deletes the previous binary, if one is beside this one.
func (u *Updater) removeOld() {
	if err := os.Remove(u.runnable(".old")); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.log.Debug("could not remove the previous version yet", "err", err)
	}
}

// refuse adds a version to the list this computer will not install again.
func (u *Updater) refuse(version string) error {
	refused := u.Refused()

	if slices.Contains(refused, version) {
		return nil
	}

	refused = append(refused, version)

	if len(refused) > maxRefused {
		refused = refused[len(refused)-maxRefused:]
	}

	return writeFile(u.refusedPath(), []byte(strings.Join(refused, "\n")+"\n"))
}

// Refused lists the versions this computer gave up on, oldest first.
//
// Exported because it is the answer to "why is this computer a version
// behind", which Settings shows.
func (u *Updater) Refused() []string {
	blob, err := os.ReadFile(u.refusedPath())
	if err != nil {
		return nil
	}

	var out []string

	for line := range strings.Lines(string(blob)) {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}

	return out
}

// Forget clears the refused list, so that a version that failed here can be
// tried again: for the case where the version was fine and the computer was
// not (a full disk, a register another program had open).
func (u *Updater) Forget() error {
	if err := os.Remove(u.refusedPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("couldn't clear the list of refused updates: %w", err)
	}

	return nil
}

// writeFile replaces a small file whole: written beside it and renamed over
// it, so that a crash leaves the old contents or the new, never half. Half a
// probation file reads as none, which would let a bad version off probation.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	if err := rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}
