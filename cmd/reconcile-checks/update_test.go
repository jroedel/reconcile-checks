package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The fingerprint is one word, so the smoke test, which reads `version` word
// by word, still finds the version beside it.
func TestTheKeyIsOneWordInVersion(t *testing.T) {
	was := releaseKey
	t.Cleanup(func() { releaseKey = was })

	releaseKey = ""
	if got := keyWord(); got != "key:none" {
		t.Errorf("no key: %q", got)
	}

	releaseKey = "not base64!"
	if got := keyWord(); got != "key:invalid" {
		t.Errorf("a bad key: %q", got)
	}

	// 32 zero bytes, base64.
	releaseKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	got := keyWord()

	// The same word scripts/releasekey's fingerprint gives for this key: the
	// release job compares the two.
	if got != "key:66687aadf862bd77" {
		t.Errorf("a key: %q", got)
	}

	if _, err := updateKey(); err != nil {
		t.Error(err)
	}
}

func TestTheCheckIsOnceADay(t *testing.T) {
	data := t.TempDir()
	now := time.Now()

	if !checkDue(data, now) {
		t.Error("never checked, and not due")
	}

	if err := checked(data, now); err != nil {
		t.Fatal(err)
	}

	if checkDue(data, now.Add(23*time.Hour)) {
		t.Error("due again within the day")
	}

	if !checkDue(data, now.Add(checkEvery)) {
		t.Error("not due a day later")
	}

	if _, err := os.Stat(checkedPath(data)); err != nil {
		t.Error(err)
	}
}

func TestTheNextCopyWaitsForTheLockWithTheSameConfig(t *testing.T) {
	got := relaunchArgs(options{config: "/tmp/x/config.toml"}, true)
	want := []string{"--wait-for-lock", "30s", "--config", "/tmp/x/config.toml", "--no-browser"}

	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	if got := relaunchArgs(options{}, false); !slices.Equal(got, []string{"--wait-for-lock", "30s"}) {
		t.Errorf("no config, the browser: %q", got)
	}
}

// Only "ok <version>" is healthy: another version answering, or one that
// can't read the register, is not the update having come up.
func TestHealthyIsTheVersionWanted(t *testing.T) {
	answer := "ok v1.2.3\n"
	status := http.StatusOK

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	if !healthy(t.Context(), port, "v1.2.3") {
		t.Error("the version wanted, answering ok, is not healthy")
	}

	if healthy(t.Context(), port, "v1.2.30") {
		t.Error("another version is healthy")
	}

	answer, status = "unhealthy v1.2.3: no such table\n", http.StatusServiceUnavailable
	if healthy(t.Context(), port, "v1.2.3") {
		t.Error("an unhealthy answer is healthy")
	}

	if waitHealthy(t.Context(), func() int { return port }, "v1.2.3", 600*time.Millisecond) {
		t.Error("waited for an answer that never came, and got one")
	}
}
