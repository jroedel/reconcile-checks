//go:build !windows

package main

import "syscall"

// detached starts a copy in a session of its own, so that closing the
// terminal it was started from does not take the restarted copy with it.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
