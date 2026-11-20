//go:build !windows

package selfupdate

// sharingViolation is never true off Windows, where an open file doesn't stop
// a rename.
func sharingViolation(error) bool { return false }
