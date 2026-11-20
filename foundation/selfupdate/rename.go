package selfupdate

import (
	"os"
	"time"
)

// renameTries and renameWait bound how long a rename waits out a file that
// something else has open: about two seconds, all told.
//
// That something is Windows Defender, almost always. It scans a new
// executable when it is written and again when it is first run, which is
// exactly what the download and the smoke test have just done, and while it
// holds the file open a rename of it fails with a sharing violation (docs/10,
// "Two updates without a restart between them"). The scan ends in moments;
// the update shouldn't wait a day for the next check because of it.
const (
	renameTries = 5
	renameWait  = 400 * time.Millisecond
)

// osRename, transient and pause are variables so that the retry can be
// tested on a system whose renames never fail that way.
var (
	osRename  = os.Rename
	transient = sharingViolation
	pause     = time.Sleep
)

// rename is os.Rename, retried while the failure is one another program's
// open handle will soon stop causing. Elsewhere a failure is returned at once:
// a rename that failed for any other reason fails the same way again.
func rename(from, to string) error {
	var err error

	for try := range renameTries {
		if err = osRename(from, to); err == nil || !transient(err) {
			return err
		}

		if try < renameTries-1 {
			pause(renameWait)
		}
	}

	return err
}
