// Package presence knows whether a page of the app is open, and decides when
// the program may close (docs/03-the-local-app.md, "When it closes").
//
// Each open page holds one connection to /events (server-sent events). The
// program closes when no page has been connected for the grace period (two
// minutes) and nothing is in flight: two minutes covers a reload, a tab moved
// between windows and a short sleep. "In flight" is anything that asked not to
// be interrupted with Hold -- following a print job, waiting on "Did it print
// correctly?" -- and an update restart respects the same holds.
//
// The same connection carries what the server has to tell the page; the first
// thing it says is the version, so a page can tell that the program behind it
// was updated.
package presence

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ping is how often an idle connection is written to, so that a computer that
// slept, or a proxy in between, doesn't leave a dead connection counted as an
// open page.
const ping = 20 * time.Second

// Tracker counts open pages and holds.
type Tracker struct {
	grace   time.Duration
	version string
	now     func() time.Time

	mu       sync.Mutex
	pages    int
	holds    int
	lastSeen time.Time // when the count last fell to zero, or the start
}

// New starts a tracker. The grace period runs from now, so a program whose
// browser never connects (--no-browser, or a browser that failed to open)
// still closes.
func New(grace time.Duration, version string) *Tracker {
	return &Tracker{grace: grace, version: version, now: time.Now, lastSeen: time.Now()}
}

// Hold marks something in flight, and returns the function that ends it.
// Calling that function more than once is harmless.
func (t *Tracker) Hold() (release func()) {
	t.mu.Lock()
	t.holds++
	t.mu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.holds--
			if t.holds == 0 {
				t.lastSeen = t.now()
			}
			t.mu.Unlock()
		})
	}
}

// Busy reports whether anything is in flight.
func (t *Tracker) Busy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.holds > 0
}

// Idle reports whether the program may close now.
func (t *Tracker) Idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.pages == 0 && t.holds == 0 && t.now().Sub(t.lastSeen) >= t.grace
}

// Wait returns when the program may close, or when ctx is done.
func (t *Tracker) Wait(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		if t.Idle() {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (t *Tracker) open() {
	t.mu.Lock()
	t.pages++
	t.mu.Unlock()
}

func (t *Tracker) close() {
	t.mu.Lock()
	t.pages--
	if t.pages == 0 {
		t.lastSeen = t.now()
	}
	t.mu.Unlock()
}

// Events is the page's open connection.
func (t *Tracker) Events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")

	t.open()
	defer t.close()

	send := func(format string, args ...any) error {
		// The server's write timeout would end the connection after thirty
		// seconds; each event moves the deadline on instead.
		if err := rc.SetWriteDeadline(time.Now().Add(ping + 10*time.Second)); err != nil {
			return err
		}

		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return err
		}

		return rc.Flush()
	}

	if err := send("event: hello\ndata: %s\n\n", t.version); err != nil {
		return
	}

	tick := time.NewTicker(ping)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if err := send(": ping\n\n"); err != nil {
				return
			}
		}
	}
}
