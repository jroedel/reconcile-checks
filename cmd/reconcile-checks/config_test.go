package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A person's copy signs in to the Reconcile it was built for; a development
// config signs in only to the one it names (CLAUDE.md, section 6).
func TestWhichReconcileAConfigSignsInTo(t *testing.T) {
	dir := t.TempDir()

	none := filepath.Join(dir, "none.toml")
	if err := os.WriteFile(none, []byte("port = 7490\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	local := filepath.Join(dir, "local.toml")
	if err := os.WriteFile(local, []byte("reconcile = \"http://127.0.0.1:8461\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for give, want := range map[string]string{none: "", local: "http://127.0.0.1:8461"} {
		c, err := loadConfig(give)
		if err != nil {
			t.Fatal(err)
		}

		if c.Reconcile != want || !c.explicit {
			t.Errorf("--config %s: reconcile %q (explicit %v), want %q", filepath.Base(give), c.Reconcile, c.explicit, want)
		}
	}

	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(dir, "roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local"))
	} else {
		t.Setenv("HOME", dir)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	}

	c, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}

	if c.Reconcile != defaultReconcile || c.explicit {
		t.Errorf("no --config: reconcile %q (explicit %v), want %q", c.Reconcile, c.explicit, defaultReconcile)
	}
}
