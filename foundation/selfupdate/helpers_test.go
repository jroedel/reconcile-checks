package selfupdate_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/selfupdate"
)

// fakeBase is testdata/fakebin, built once for this run. See its package
// comment for how one build becomes every version.
var fakeBase []byte

// exeExt is what a program's name ends with on this system.
var exeExt = map[bool]string{true: ".exe", false: ""}[runtime.GOOS == "windows"]

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "selfupdate-fakebin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "fakebin"+exeExt)

	// `go test` puts its own go first on PATH, so this is the toolchain the
	// tests are running under.
	build := exec.Command("go", "build", "-trimpath", "-o", out, "./testdata/fakebin")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")

	if b, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the fake program: %v\n%s", err, b)
		return 1
	}

	if fakeBase, err = os.ReadFile(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return m.Run()
}

// fake is a program that reports version when run as `<it> version`, or
// fails if version is "crash".
func fake(version string) []byte {
	return slices.Concat(fakeBase, []byte("\nFAKEBIN-VERSION="+version+"\n"))
}

// writeFake puts a fake program at path.
func writeFake(t *testing.T, path, version string) []byte {
	t.Helper()

	body := fake(version)

	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}

	return body
}

// reports runs the program at path and returns the version it reports, or
// fails the test.
func reports(t *testing.T, path string) string {
	t.Helper()

	out, err := exec.Command(path, "version").Output()
	if err != nil {
		t.Fatalf("running %s: %v", filepath.Base(path), err)
	}

	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Fatalf("%s printed %q", filepath.Base(path), out)
	}

	return fields[1]
}

// program makes a directory with a fake program in it, at version, and
// returns its path.
func program(t *testing.T, version string) string {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "reconcile-checks"+exeExt)
	writeFake(t, exe, version)

	return exe
}

// besides is a file next to exe: suffix inserted before a trailing .exe when
// runnable, after the name without it otherwise.
func besides(exe, suffix string, runnable bool) string {
	stem := strings.TrimSuffix(exe, ".exe")
	if runnable && strings.HasSuffix(exe, ".exe") {
		return stem + suffix + ".exe"
	}

	return stem + suffix
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// keys is a release key made for one test.
func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	return pub, priv
}

// asset is the binary's name for this system, which Latest looks for.
var asset = selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)

// release is what the fake GitHub publishes.
type release struct {
	Tag        string
	Draft      bool
	Prerelease bool

	// Files are the assets, by name.
	Files map[string][]byte

	// Gone are assets listed in the release that answer 404.
	Gone []string
}

// publish makes a release of binary as the release workflow would: the
// binary, a binary for another system, SHA256SUMS over both, and its
// signature.
func publish(priv ed25519.PrivateKey, tag string, binary []byte) release {
	other := "reconcile-checks-plan9-mips"
	otherBody := []byte("another system's build")

	sums := sumsOf(map[string][]byte{asset: binary, other: otherBody})

	return release{
		Tag: tag,
		Files: map[string][]byte{
			asset:            binary,
			other:            otherBody,
			"SHA256SUMS":     sums,
			"SHA256SUMS.sig": sign(priv, sums),
		},
	}
}

// sumsOf writes SHA256SUMS as sha256sum does, in name order.
func sumsOf(files map[string][]byte) []byte {
	var b strings.Builder

	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(files[name]), name)
	}

	return []byte(b.String())
}

// sign writes SHA256SUMS.sig in the format github.go documents.
func sign(priv ed25519.PrivateKey, sums []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n")
}

// gitHub is a fake of the two things the updater asks of GitHub: the latest
// release, and its assets.
type gitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	rel      release
	requests []string
}

func newGitHub(t *testing.T, rel release) *gitHub {
	t.Helper()

	g := &gitHub{rel: rel}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)

	return g
}

func (g *gitHub) set(rel release) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.rel = rel
}

// asked lists the paths requested so far.
func (g *gitHub) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.requests)
}

func (g *gitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.requests = append(g.requests, r.URL.Path)

	if r.Header.Get("User-Agent") == "" {
		http.Error(w, "GitHub refuses a request with no User-Agent", http.StatusForbidden)
		return
	}

	if r.URL.Path == "/repos/"+selfupdate.DefaultRepository+"/releases/latest" {
		if g.rel.Tag == "" {
			http.NotFound(w, r)
			return
		}

		type ghAsset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}

		names := append(slices.Collect(maps.Keys(g.rel.Files)), g.rel.Gone...)
		slices.Sort(names)

		var assets []ghAsset

		for _, name := range names {
			assets = append(assets, ghAsset{Name: name, URL: g.srv.URL + "/download/" + g.rel.Tag + "/" + name})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   g.rel.Tag,
			"draft":      g.rel.Draft,
			"prerelease": g.rel.Prerelease,
			"assets":     assets,
		})

		return
	}

	if name, ok := strings.CutPrefix(r.URL.Path, "/download/"+g.rel.Tag+"/"); ok {
		if body, ok := g.rel.Files[name]; ok {
			_, _ = w.Write(body)
			return
		}
	}

	http.NotFound(w, r)
}

// updater makes one for the program at exe, asking g.
func updater(t *testing.T, g *gitHub, exe, current string, key ed25519.PublicKey) *selfupdate.Updater {
	t.Helper()

	cfg := selfupdate.Config{Current: current, Key: key, Executable: exe}
	if g != nil {
		cfg.API = g.srv.URL
	}

	u, err := selfupdate.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return u
}
