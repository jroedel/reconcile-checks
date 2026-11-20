package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A second start finds the running copy, gets a fresh address from it, and
// exits; the running copy closes by itself once no page has been open for the
// grace period. Both copies run in this one process, which is enough: the
// lock is held by an open file, not by the process, so the second Acquire is
// refused exactly as a second program's would be.
func TestASecondStartHandsOverAndTheFirstClosesByItself(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.toml")
	// A port away from the usual one, so the test never meets a real copy
	// running on this computer; if it is taken, the program falls back to
	// any free port, which the test reads from runtime.json.
	if err := os.WriteFile(config, []byte("port = 17463\nclose_after = \"2s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		first     bytes.Buffer
		firstCode int
		wg        sync.WaitGroup
	)

	wg.Go(func() {
		firstCode = run([]string{"--config", config, "--no-browser"}, &syncWriter{w: &first}, &bytes.Buffer{})
	})

	port := waitForPort(t, filepath.Join(dir, "data"))

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %s", resp.Status)
	}

	var second bytes.Buffer
	if code := run([]string{"--config", config, "--no-browser"}, &second, &bytes.Buffer{}); code != 0 {
		t.Fatalf("second start exited %d", code)
	}

	if got := second.String(); !strings.Contains(got, "/open?k=") {
		t.Errorf("second start printed %q, want an address to open", got)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the first copy did not close by itself")
	}

	if firstCode != 0 {
		t.Errorf("first copy exited %d", firstCode)
	}
}

func waitForPort(t *testing.T, data string) int {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rt, err := loadRuntime(data); err == nil && rt.Port > 0 {
			return rt.Port
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("the program never wrote its port")
	return 0
}

// syncWriter lets the test read what one goroutine writes.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.Write(b)
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	if !strings.HasPrefix(out.String(), "reconcile-checks dev ") {
		t.Errorf("version printed %q", out.String())
	}
}

func TestAnUnknownCommandIsRefused(t *testing.T) {
	if code := run([]string{"frobnicate"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
