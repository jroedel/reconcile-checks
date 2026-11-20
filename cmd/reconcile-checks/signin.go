package main

import (
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jroedel/reconcile-checks/business/domain/signin/signinbus"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/foundation/secretstore"
)

// signIn is signing in to the Reconcile the config names
// (docs/08-signing-in.md).
//
// The key is kept in the system's keychain, under the program's name and
// Reconcile's host, or in a file beside config.toml where there is no
// keychain. A development config (--config) always uses the file beside
// it: a test or a development run never reads or writes the keychain entry
// that holds the real key (CLAUDE.md, section 6).
func signIn(cfg config, log *slog.Logger) *signinbus.Business {
	host, _ := os.Hostname()

	sc := signinbus.Config{Device: signinbus.DeviceName(host), Log: log}

	file := secretstore.File(filepath.Join(filepath.Dir(cfg.file), "key"))
	sc.Store = file

	if cfg.Reconcile == "" {
		return signinbus.New(sc)
	}

	client, err := reconcile.New(cfg.Reconcile, version)
	if err != nil {
		// A wrong address costs signing in, not the program: the checks
		// still print, and Settings says nothing can be signed in to.
		log.Warn("the Reconcile in the config can't be used", "reconcile", cfg.Reconcile, "error", err)
		return signinbus.New(sc)
	}

	sc.Client = client

	if !cfg.explicit {
		u, _ := url.Parse(client.Base())
		sc.Store = secretstore.System(app, u.Host, file)
	}

	return signinbus.New(sc)
}
