// Package secretstore keeps one secret for one program: in the operating
// system's keychain where there is one (Windows Credential Manager, the
// Secret Service keyring of a Linux desktop), and otherwise in a file only
// its user can read.
//
// It knows nothing of what the secret is. The program keeps its Reconcile
// key here (docs/08-signing-in.md, "The key, kept").
//
// Why a keychain at all, when the file would do: the file is readable by
// every program the person runs and by every backup of their home folder;
// the keychain is locked with their login, and on Windows is roamed and
// protected by the system. Why the file as well: a Linux computer with no
// desktop keyring running (a minimal desktop, a session started over ssh)
// has nowhere else, and signing in must still work there.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/zalando/go-keyring"
)

// ErrNone is nothing kept.
var ErrNone = errors.New("nothing is kept")

// Store keeps one secret.
type Store interface {
	Get() (string, error) // ErrNone when nothing is kept
	Set(secret string) error
	Delete() error // nothing kept is not an error
}

// --- a file -------------------------------------------------------------------

// File keeps the secret in the file at path, readable and writable only by
// its user, in a folder only its user can open.
func File(path string) Store { return file{path} }

type file struct{ path string }

func (f file) Get() (string, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNone
	}

	if err != nil {
		return "", err
	}

	return string(b), nil
}

// Set writes beside the file and renames it into place, so that the secret
// is never half written, and its mode is set before the secret is in it.
func (f file) Set(secret string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".secret-*")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}

	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), f.path)
}

func (f file) Delete() error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// --- the system's keychain ----------------------------------------------------

// System keeps the secret in the operating system's keychain under service
// and user, and in fallback when there is no keychain to keep it in.
//
// A secret is in one place at a time: kept in the keychain, any copy in the
// file is deleted, so that signing in again on a computer whose keyring has
// come back doesn't leave an older key in a file.
func System(service, user string, fallback Store) Store {
	return system{service: service, user: user, fallback: fallback}
}

type system struct {
	service, user string
	fallback      Store
}

// wait is how long the keychain is given. Unlocking a locked Linux keyring
// shows the person a prompt, which the call waits on; a person who walked
// away from it shouldn't leave the page waiting for ever.
const wait = 2 * time.Minute

func (s system) Get() (string, error) {
	if available() {
		v, err := call(func() (string, error) { return keyring.Get(s.service, s.user) })
		if err == nil {
			return v, nil
		}
	}

	return s.fallback.Get()
}

func (s system) Set(secret string) error {
	if available() {
		_, err := call(func() (string, error) { return "", keyring.Set(s.service, s.user, secret) })
		if err == nil {
			// The file's copy, if any, is older than this one.
			return s.fallback.Delete()
		}
	}

	return s.fallback.Set(secret)
}

func (s system) Delete() error {
	if available() {
		_, err := call(func() (string, error) { return "", keyring.Delete(s.service, s.user) })
		if err != nil && !errors.Is(err, keyring.ErrNotFound) && !errors.Is(err, errTimedOut) {
			return fmt.Errorf("removing the key from the system keychain: %w", err)
		}
	}

	return s.fallback.Delete()
}

var errTimedOut = errors.New("the system keychain didn't answer")

// call runs f, giving up after wait. A keychain call can't be cancelled, so
// one given up on finishes, or doesn't, on its own.
func call(f func() (string, error)) (string, error) {
	type answer struct {
		v   string
		err error
	}

	ch := make(chan answer, 1)
	go func() {
		v, err := f()
		ch <- answer{v, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	select {
	case a := <-ch:
		return a.v, a.err
	case <-ctx.Done():
		return "", errTimedOut
	}
}

// available is whether there is a keychain to ask. On Linux that is a
// session bus: without one, the D-Bus library would try to start one, which
// on a computer with no desktop is a long wait for nothing.
func available() bool {
	if runtime.GOOS != "linux" {
		return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	}

	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}

	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
			return true
		}
	}

	return false
}
