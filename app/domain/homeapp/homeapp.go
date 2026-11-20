// Package homeapp is the home page. In this version it only says what is
// coming; writing a check arrives with the form (docs/06-writing-a-check.md).
// Settings are settingsapp's.
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
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	if err := cfg.Render.Add("home", templates, "templates/home.html"); err != nil {
		return nil, err
	}

	return &App{cfg: cfg}, nil
}

// Home is the home page.
func (a *App) Home(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "home", nil)
}
