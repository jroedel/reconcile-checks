package muxer_test

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/app/sdk/mid"
	"github.com/jroedel/reconcile-checks/app/sdk/muxer"
	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/app/sdk/presence"
	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/stores/settingsfile"
	"github.com/jroedel/reconcile-checks/foundation/ipp"
	"github.com/jroedel/reconcile-checks/foundation/ipp/ipptest"
	"github.com/jroedel/reconcile-checks/foundation/mdns"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

const host = "127.0.0.1:7461"

type fixture struct {
	h        http.Handler
	sessions *mid.Sessions
	keys     mid.Keys
	quit     chan struct{}
	printer  *ipptest.Server
	config   string
}

// finder "finds" the fake printer, as mDNS would find a real one.
type finder struct{ s *ipptest.Server }

func (f finder) Browse(_ context.Context, service string) ([]mdns.Service, error) {
	if service != "_ipp._tcp" {
		return nil, nil
	}

	u, _ := url.Parse(f.s.Server.URL)
	port, _ := strconv.Atoi(u.Port())

	return []mdns.Service{{
		Instance: "HP LaserJet M612 [46CBDF]", Type: service, Host: u.Hostname(), Port: port,
		TXT: map[string]string{"rp": "ipp/print", "ty": "HP LaserJet M612"},
	}}, nil
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

	printer := ipptest.New()
	t.Cleanup(printer.Close)

	config := filepath.Join(t.TempDir(), "config.toml")
	settings := settingsbus.New(settingsfile.New(config))

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
		Settings: settings,
		Print:    printbus.New(settings, &ipp.Client{User: "reconcile-checks"}),
		Finder:   finder{printer},
	})
	if err != nil {
		t.Fatal(err)
	}

	return &fixture{h: h, sessions: sessions, keys: keys, quit: quit, printer: printer, config: config}
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
		{"adding a printer from another website", req{method: "POST", path: "/settings/printers", session: true, site: "cross-site", origin: "https://evil.example", form: f.token()}, http.StatusForbidden},
		{"adding a printer without the session", req{method: "POST", path: "/settings/printers", site: "same-origin", origin: "http://" + host, form: f.token()}, http.StatusUnauthorized},
		{"a calibration page from another website", req{method: "POST", path: "/settings/accounts/checking-a/calibrate", session: true, site: "cross-site", origin: "https://evil.example", form: f.token()}, http.StatusForbidden},
		{"a printer that isn't there", req{method: "GET", path: "/settings/printer?name=Nowhere", session: true}, http.StatusNotFound},
		{"an account that isn't there", req{method: "GET", path: "/settings/accounts/nowhere", session: true}, http.StatusNotFound},
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

// A printer is found, added and asked what it offers; an account is set up on
// it; and its calibration page reaches the printer with the account's options
// -- all through the pages, as a person would.
func TestSettingUpAnAccountAndPrintingTheCalibrationPage(t *testing.T) {
	f := setup(t)

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()

		for k, v := range f.token() {
			form[k] = v
		}

		return f.do(req{method: "POST", path: path, session: true, site: "same-origin", origin: "http://" + host, form: form})
	}

	get := func(path string) string {
		t.Helper()

		w := f.do(req{method: "GET", path: path, session: true})
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d\n%s", path, w.Code, w.Body)
		}

		return w.Body.String()
	}

	found := get("/settings/printers/find")
	if !strings.Contains(found, "HP LaserJet M612 [46CBDF]") || !strings.Contains(found, f.printer.URL) {
		t.Fatalf("the printer on the network is not listed:\n%s", found)
	}

	w := post("/settings/printers", url.Values{"name": {"Office M612"}, "url": {f.printer.URL}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("adding the printer: %d\n%s", w.Code, w.Body)
	}

	if page := get(w.Header().Get("Location")); !strings.Contains(page, "HP LaserJet M612") || !strings.Contains(page, "tray-1") {
		t.Errorf("the printer's page doesn't show what it offers:\n%s", page)
	}

	if page := get("/settings/accounts/new"); !strings.Contains(page, `<option value="tray-1">`) {
		t.Errorf("the account form doesn't offer the printer's trays:\n%s", page)
	}

	// An invented account (CLAUDE.md, "This repository is public").
	account := url.Values{
		"key": {"checking-a"}, "name": {"Checking A"}, "last4": {"1234"},
		"stock": {"walmart-voucher-top"}, "printer": {"Office M612"},
		"tray": {"tray-1"}, "media_type": {"stationery-preprinted"},
		"feed": {"face down, top edge in first"}, "ceiling": {"5,000"},
		"offset_x": {"0.5"}, "offset_y": {"-1"},
	}

	bad := url.Values{}
	for k, v := range account {
		bad[k] = v
	}
	bad["last4"] = []string{"123456789"}

	if w := post("/settings/accounts", bad); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "last four digits") {
		t.Errorf("a whole account number was not refused: %d", w.Code)
	}

	if w := post("/settings/accounts", account); w.Code != http.StatusSeeOther {
		t.Fatalf("adding the account: %d\n%s", w.Code, w.Body)
	}

	if w := post("/settings/accounts/checking-a/calibrate", url.Values{}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "job 1") {
		t.Fatalf("the calibration page: %d\n%s", w.Code, w.Body)
	}

	printed := f.printer.Printed()
	if len(printed) != 1 {
		t.Fatalf("%d pages reached the printer", len(printed))
	}

	if got := printed[0].Job["media-col/media-source"]; len(got) != 1 || got[0] != "tray-1" {
		t.Errorf("printed from %q", got)
	}

	if page := get("/settings/job?printer=Office+M612&job=1"); !strings.Contains(page, "completed") {
		t.Errorf("the job's page:\n%s", page)
	}

	if page := get("/settings"); !strings.Contains(page, "Checking A") || !strings.Contains(page, "Office M612") {
		t.Errorf("the overview doesn't list the account and printer:\n%s", page)
	}
}
