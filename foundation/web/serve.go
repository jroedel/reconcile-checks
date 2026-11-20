package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Timeouts a server is built with. Every one of them is set, because a server
// with no timeouts is a server one slow client can hold open. The page's open
// connection (server-sent events) outlives the write timeout by design, and
// extends its own deadline before each event through http.ResponseController.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 90 * time.Second
)

// Serve answers on ln until the context is cancelled or the listener fails,
// then shuts down within grace.
//
// It takes a socket already bound, where stewards' took an address. Here the
// caller binds first because the port is not known until it is bound (the
// fixed port, or any free one when something else holds it), and the port has
// to be written down for a second start before the browser is opened. Binding
// before serving also keeps stewards' point: a port that can't be had is a
// startup error, not a failure discovered after a line claiming success.
func Serve(ctx context.Context, log *slog.Logger, grace time.Duration, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,

		// net/http's own complaints go through the same logger as everything
		// else, rather than to a bare stderr that nothing is tailing.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// Buffered, so the goroutine reporting a failure during shutdown cannot
	// block on a receiver that has already moved on.
	fatal := make(chan error, 1)

	log.Info("listening", "addr", ln.Addr().String())

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal <- fmt.Errorf("listener on %s: %w", ln.Addr(), err)
		}

		close(fatal)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down", "reason", context.Cause(ctx))
	case serveErr = <-fatal:
		log.Error("shutting down", "reason", "the listener failed", "err", serveErr)
	}

	// WithoutCancel, because by here the parent context is usually the one
	// that was just cancelled -- and a shutdown deadline derived from an
	// already-cancelled context gives in-flight requests no grace at all.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown did not finish in time", "err", err)
		srv.Close()
	}

	// Wait for the serving goroutine, so that nothing of this server outlives
	// the call. A closed channel reads as nil, which is the ordinary ending.
	<-fatal

	return serveErr
}
