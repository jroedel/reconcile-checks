// Package homeapp is the home page and Settings. In this first version the
// home page only says what is coming; writing a check arrives with the form
// (docs/06-writing-a-check.md), and Settings with its printers and accounts.
package homeapp

import (
	"embed"
	"net/http"

	"github.com/jroedel/reconcile-checks/app/sdk/page"
)

//go:embed templates
var templates embed.FS

// Config is what the app needs.
type Config struct {
	Render *page.Renderer

	// Shown under Settings, About, so that a person asked "where is it
	// keeping things?" can answer.
	ConfigFile string
	DataDir    string
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	for name, file := range map[string]string{
		"home":     "templates/home.html",
		"settings": "templates/settings.html",
	} {
		if err := cfg.Render.Add(name, templates, file); err != nil {
			return nil, err
		}
	}

	return &App{cfg: cfg}, nil
}

// Home is the home page.
func (a *App) Home(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "home", nil)
}

// Settings is the settings page.
func (a *App) Settings(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "settings", struct{ Config, Data string }{
		Config: a.cfg.ConfigFile,
		Data:   a.cfg.DataDir,
	})
}
