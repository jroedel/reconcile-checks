// Package install copies a program into the folder it runs from from now on
// (docs/10-install-update-and-releases.md, "Installing").
//
// By reading and writing the file, never by the system's copy: on Windows,
// CopyFile carries the "downloaded from the internet" mark (the
// Zone.Identifier stream) to the copy, and SmartScreen would then warn again
// at every start from the Start menu. A copy made byte by byte is a new file
// that was never downloaded.
//
// This is a foundation leaf: it knows files, not checks.
package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Copy copies the file at from into dir as name, creating dir, through a
// temporary file in dir and a rename, so that the program in dir is never
// half written. It is executable on Linux. It returns the installed path.
func Copy(from, dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("couldn't make the folder %s: %w", dir, err)
	}

	src, err := os.Open(from)
	if err != nil {
		return "", fmt.Errorf("couldn't read the program to install it: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(dir, name+".installing-*")
	if err != nil {
		return "", fmt.Errorf("couldn't write in %s: %w", dir, err)
	}

	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)

		return "", err
	}

	if _, err := io.Copy(tmp, src); err != nil {
		return fail(fmt.Errorf("couldn't copy the program into %s: %w", dir, err))
	}

	if err := tmp.Sync(); err != nil {
		return fail(err)
	}

	if err := tmp.Chmod(0o755); err != nil {
		return fail(err)
	}

	if err := tmp.Close(); err != nil {
		return fail(err)
	}

	to := filepath.Join(dir, name)

	if err := os.Rename(tmpName, to); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("couldn't put the program in place at %s; if it is running, quit it and try again: %w", to, err)
	}

	return to, nil
}
