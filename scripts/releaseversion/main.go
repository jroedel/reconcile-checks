// Command releaseversion is the version the release workflow publishes next
// (docs/10-install-update-and-releases.md, "The path from a pull request to
// the computers"): the latest release's vX.Y.Z with Z + 1, or Y + 1 or X + 1
// when the merged pull request carries the label "release: minor" or
// "release: major"; v0.1.0 when there is no release yet.
//
//	releaseversion -latest v0.3.1 -labels "release: minor,bug"   # v0.4.0
//
// A program rather than a line of shell, so that the rule has tests and the
// workflow has no arithmetic in it.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("releaseversion", flag.ContinueOnError)
	fs.SetOutput(stderr)
	latest := fs.String("latest", "", "the latest published release's tag; empty, or null, for none")
	labels := fs.String("labels", "", "the merged pull request's labels, comma-separated")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	v, err := next(*latest, strings.Split(*labels, ","))
	if err != nil {
		fmt.Fprintln(stderr, "releaseversion:", err)
		return 1
	}

	fmt.Fprintln(stdout, v)

	return 0
}

// first is the version of the first release.
const first = "v0.1.0"

func next(latest string, labels []string) (string, error) {
	latest = strings.TrimSpace(latest)
	if latest == "" || latest == "null" {
		return first, nil
	}

	parts := strings.Split(strings.TrimPrefix(latest, "v"), ".")
	if !strings.HasPrefix(latest, "v") || len(parts) != 3 {
		return "", fmt.Errorf("the latest release is %q, which is not vX.Y.Z", latest)
	}

	var n [3]int

	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || p != strconv.Itoa(v) {
			return "", fmt.Errorf("the latest release is %q, which is not vX.Y.Z", latest)
		}

		n[i] = v
	}

	bump := "patch"

	for _, l := range labels {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "release: major":
			bump = "major"
		case "release: minor":
			if bump != "major" {
				bump = "minor"
			}
		}
	}

	switch bump {
	case "major":
		n = [3]int{n[0] + 1, 0, 0}
	case "minor":
		n = [3]int{n[0], n[1] + 1, 0}
	default:
		n[2]++
	}

	return fmt.Sprintf("v%d.%d.%d", n[0], n[1], n[2]), nil
}
