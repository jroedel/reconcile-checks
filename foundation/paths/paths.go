// Package paths resolves where this program keeps things on each operating
// system: its settings, its data (the register, each check's files, the lock,
// the log), and the program itself once installed.
//
//	                Linux                              Windows
//	settings        $XDG_CONFIG_HOME/<app>             %AppData%\<app>
//	                  or ~/.config/<app>
//	data            $XDG_DATA_HOME/<app>               %LocalAppData%\<app>
//	                  or ~/.local/share/<app>
//	the program     ~/.local/bin                       %LocalAppData%\Programs\<app>
//
// docs/02-architecture.md, "Where files live", is the reasoning. The program
// lives where its own user can write, so that it can replace itself when it
// updates, without asking for an administrator (sion-backup#69 is what
// happened when it didn't).
//
// No default path resolves inside the source tree: this repository is public,
// and a register in it would be a register one `git add -A` away from GitHub.
//
// This is a foundation leaf: it knows about directories, not about checks.
package paths

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Dirs is the resolved set of locations.
type Dirs struct {
	Config  string // the directory holding config.toml
	Data    string // the register, the lock, runtime.json, the log, each check's files
	Install string // where the program copies itself on its first run
}

// For resolves the directories for app on this operating system.
func For(app string) (Dirs, error) {
	switch runtime.GOOS {
	case "windows":
		roaming, err := os.UserConfigDir() // %AppData%
		if err != nil {
			return Dirs{}, fmt.Errorf("paths: locating %%AppData%%: %w", err)
		}

		local, err := localAppData()
		if err != nil {
			return Dirs{}, err
		}

		return Dirs{
			Config:  filepath.Join(roaming, app),
			Data:    filepath.Join(local, app),
			Install: filepath.Join(local, "Programs", app),
		}, nil

	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("paths: there is no home directory: %w", err)
		}

		config := cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
		data := cmp.Or(os.Getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share"))

		return Dirs{
			Config:  filepath.Join(config, app),
			Data:    filepath.Join(data, app),
			Install: filepath.Join(home, ".local", "bin"),
		}, nil
	}
}

// localAppData is %LocalAppData%, rather than os.UserConfigDir's %AppData%:
// on a domain computer AppData\Roaming is copied to the profile server at
// sign-out, and the register has no business on a file share.
func localAppData() (string, error) {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return local, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("paths: LOCALAPPDATA is unset and there is no home directory")
	}

	return filepath.Join(home, "AppData", "Local"), nil
}
