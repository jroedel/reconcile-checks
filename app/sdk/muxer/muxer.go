// Package muxer assembles the one handler the server answers with: every route,
// and what each sits behind. Adding a route should be one line in the right
// list below, and the thing most easily got wrong about a new route is which
// list that is.
//
// The chain, outermost first (.claude/skills/writing-go-web/SKILL.md):
//
//	RequestID              mints the id every line of this request will carry
//	Logging                writes the one "request" line, whatever answers
//	Panics                 recovers, logs it, answers 500
//	SecureHeaders          the Content-Security-Policy and the rest, on everything
//	RequireLocalHost       refuses a Host that is not 127.0.0.1:{port}: DNS rebinding
//	Authenticate           reads the session cookie, and refuses nobody
//	MaxBody                caps every body
//	  -- the open routes: the embedded files, /healthz, /open, /launch
//	  Require              refuses a request with no session
//	    RequireSameOrigin  refuses a write that no page of this server asked for
//	      the app
//
// The open routes, and why each is outside the session:
//
//   - /static/ is the stylesheet, the script and the icons, which the page
//     saying "open the program from its shortcut" needs too.
//   - /healthz is asked by an update restart, a program and not a browser
//     (app/sdk/health); it says the version and nothing else.
//   - /open is how a session begins (app/domain/launchapp).
//   - /launch is a second start of the program asking for a fresh token. It
//     carries no cookie, and proves itself with the launch key instead; it
//     sits behind SameOriginOnly, which lets a program through and refuses
//     another website.
package muxer

import (
	"cmp"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/jroedel/reconcile-checks/app/domain/checksapp"
	"github.com/jroedel/reconcile-checks/app/domain/launchapp"
	"github.com/jroedel/reconcile-checks/app/domain/previewapp"
	"github.com/jroedel/reconcile-checks/app/domain/settingsapp"
	"github.com/jroedel/reconcile-checks/app/domain/writeapp"
	"github.com/jroedel/reconcile-checks/app/sdk/health"
	"github.com/jroedel/reconcile-checks/app/sdk/mid"
	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/app/sdk/presence"
	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
	"github.com/jroedel/reconcile-checks/foundation/web"
)

// maxBody is the most any request may send. Nothing here sends more than a
// form of a few fields.
const maxBody = 64 << 10

// Policy is the response headers for every answer. Nothing from the internet:
// the page never talks to Reconcile or the printer, the server does.
var Policy = web.Policy{
	ContentSecurityPolicy: "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; " +
		"form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
	ReferrerPolicy: "same-origin",
	CacheControl:   "no-store",
	FrameOptions:   "DENY",
}

// Config is everything the routes need.
type Config struct {
	Log      *slog.Logger
	Version  string
	Port     func() string // the port this server is bound to, known once bound
	Sessions *mid.Sessions
	Render   *page.Renderer
	Presence *presence.Tracker
	DB       *sql.DB
	Schema   sqldb.Expected
	Quit     func()

	ConfigFile string // shown in Settings
	DataDir    string

	// Stock is the layout the preview draws on, until accounts each name
	// their own.
	Stock string

	Settings *settingsbus.Business
	Print    *printbus.Business
	Finder   settingsapp.Finder
	Checks   *checkbus.Business
}

// DefaultStock is the one stock this program knows today.
const DefaultStock = "walmart-voucher-top"

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	launch, err := launchapp.New(launchapp.Config{Sessions: cfg.Sessions, Render: cfg.Render, Quit: cfg.Quit})
	if err != nil {
		return nil, err
	}

	write, err := writeapp.New(writeapp.Config{Render: cfg.Render, Checks: cfg.Checks, Print: cfg.Print, Settings: cfg.Settings})
	if err != nil {
		return nil, err
	}

	checks, err := checksapp.New(checksapp.Config{Render: cfg.Render, Checks: cfg.Checks})
	if err != nil {
		return nil, err
	}

	settings, err := settingsapp.New(settingsapp.Config{
		Render:     cfg.Render,
		Settings:   cfg.Settings,
		Print:      cfg.Print,
		Finder:     cfg.Finder,
		ConfigFile: cfg.ConfigFile,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return nil, err
	}

	preview, err := previewapp.New(previewapp.Config{Stock: cmp.Or(cfg.Stock, DefaultStock), Accounts: cfg.Settings})
	if err != nil {
		return nil, err
	}

	// The routes behind the session. Every write among them is checked for
	// where it came from.
	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", write.Write)
	app.HandleFunc("POST /checks", write.Print)
	app.HandleFunc("GET /checks/{account}/{number}/printing", write.Printing)
	app.HandleFunc("POST /checks/{account}/{number}/printed", write.Printed)
	app.HandleFunc("POST /checks/{account}/{number}/reprint", write.Reprint)
	app.HandleFunc("GET /checks/{account}/{number}/job", write.Job)
	app.HandleFunc("GET /write/amount", write.Amount)
	app.HandleFunc("GET /write/defaults", write.Defaults)
	app.HandleFunc("GET /write/proposal", write.Proposal)

	app.HandleFunc("GET /checks", checks.List)
	app.HandleFunc("GET /checks/{account}/{number}", checks.Check)
	app.HandleFunc("GET /checks/{account}/{number}/image.png", checks.Image)
	app.HandleFunc("POST /checks/{account}/{number}/void", checks.Void)
	app.HandleFunc("POST /checks/{account}/{number}/renumber", checks.Renumber)
	app.HandleFunc("GET /settings", settings.Settings)
	app.HandleFunc("GET /settings/printers/find", settings.Find)
	app.HandleFunc("POST /settings/printers", settings.AddPrinter)
	app.HandleFunc("GET /settings/printer", settings.Printer)
	app.HandleFunc("POST /settings/printer/trust", settings.Trust)
	app.HandleFunc("POST /settings/printer/remove", settings.RemovePrinter)
	app.HandleFunc("GET /settings/accounts/new", settings.NewAccount)
	app.HandleFunc("POST /settings/accounts", settings.SaveAccount)
	app.HandleFunc("GET /settings/accounts/{key}", settings.Account)
	app.HandleFunc("POST /settings/accounts/{key}", settings.SaveAccount)
	app.HandleFunc("POST /settings/accounts/{key}/calibrate", settings.Calibrate)
	app.HandleFunc("GET /settings/job", settings.Job)
	app.HandleFunc("GET /events", cfg.Presence.Events)
	app.HandleFunc("GET /preview.png", preview.Preview)
	app.HandleFunc("POST /quit", launch.Quit)

	// The open routes.
	root := http.NewServeMux()
	root.HandleFunc("GET "+page.StaticPrefix+"{hash}/{name...}", cfg.Render.Static)
	root.HandleFunc("GET /healthz", health.Handler(cfg.Version, cfg.DB, cfg.Schema))
	root.HandleFunc("GET /open", launch.Open)
	root.Handle("POST /launch", web.SameOriginOnly(cfg.Port)(http.HandlerFunc(launch.Launch)))
	root.Handle("/", web.Wrap(app,
		mid.Require(http.HandlerFunc(launch.Refused)),
		mid.RequireSameOrigin(cfg.Sessions, cfg.Port),
	))

	return web.Wrap(root,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.Panics(cfg.Log),
		web.SecureHeaders(Policy),
		web.RequireLocalHost(cfg.Port),
		mid.Authenticate(cfg.Sessions),
		web.MaxBody(maxBody),
	), nil
}
