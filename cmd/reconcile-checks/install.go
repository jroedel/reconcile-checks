package main

// Installing itself (docs/10-install-update-and-releases.md, "Installing"):
// a release run from where it was downloaded copies itself to where its user
// can write, adds itself to the Start menu or the applications menu, starts
// from there at Settings, and is done. No installer, no administrator.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/foundation/install"
	"github.com/jroedel/reconcile-checks/foundation/lockfile"
	"github.com/jroedel/reconcile-checks/foundation/paths"
	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
	"github.com/jroedel/reconcile-checks/foundation/shortcut"
)

// installName is the program's file name once installed.
func installName() string {
	if runtime.GOOS == "windows" {
		return app + ".exe"
	}

	return app
}

// menuEntry is the program's shortcut.
func menuEntry(target string) shortcut.Shortcut {
	return shortcut.Shortcut{
		ID:      app,
		Name:    "Reconcile Checks",
		Comment: "Write and print checks, and send each one's image to Reconcile",
		Target:  target,
		Icon:    page.Icon(),
	}
}

// installedAt is where the program is installed, on this computer.
func installedAt() (string, paths.Dirs, error) {
	dirs, err := paths.For(app)
	if err != nil {
		return "", paths.Dirs{}, err
	}

	return filepath.Join(dirs.Install, installName()), dirs, nil
}

// wantsInstalling reports whether a start should install first: only a
// release, only started as a person starts it (no --config, which is
// development and the tests, and no --no-install), and only when not
// running from where it is installed.
func wantsInstalling(v string, opt options, self, target string) bool {
	return selfupdate.Newer("v0.0.0", v) && opt.config == "" && !opt.noInstall && !sameFile(self, target)
}

// firstRun installs a release run from where it was downloaded, and starts
// the installed copy. It reports true when it did, and the start is over.
// Started from where it is installed, it puts the menu shortcut back if it
// is missing, and reports false.
func firstRun(opt options, stdout, stderr io.Writer) (bool, error) {
	if !selfupdate.Newer("v0.0.0", version) || opt.config != "" || opt.noInstall {
		return false, nil
	}

	self, err := selfupdate.Self()
	if err != nil {
		return false, err
	}

	target, dirs, err := installedAt()
	if err != nil {
		return false, err
	}

	if !wantsInstalling(version, opt, self, target) {
		// The installed copy: its shortcut, put back if someone removed it.
		if err := shortcut.Ensure(menuEntry(target)); err != nil {
			fmt.Fprintln(quiet{stderr}, "reconcile-checks: couldn't put back the menu shortcut:", err)
		}

		return false, nil
	}

	// A copy already running is handed over to as at any second start, by
	// start; this download replaces nothing that is running.
	if running(dirs.Data) {
		return false, nil
	}

	installed := installedVersion(target)
	fresh := installed == ""

	if fresh || selfupdate.Newer(installed, version) {
		if _, err := install.Copy(self, dirs.Install, installName()); err != nil {
			return true, err
		}
	}

	if err := shortcut.Ensure(menuEntry(target)); err != nil {
		fmt.Fprintln(quiet{stderr}, "reconcile-checks: installed, but couldn't add the menu shortcut:", err)
	}

	// The first start opens Settings, where the printer is set up; a
	// download run again over an install just opens the program.
	args := []string{}
	if fresh {
		args = append(args, "--settings")
	}

	cmd, err := startCopy(target, args)
	if err != nil {
		return true, fmt.Errorf("installed at %s, but couldn't start it: %w", target, err)
	}

	_ = cmd.Process.Release()

	fmt.Fprintf(quiet{stdout}, "Reconcile Checks is installed at %s, and in the menu. This download can be deleted.\n", target)

	return true, nil
}

// installCommand is `reconcile-checks install`: copy and shortcut, without
// starting anything. For support, and the Windows check in CI.
func installCommand(stdout, stderr io.Writer) int {
	self, err := selfupdate.Self()
	if err == nil {
		var target string
		var dirs paths.Dirs

		target, dirs, err = installedAt()
		if err == nil && !sameFile(self, target) {
			if running(dirs.Data) {
				err = errors.New("the program is running: quit Reconcile Checks first, from its menu")
			} else {
				_, err = install.Copy(self, dirs.Install, installName())
			}
		}

		if err == nil {
			err = shortcut.Ensure(menuEntry(target))
		}

		if err == nil {
			fmt.Fprintf(stdout, "Installed at %s, and in the menu.\n", target)
			return 0
		}
	}

	fmt.Fprintln(stderr, "reconcile-checks:", err)

	return 1
}

// uninstallCommand is `reconcile-checks uninstall`: the program and its
// shortcut go; the register, the checks and the settings stay, and it says
// where.
func uninstallCommand(stdout, stderr io.Writer) int {
	target, dirs, err := installedAt()
	if err != nil {
		fmt.Fprintln(stderr, "reconcile-checks:", err)
		return 1
	}

	if running(dirs.Data) {
		fmt.Fprintln(stderr, "reconcile-checks: the program is running: quit Reconcile Checks first, from its menu")
		return 1
	}

	if err := shortcut.Remove(menuEntry(target)); err != nil {
		fmt.Fprintln(stderr, "reconcile-checks: couldn't remove the menu shortcut:", err)
		return 1
	}

	self, _ := selfupdate.Self()

	switch err := os.Remove(target); {
	case err == nil, errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(stdout, "Removed the program and its menu shortcut.\n")
	case runtime.GOOS == "windows" && sameFile(self, target):
		// Windows won't delete a running program, and this is it.
		fmt.Fprintf(stdout, "Removed the menu shortcut. Delete %s once this window has closed.\n", target)
	default:
		fmt.Fprintln(stderr, "reconcile-checks: couldn't remove the program:", err)
		return 1
	}

	fmt.Fprintf(stdout, "The checks, the register and the settings are kept, in %s and %s. Delete them by hand only if they are no longer needed.\n", dirs.Data, dirs.Config)

	return 0
}

// running reports whether a copy of the program holds the lock in data.
func running(data string) bool {
	lock, err := lockfile.Acquire(filepath.Join(data, "instance.lock"))
	if errors.Is(err, lockfile.ErrHeld) {
		return true
	}

	if err == nil {
		lock.Release()
	}

	return false
}

// installedVersion is the version the program at path says it is, "" when
// it isn't there or doesn't run.
func installedVersion(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}

	if f := strings.Fields(string(out)); len(f) >= 2 && f[0] == app {
		return f[1]
	}

	return ""
}

// sameFile reports whether a and b are one file.
func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}

	ib, err := os.Stat(b)
	if err != nil {
		return false
	}

	return os.SameFile(ia, ib)
}
