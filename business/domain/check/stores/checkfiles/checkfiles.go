// Package checkfiles keeps each check's PDF and PNG under the data folder, at
// checks/<account>/<number>.pdf and .png (docs/02-architecture.md, "Where
// files live").
//
// The PDF is the only copy there will ever be of what went to the printer, and
// the PNG the only copy of the image until Reconcile has it, so every write is
// to a temporary file in the same folder, synced and then renamed over the
// name: a crash leaves the old file or the new one, never half of either.
package checkfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
)

// Files is the folder of checks.
type Files struct {
	dir string
}

// New keeps the checks under dir, normally <data>/checks.
func New(dir string) *Files { return &Files{dir: dir} }

var _ checkbus.Files = (*Files)(nil)

// Path is where a check's file of that extension ("pdf", "png") lives.
func (f *Files) Path(account string, number int, ext string) (string, error) {
	// The key names a folder; checkbus holds it to letters, digits and
	// hyphens, and this is the last place a bad one could escape the folder.
	if !checkbus.ValidKey(account) {
		return "", fmt.Errorf("%q can't name an account's folder", account)
	}

	return filepath.Join(f.dir, account, strconv.Itoa(number)+"."+ext), nil
}

// Save writes both files.
func (f *Files) Save(account string, number int, pdf, png []byte) error {
	if err := f.write(account, number, "pdf", pdf); err != nil {
		return err
	}

	return f.write(account, number, "png", png)
}

// SavePNG writes the image again: after a void, or a corrected number.
func (f *Files) SavePNG(account string, number int, png []byte) error {
	return f.write(account, number, "png", png)
}

// Rename moves both files to another number. A file no longer at the old
// number is skipped, so a rename can be run again after one that stopped
// halfway; a file at the new number is never overwritten while the old one
// is still there.
func (f *Files) Rename(account string, from, to int) error {
	for _, ext := range []string{"pdf", "png"} {
		src, err := f.Path(account, from, ext)
		if err != nil {
			return err
		}

		dst, err := f.Path(account, to, ext)
		if err != nil {
			return err
		}

		_, serr := os.Stat(src)
		_, derr := os.Stat(dst)

		switch {
		case serr == nil && derr == nil:
			return fmt.Errorf("there is already a file for check %d (%s); move it aside and try again", to, dst)
		case serr != nil:
			// Already moved, or never there.
			continue
		}

		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("moving check %d's %s file to %d: %w", from, ext, to, err)
		}
	}

	return nil
}

// Remove deletes both files of a check that never reached the printer.
func (f *Files) Remove(account string, number int) error {
	for _, ext := range []string{"pdf", "png"} {
		p, err := f.Path(account, number, ext)
		if err != nil {
			return err
		}

		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing check %d's %s file: %w", number, ext, err)
		}
	}

	return nil
}

func (f *Files) write(account string, number int, ext string, b []byte) error {
	path, err := f.Path(account, number, ext)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making the folder for %s's checks: %w", account, err)
	}

	tmp, err := os.CreateTemp(dir, "."+strconv.Itoa(number)+"-*."+ext)
	if err != nil {
		return fmt.Errorf("saving check %d's %s file: %w", number, ext, err)
	}

	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()

	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving check %d's %s file: %w", number, ext, err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving check %d's %s file: %w", number, ext, err)
	}

	return nil
}
