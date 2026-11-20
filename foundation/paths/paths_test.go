package paths_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/paths"
)

func TestForNamesEveryDirectoryAfterTheApp(t *testing.T) {
	if runtime.GOOS != "windows" {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("XDG_DATA_HOME", "")
	}

	d, err := paths.For("reconcile-checks")
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	for name, dir := range map[string]string{"Config": d.Config, "Data": d.Data} {
		if filepath.Base(dir) != "reconcile-checks" {
			t.Errorf("%s = %s, want a directory named reconcile-checks", name, dir)
		}
	}

	if d.Install == "" || !filepath.IsAbs(d.Install) {
		t.Errorf("Install = %q, want an absolute directory", d.Install)
	}
}

// No default may land in the source tree, which is public.
func TestNoDefaultIsInsideTheRepository(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	d, err := paths.For("reconcile-checks")
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	for _, dir := range []string{d.Config, d.Data, d.Install} {
		if strings.HasPrefix(dir, repo+string(filepath.Separator)) {
			t.Errorf("%s is inside the repository", dir)
		}
	}
}

func TestXDGIsHonouredOnLinux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG is a Linux convention")
	}

	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_DATA_HOME", "/x/data")

	d, err := paths.For("app")
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	if d.Config != "/x/config/app" || d.Data != "/x/data/app" {
		t.Errorf("got %+v", d)
	}
}
