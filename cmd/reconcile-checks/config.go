package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jroedel/reconcile-checks/foundation/paths"
)

// app is the name of the program's folders on every system.
const app = "reconcile-checks"

// defaultPort is the port the app is served on unless the config says
// otherwise, or something else already holds it (then any free port).
//
// A fixed port, so that an open tab finds the program again after it restarts
// into an update (docs/03-the-local-app.md, "The port"). Below 32768, so it is
// never one the system hands out to outgoing connections on Linux or Windows,
// and not one any common program listens on.
const defaultPort = 7461

// defaultCloseAfter is how long the program waits with no page open before it
// closes (docs/03-the-local-app.md, "When it closes"; decided 2026-11-20).
const defaultCloseAfter = 2 * time.Minute

// config is config.toml. config.example.toml is its documentation.
type config struct {
	// Data is where the register, the lock, runtime.json and the log live.
	// Empty means the system's place for it (foundation/paths); with
	// --config, a folder named data beside the config file, so that a
	// development run never touches the real register.
	Data string `toml:"data"`

	Port       int      `toml:"port"`
	CloseAfter duration `toml:"close_after"`
	LogLevel   string   `toml:"log_level"`

	// Where the file was read from, and the folder it chose: shown in
	// Settings.
	file string
}

// duration reads "2m" or "90s" from TOML.
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%q is not a length of time such as 2m or 90s", b)
	}
	d.Duration = v

	return nil
}

// loadConfig reads the config at path, or, when path is empty, the one in the
// system's place for it, which need not exist.
func loadConfig(path string) (config, error) {
	var c config

	explicit := path != ""
	if !explicit {
		dirs, err := paths.For(app)
		if err != nil {
			return c, err
		}

		path = filepath.Join(dirs.Config, "config.toml")
		c.Data = dirs.Data
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return c, err
		}

		path = abs
		c.Data = filepath.Join(filepath.Dir(path), "data")
	}

	c.file = path

	switch _, err := toml.DecodeFile(path, &c); {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// No config yet: the defaults are the whole configuration.
	default:
		return c, fmt.Errorf("reading %s: %w", path, err)
	}

	c.Port = cmp.Or(c.Port, defaultPort)
	c.CloseAfter.Duration = cmp.Or(c.CloseAfter.Duration, defaultCloseAfter)

	if c.Port < 1024 || c.Port > 65535 {
		return c, fmt.Errorf("%s: port %d is not one this program can use; choose one between 1024 and 65535", path, c.Port)
	}

	if !filepath.IsAbs(c.Data) {
		c.Data = filepath.Join(filepath.Dir(path), c.Data)
	}

	if err := os.MkdirAll(c.Data, 0o700); err != nil {
		return c, fmt.Errorf("making the data folder %s: %w", c.Data, err)
	}

	return c, nil
}
