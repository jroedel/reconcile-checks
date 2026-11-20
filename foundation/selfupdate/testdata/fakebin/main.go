// Command fakebin stands in for reconcile-checks in selfupdate's tests: asked
// for its version, it answers the way the real program does.
//
// It is built once per test run, and each fake version is that build with a
// line appended after the end of the executable, which both ELF and PE ignore.
// So one build serves every version, every fake hashes differently, and the
// fakes run on Windows too, where sion-backup's shell scripts couldn't, and
// where the renames this package does are most worth testing.
//
// The appended line is the version it reports; "crash" makes it fail instead.
package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// marker begins the appended line. The same bytes are in the binary's own
// data, so the last occurrence is the appended one.
const marker = "\nFAKEBIN-VERSION="

func main() {
	if len(os.Args) != 2 || os.Args[1] != "version" {
		fmt.Fprintln(os.Stderr, "fakebin: only version is a command")
		os.Exit(2)
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	b, err := os.ReadFile(exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	i := bytes.LastIndex(b, []byte(marker))
	if i < 0 {
		fmt.Fprintln(os.Stderr, "fakebin: no version appended")
		os.Exit(1)
	}

	version := strings.TrimSpace(string(b[i+len(marker):]))

	if version == "crash" {
		fmt.Fprintln(os.Stderr, "fakebin: crashing, as asked")
		os.Exit(3)
	}

	fmt.Printf("reconcile-checks %s %s/%s %s\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
