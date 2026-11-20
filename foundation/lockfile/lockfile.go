// Package lockfile stops two copies of the program running at once.
//
// One program writes the register, so a second start (a second click on the
// shortcut, or the browser's shortcut opened while the first copy is still
// running) must find out that a copy is already running, and hand over to it
// rather than open the register too.
//
// The lock is held by the open file, so the operating system releases it when
// the process exits by any means, including a crash. There is no stale lock to
// clean up and no process id to go wrong.
//
// Taken from dmarc-monitor, with a Windows implementation added.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrHeld is returned by Acquire when another process holds the lock. It is not
// a failure: the normal response is to hand over to the running copy.
var ErrHeld = errors.New("lockfile: held by another process")

// Lock is a held file lock.
type Lock struct {
	file *os.File
}

// Acquire takes the lock at path without blocking, creating the file and its
// directory if needed.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lockfile: creating %s: %w", filepath.Dir(path), err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lockfile: opening %s: %w", path, err)
	}

	if err := lock(f); err != nil {
		f.Close()

		if errors.Is(err, ErrHeld) {
			return nil, ErrHeld
		}

		return nil, fmt.Errorf("lockfile: locking %s: %w", path, err)
	}

	return &Lock{file: f}, nil
}

// Release drops the lock. Safe to call on a nil Lock, so callers can defer it
// without first checking whether they got one.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	err := l.file.Close()
	l.file = nil

	return err
}
