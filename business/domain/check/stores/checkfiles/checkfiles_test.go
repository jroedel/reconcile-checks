package checkfiles_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile-checks/business/domain/check/stores/checkfiles"
)

func TestSaveRenameRemove(t *testing.T) {
	dir := t.TempDir()
	f := checkfiles.New(dir)

	if err := f.Save("checking-a", 1043, []byte("%PDF-"), []byte("png")); err != nil {
		t.Fatal(err)
	}

	pdf := filepath.Join(dir, "checking-a", "1043.pdf")
	if b, err := os.ReadFile(pdf); err != nil || string(b) != "%PDF-" {
		t.Fatalf("the PDF is %q (%v)", b, err)
	}

	// Nothing but the two files: no temporary file left behind.
	if es, _ := os.ReadDir(filepath.Join(dir, "checking-a")); len(es) != 2 {
		t.Errorf("%d files in the account's folder, want 2", len(es))
	}

	if err := f.Save("checking-a", 1044, []byte("other"), []byte("other")); err != nil {
		t.Fatal(err)
	}

	if err := f.Rename("checking-a", 1043, 1044); err == nil {
		t.Error("a rename overwrote another check's files")
	}

	if err := f.Rename("checking-a", 1043, 1042); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(filepath.Join(dir, "checking-a", "1042.pdf")); string(b) != "%PDF-" {
		t.Error("the PDF did not move to 1042")
	}

	// Again, after it already moved: nothing to do, and no error.
	if err := f.Rename("checking-a", 1043, 1042); err != nil {
		t.Errorf("a rename run again: %v", err)
	}

	if err := f.Remove("checking-a", 1042); err != nil {
		t.Fatal(err)
	}

	if err := f.Remove("checking-a", 1042); err != nil {
		t.Errorf("removing files already gone: %v", err)
	}
}

func TestAKeyCantLeaveTheFolder(t *testing.T) {
	f := checkfiles.New(t.TempDir())

	for _, key := range []string{"../elsewhere", "a/b", `a\b`, "", "con"} {
		if err := f.Save(key, 1, nil, nil); err == nil {
			t.Errorf("saved under the account %q", key)
		}
	}
}
