//go:build windows

package selfupdate

import (
	"errors"

	"golang.org/x/sys/windows"
)

// sharingViolation reports a rename refused because another program has the
// file open: a sharing violation, or access denied, which Windows also gives
// for a file an antivirus has open or one still being deleted. The Go
// toolchain retries the same two for the same reason when it renames its own
// build outputs (cmd/go/internal/robustio). A rename refused by a permission
// gets the same answer five times and costs two seconds; Writable has ruled
// that out before any rename is tried.
func sharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
