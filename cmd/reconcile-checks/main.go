// Command reconcile-checks writes checks on pre-printed stock and sends each
// one's image to Reconcile.
//
// A person never needs its command line: opening it from its shortcut starts
// the app and opens the browser at it (docs/03-the-local-app.md). The command
// line is for development and support (docs/02-architecture.md, "On the
// command line"):
//
//	reconcile-checks                       start, or find the running copy, and open the browser
//	reconcile-checks --no-browser          start and print the address instead
//	reconcile-checks --config <path>       use another config, and its own data folder
//	reconcile-checks version               the version, system and architecture
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// version is stamped by the build: -ldflags "-X main.version=vX.Y.Z". A build
// without it is "dev", which never updates itself.
var version = "dev"

func main() {
	attachConsole()

	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the flags.
type options struct {
	config      string
	noBrowser   bool
	waitForLock time.Duration
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintf(stdout, "reconcile-checks %s %s/%s %s\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	}

	var opt options

	fs := flag.NewFlagSet("reconcile-checks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opt.config, "config", "", "use this config file, and a data folder beside it")
	fs.BoolVar(&opt.noBrowser, "no-browser", false, "print the address instead of opening the browser")
	fs.DurationVar(&opt.waitForLock, "wait-for-lock", 0, "wait this long for a running copy to close, instead of handing over to it (used when restarting into an update)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "reconcile-checks: %q is not a command this program has; try version\n", fs.Arg(0))
		return 2
	}

	if err := start(opt, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "reconcile-checks:", err)
		return 1
	}

	return 0
}
