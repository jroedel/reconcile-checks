package selfupdate_test

import (
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// install fakes what a swap leaves behind: the new program in place, the
// previous one beside it, and a probation file. Written by hand rather than
// through Apply, so that a test can start from the third start without
// running the first two.
func install(t *testing.T, exe, installed, previous string, starts int) {
	t.Helper()

	writeFake(t, exe, installed)
	writeFake(t, besides(exe, ".old", true), previous)

	blob := `{"installed":"` + installed + `","previous":"` + previous +
		`","starts":` + strconv.Itoa(starts) + `,"began":"2026-11-20T12:00:00Z"}`

	if err := os.WriteFile(besides(exe, ".probation", false), []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAVersionThatWillNotStartIsPutBack is the point of probation. Three
// starts that never pass; the fourth goes back.
func TestAVersionThatWillNotStartIsPutBack(t *testing.T) {
	exe := program(t, "v1.1.0")
	install(t, exe, "v1.1.0", "v1.0.0", 0)

	u := updater(t, nil, exe, "v1.1.0", nil)

	// A person opening a program that doesn't appear tries again; the first
	// failures could be a bad moment rather than a bad version.
	for i := 1; i <= 3; i++ {
		s := u.Start(t.Context())

		if s.Outcome != selfupdate.OnProbation || s.Starts != i {
			t.Fatalf("start %d = %+v, want on probation, counted %d", i, s, i)
		}

		if v := reports(t, exe); v != "v1.1.0" {
			t.Fatalf("start %d went back before running out of starts", i)
		}
	}

	s := u.Start(t.Context())

	if s.Outcome != selfupdate.RolledBack || s.Version != "v1.0.0" || s.Failed != "v1.1.0" {
		t.Fatalf("fourth start = %+v, want rolled back to v1.0.0 from v1.1.0", s)
	}

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program reports %s, want the version that worked", v)
	}

	if exists(besides(exe, ".probation", false)) {
		t.Error("probation outlived the rollback")
	}

	if got := u.Refused(); !slices.Equal(got, []string{"v1.1.0"}) {
		t.Errorf("Refused() = %v, want [v1.1.0]", got)
	}

	// The failed program is kept aside as .old, renamed rather than deleted
	// because on Windows it is the process doing the renaming. The next start
	// is ordinary, and clears it.
	if v := reports(t, besides(exe, ".old", true)); v != "v1.1.0" {
		t.Errorf(".old reports %s, want the failed version", v)
	}

	back := updater(t, nil, exe, "v1.0.0", nil)

	if s := back.Start(t.Context()); s.Outcome != selfupdate.Nothing {
		t.Errorf("the start after going back = %v, want nothing", s.Outcome)
	}

	if exists(besides(exe, ".old", true)) {
		t.Error("the failed program was never cleared")
	}
}

// TestARefusedVersionIsNotInstalledAgain closes the loop: without it the
// computer installs the same bad release at its next check, forever.
func TestARefusedVersionIsNotInstalledAgain(t *testing.T) {
	pub, priv := keys(t)
	exe := program(t, "v1.1.0")
	install(t, exe, "v1.1.0", "v1.0.0", 3)

	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))

	if s := updater(t, g, exe, "v1.1.0", pub).Start(t.Context()); s.Outcome != selfupdate.RolledBack {
		t.Fatalf("start = %v, want rolled back", s.Outcome)
	}

	// On v1.0.0 now, with v1.1.0 still the latest release.
	u := updater(t, g, exe, "v1.0.0", pub)

	if got, ok, err := u.Apply(t.Context()); err != nil || ok {
		t.Fatalf("Apply = %+v, %v, %v; want nothing to do", got, ok, err)
	}

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program reports %s", v)
	}

	// Forget is the way out, for a version that was fine on a computer that
	// was not.
	if err := u.Forget(); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
		t.Errorf("after Forget, Apply = %v, %v; want the update installed", ok, err)
	}
}

// TestAComputerWithNoWayBackIsLeftAlone. A program that starts badly is bad;
// no working program at all can't even be updated out of it.
func TestAComputerWithNoWayBackIsLeftAlone(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(t *testing.T, old string)
	}{
		{"the previous version is gone", func(t *testing.T, old string) { _ = os.Remove(old) }},
		{"the previous version does not run", func(t *testing.T, old string) { writeFake(t, old, "crash") }},
		{"the previous version reports another version", func(t *testing.T, old string) { writeFake(t, old, "v0.9.0") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := program(t, "v1.1.0")
			install(t, exe, "v1.1.0", "v1.0.0", 3)
			c.set(t, besides(exe, ".old", true))

			u := updater(t, nil, exe, "v1.1.0", nil)

			s := u.Start(t.Context())
			if s.Outcome != selfupdate.Stuck || s.Failed != "v1.1.0" {
				t.Fatalf("start = %+v, want stuck on v1.1.0", s)
			}

			if v := reports(t, exe); v != "v1.1.0" {
				t.Errorf("the program reports %s; it should have been left alone", v)
			}

			if exists(besides(exe, ".probation", false)) {
				t.Error("probation outlived a rollback that could not happen")
			}

			if len(u.Refused()) != 0 {
				t.Errorf("refused %v without going back", u.Refused())
			}
		})
	}
}

// TestRevertGoesBackAtTheRestart is the other rollback: the process that
// restarted into the update watched it fail to come up, and puts itself back.
func TestRevertGoesBackAtTheRestart(t *testing.T) {
	pub, priv := keys(t)
	exe := program(t, "v1.0.0")
	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
		t.Fatalf("Apply = %v, %v", ok, err)
	}

	restored, err := u.Revert(t.Context())
	if err != nil || restored != "v1.0.0" {
		t.Fatalf("Revert = %q, %v; want v1.0.0", restored, err)
	}

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program reports %s", v)
	}

	if !slices.Contains(u.Refused(), "v1.1.0") {
		t.Errorf("Refused() = %v; the version that didn't come up should be on it", u.Refused())
	}

	if exists(besides(exe, ".probation", false)) {
		t.Error("probation outlived the revert")
	}

	if _, err := u.Revert(t.Context()); err == nil {
		t.Error("a second Revert, with nothing to undo, did not say so")
	}

	// And it is not fetched again.
	if _, ok, err := u.Apply(t.Context()); err != nil || ok {
		t.Errorf("Apply after Revert = %v, %v; want nothing to do", ok, err)
	}
}

// TestACorruptProbationFileIsIgnored. A computer whose program never starts
// again because a bookkeeping file was half-written is worse than the
// failure probation exists to prevent.
func TestACorruptProbationFileIsIgnored(t *testing.T) {
	exe := program(t, "v1.1.0")

	if err := os.WriteFile(besides(exe, ".probation", false), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if s := updater(t, nil, exe, "v1.1.0", nil).Start(t.Context()); s.Outcome != selfupdate.Nothing {
		t.Errorf("start = %v, want nothing", s.Outcome)
	}

	if v := reports(t, exe); v != "v1.1.0" {
		t.Errorf("the program reports %s", v)
	}
}

// TestPassedIsSafeWithNothingOnProbation, because the caller reports passing
// at every start and should not have to know which ones followed an update.
func TestPassedIsSafeWithNothingOnProbation(t *testing.T) {
	exe := program(t, "v1.0.0")
	u := updater(t, nil, exe, "v1.0.0", nil)

	u.Passed()
	u.Passed()

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program reports %s", v)
	}
}
