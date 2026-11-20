package shortcut

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Path is where the entry is: a .lnk in the person's own Start menu,
// %AppData%\Microsoft\Windows\Start Menu\Programs, which needs no
// administrator.
func Path(s Shortcut) (string, error) {
	roaming, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs", s.Name+".lnk"), nil
}

// Ensure writes the Start menu shortcut, through the shell's own
// WScript.Shell, which is what writes a .lnk Windows reads as its own. Its
// icon is the program's, from the resource built into it. Written at every
// start of the installed program, so one removed by hand is put back.
//
// COM wants one thread for the whole conversation, initialised for a
// single-threaded apartment, which is what WScript.Shell is written for; a
// goroutine moves between threads unless locked to one.
func Ensure(s Shortcut) (err error) {
	path, err := Path(s)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// S_FALSE: this thread had COM already, which is as good.
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return fmt.Errorf("couldn't start COM to make the Start menu shortcut: %w", err)
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("couldn't reach WScript.Shell to make the Start menu shortcut: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()

	made, err := oleutil.CallMethod(shell, "CreateShortcut", path)
	if err != nil {
		return fmt.Errorf("couldn't make the Start menu shortcut: %w", err)
	}

	link := made.ToIDispatch()
	defer link.Release()

	for _, p := range []struct{ name, value string }{
		{"TargetPath", s.Target},
		{"WorkingDirectory", filepath.Dir(s.Target)},
		{"Description", s.Comment},
		{"IconLocation", s.Target + ",0"},
	} {
		if _, err := oleutil.PutProperty(link, p.name, p.value); err != nil {
			return fmt.Errorf("couldn't set the shortcut's %s: %w", p.name, err)
		}
	}

	if _, err := oleutil.CallMethod(link, "Save"); err != nil {
		return fmt.Errorf("couldn't save the Start menu shortcut at %s: %w", path, err)
	}

	return nil
}

// Remove takes the shortcut away.
func Remove(s Shortcut) error {
	path, err := Path(s)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}
