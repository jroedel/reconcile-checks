package muxer_test

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/app/sdk/mid"
	"github.com/jroedel/reconcile-checks/app/sdk/muxer"
	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/app/sdk/presence"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

const host = "127.0.0.1:7461"

type fixture struct {
	h        http.Handler
	sessions *mid.Sessions
	keys     mid.Keys
	quit     chan struct{}
}

func setup(t *testing.T) *fixture {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	keys := mid.NewKeys()
	sessions := mid.New(keys)

	render, err := page.New(log, sessions, "v0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}

	quit := make(chan struct{}, 1)

	h, err := muxer.New(muxer.Config{
		Log:      log,
		Version:  "v0.0.0-test",
		Port:     func() string { return "7461" },
		Sessions: sessions,
		Render:   render,
		Presence: presence.New(time.Hour, "v0.0.0-test"),
		DB:       db,
		Schema:   sqldb.Infrastructure,
		Quit:     func() { quit <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}

	return &fixture{h: h, sessions: sessions, keys: keys, quit: quit}
}

type req struct {
	method, path string
	host         string
	session      bool
	site, origin string
	form         url.Values
}

func (f *fixture) do(r req) *httptest.ResponseRecorder {
	var body io.Reader
	if r.form != nil {
		body = strings.NewReader(r.form.Encode())
	}

	q := httptest.NewRequest(r.method, r.path, body)
	q.Host = host
	if r.host != "" {
		q.Host = r.host
	}
	if r.form != nil {
		q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.session {
		q.AddCookie(f.sessions.Cookie())
	}
	if r.site != "" {
		q.Header.Set("Sec-Fetch-Site", r.site)
	}
	if r.origin != "" {
		q.Header.Set("Origin", r.origin)
	}

	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, q)

	return w
}

func (f *fixture) token() url.Values {
	return url.Values{mid.FormField: {f.sessions.FormToken()}}
}

// Each route, reached the wrong way, is refused by the gate that should
// refuse it -- and reached the right way, is answered.
func TestTheGates(t *testing.T) {
	f := setup(t)

	cases := []struct {
		name string
		r    req
		want int
	}{
		{"home with the session", req{method: "GET", path: "/", session: true}, http.StatusOK},
		{"home without it", req{method: "GET", path: "/"}, http.StatusUnauthorized},
		{"home, rebound name", req{method: "GET", path: "/", host: "evil.example:7461", session: true}, http.StatusMisdirectedRequest},
		{"settings with the session", req{method: "GET", path: "/settings", session: true}, http.StatusOK},
		{"an unknown page with the session", req{method: "GET", path: "/nowhere", session: true}, http.StatusNotFound},
		{"an unknown page without it", req{method: "GET", path: "/nowhere"}, http.StatusUnauthorized},
		{"healthz, no session needed", req{method: "GET", path: "/healthz"}, http.StatusOK},
		{"healthz, rebound name", req{method: "GET", path: "/healthz", host: "evil.example:7461"}, http.StatusMisdirectedRequest},
		{"quit from another website", req{method: "POST", path: "/quit", session: true, site: "cross-site", origin: "https://evil.example", form: f.token()}, http.StatusForbidden},
		{"quit without the form token", req{method: "POST", path: "/quit", session: true, site: "same-origin", origin: "http://" + host, form: url.Values{}}, http.StatusForbidden},
		{"quit with a stale form token", req{method: "POST", path: "/quit", session: true, site: "same-origin", origin: "http://" + host, form: url.Values{mid.FormField: {"stale"}}}, http.StatusForbidden},
		{"quit without the session", req{method: "POST", path: "/quit", site: "same-origin", origin: "http://" + host, form: f.token()}, http.StatusUnauthorized},
		{"launch with no key", req{method: "POST", path: "/launch", form: url.Values{}}, http.StatusForbidden},
		{"launch with the wrong key", req{method: "POST", path: "/launch", form: url.Values{"key": {base64.RawURLEncoding.EncodeToString([]byte("wrong"))}}}, http.StatusForbidden},
		{"launch from another website, even with the key", req{method: "POST", path: "/launch", site: "cross-site", origin: "https://evil.example", form: url.Values{"key": {base64.RawURLEncoding.EncodeToString(f.keys.Launch)}}}, http.StatusForbidden},
		{"open with a token never made", req{method: "GET", path: "/open?k=nonsense"}, http.StatusForbidden},
		{"a preview with the session", req{method: "GET", path: "/preview.png?payee=Payee+A&amount=12.50", session: true}, http.StatusOK},
		{"a whole-page preview", req{method: "GET", path: "/preview.png?payee=Payee+A&amount=12.50&whole=1", session: true}, http.StatusOK},
		{"a preview without the session", req{method: "GET", path: "/preview.png?payee=Payee+A"}, http.StatusUnauthorized},
		{"a preview of an amount that isn't one", req{method: "GET", path: "/preview.png?amount=12.345", session: true}, http.StatusUnprocessableEntity},
		{"a preview of a payee too long", req{method: "GET", path: "/preview.png?amount=1&payee=" + strings.Repeat("x", 300), session: true}, http.StatusUnprocessableEntity},
	}

	for _, c := range cases {
		if got := f.do(c.r).Code; got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestQuitFromOurOwnPageCloses(t *testing.T) {
	f := setup(t)

	w := f.do(req{method: "POST", path: "/quit", session: true, site: "same-origin", origin: "http://" + host, form: f.token()})
	if w.Code != http.StatusOK {
		t.Fatalf("quit: %d %s", w.Code, w.Body)
	}

	select {
	case <-f.quit:
	default:
		t.Error("quit answered but did not close the program")
	}
}

// A second start: the launch key buys a fresh token, the token buys the
// session cookie once, and only once.
func TestASecondStartGetsInWithTheLaunchKey(t *testing.T) {
	f := setup(t)

	w := f.do(req{method: "POST", path: "/launch", form: url.Values{"key": {base64.RawURLEncoding.EncodeToString(f.keys.Launch)}}})
	if w.Code != http.StatusOK {
		t.Fatalf("launch: %d %s", w.Code, w.Body)
	}

	path := w.Body.String()
	if !strings.HasPrefix(path, "/open?k=") {
		t.Fatalf("launch answered %q", path)
	}

	first := f.do(req{method: "GET", path: path})
	if first.Code != http.StatusSeeOther {
		t.Fatalf("open: %d", first.Code)
	}

	var cookie *http.Cookie
	for _, c := range first.Result().Cookies() {
		if c.Name == mid.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %+v; want HttpOnly and SameSite=Strict", cookie)
	}

	if again := f.do(req{method: "GET", path: path}); again.Code != http.StatusForbidden {
		t.Errorf("the same token twice: %d, want 403", again.Code)
	}
}

func TestEveryAnswerCarriesThePolicy(t *testing.T) {
	f := setup(t)

	w := f.do(req{method: "GET", path: "/", session: true})

	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

// The page links its own files, and each is served under its hash, for ever.
func TestTheEmbeddedFilesAreServedUnderTheirHash(t *testing.T) {
	f := setup(t)

	page := f.do(req{method: "GET", path: "/", session: true}).Body.String()

	for _, name := range []string{"app.css", "app.mjs", "img/icon.svg"} {
		link := find(page, name)
		if link == "" {
			t.Errorf("the page does not link %s", name)
			continue
		}

		w := f.do(req{method: "GET", path: link})
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", link, w.Code)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s: Cache-Control = %q", link, cc)
		}
	}

	if ct := f.do(req{method: "GET", path: find(page, "app.mjs")}).Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("the script is served as %q; a module script must be JavaScript", ct)
	}
}

// find returns the /static/ link in page that ends in name.
func find(page, name string) string {
	for part := range strings.SplitSeq(page, `"`) {
		if strings.HasPrefix(part, "/static/") && strings.HasSuffix(part, "/"+name) {
			return part
		}
	}

	return ""
}
