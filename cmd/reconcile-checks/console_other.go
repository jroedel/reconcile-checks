//go:build !windows

package main

// attachConsole is for the Windows GUI build; elsewhere the program always
// has the terminal it was started from.
func attachConsole() {}
