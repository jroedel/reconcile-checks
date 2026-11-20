//go:build !windows

package shortcut_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/shortcut"
)

func TestTheMenuEntryOpensTheInstalledProgram(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := shortcut.Shortcut{
		ID: "app", Name: "The App", Comment: "Does a thing",
		Target: "/home/a person/.local/bin/app", Icon: []byte("png"),
	}

	if err := shortcut.Ensure(s); err != nil {
		t.Fatal(err)
	}

	path, _ := shortcut.Path(s)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"[Desktop Entry]", "Name=The App", `Exec="/home/a person/.local/bin/app"`, "Icon=app", "Terminal=false"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the entry has no %q:\n%s", want, b)
		}
	}

	icon := filepath.Join(os.Getenv("XDG_DATA_HOME"), "icons", "hicolor", "256x256", "apps", "app.png")
	if b, _ := os.ReadFile(icon); string(b) != "png" {
		t.Error("the icon wasn't written")
	}

	// Removed by hand, it comes back at the next start.
	os.Remove(path)

	if err := shortcut.Ensure(s); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Error("the entry wasn't put back")
	}

	if err := shortcut.Remove(s); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the entry is still there after Remove")
	}
}
