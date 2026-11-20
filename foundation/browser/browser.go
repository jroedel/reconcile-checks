// Package browser opens a URL in the person's default browser.
//
// Taken from sion-backup's openBrowser (cmd/sion-backup/handoff.go).
package browser

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// ErrNoDisplay is returned on a Linux computer with no graphical session, where
// there is no browser to open; the caller prints the address instead.
var ErrNoDisplay = errors.New("browser: there is no graphical session here")

// Open opens url and returns without waiting for the browser.
func Open(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()

	case "windows":
		// Through the shell's URL handler rather than by name: there is no
		// "open" on Windows, and this is the documented way to reach whatever
		// the person has set as their browser.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()

	default:
		// Reached over SSH there is no display, and xdg-open there either
		// fails or opens a text browser in the terminal being used.
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return ErrNoDisplay
		}

		return exec.Command("xdg-open", url).Start()
	}
}
