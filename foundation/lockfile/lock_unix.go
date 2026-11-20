//go:build unix

package lockfile

import (
	"errors"
	"os"
	"syscall"
)

// lock takes an exclusive, non-blocking lock on the open file. The lock lives
// on the open file description, so the kernel drops it when the process
// exits.
func lock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return ErrHeld
	default:
		return err
	}
}
