// Package shortcut puts a program where a person opens it from: the Start
// menu on Windows, the applications menu on a Linux desktop
// (docs/10-install-update-and-releases.md, "Installing").
//
// This is a foundation leaf: it knows menus, not checks.
package shortcut

// Shortcut is one menu entry.
type Shortcut struct {
	ID      string // the file's name, without extension: "reconcile-checks"
	Name    string // what the menu shows: "Reconcile Checks"
	Comment string // a line about it
	Target  string // the program
	Icon    []byte // a 256x256 PNG, for Linux; Windows takes the program's own
}
