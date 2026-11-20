// Package launchapp is how a person gets into the app, and out of it:
// trading a launch token for the session, a second start asking the running
// copy for a fresh token, the page for someone who arrived without either,
// and Quit (docs/03-the-local-app.md).
package launchapp

import (
	"embed"
	"encoding/base64"
	"net/http"
	"net/url"

	"github.com/jroedel/reconcile-checks/app/sdk/mid"
	"github.com/jroedel/reconcile-checks/app/sdk/page"
)

//go:embed templates
var templates embed.FS

// LaunchField is the form field a second start sends the launch key in.
const LaunchField = "key"

// Config is what the app needs.
type Config struct {
	Sessions *mid.Sessions
	Render   *page.Renderer
	Quit     func() // closes the program, after the answer has been sent
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	for name, file := range map[string]string{
		"refused": "templates/refused.html",
		"quit":    "templates/quit.html",
	} {
		if err := cfg.Render.Add(name, templates, file); err != nil {
			return nil, err
		}
	}

	return &App{cfg: cfg}, nil
}

// Open trades a launch token for the session cookie, and goes home, or to
// Settings when the address says to=settings: the first start after
// installing, when there is a printer to set up before anything else.
//
// A token already used -- the address reloaded, or opened from the history --
// is no good; but if the browser already has the session, it is simply sent
// on rather than told off.
func (a *App) Open(w http.ResponseWriter, r *http.Request) {
	// One fixed place or home, never an address from the query: nothing here
	// sends a browser anywhere a link chose.
	to := "/"
	if r.URL.Query().Get("to") == "settings" {
		to = "/settings"
	}

	if a.cfg.Sessions.Redeem(r.URL.Query().Get("k")) {
		http.SetCookie(w, a.cfg.Sessions.Cookie())
		http.Redirect(w, r, to, http.StatusSeeOther)

		return
	}

	if mid.HasSession(r.Context()) {
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}

	a.cfg.Render.Render(w, r, http.StatusForbidden, "refused", nil)
}

// Launch answers a second start of the program: given this install's launch
// key, it makes a fresh launch token and answers with the address to open,
// as a path (the second start knows the port it reached).
func (a *App) Launch(w http.ResponseWriter, r *http.Request) {
	key, err := base64.RawURLEncoding.DecodeString(r.PostFormValue(LaunchField))
	if err != nil || !a.cfg.Sessions.LaunchKeyMatches(key) {
		http.Error(w, "That is not this program's launch key.", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("/open?k=" + url.QueryEscape(a.cfg.Sessions.Mint())))
}

// Refused is the page for a request without the session.
func (a *App) Refused(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusUnauthorized, "refused", nil)
}

// Quit says goodbye and closes the program.
func (a *App) Quit(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "quit", nil)

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	a.cfg.Quit()
}
