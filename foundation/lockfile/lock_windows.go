//go:build windows

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh places the locked byte at 2^62, far past the end of any file
// this will ever be.
//
// Windows byte-range locks are mandatory, unlike flock: a lock on the file's
// first byte would make every other read of the file fail. Locking a byte that
// isn't there excludes a second locker exactly as well, and leaves the file
// itself readable. LockFileEx allows a range beyond the end of the file.
const lockOffsetHigh = 1 << 30

// lock takes an exclusive, non-blocking lock. Windows releases it when the
// handle is closed, which it does for every handle when the process exits.
func lock(f *os.File) error {
	ol := new(windows.Overlapped)
	ol.OffsetHigh = lockOffsetHigh

	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return ErrHeld
	default:
		return err
	}
}
