// Package settingsapp is the Settings pages (docs/03-the-local-app.md,
// "Settings"): this computer's printers, found on the network or typed in,
// with what each says it can do, how checks go through it (the tray, the
// media type, which way up, the offset) and the calibration page that sets
// the offset; and its accounts, each with its stock and printer.
//
// The printer comes first and stands alone, so that it can be set up and
// calibrated before there is any account, which is before signing in to
// Reconcile.
//
// Every page that shows a printer asks the printer, then and there: a tray
// list remembered from last month is the kind of thing that is wrong on the
// one day it matters. A printer that can't be reached is said so, and its
// settings can still be edited by hand.
package settingsapp

import (
	"context"
	"embed"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/ipp"
	"github.com/jroedel/reconcile-checks/foundation/mdns"
)

//go:embed templates
var templates embed.FS

// Finder lists the services on the network (foundation/mdns).
type Finder interface {
	Browse(ctx context.Context, service string) ([]mdns.Service, error)
}

// Config is what the app needs.
type Config struct {
	Render   *page.Renderer
	Settings *settingsbus.Business
	Print    *printbus.Business
	Finder   Finder

	// Shown under About, so that a person asked "where is it keeping
	// things?" can answer.
	ConfigFile string
	DataDir    string
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	for name, file := range map[string]string{
		"settings":    "templates/settings.html",
		"find":        "templates/find.html",
		"printer":     "templates/printer.html",
		"account":     "templates/account.html",
		"calibrating": "templates/calibrating.html",
		"job":         "templates/job.html",
	} {
		if err := cfg.Render.Add(name, templates, file); err != nil {
			return nil, err
		}
	}

	return &App{cfg: cfg}, nil
}

// askPrinter is how long a page waits for a printer before it says the
// printer can't be reached: a printer that is on answers in well under a
// second, and one waking from sleep in a few.
const askPrinter = 8 * time.Second

// --- the overview -------------------------------------------------------------

type settingsView struct {
	Settings settingsbus.Settings
	Error    string
	Config   string
	Data     string
	Saved    string
}

// Settings is the overview: about, the printers, the accounts.
func (a *App) Settings(w http.ResponseWriter, r *http.Request) {
	s, err := a.cfg.Settings.Settings()

	a.cfg.Render.Render(w, r, http.StatusOK, "settings", settingsView{
		Settings: s, Error: sentence(err), Config: a.cfg.ConfigFile, Data: a.cfg.DataDir, Saved: r.URL.Query().Get("saved"),
	})
}

// --- printers -----------------------------------------------------------------

type found struct {
	Name, Model, URL string
	Secure           bool
}

type findView struct {
	Found     []found
	Error     string
	Name, URL string // a printer typed in and refused, to type again
}

// Find lists the printers on the network, and takes an address typed in.
func (a *App) Find(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Both kinds at once: a printer that offers ipps is listed once, by its
	// ipps address.
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		byHost = map[string]found{}
		errs   []error
	)

	for _, typ := range []string{"_ipp._tcp", "_ipps._tcp"} {
		wg.Go(func() {
			svcs, err := a.cfg.Finder.Browse(ctx, typ)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)
				return
			}

			for _, s := range svcs {
				secure := typ == "_ipps._tcp"
				if have, ok := byHost[strings.ToLower(s.Host)]; ok && have.Secure && !secure {
					continue
				}

				scheme := "ipp"
				if secure {
					scheme = "ipps"
				}

				rp := strings.TrimPrefix(s.TXT["rp"], "/")
				byHost[strings.ToLower(s.Host)] = found{
					Name:   s.Instance,
					Model:  s.TXT["ty"],
					URL:    scheme + "://" + s.Host + ":" + strconv.Itoa(s.Port) + "/" + rp,
					Secure: secure,
				}
			}
		})
	}
	wg.Wait()

	var list []found
	for _, f := range byHost {
		list = append(list, f)
	}
	slices.SortFunc(list, func(x, y found) int { return strings.Compare(x.Name, y.Name) })

	msg := ""
	if len(list) == 0 && len(errs) > 0 {
		msg = "The network couldn't be asked: " + sentence(errors.Join(errs...))
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "find", findView{Found: list, Error: msg})
}

// AddPrinter keeps a printer and the certificate it presents now.
func (a *App) AddPrinter(w http.ResponseWriter, r *http.Request) {
	p := settingsbus.Printer{Name: r.PostFormValue("name"), URL: r.PostFormValue("url")}

	if err := a.cfg.Settings.SavePrinter("", p); err != nil {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "find", findView{Error: sentence(err), Name: p.Name, URL: p.URL})

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), askPrinter)
	defer cancel()

	// The first contact pins it. A printer that can't be reached now is kept
	// anyway, unpinned; its page says so, and Trust pins it later.
	_ = a.cfg.Print.Trust(ctx, strings.TrimSpace(p.Name))

	http.Redirect(w, r, printerPage(strings.TrimSpace(p.Name)), http.StatusSeeOther)
}

// printerView is a printer's page.
type printerView struct {
	Printer  settingsbus.Printer
	Attrs    ipp.Attributes
	Asked    string // the printer's error, when it didn't answer
	Changed  bool   // its certificate changed
	Unpinned bool   // ipps, and no certificate remembered
	Stocks   []string
	Error    string // what was refused, from a form on the page
	Field    string
	Saved    bool
}

// Printer shows what a printer says about itself, how checks go through it,
// and the calibration page.
func (a *App) Printer(w http.ResponseWriter, r *http.Request) {
	p, err := a.cfg.Settings.Printer(r.URL.Query().Get("name"))
	if err != nil {
		a.notFound(w, r)
		return
	}

	a.printer(w, r, http.StatusOK, p, nil)
}

// SavePrinterChecks keeps how checks go through a printer: the tray, the
// media type, which way up, and the offset.
func (a *App) SavePrinterChecks(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")

	p, err := a.cfg.Settings.Printer(name)
	if err != nil {
		a.notFound(w, r)
		return
	}

	p.Tray = strings.TrimSpace(r.PostFormValue("tray"))
	p.MediaType = strings.TrimSpace(r.PostFormValue("media_type"))
	p.Feed = r.PostFormValue("feed")

	err = parseOffset(r, &p.Offset)
	if err == nil {
		err = a.cfg.Settings.SavePrinter(name, p)
	}

	if err != nil {
		a.printer(w, r, http.StatusUnprocessableEntity, p, err)
		return
	}

	http.Redirect(w, r, printerPage(name)+"&saved=1", http.StatusSeeOther)
}

// Calibrate prints the calibration page for a stock on a printer.
func (a *App) Calibrate(w http.ResponseWriter, r *http.Request) {
	p, err := a.cfg.Settings.Printer(r.PostFormValue("name"))
	if err != nil {
		a.notFound(w, r)
		return
	}

	stock := r.PostFormValue("stock")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	sent, err := a.cfg.Print.Calibrate(ctx, p.Name, stock)
	if err != nil {
		a.printer(w, r, http.StatusUnprocessableEntity, p, err)
		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "calibrating", struct {
		Printer settingsbus.Printer
		Sent    printbus.Sent
		Back    string
	}{p, sent, printerPage(p.Name)})
}

func (a *App) printer(w http.ResponseWriter, r *http.Request, status int, p settingsbus.Printer, failed error) {
	ctx, cancel := context.WithTimeout(r.Context(), askPrinter)
	defer cancel()

	attrs, err := a.cfg.Print.Attributes(ctx, p.Name)
	_, changed := errors.AsType[ipp.PinChanged](err)

	v := printerView{
		Printer:  p,
		Attrs:    attrs,
		Asked:    sentence(err),
		Changed:  changed,
		Unpinned: strings.HasPrefix(strings.ToLower(p.URL), "ipps:") && p.Pin == "",
		Stocks:   layoutbus.Names(),
		Error:    sentence(failed),
		Saved:    r.URL.Query().Get("saved") != "",
	}

	if inv, ok := errors.AsType[settingsbus.Invalid](failed); ok {
		v.Field = inv.Field
	}

	a.cfg.Render.Render(w, r, status, "printer", v)
}

// parseOffset reads the offset fields; an empty one is 0.
func parseOffset(r *http.Request, o *settingsbus.Offset) error {
	for _, f := range []struct {
		name string
		to   *float64
	}{{"offset_x", &o.X}, {"offset_y", &o.Y}} {
		v := strings.TrimSpace(r.PostFormValue(f.name))
		if v == "" {
			*f.to = 0
			continue
		}

		n, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
		if err != nil {
			return settingsbus.Invalid{Field: "offset", Sentence: "An offset is a number of millimetres, such as 0.5 or -1.2."}
		}
		*f.to = n
	}

	return nil
}

// Trust keeps the printer's certificate as it is now.
func (a *App) Trust(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")

	ctx, cancel := context.WithTimeout(r.Context(), askPrinter)
	defer cancel()

	if err := a.cfg.Print.Trust(ctx, name); err != nil {
		a.fail(w, r, err)
		return
	}

	http.Redirect(w, r, printerPage(name), http.StatusSeeOther)
}

// RemovePrinter removes a printer no account uses.
func (a *App) RemovePrinter(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Settings.RemovePrinter(r.PostFormValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/settings?saved=printer", http.StatusSeeOther)
}

// --- accounts -----------------------------------------------------------------

type accountView struct {
	New      bool
	Account  settingsbus.Account
	Ceiling  string
	Stocks   []string
	Printers []settingsbus.Printer
	Error    string
	Field    string
}

// NewAccount is the form for an account this computer will print on.
func (a *App) NewAccount(w http.ResponseWriter, r *http.Request) {
	acct := settingsbus.Account{Stock: layoutbus.Names()[0]}
	a.account(w, r, http.StatusOK, true, acct, nil)
}

// Account is the form for an account already kept.
func (a *App) Account(w http.ResponseWriter, r *http.Request) {
	acct, err := a.cfg.Settings.Account(r.PathValue("key"))
	if err != nil {
		a.notFound(w, r)
		return
	}

	a.account(w, r, http.StatusOK, false, acct, nil)
}

// SaveAccount adds or changes an account.
func (a *App) SaveAccount(w http.ResponseWriter, r *http.Request) {
	isNew := r.PathValue("key") == ""

	acct, err := parseAccount(r)
	if err == nil {
		if !isNew {
			acct.Key = r.PathValue("key")
		}
		err = a.cfg.Settings.SaveAccount(acct, isNew)
	}

	if err != nil {
		a.account(w, r, http.StatusUnprocessableEntity, isNew, acct, err)
		return
	}

	http.Redirect(w, r, "/settings/accounts/"+acct.Key+"?saved=1", http.StatusSeeOther)
}

// Job shows where a job is.
func (a *App) Job(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("job"))
	sent := printbus.Sent{Printer: r.URL.Query().Get("printer"), Job: id}

	ctx, cancel := context.WithTimeout(r.Context(), askPrinter)
	defer cancel()

	st, err := a.cfg.Print.Status(ctx, sent)

	a.cfg.Render.Render(w, r, http.StatusOK, "job", struct {
		Sent   printbus.Sent
		Status ipp.JobStatus
		Error  string
		Back   string
	}{sent, st, sentence(err), r.URL.Query().Get("back")})
}

func (a *App) account(w http.ResponseWriter, r *http.Request, status int, isNew bool, acct settingsbus.Account, failed error) {
	s, err := a.cfg.Settings.Settings()
	if err != nil {
		a.fail(w, r, err)
		return
	}

	v := accountView{
		New:      isNew,
		Account:  acct,
		Ceiling:  acct.Ceiling.Figures(),
		Stocks:   layoutbus.Names(),
		Printers: s.Printers,
		Error:    sentence(failed),
	}

	if acct.Ceiling <= 0 {
		v.Ceiling = ""
	}

	if inv, ok := errors.AsType[settingsbus.Invalid](failed); ok {
		v.Field = inv.Field
	}

	if acct.Printer == "" && len(s.Printers) > 0 {
		v.Account.Printer = s.Printers[0].Name
	}

	a.cfg.Render.Render(w, r, status, "account", v)
}

func parseAccount(r *http.Request) (settingsbus.Account, error) {
	acct := settingsbus.Account{
		Key:     strings.TrimSpace(r.PostFormValue("key")),
		Name:    r.PostFormValue("name"),
		Last4:   r.PostFormValue("last4"),
		Stock:   r.PostFormValue("stock"),
		Printer: r.PostFormValue("printer"),
	}

	if c := strings.TrimSpace(r.PostFormValue("ceiling")); c != "" {
		cents, err := money.Parse(c)
		if err != nil {
			return acct, settingsbus.Invalid{Field: "ceiling", Sentence: "The ceiling is an amount, such as 10,000.00."}
		}
		acct.Ceiling = cents
	}

	return acct, nil
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	s, _ := a.cfg.Settings.Settings()

	a.cfg.Render.Render(w, r, http.StatusNotFound, "settings", settingsView{
		Settings: s, Error: "There is no such printer or account in Settings.", Config: a.cfg.ConfigFile, Data: a.cfg.DataDir,
	})
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	s, _ := a.cfg.Settings.Settings()

	status := http.StatusInternalServerError
	if _, ok := errors.AsType[settingsbus.Invalid](err); ok {
		status = http.StatusUnprocessableEntity
	}

	a.cfg.Render.Render(w, r, status, "settings", settingsView{
		Settings: s, Error: sentence(err), Config: a.cfg.ConfigFile, Data: a.cfg.DataDir,
	})
}

func printerPage(name string) string {
	return "/settings/printer?name=" + url.QueryEscape(name)
}

// sentence is an error as a person reads it: its first letter capitalised and
// a full stop at the end. Every error that reaches here was written to be
// read (CLAUDE.md, "House style").
func sentence(err error) string {
	if err == nil {
		return ""
	}

	s := err.Error()
	if s == "" {
		return ""
	}

	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}

	return s
}
