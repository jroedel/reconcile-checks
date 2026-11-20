// Package checksapp is the Checks screen (docs/03-the-local-app.md, "The
// screens"): the register, most recent first, with what Reconcile has of
// each; one check with its image; voiding a check later; and correcting a
// number to the one on the paper.
package checksapp

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
)

//go:embed templates
var templates embed.FS

// Config is what the app needs.
type Config struct {
	Render *page.Renderer
	Checks *checkbus.Business
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	for name, file := range map[string]string{
		"checks": "templates/checks.html",
		"check":  "templates/check.html",
	} {
		if err := cfg.Render.Add(name, templates, file); err != nil {
			return nil, err
		}
	}

	return &App{cfg: cfg}, nil
}

// shown is how many checks the register screen lists: a few months of
// checks, which is what anyone looks back over here. Reconcile is the books.
const shown = 200

type listView struct {
	Checks []checkbus.Check
	Show   string // all, void, unsent
	Unsent int
	Error  string
}

// List is the register.
func (a *App) List(w http.ResponseWriter, r *http.Request) {
	show := r.URL.Query().Get("show")

	all, err := a.cfg.Checks.Latest(r.Context(), shown)
	unsent, uerr := a.cfg.Checks.Unsent(r.Context())

	v := listView{Show: show, Unsent: len(unsent), Error: sentence(errors.Join(err, uerr))}

	switch show {
	case "void":
		for _, c := range all {
			if c.State == checkbus.Void {
				v.Checks = append(v.Checks, c)
			}
		}
	case "unsent":
		v.Checks = unsent
	default:
		v.Show = "all"
		v.Checks = all
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "checks", v)
}

type checkView struct {
	Check   checkbus.Check
	Error   string
	Field   string
	Notice  string
	Sending string // what Reconcile has of it, in words
}

// Check is one check, with its image.
func (a *App) Check(w http.ResponseWriter, r *http.Request) {
	c, ok := a.get(w, r)
	if !ok {
		return
	}

	notice := ""
	switch r.URL.Query().Get("done") {
	case "void":
		notice = fmt.Sprintf("Check %d is void. Write VOID across the paper, if you have it, and keep it.", c.Number)
	case "renumbered":
		notice = fmt.Sprintf("This check is now number %d.", c.Number)
	}

	a.show(w, r, http.StatusOK, checkView{Check: c, Notice: notice})
}

// Image is the check's PNG.
func (a *App) Image(w http.ResponseWriter, r *http.Request) {
	account, number, ok := key(w, r)
	if !ok {
		return
	}

	b, err := a.cfg.Checks.Image(r.Context(), account, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(b)
}

// Void voids a check later: lost in the mail, written in error, never cashed.
func (a *App) Void(w http.ResponseWriter, r *http.Request) {
	c, ok := a.get(w, r)
	if !ok {
		return
	}

	if err := a.cfg.Checks.Void(r.Context(), c.Account, c.Number, r.PostFormValue("reason")); err != nil {
		a.show(w, r, http.StatusUnprocessableEntity, checkView{Check: c, Error: sentence(err), Field: "reason"})
		return
	}

	http.Redirect(w, r, checkPage(c.Account, c.Number)+"?done=void", http.StatusSeeOther)
}

// Renumber corrects the check's number to the one on the paper.
func (a *App) Renumber(w http.ResponseWriter, r *http.Request) {
	c, ok := a.get(w, r)
	if !ok {
		return
	}

	to, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("to")))
	if err != nil {
		to = 0
	}

	if err := a.cfg.Checks.Renumber(r.Context(), c.Account, c.Number, to); err != nil {
		a.show(w, r, http.StatusUnprocessableEntity, checkView{Check: c, Error: sentence(err), Field: "to"})
		return
	}

	http.Redirect(w, r, checkPage(c.Account, to)+"?done=renumbered", http.StatusSeeOther)
}

func (a *App) show(w http.ResponseWriter, r *http.Request, status int, v checkView) {
	v.Sending = sending(v.Check)
	a.cfg.Render.Render(w, r, status, "check", v)
}

// sending says what Reconcile has of the check.
func sending(c checkbus.Check) string {
	switch {
	case c.State == checkbus.Printing:
		return "Not yet: nobody has said whether it printed correctly."
	case c.Send.Refusal != "":
		return "Reconcile refused it: " + c.Send.Refusal
	case c.State == checkbus.Void && !c.Send.VoidSentAt.IsZero():
		return "Reconcile has it, voided."
	case !c.Send.SentAt.IsZero() && c.State == checkbus.Void:
		return "Reconcile has it; the void is waiting to be sent."
	case !c.Send.SentAt.IsZero():
		return "Reconcile has it."
	default:
		return "Waiting to be sent to Reconcile."
	}
}

func (a *App) get(w http.ResponseWriter, r *http.Request) (checkbus.Check, bool) {
	account, number, ok := key(w, r)
	if !ok {
		return checkbus.Check{}, false
	}

	c, err := a.cfg.Checks.Get(r.Context(), account, number)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, checkbus.ErrNotFound) {
			status = http.StatusNotFound
		}

		a.cfg.Render.Render(w, r, status, "checks", listView{Show: "all", Error: sentence(err)})

		return checkbus.Check{}, false
	}

	return c, true
}

func key(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.NotFound(w, r)
		return "", 0, false
	}

	return r.PathValue("account"), n, true
}

func checkPage(account string, number int) string {
	return fmt.Sprintf("/checks/%s/%d", url.PathEscape(account), number)
}

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
