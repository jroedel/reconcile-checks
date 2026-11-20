package main

import "syscall"

// Process creation flags (winbase.h); syscall has no names for them.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// detached starts a copy with no console and in a group of its own, so that
// a terminal it was started from closing, or Ctrl+C in it, does not reach
// the restarted copy.
func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
