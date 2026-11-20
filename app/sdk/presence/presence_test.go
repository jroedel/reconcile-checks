package presence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// clock is a time the test moves by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func tracker(grace time.Duration) (*Tracker, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	t := New(grace, "v0.0.0-test")
	t.now = c.now
	t.lastSeen = c.t

	return t, c
}

func TestIdleOnlyAfterTheGraceWithNoPage(t *testing.T) {
	tr, c := tracker(2 * time.Minute)

	if tr.Idle() {
		t.Fatal("idle at once; the grace runs from the start")
	}

	c.t = c.t.Add(2 * time.Minute)
	if !tr.Idle() {
		t.Fatal("not idle after the grace with no page ever connected")
	}
}

func TestAnOpenPageKeepsItOpen(t *testing.T) {
	tr, c := tracker(2 * time.Minute)

	tr.open()
	c.t = c.t.Add(time.Hour)
	if tr.Idle() {
		t.Fatal("idle with a page open")
	}

	// The page closes: the grace starts again from then, so a reload has
	// two minutes to come back.
	tr.close()
	if tr.Idle() {
		t.Fatal("idle the moment the page closed")
	}

	c.t = c.t.Add(2 * time.Minute)
	if !tr.Idle() {
		t.Fatal("not idle two minutes after the last page closed")
	}
}

func TestAHoldKeepsItOpenWithNoPage(t *testing.T) {
	tr, c := tracker(2 * time.Minute)

	release := tr.Hold()
	c.t = c.t.Add(time.Hour)
	if tr.Idle() || !tr.Busy() {
		t.Fatal("idle while something is in flight")
	}

	release()
	release() // twice is harmless
	if tr.Busy() {
		t.Fatal("still busy after the hold was released")
	}

	c.t = c.t.Add(2 * time.Minute)
	if !tr.Idle() {
		t.Fatal("not idle two minutes after the hold was released")
	}
}

func TestWaitReturnsWhenContextEnds(t *testing.T) {
	tr, _ := tracker(time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() { tr.Wait(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the context ended")
	}
}

func TestEventsCountsThePageAndSaysTheVersion(t *testing.T) {
	tr := New(time.Hour, "v1.2.3")

	srv := httptest.NewServer(http.HandlerFunc(tr.Events))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); !strings.Contains(got, "event: hello\ndata: v1.2.3") {
		t.Errorf("first event = %q", got)
	}

	tr.mu.Lock()
	pages := tr.pages
	tr.mu.Unlock()
	if pages != 1 {
		t.Errorf("pages = %d with one connection open", pages)
	}

	cancel()
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		pages = tr.pages
		tr.mu.Unlock()
		if pages == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("pages = %d after the connection closed", pages)
}
