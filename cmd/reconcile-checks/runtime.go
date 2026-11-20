package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jroedel/reconcile-checks/app/sdk/mid"
)

// runtimeFile is runtime.json, in the data folder: what a second start needs
// to reach the running copy, and the keys that make a session
// (docs/03-the-local-app.md, "Only one at a time").
//
// It is readable only by this user: mode 0600 on Linux, and on Windows the
// protection of the user's own %LocalAppData%, where nobody else may read.
// That is the whole of its protection, and enough: anybody who can read it is
// already this user, and could as well open the program from its shortcut.
type runtimeFile struct {
	Port       int    `json:"port"`        // 0 until the server is bound
	LaunchKey  string `json:"launch_key"`  // base64url
	SessionKey string `json:"session_key"` // base64url
}

func runtimePath(data string) string { return filepath.Join(data, "runtime.json") }

// loadRuntime reads runtime.json.
func loadRuntime(data string) (runtimeFile, error) {
	var rt runtimeFile

	b, err := os.ReadFile(runtimePath(data))
	if err != nil {
		return rt, err
	}

	if err := json.Unmarshal(b, &rt); err != nil {
		return rt, fmt.Errorf("reading %s: %w", runtimePath(data), err)
	}

	return rt, nil
}

// keys returns the install's keys, making and saving them on the first run.
// They are kept across runs, so the session cookie and form token outlive a
// restart into an update.
func keys(data string) (mid.Keys, error) {
	rt, err := loadRuntime(data)

	switch {
	case err == nil:
		launch, err1 := base64.RawURLEncoding.DecodeString(rt.LaunchKey)
		session, err2 := base64.RawURLEncoding.DecodeString(rt.SessionKey)

		if err1 == nil && err2 == nil && len(launch) >= 32 && len(session) >= 32 {
			return mid.Keys{Launch: launch, Session: session}, nil
		}

		// Unreadable keys are replaced. The cost is one tab that has to be
		// opened again from the shortcut.

	case errors.Is(err, fs.ErrNotExist):
	default:
		// A runtime.json that won't parse is replaced, for the same reason.
	}

	k := mid.NewKeys()

	return k, saveRuntime(data, runtimeFile{
		LaunchKey:  base64.RawURLEncoding.EncodeToString(k.Launch),
		SessionKey: base64.RawURLEncoding.EncodeToString(k.Session),
	})
}

// setPort records the port the server is bound to.
func setPort(data string, k mid.Keys, port int) error {
	return saveRuntime(data, runtimeFile{
		Port:       port,
		LaunchKey:  base64.RawURLEncoding.EncodeToString(k.Launch),
		SessionKey: base64.RawURLEncoding.EncodeToString(k.Session),
	})
}

// saveRuntime writes runtime.json whole or not at all: to a temporary file,
// then renamed over the old one, so a second start never reads half a file.
func saveRuntime(data string, rt runtimeFile) error {
	b, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(data, "runtime-*.json")
	if err != nil {
		return fmt.Errorf("writing runtime.json: %w", err)
	}
	defer os.Remove(tmp.Name()) // harmless once renamed

	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("writing runtime.json: %w", err)
	}

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("writing runtime.json: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing runtime.json: %w", err)
	}

	if err := os.Rename(tmp.Name(), runtimePath(data)); err != nil {
		return fmt.Errorf("writing runtime.json: %w", err)
	}

	return nil
}
