//go:build !windows

package shortcut

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dataHome is $XDG_DATA_HOME, or ~/.local/share.
func dataHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return cmp.Or(os.Getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share")), nil
}

// Path is where the entry is: a .desktop file in the applications folder.
func Path(s Shortcut) (string, error) {
	data, err := dataHome()
	if err != nil {
		return "", err
	}

	return filepath.Join(data, "applications", s.ID+".desktop"), nil
}

// Ensure writes the .desktop entry and its icon, when either is missing or
// different. Called at every start of the installed program, so a menu entry
// removed by hand, or a program moved, is put right.
func Ensure(s Shortcut) error {
	data, err := dataHome()
	if err != nil {
		return err
	}

	if len(s.Icon) > 0 {
		icon := filepath.Join(data, "icons", "hicolor", "256x256", "apps", s.ID+".png")
		if err := writeIfChanged(icon, s.Icon, 0o644); err != nil {
			return err
		}
	}

	path, err := Path(s)
	if err != nil {
		return err
	}

	return writeIfChanged(path, []byte(desktop(s)), 0o644)
}

// desktop is the entry (the freedesktop.org Desktop Entry Specification).
// Exec's path is quoted, which the specification allows for any path and
// requires for one with a space.
func desktop(s Shortcut) string {
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(s.Target) + `"`

	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=%s
Exec=%s
Icon=%s
Terminal=false
Categories=Office;Finance;
`, s.Name, s.Comment, quoted, s.ID)
}

// Remove takes the entry away, and its icon.
func Remove(s Shortcut) error {
	data, err := dataHome()
	if err != nil {
		return err
	}

	for _, p := range []string{
		filepath.Join(data, "applications", s.ID+".desktop"),
		filepath.Join(data, "icons", "hicolor", "256x256", "apps", s.ID+".png"),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
