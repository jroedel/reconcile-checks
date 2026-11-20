// Package settingsfile keeps the printers and accounts in config.toml, as its
// [[printer]] and [[account]] tables (docs/02-architecture.md, "The config,
// sketched"), beside the settings main reads from the same file.
//
// Saving rewrites the file: it reads every key there is, replaces the two
// tables, and writes the whole file again, so a setting this package knows
// nothing about (the port, the data folder) is kept. Comments are not: no
// TOML library keeps them, and config.example.toml says so.
package settingsfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
)

// File is config.toml.
type File struct{ path string }

// New keeps the settings in the file at path, which need not exist yet.
func New(path string) *File { return &File{path: path} }

var _ settingsbus.Storer = (*File)(nil)

// The tables as TOML has them.
type (
	tables struct {
		Printer []printer `toml:"printer"`
		Account []account `toml:"account"`
	}

	printer struct {
		Name string `toml:"name"`
		URL  string `toml:"url"`
		Pin  string `toml:"pin,omitempty"`
	}

	account struct {
		Key         string `toml:"key"`
		Name        string `toml:"name"`
		Last4       string `toml:"last4"`
		Stock       string `toml:"stock"`
		Printer     string `toml:"printer"`
		Tray        string `toml:"tray"`
		MediaType   string `toml:"media_type"`
		Feed        string `toml:"feed,omitempty"`
		Ceiling     string `toml:"ceiling,omitempty"`
		ReconcileID string `toml:"reconcile_id,omitempty"`
		Offset      offset `toml:"offset_mm"`
	}

	offset struct {
		X float64 `toml:"x"`
		Y float64 `toml:"y"`
	}
)

// Load reads the printers and accounts.
func (f *File) Load() (settingsbus.Settings, error) {
	var t tables

	_, err := toml.DecodeFile(f.path, &t)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return settingsbus.Settings{}, nil
	case err != nil:
		return settingsbus.Settings{}, fmt.Errorf("reading %s: %w", f.path, err)
	}

	var s settingsbus.Settings

	for _, p := range t.Printer {
		s.Printers = append(s.Printers, settingsbus.Printer{Name: p.Name, URL: p.URL, Pin: p.Pin})
	}

	for _, a := range t.Account {
		var ceiling money.Cents
		if a.Ceiling != "" {
			c, err := money.Parse(a.Ceiling)
			if err != nil {
				return settingsbus.Settings{}, fmt.Errorf("%s: the account %s has the ceiling %q, which is not an amount", f.path, a.Key, a.Ceiling)
			}
			ceiling = c
		}

		s.Accounts = append(s.Accounts, settingsbus.Account{
			Key: a.Key, Name: a.Name, Last4: a.Last4,
			Stock: a.Stock, Printer: a.Printer, Tray: a.Tray, MediaType: a.MediaType, Feed: a.Feed,
			Ceiling:     ceiling,
			Offset:      settingsbus.Offset{X: a.Offset.X, Y: a.Offset.Y},
			ReconcileID: a.ReconcileID,
		})
	}

	return s, nil
}

// header is the first lines of every file this package writes.
const header = `# Reconcile Checks' settings, written by its Settings page. Every key is
# documented in config.example.toml. A comment written here by hand is lost
# the next time Settings saves.

`

// Save writes the printers and accounts, keeping every other key.
func (f *File) Save(s settingsbus.Settings) error {
	all := map[string]any{}

	switch _, err := toml.DecodeFile(f.path, &all); {
	case err == nil, errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf("reading %s: %w", f.path, err)
	}

	var t tables

	for _, p := range s.Printers {
		t.Printer = append(t.Printer, printer{Name: p.Name, URL: p.URL, Pin: p.Pin})
	}

	for _, a := range s.Accounts {
		t.Account = append(t.Account, account{
			Key: a.Key, Name: a.Name, Last4: a.Last4,
			Stock: a.Stock, Printer: a.Printer, Tray: a.Tray, MediaType: a.MediaType, Feed: a.Feed,
			Ceiling:     a.Ceiling.Figures(),
			ReconcileID: a.ReconcileID,
			Offset:      offset{X: a.Offset.X, Y: a.Offset.Y},
		})
	}

	delete(all, "printer")
	delete(all, "account")

	var b bytes.Buffer
	b.WriteString(header)

	// The other keys first, then the two tables, so the file reads in the
	// order config.example.toml documents it.
	if len(all) > 0 {
		if err := toml.NewEncoder(&b).Encode(all); err != nil {
			return fmt.Errorf("writing the settings: %w", err)
		}
		b.WriteString("\n")
	}

	if err := toml.NewEncoder(&b).Encode(t); err != nil {
		return fmt.Errorf("writing the settings: %w", err)
	}

	return write(f.path, b.Bytes())
}

// write replaces the file whole: a temporary file beside it, synced, then
// renamed over it, so that a crash leaves the old settings or the new ones.
func write(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making the folder for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}

	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()

	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving %s: %w", path, err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving %s: %w", path, err)
	}

	return nil
}
