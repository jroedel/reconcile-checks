package install_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/install"
)

func TestTheProgramIsCopiedWholeAndRunnable(t *testing.T) {
	from := filepath.Join(t.TempDir(), "download")
	body := bytes.Repeat([]byte("program"), 100_000)

	if err := os.WriteFile(from, body, 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "Programs", "app")

	got, err := install.Copy(from, dir, "app")
	if err != nil {
		t.Fatal(err)
	}

	if got != filepath.Join(dir, "app") {
		t.Errorf("installed at %s", got)
	}

	if b, _ := os.ReadFile(got); !bytes.Equal(b, body) {
		t.Error("the copy differs")
	}

	if info, _ := os.Stat(got); runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("not executable: %v", info.Mode())
	}

	// Again, over the top: a newer download replacing an older install.
	if err := os.WriteFile(from, []byte("newer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := install.Copy(from, dir, "app"); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(got); string(b) != "newer" {
		t.Errorf("after a second install: %q", b)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left beside the program: %v", entries)
	}
}
