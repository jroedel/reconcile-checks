package selfupdate_test

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// TestNewerAcceptsOnlyStrictReleases is the rule that keeps a working copy
// safe: a build stamped "dev" or a bare commit that counted as "older than
// v1.0.0" would replace the change somebody was testing.
func TestNewerAcceptsOnlyStrictReleases(t *testing.T) {
	for _, c := range []struct {
		current, candidate string
		want               bool
	}{
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.3", "v1.3.0", true},
		{"v1.2.3", "v2.0.0", true},
		{"v1.9.9", "v1.10.0", true},

		{"v1.2.3", "v1.2.3", false},
		{"v1.2.4", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", false},

		// Neither side may be a build this can't compare.
		{"dev", "v1.2.3", false},
		{"v1.2.3", "dev", false},
		{"abc1234", "v1.2.3", false},
		{"v1.2.3-4-gabc1234", "v1.2.4", false},
		{"v1.2.3-dirty", "v1.2.4", false},
		{"v1.2.3", "v1.2.4-rc1", false},
		{"v1.2", "v1.3", false},
		{"1.2.3", "1.2.4", false},
		{"v1.2.3", "v+1.2.4", false},
		{"v1.2.3", "v1.2.04", false},
		{"v1.2.3", " v1.2.4", false},
		{"v1.2.3", "v1..4", false},
	} {
		if got := selfupdate.Newer(c.current, c.candidate); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.candidate, got, c.want)
		}
	}
}

func TestAssetNameMatchesWhatTheReleaseBuilds(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "reconcile-checks-linux-amd64"},
		{"windows", "amd64", "reconcile-checks-windows-amd64.exe"},
	} {
		if got := selfupdate.AssetName(c.goos, c.goarch); got != c.want {
			t.Errorf("AssetName(%s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

// TestAnUpdateIsInstalledAndTheOldOneKept walks the whole way on a fake
// program: signature, hash, smoke test, swap, probation, and Passed clearing
// the way back once it is no longer needed.
func TestAnUpdateIsInstalledAndTheOldOneKept(t *testing.T) {
	pub, priv := keys(t)
	exe := program(t, "v1.0.0")
	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	got, ok, err := u.Apply(t.Context())
	if err != nil || !ok || got.Version != "v1.1.0" {
		t.Fatalf("Apply = %+v, %v, %v; want v1.1.0 installed", got, ok, err)
	}

	if v := reports(t, exe); v != "v1.1.0" {
		t.Errorf("the program reports %s after the update", v)
	}

	old := besides(exe, ".old", true)
	if v := reports(t, old); v != "v1.0.0" {
		t.Errorf("the previous version kept beside it reports %s", v)
	}

	if exists(besides(exe, ".new", true)) {
		t.Error("the download was left behind")
	}

	probation := besides(exe, ".probation", false)
	if !exists(probation) {
		t.Error("the new version was not put on probation")
	}

	// The new process starts on probation, passes, and the way back goes.
	if s := u.Start(t.Context()); s.Outcome != selfupdate.OnProbation || s.Version != "v1.1.0" || s.Starts != 1 {
		t.Errorf("first start = %+v, want on probation, v1.1.0, 1", s)
	}

	u.Passed()

	if exists(probation) {
		t.Error("Passed left the probation file")
	}

	if exists(old) {
		t.Error("Passed left the previous version")
	}
}

// TestTheFilesBesideAWindowsProgramKeepExeLast runs the update on a program
// named as on Windows, on every system: the download and the previous version
// are run (the smoke test, and before going back), so they must still be
// programs to Windows; the bookkeeping files are not run.
func TestTheFilesBesideAWindowsProgramKeepExeLast(t *testing.T) {
	pub, priv := keys(t)
	exe := filepath.Join(t.TempDir(), "reconcile-checks.exe")
	writeFake(t, exe, "v1.0.0")

	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
		t.Fatalf("Apply = %v, %v", ok, err)
	}

	dir := filepath.Dir(exe)

	for _, name := range []string{"reconcile-checks.exe", "reconcile-checks.old.exe", "reconcile-checks.probation"} {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("%s is missing", name)
		}
	}

	if u.Path() != exe {
		t.Errorf("Path() = %s, want %s", u.Path(), exe)
	}
}

// TestNothingIsInstalledWithoutAValidSignature is the reason the key exists.
// Every case must leave the program as it was, with nothing beside it.
func TestNothingIsInstalledWithoutAValidSignature(t *testing.T) {
	pub, priv := keys(t)
	_, otherPriv := keys(t)
	binary := fake("v1.1.0")

	for _, c := range []struct {
		name   string
		mutate func(r *release)
		want   string
	}{
		{
			name:   "no SHA256SUMS.sig in the release",
			mutate: func(r *release) { delete(r.Files, "SHA256SUMS.sig") },
			want:   "publishes no SHA256SUMS.sig",
		},
		{
			name: "SHA256SUMS.sig listed but not there",
			mutate: func(r *release) {
				delete(r.Files, "SHA256SUMS.sig")
				r.Gone = append(r.Gone, "SHA256SUMS.sig")
			},
			want: "SHA256SUMS.sig",
		},
		{
			name:   "signed with another key",
			mutate: func(r *release) { r.Files["SHA256SUMS.sig"] = sign(otherPriv, r.Files["SHA256SUMS"]) },
			want:   "not signed with this program's release key",
		},
		{
			name: "SHA256SUMS altered after it was signed",
			mutate: func(r *release) {
				evil := fake("v1.1.0 ")
				r.Files[asset] = evil
				r.Files["SHA256SUMS"] = sumsOf(map[string][]byte{asset: evil})
			},
			want: "not signed with this program's release key",
		},
		{
			name: "SHA256SUMS altered by one line ending",
			mutate: func(r *release) {
				r.Files["SHA256SUMS"] = []byte(strings.ReplaceAll(string(r.Files["SHA256SUMS"]), "\n", "\r\n"))
			},
			want: "not signed with this program's release key",
		},
		{
			name:   "a signature that is not base64",
			mutate: func(r *release) { r.Files["SHA256SUMS.sig"] = []byte("-----BEGIN SIGNATURE-----\n") },
			want:   "is not a signature",
		},
		{
			name:   "a signature of the wrong length",
			mutate: func(r *release) { r.Files["SHA256SUMS.sig"] = []byte("c2hvcnQ=\n") },
			want:   "is not a signature",
		},
		{
			name:   "no SHA256SUMS in the release",
			mutate: func(r *release) { delete(r.Files, "SHA256SUMS") },
			want:   "publishes no SHA256SUMS",
		},
		{
			name: "a signed SHA256SUMS that does not list this system",
			mutate: func(r *release) {
				sums := sumsOf(map[string][]byte{"reconcile-checks-plan9-mips": []byte("x")})
				r.Files["SHA256SUMS"] = sums
				r.Files["SHA256SUMS.sig"] = sign(priv, sums)
			},
			want: "does not list",
		},
		{
			name: "the binary does not match its signed hash",
			mutate: func(r *release) {
				r.Files[asset] = fake("v1.1.0-tampered")
			},
			want: "hashes to",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rel := publish(priv, "v1.1.0", binary)
			c.mutate(&rel)

			exe := program(t, "v1.0.0")
			u := updater(t, newGitHub(t, rel), exe, "v1.0.0", pub)

			_, ok, err := u.Apply(t.Context())
			if ok || !errors.Is(err, selfupdate.ErrUnverified) {
				t.Fatalf("Apply = %v, %v; want ErrUnverified", ok, err)
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}

			if v := reports(t, exe); v != "v1.0.0" {
				t.Errorf("the program now reports %s", v)
			}

			entries, _ := os.ReadDir(filepath.Dir(exe))
			if len(entries) != 1 {
				t.Errorf("left beside the program: %v", entries)
			}
		})
	}

	// And the good case, from the same publish, so that the refusals above
	// are each the one thing they change.
	t.Run("valid", func(t *testing.T) {
		exe := program(t, "v1.0.0")
		u := updater(t, newGitHub(t, publish(priv, "v1.1.0", binary)), exe, "v1.0.0", pub)

		if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
			t.Fatalf("Apply = %v, %v", ok, err)
		}
	})
}

// TestAnUpdaterWithNoKeyRefusesToUpdate: never skip the check, and don't
// even ask GitHub.
func TestAnUpdaterWithNoKeyRefusesToUpdate(t *testing.T) {
	_, priv := keys(t)
	exe := program(t, "v1.0.0")
	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", nil)

	if _, ok, err := u.Apply(t.Context()); ok || !errors.Is(err, selfupdate.ErrNoKey) {
		t.Fatalf("Apply = %v, %v; want ErrNoKey", ok, err)
	}

	if _, ok, err := u.Latest(t.Context()); ok || !errors.Is(err, selfupdate.ErrNoKey) {
		t.Fatalf("Latest = %v, %v; want ErrNoKey", ok, err)
	}

	if asked := g.asked(); len(asked) != 0 {
		t.Errorf("asked GitHub %v", asked)
	}

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program now reports %s", v)
	}
}

// TestNewRefusesAKeyOfTheWrongLength, which would otherwise be a panic in
// ed25519.Verify on the day an update arrives.
func TestNewRefusesAKeyOfTheWrongLength(t *testing.T) {
	_, err := selfupdate.New(selfupdate.Config{
		Current:    "v1.0.0",
		Key:        ed25519.PublicKey("too short"),
		Executable: filepath.Join(t.TempDir(), "reconcile-checks"),
	})
	if err == nil {
		t.Fatal("New accepted a nine-byte key")
	}
}

// TestWhichReleasesAreTaken covers step 1 and 2 of docs/10: published, strict,
// newer, not refused. Nothing to do is not an error; a draft or pre-release
// where the latest should be is.
func TestWhichReleasesAreTaken(t *testing.T) {
	pub, priv := keys(t)

	for _, c := range []struct {
		name    string
		current string
		rel     func() release
		refused []string
		wantErr error

		// quiet is a build that must not ask GitHub anything at all.
		quiet bool
	}{
		{
			name:    "a draft",
			current: "v1.0.0",
			rel: func() release {
				r := publish(priv, "v1.1.0", fake("v1.1.0"))
				r.Draft = true
				return r
			},
			wantErr: selfupdate.ErrNoRelease,
		},
		{
			name:    "a pre-release",
			current: "v1.0.0",
			rel: func() release {
				r := publish(priv, "v1.1.0", fake("v1.1.0"))
				r.Prerelease = true
				return r
			},
			wantErr: selfupdate.ErrNoRelease,
		},
		{
			name:    "an older release",
			current: "v1.2.0",
			rel:     func() release { return publish(priv, "v1.1.0", fake("v1.1.0")) },
		},
		{
			name:    "the same release",
			current: "v1.1.0",
			rel:     func() release { return publish(priv, "v1.1.0", fake("v1.1.0")) },
		},
		{
			name:    "a tag that is not strict",
			current: "v1.0.0",
			rel:     func() release { return publish(priv, "v1.1.0-rc1", fake("v1.1.0-rc1")) },
		},
		{
			name:    "a development build",
			current: "dev",
			rel:     func() release { return publish(priv, "v1.1.0", fake("v1.1.0")) },
			quiet:   true,
		},
		{
			name:    "a build between tags",
			current: "v1.0.0-3-gabc1234-dirty",
			rel:     func() release { return publish(priv, "v1.1.0", fake("v1.1.0")) },
			quiet:   true,
		},
		{
			name:    "a refused version",
			current: "v1.0.0",
			rel:     func() release { return publish(priv, "v1.1.0", fake("v1.1.0")) },
			refused: []string{"v0.9.0", "v1.1.0"},
		},
		{
			name:    "no build for this system",
			current: "v1.0.0",
			rel: func() release {
				r := publish(priv, "v1.1.0", fake("v1.1.0"))
				delete(r.Files, asset)
				return r
			},
			wantErr: selfupdate.ErrNoRelease,
		},
		{
			name:    "no release yet",
			current: "v1.0.0",
			rel:     func() release { return release{} },
			wantErr: selfupdate.ErrNoRelease,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := program(t, "v1.0.0")

			if c.refused != nil {
				if err := os.WriteFile(besides(exe, ".refused", false), []byte(strings.Join(c.refused, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			g := newGitHub(t, c.rel())
			u := updater(t, g, exe, c.current, pub)

			got, ok, err := u.Apply(t.Context())

			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("Apply = %v; want %v", err, c.wantErr)
				}
			} else if err != nil || ok {
				t.Fatalf("Apply = %+v, %v, %v; want nothing to do", got, ok, err)
			}

			if v := reports(t, exe); v != "v1.0.0" {
				t.Errorf("the program now reports %s", v)
			}

			for _, p := range g.asked() {
				if strings.Contains(p, "SHA256SUMS") || strings.HasSuffix(p, asset) {
					t.Errorf("fetched %s for a release it was not going to install", p)
				}
			}

			if c.quiet && len(g.asked()) != 0 {
				t.Errorf("a development build asked GitHub %v", g.asked())
			}
		})
	}
}

// TestABinaryThatDoesNotRunIsNotInstalled covers what a hash can't: a
// release whose binary is the wrong architecture, truncated before it was
// hashed, or the wrong build. All three hash correctly to the wrong thing.
func TestABinaryThatDoesNotRunIsNotInstalled(t *testing.T) {
	pub, priv := keys(t)

	for _, c := range []struct {
		name   string
		tag    string
		binary []byte
		want   string
	}{
		{"it crashes", "v1.1.0", fake("crash"), "does not run"},
		{"it is not a program", "v1.1.0", []byte("not a program at all"), "does not run"},
		{"it is another version", "v1.1.0", fake("v1.0.9"), "reports"},
		// sion-backup found the version as a substring, which v1.2.30 contains.
		{"it is a version that starts like this one", "v1.2.3", fake("v1.2.30"), "reports"},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := program(t, "v1.0.0")
			u := updater(t, newGitHub(t, publish(priv, c.tag, c.binary)), exe, "v1.0.0", pub)

			_, ok, err := u.Apply(t.Context())
			if ok || !errors.Is(err, selfupdate.ErrUnverified) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Apply = %v, %v; want ErrUnverified saying %q", ok, err, c.want)
			}

			if v := reports(t, exe); v != "v1.0.0" {
				t.Errorf("the program now reports %s", v)
			}

			if exists(besides(exe, ".new", true)) || exists(besides(exe, ".old", true)) {
				t.Error("left a .new or .old beside the program")
			}
		})
	}
}

// TestAFolderThatCannotBeWrittenIsSaidSo, before anything is downloaded.
func TestAFolderThatCannotBeWrittenIsSaidSo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only folder is made with an ACL on Windows, not with chmod")
	}

	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}

	pub, priv := keys(t)
	exe := program(t, "v1.0.0")
	dir := filepath.Dir(exe)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	if _, ok, err := u.Apply(t.Context()); ok || !errors.Is(err, selfupdate.ErrNotWritable) {
		t.Fatalf("Apply = %v, %v; want ErrNotWritable", ok, err)
	}

	if slices.ContainsFunc(g.asked(), func(p string) bool { return strings.HasSuffix(p, asset) }) {
		t.Error("downloaded a binary it had nowhere to put")
	}
}

// TestASecondUpdateWaitsForTheRestart: docs/10, "Two updates without a
// restart between them". The first is installed; the second must not replace
// it before the program has restarted into it and it has passed, or .old
// would be a version that never ran.
func TestASecondUpdateWaitsForTheRestart(t *testing.T) {
	pub, priv := keys(t)
	exe := program(t, "v1.0.0")
	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
		t.Fatalf("first Apply = %v, %v", ok, err)
	}

	g.set(publish(priv, "v1.2.0", fake("v1.2.0")))

	// The process that installed it, asked again before restarting.
	if _, ok, err := u.Apply(t.Context()); ok || !errors.Is(err, selfupdate.ErrWaitForRestart) {
		t.Fatalf("second Apply = %v, %v; want ErrWaitForRestart", ok, err)
	}

	// The new process, on probation and not yet passed.
	next := updater(t, g, exe, "v1.1.0", pub)
	next.Start(t.Context())

	if _, ok, err := next.Apply(t.Context()); ok || !errors.Is(err, selfupdate.ErrWaitForRestart) {
		t.Fatalf("Apply on probation = %v, %v; want ErrWaitForRestart", ok, err)
	}

	if v := reports(t, exe); v != "v1.1.0" {
		t.Errorf("the program reports %s", v)
	}

	// Once it has passed, the next update goes ahead.
	next.Passed()

	if got, ok, err := next.Apply(t.Context()); err != nil || !ok || got.Version != "v1.2.0" {
		t.Fatalf("Apply after passing = %+v, %v, %v", got, ok, err)
	}

	if v := reports(t, besides(exe, ".old", true)); v != "v1.1.0" {
		t.Errorf("the way back is %s, want the version that passed", v)
	}
}

// TestAnOldThatCannotBeRemovedWaits is Windows after an update whose process
// hasn't stopped: .old is its image and can't be deleted. Made here with a
// folder that isn't empty, which no system removes with os.Remove.
func TestAnOldThatCannotBeRemovedWaits(t *testing.T) {
	pub, priv := keys(t)
	exe := program(t, "v1.0.0")
	old := besides(exe, ".old", true)

	if err := os.MkdirAll(filepath.Join(old, "busy"), 0o700); err != nil {
		t.Fatal(err)
	}

	g := newGitHub(t, publish(priv, "v1.1.0", fake("v1.1.0")))
	u := updater(t, g, exe, "v1.0.0", pub)

	if _, ok, err := u.Apply(t.Context()); ok || !errors.Is(err, selfupdate.ErrWaitForRestart) {
		t.Fatalf("Apply = %v, %v; want ErrWaitForRestart", ok, err)
	}

	if asked := g.asked(); len(asked) != 0 {
		t.Errorf("asked GitHub %v before finding it could not swap", asked)
	}

	if v := reports(t, exe); v != "v1.0.0" {
		t.Errorf("the program reports %s", v)
	}

	// Once it can be removed, the update goes ahead and replaces it.
	if err := os.RemoveAll(filepath.Join(old, "busy")); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := u.Apply(t.Context()); err != nil || !ok {
		t.Fatalf("Apply = %v, %v", ok, err)
	}

	if v := reports(t, old); v != "v1.0.0" {
		t.Errorf(".old reports %s", v)
	}
}
