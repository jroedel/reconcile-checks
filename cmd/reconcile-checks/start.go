package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jroedel/reconcile-checks/app/domain/launchapp"
	"github.com/jroedel/reconcile-checks/app/sdk/mid"
	"github.com/jroedel/reconcile-checks/app/sdk/muxer"
	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/app/sdk/presence"
	"github.com/jroedel/reconcile-checks/foundation/browser"
	"github.com/jroedel/reconcile-checks/foundation/lockfile"
	"github.com/jroedel/reconcile-checks/foundation/logger"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
	"github.com/jroedel/reconcile-checks/foundation/web"
)

// shutdownGrace is how long requests in flight get once the program decides
// to close.
const shutdownGrace = 5 * time.Second

// start runs the program: it takes the lock, or hands over to the copy that
// holds it; then it opens the register, binds the port, opens the browser and
// serves until Quit, a signal, or two minutes with no page open.
func start(opt options, stdout, stderr io.Writer) error {
	cfg, err := loadConfig(opt.config)
	if err != nil {
		return err
	}

	lock, err := takeLock(cfg.Data, opt.waitForLock)
	if errors.Is(err, lockfile.ErrHeld) {
		return handoff(cfg.Data, opt.noBrowser, stdout)
	}
	if err != nil {
		return err
	}
	defer lock.Release()

	logFile, err := openLog(cfg.Data)
	if err != nil {
		return err
	}
	defer logFile.Close()

	level, err := logger.Level(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("%s: %w", cfg.file, err)
	}

	// The log goes to the file, always: on Windows the program has no console,
	// and it is the only place a failure can be read afterwards. A crash's
	// own report goes there too.
	log := logger.New(io.MultiWriter(logFile, quiet{stderr}), level)
	if err := debug.SetCrashOutput(logFile, debug.CrashOptions{}); err != nil {
		log.Warn("crash reports will not reach the log", "err", err)
	}

	// SetCrashOutput keeps its own handle on the file for as long as it is
	// set, and Windows won't delete or replace a file that is open. Let go of
	// it on the way out, before the file is closed.
	defer debug.SetCrashOutput(nil, debug.CrashOptions{})

	log.Info("starting", "version", version, "config", cfg.file, "data", cfg.Data)

	k, err := keys(cfg.Data)
	if err != nil {
		return err
	}

	db, err := openRegister(cfg.Data)
	if err != nil {
		return err
	}
	defer db.Close()

	ln, err := listen(cfg.Port, log)
	if err != nil {
		return err
	}

	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if err := setPort(cfg.Data, k, ln.Addr().(*net.TCPAddr).Port); err != nil {
		ln.Close()
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx, quit := context.WithCancel(ctx)
	defer quit()

	sessions := mid.New(k)
	tracker := presence.New(cfg.CloseAfter.Duration, version)

	render, err := page.New(log, sessions, version)
	if err != nil {
		ln.Close()
		return err
	}

	handler, err := muxer.New(muxer.Config{
		Log:        log,
		Version:    version,
		Port:       func() string { return port },
		Sessions:   sessions,
		Render:     render,
		Presence:   tracker,
		DB:         db,
		Schema:     sqldb.Infrastructure,
		Quit:       quit,
		ConfigFile: cfg.file,
		DataDir:    cfg.Data,
	})
	if err != nil {
		ln.Close()
		return err
	}

	// Close when no page has been open for the grace period.
	go func() {
		tracker.Wait(ctx)

		if ctx.Err() == nil {
			log.Info("closing", "reason", "no page open", "after", cfg.CloseAfter.Duration)
			quit()
		}
	}()

	address := "http://127.0.0.1:" + port + "/open?k=" + url.QueryEscape(sessions.Mint())
	show(address, opt.noBrowser, stdout, log)

	return web.Serve(ctx, log, shutdownGrace, ln, handler)
}

// takeLock takes the single-instance lock, waiting up to wait for it if asked
// (a restart into an update waits for the old copy to let go).
func takeLock(data string, wait time.Duration) (*lockfile.Lock, error) {
	path := filepath.Join(data, "instance.lock")
	deadline := time.Now().Add(wait)

	for {
		lock, err := lockfile.Acquire(path)
		if !errors.Is(err, lockfile.ErrHeld) || time.Now().After(deadline) {
			return lock, err
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// openRegister opens the register and checks its schema. In this first
// version it holds only the schema's own bookkeeping; the checks arrive with
// the register's store (docs/06-writing-a-check.md, "The register").
func openRegister(data string) (*sql.DB, error) {
	path := filepath.Join(data, "register.db")

	d, err := sqldb.Open(path)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	if err := sqldb.Init(ctx, d); err != nil {
		d.Close()
		return nil, fmt.Errorf("opening the register: %w", err)
	}

	if err := sqldb.Restrict(path); err != nil {
		d.Close()
		return nil, err
	}

	if err := sqldb.CheckSchema(ctx, d, sqldb.Infrastructure); err != nil {
		d.Close()
		return nil, fmt.Errorf("the register is not one this version can read: %w", err)
	}

	return d, nil
}

// listen binds 127.0.0.1 on the configured port, or on any free port when
// something else holds it. Never 0.0.0.0: nothing on the network may reach
// this app (docs/03-the-local-app.md).
func listen(port int, log *slog.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return ln, nil
	}

	log.Warn("the usual port is taken; using another", "port", port, "err", err)

	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not listen on 127.0.0.1: %w", err)
	}

	return ln, nil
}

// handoff is a second start: the lock is held, so a copy is running. It asks
// that copy for a fresh launch token with this install's launch key, and opens
// the browser at it.
//
// The running copy may have only just started and not yet written its port,
// so this tries for a few seconds before giving up.
func handoff(data string, noBrowser bool, stdout io.Writer) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)

	var last error
	for time.Now().Before(deadline) {
		rt, err := loadRuntime(data)
		if err == nil && rt.Port > 0 {
			var path string
			path, last = askForLaunch(client, rt)

			if last == nil {
				show("http://127.0.0.1:"+strconv.Itoa(rt.Port)+path, noBrowser, stdout, nil)
				return nil
			}
		} else if err != nil {
			last = err
		}

		time.Sleep(250 * time.Millisecond)
	}

	return fmt.Errorf("a copy of Reconcile Checks is already running, but did not answer: %v", last)
}

func askForLaunch(client *http.Client, rt runtimeFile) (string, error) {
	form := url.Values{launchapp.LaunchField: {rt.LaunchKey}}

	resp, err := client.PostForm("http://127.0.0.1:"+strconv.Itoa(rt.Port)+"/launch", form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the running copy answered %s", resp.Status)
	}

	path := strings.TrimSpace(string(body))
	if !strings.HasPrefix(path, "/open?") {
		return "", fmt.Errorf("the running copy answered %q", path)
	}

	return path, nil
}

// show opens the browser at address, and prints it as well: with
// --no-browser, where there is no browser to open, or when opening one failed.
// The address carries a one-time token, so printing it is harmless.
func show(address string, noBrowser bool, stdout io.Writer, log *slog.Logger) {
	if !noBrowser {
		err := browser.Open(address)
		if err == nil {
			return
		}

		if log != nil {
			log.Warn("could not open the browser", "err", err)
		}
	}

	fmt.Fprintln(stdout, address)
}

// quiet is a writer whose failures are ignored. The terminal is a convenience:
// on Windows the program usually has none, and writing to it then fails, which
// must not stop the same line reaching the log file.
type quiet struct{ w io.Writer }

func (q quiet) Write(b []byte) (int, error) {
	q.w.Write(b)
	return len(b), nil
}
