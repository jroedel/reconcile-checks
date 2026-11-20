package secretstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/secretstore"
)

func TestAFileKeepsTheSecretForItsUserAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "key")
	s := secretstore.File(path)

	if _, err := s.Get(); !errors.Is(err, secretstore.ErrNone) {
		t.Fatalf("before anything is kept: %v, want ErrNone", err)
	}

	if err := s.Delete(); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}

	for _, v := range []string{"first", "second"} {
		if err := s.Set(v); err != nil {
			t.Fatal(err)
		}

		got, err := s.Get()
		if err != nil || got != v {
			t.Fatalf("got %q, %v; want %q", got, err, v)
		}
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != 0o600 {
			t.Errorf("the file is %v, want -rw-------", info.Mode().Perm())
		}

		dir, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}

		if dir.Mode().Perm() != 0o700 {
			t.Errorf("its folder is %v, want drwx------", dir.Mode().Perm())
		}
	}

	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".secret-*"))
	if len(left) > 0 {
		t.Errorf("a temporary file was left behind: %v", left)
	}

	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(); !errors.Is(err, secretstore.ErrNone) {
		t.Fatalf("after deleting: %v, want ErrNone", err)
	}
}

// With no keychain to ask, the system store is its fallback. The test
// can't reach a real keychain (and mustn't write to the person's), so it
// takes the keychain away.
func TestWithNoKeychainTheFileIsUsed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux can be told it has no keychain")
	}

	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	path := filepath.Join(t.TempDir(), "key")
	s := secretstore.System("reconcile-checks-test", "nobody", secretstore.File(path))

	if err := s.Set("kept"); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(path); err != nil || string(b) != "kept" {
		t.Fatalf("the file holds %q, %v", b, err)
	}

	if got, err := s.Get(); err != nil || got != "kept" {
		t.Fatalf("got %q, %v", got, err)
	}

	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file is still there after Delete: %v", err)
	}
}
