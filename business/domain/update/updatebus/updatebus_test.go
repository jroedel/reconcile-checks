package updatebus_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/update/updatebus"
	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// fake is a selfupdate.Updater that answers as told.
type fake struct {
	mu       sync.Mutex
	release  selfupdate.Release
	newer    bool
	err      error
	writable error
	refused  []string
	applied  int
	gate     chan struct{} // when set, Apply waits for it
}

func (f *fake) Apply(context.Context) (selfupdate.Release, bool, error) {
	if f.gate != nil {
		<-f.gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.applied++

	return f.release, f.newer, f.err
}

func (f *fake) Writable() error   { return f.writable }
func (f *fake) Refused() []string { return f.refused }
func (f *fake) Forget() error     { f.refused = nil; return nil }
func (f *fake) times() int        { f.mu.Lock(); defer f.mu.Unlock(); return f.applied }

type setup struct {
	bus       *updatebus.Business
	updater   *fake
	busy      atomic.Bool
	restarted chan string
	data      string
}

func newSetup(t *testing.T, version string, u *fake) *setup {
	t.Helper()

	s := &setup{updater: u, restarted: make(chan string, 1), data: t.TempDir()}

	var asUpdater updatebus.Updater
	if u != nil {
		asUpdater = u
	}

	s.bus = updatebus.New(updatebus.Config{
		Updater: asUpdater,
		Version: version,
		HasKey:  true,
		Data:    s.data,
		Busy:    func(context.Context) (bool, error) { return s.busy.Load(), nil },
		Restart: func(v string) { s.restarted <- v },
	})

	return s
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for range 200 {
		if ok() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("never: %s", what)
}

func TestTheCheckAtStartIsOnceADay(t *testing.T) {
	s := newSetup(t, "v1.2.3", &fake{})

	s.bus.CheckIfDue(t.Context())
	s.bus.CheckIfDue(t.Context())

	if n := s.updater.times(); n != 1 {
		t.Errorf("asked %d times in one day", n)
	}

	st := s.bus.Status()
	if st.Checked.IsZero() || !st.Latest || st.State != updatebus.Idle {
		t.Errorf("status after finding nothing newer: %+v", st)
	}

	// A day later, it asks again.
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(filepath.Join(s.data, "update-checked"), old, old)

	s.bus.CheckIfDue(t.Context())

	if n := s.updater.times(); n != 2 {
		t.Errorf("asked %d times over two days", n)
	}
}

// A check that failed isn't recorded, so the next start asks again; the page
// says why.
func TestAFailedCheckIsSaidAndTriedAgain(t *testing.T) {
	s := newSetup(t, "v1.2.3", &fake{err: errors.New("couldn't reach GitHub to check for an update")})

	s.bus.CheckIfDue(t.Context())

	st := s.bus.Status()
	if !st.Checked.IsZero() {
		t.Error("a failed check was recorded as a check")
	}

	if st.Problem != "Couldn't reach GitHub to check for an update." {
		t.Errorf("the problem: %q", st.Problem)
	}

	s.bus.CheckIfDue(t.Context())

	if n := s.updater.times(); n != 2 {
		t.Errorf("asked %d times after a failure", n)
	}
}

// Installed, it waits until no check is printing, then asks for the restart.
func TestARestartWaitsForPrintingToFinish(t *testing.T) {
	s := newSetup(t, "v1.2.3", &fake{release: selfupdate.Release{Version: "v1.2.4"}, newer: true})
	s.busy.Store(true)

	if err := s.bus.CheckNow(t.Context()); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "waiting for printing", func() bool { return s.bus.Status().State == updatebus.Waiting })

	if st := s.bus.Status(); st.Installed != "v1.2.4" {
		t.Errorf("status while waiting: %+v", st)
	}

	select {
	case v := <-s.restarted:
		t.Fatalf("restarted into %s while a check was printing", v)
	case <-time.After(100 * time.Millisecond):
	}

	s.busy.Store(false)

	select {
	case v := <-s.restarted:
		if v != "v1.2.4" {
			t.Errorf("restarted into %q", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no restart once printing finished")
	}

	if st := s.bus.Status(); st.State != updatebus.Restarting {
		t.Errorf("state %q after asking for the restart", st.State)
	}
}

// Update now pressed twice is one check.
func TestUpdateNowTwiceIsOneCheck(t *testing.T) {
	gate := make(chan struct{})
	s := newSetup(t, "v1.2.3", &fake{gate: gate})

	if err := s.bus.CheckNow(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := s.bus.CheckNow(t.Context()); err != nil {
		t.Fatal(err)
	}

	if st := s.bus.Status(); st.State != updatebus.Checking {
		t.Errorf("state %q while checking", st.State)
	}

	close(gate)
	waitFor(t, "the check to end", func() bool { return s.bus.Status().State == updatebus.Idle })

	if n := s.updater.times(); n != 1 {
		t.Errorf("%d checks for two presses", n)
	}
}

func TestACopyThatCantUpdateSaysWhy(t *testing.T) {
	cases := map[string]struct {
		version string
		u       *fake
		want    string
	}{
		"a development build":     {"dev", &fake{}, "development build"},
		"no key":                  {"v1.2.3", nil, "without the key"},
		"a folder it can't write": {"v1.2.3", &fake{writable: selfupdate.ErrNotWritable}, "can't write to its own folder"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSetup(t, c.version, c.u)

			if why := s.bus.Status().CantUpdate; !strings.Contains(why, c.want) {
				t.Errorf("CantUpdate %q, want it to say %q", why, c.want)
			}

			if err := s.bus.CheckNow(t.Context()); err == nil {
				t.Error("Update now was taken")
			}

			s.bus.CheckIfDue(t.Context())

			if c.u != nil && c.u.times() != 0 {
				t.Error("it checked anyway")
			}
		})
	}
}

func TestRefusedVersionsAreShownAndForgotten(t *testing.T) {
	s := newSetup(t, "v1.2.3", &fake{refused: []string{"v1.2.4"}})

	if got := s.bus.Status().Refused; len(got) != 1 || got[0] != "v1.2.4" {
		t.Errorf("refused %q", got)
	}

	if err := s.bus.Forget(); err != nil {
		t.Fatal(err)
	}

	if got := s.bus.Status().Refused; len(got) != 0 {
		t.Errorf("refused after forgetting: %q", got)
	}
}
