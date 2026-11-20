//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole connects the program to the console it was started from, if
// any.
//
// The Windows build is a GUI program (-H windowsgui), so opening it from the
// Start menu shows no console window. The cost is that a GUI program has no
// console of its own: `reconcile-checks version` typed in a terminal would
// print nothing. Attaching to the parent's console, when there is one, gives
// that back; from the Start menu there is none, and this does nothing.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1

	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	if ok, _, _ := attach.Call(attachParentProcess); ok == 0 {
		return
	}

	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
}
