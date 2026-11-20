package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// maxLog is the size past which the log is started afresh, keeping the one
// before as reconcile-checks.log.1. A program that runs for minutes a week
// writes little, but a log nobody empties must still stop somewhere.
const maxLog = 5 << 20

// openLog opens the log file in the data folder, for appending.
func openLog(data string) (*os.File, error) {
	path := filepath.Join(data, "reconcile-checks.log")

	if info, err := os.Stat(path); err == nil && info.Size() > maxLog {
		os.Rename(path, path+".1") // best effort; a failure only means a longer log
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the log %s: %w", path, err)
	}

	return f, nil
}
