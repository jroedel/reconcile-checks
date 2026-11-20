package selfupdate

import (
	"errors"
	"testing"
	"time"
)

// errHeld is a rename refused because another program has the file open, as
// Windows Defender does to a program it is scanning.
var errHeld = errors.New("the process cannot access the file because it is being used by another process")

// fakeRename replaces the rename and the wait for one test, failing the
// first fails calls with err, and counts both.
func fakeRename(t *testing.T, fails int, err error) (calls, waits *int) {
	t.Helper()

	calls, waits = new(int), new(int)

	savedRename, savedTransient, savedPause := osRename, transient, pause
	t.Cleanup(func() { osRename, transient, pause = savedRename, savedTransient, savedPause })

	osRename = func(string, string) error {
		*calls++
		if *calls <= fails {
			return err
		}

		return nil
	}
	transient = func(e error) bool { return errors.Is(e, errHeld) }
	pause = func(time.Duration) { *waits++ }

	return calls, waits
}

func TestARenameIsRetriedWhileTheFileIsHeld(t *testing.T) {
	calls, waits := fakeRename(t, 2, errHeld)

	if err := rename("a", "b"); err != nil {
		t.Fatalf("rename = %v, want it to succeed on the third try", err)
	}

	if *calls != 3 || *waits != 2 {
		t.Errorf("tried %d times, waited %d; want 3 and 2", *calls, *waits)
	}
}

func TestARenameThatFailsOtherwiseIsNotRetried(t *testing.T) {
	denied := errors.New("permission denied")
	calls, waits := fakeRename(t, 99, denied)

	if err := rename("a", "b"); !errors.Is(err, denied) {
		t.Fatalf("rename = %v, want %v", err, denied)
	}

	if *calls != 1 || *waits != 0 {
		t.Errorf("tried %d times, waited %d; want once and no wait", *calls, *waits)
	}
}

func TestARenameStillHeldGivesUp(t *testing.T) {
	calls, waits := fakeRename(t, 99, errHeld)

	if err := rename("a", "b"); !errors.Is(err, errHeld) {
		t.Fatalf("rename = %v, want %v", err, errHeld)
	}

	if *calls != renameTries || *waits != renameTries-1 {
		t.Errorf("tried %d times, waited %d; want %d and %d", *calls, *waits, renameTries, renameTries-1)
	}
}
