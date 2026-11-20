// Command brokenbuild is a release that passes every check self-update makes
// and then doesn't work. Ported from sion-backup's.
//
// It answers `version` with the version it was stamped with, which is the
// whole smoke test, and then fails in one of two ways, stamped as mode:
//
//   - hang: it starts and never answers. It doesn't take the lock or the
//     port, as a build stuck on a register it can't open wouldn't. The old
//     process watching the restart is what must put the previous version
//     back (docs/10, "Rollback, twice over": at the restart).
//   - crash: it runs the probation check exactly as cmd/reconcile-checks
//     does, then dies where opening the register would. Opened by a person
//     three times, the fourth start gives up on it and starts the previous
//     version (later, by probation).
//
// A build that dies before the probation check can't roll itself back; that
// is the design's real limit, and the smoke test, which runs the downloaded
// binary before it is installed, is what covers it.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// Stamped as the real binary's are.
var (
	version = "dev"
	mode    = "crash"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("reconcile-checks %s %s/%s %s key:broken\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	if mode == "hang" {
		fmt.Fprintln(os.Stderr, "reconcile-checks: opening the register: waiting for a lock that never comes")
		time.Sleep(time.Hour)

		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	u, err := selfupdate.New(selfupdate.Config{Current: version, Log: log})
	if err != nil {
		log.Warn("cannot watch the last update", "err", err)
	}

	if u != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		start := u.Start(ctx)
		cancel()

		if start.Outcome == selfupdate.RolledBack {
			// As cmd/reconcile-checks does: start the restored version with
			// the same flags, and leave.
			cmd := exec.Command(u.Path(), append([]string{"--wait-for-lock", "30s"}, os.Args[1:]...)...)
			if err := cmd.Start(); err != nil {
				log.Error("went back, and could not start the previous version", "err", err)
				os.Exit(1)
			}

			log.Warn("went back to the previous version and started it", "gave_up_on", start.Failed, "now", start.Version)

			return
		}
	}

	fmt.Fprintln(os.Stderr, "reconcile-checks: opening the register: no such column: printed_at")
	os.Exit(1)
}
