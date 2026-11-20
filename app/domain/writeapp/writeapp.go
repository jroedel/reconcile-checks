// Package writeapp is the Write a check screen (docs/06-writing-a-check.md):
// the form, the questions asked again before printing, the press of Print,
// the job followed at the printer, and "Did it print correctly?", with void
// and reprint when it didn't.
//
// Every step is a plain form post and a page, so all of it works with no
// script at all; the live preview, completion and the amount in words as it
// is typed are the page's scripts, and only add to it.
package writeapp

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/app/sdk/page"
	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/ipp"
)

//go:embed templates
var templates embed.FS

// Config is what the app needs.
type Config struct {
	Render   *page.Renderer
	Checks   *checkbus.Business
	Print    *printbus.Business
	Settings *settingsbus.Business
	Now      func() time.Time
}

// App holds the handlers.
type App struct{ cfg Config }

// New parses the app's pages.
func New(cfg Config) (*App, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	for name, file := range map[string]string{
		"write":    "templates/write.html",
		"printing": "templates/printing.html",
	} {
		if err := cfg.Render.Add(name, templates, file); err != nil {
			return nil, err
		}
	}

	return &App{cfg: cfg}, nil
}

// follow is how long the printing page follows the job at the printer before
// it stops asking and leaves the question to the person (docs/05, "Did it
// print?").
const follow = 2 * time.Minute

// fields is the form as typed: strings, so a value refused is shown back as
// it was typed, not as it was understood.
type fields struct {
	Account, Number, Date, Payee, Amount, Memo, StubNote, ReprintOf string
}

type formView struct {
	Accounts []settingsbus.Account
	Account  settingsbus.Account // the one chosen
	Form     fields
	Proposal checkbus.Proposal
	Payees   []string

	Questions []checkbus.Question
	Answered  []string // code=value, carried to the next press

	Error, Field string
	Notice       string // a check printed, or voided for a reprint
	Left         []checkbus.Check
	Summary      string // the button: "Print check 1043 to Payee A for $1,234.50"
	Words        string // the amount in words, as it will be printed

	Preview, PreviewWhole template.URL // the preview of the form as it stands
}

// Write is the form.
func (a *App) Write(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := formView{Form: fields{Account: q.Get("account")}}

	switch {
	case q.Get("printed") != "":
		v.Notice = fmt.Sprintf("Check %s printed. Its image will go to Reconcile when it can be reached.", q.Get("printed"))

	case q.Get("reprint") != "":
		n, _ := strconv.Atoi(q.Get("reprint"))

		nc, err := a.cfg.Checks.Reprint(r.Context(), q.Get("account"), n)
		if err != nil {
			v.Error = sentence(err)
			break
		}

		v.Form = formOf(nc)
		v.Notice = fmt.Sprintf("Check %d is void. Write VOID across it and keep it. "+
			"The same check is below on %d: make sure that is the number of the next check in the tray.", n, nc.Number)
	}

	a.form(w, r, http.StatusOK, v)
}

// Print takes the number, makes the check and sends it to the printer.
func (a *App) Print(w http.ResponseWriter, r *http.Request) {
	f := fields{
		Account:   r.PostFormValue("account"),
		Number:    strings.TrimSpace(r.PostFormValue("number")),
		Date:      r.PostFormValue("date"),
		Payee:     r.PostFormValue("payee"),
		Amount:    r.PostFormValue("amount"),
		Memo:      r.PostFormValue("memo"),
		StubNote:  r.PostFormValue("stub_note"),
		ReprintOf: r.PostFormValue("reprint_of"),
	}

	v := formView{Form: f}

	nc, err := parse(f)
	if err == nil {
		nc.Answered = answered(r.PostForm["answered"], nc)

		var (
			c   checkbus.Check
			pdf []byte
		)

		c, pdf, err = a.cfg.Checks.Write(r.Context(), nc)
		if err == nil {
			a.send(w, r, c, pdf, v)
			return
		}
	}

	if asked, ok := errors.AsType[checkbus.Asked](err); ok {
		v.Questions = asked.Questions
		for _, q := range asked.Questions {
			v.Answered = append(v.Answered, answer(q.Code, nc))
		}
		// Those already answered stay answered.
		for _, code := range nc.Answered {
			v.Answered = append(v.Answered, answer(code, nc))
		}

		a.form(w, r, http.StatusOK, v)

		return
	}

	v.Error, v.Field = sentence(err), field(err)
	a.form(w, r, http.StatusUnprocessableEntity, v)
}

// send prints the check the register has just made.
func (a *App) send(w http.ResponseWriter, r *http.Request, c checkbus.Check, pdf []byte, v formView) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	sent, err := a.cfg.Print.Check(ctx, c.Account, c.Number, pdf)

	if _, uncertain := errors.AsType[ipp.Uncertain](err); uncertain {
		// It may be on paper: the check stays, and the person says.
		http.Redirect(w, r, printingPage(c, printbus.Sent{}, "uncertain"), http.StatusSeeOther)
		return
	}

	if err != nil {
		// Certainly not printed, so the number is given back.
		if aerr := a.cfg.Checks.Abandon(r.Context(), c.Account, c.Number); aerr != nil {
			err = errors.Join(err, aerr)
		}

		v.Error = "Nothing was printed. " + sentence(err)
		a.form(w, r, http.StatusUnprocessableEntity, v)

		return
	}

	http.Redirect(w, r, printingPage(c, sent, ""), http.StatusSeeOther)
}

type printingView struct {
	Check     checkbus.Check
	Account   settingsbus.Account
	Sent      printbus.Sent
	Status    ipp.JobStatus
	Asked     string // the printer's error, when asking failed
	Uncertain bool
	Refresh   bool
	Next      int
	Self      string
	State     string // the job's state, as a person reads it
}

// Printing follows the job and asks whether it printed correctly.
func (a *App) Printing(w http.ResponseWriter, r *http.Request) {
	c, ok := a.check(w, r)
	if !ok {
		return
	}

	if c.State != checkbus.Printing {
		http.Redirect(w, r, "/?account="+url.QueryEscape(c.Account), http.StatusSeeOther)
		return
	}

	q := r.URL.Query()
	job, _ := strconv.Atoi(q.Get("job"))
	sent := printbus.Sent{Printer: q.Get("printer"), Job: job}

	acct, _ := a.cfg.Settings.Account(c.Account)
	p, _ := a.cfg.Checks.Propose(r.Context(), c.Account)

	v := printingView{
		Check:     c,
		Account:   acct,
		Sent:      sent,
		Uncertain: q.Get("uncertain") != "",
		Next:      p.Next,
		Self:      r.URL.String(),
	}

	if sent.Job > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		st, err := a.cfg.Print.Status(ctx, sent)
		v.Status, v.Asked, v.State = st, sentence(err), stateText(st)

		// Without a script, the page asks again by reloading itself, until
		// the printer says the job is done or two minutes have passed.
		v.Refresh = err == nil && !st.Done() && a.cfg.Now().Sub(c.CreatedAt) < follow
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "printing", v)
}

// Printed is "Yes, it printed correctly".
func (a *App) Printed(w http.ResponseWriter, r *http.Request) {
	account, number, ok := a.key(w, r)
	if !ok {
		return
	}

	if err := a.cfg.Checks.Confirm(r.Context(), account, number); err != nil {
		a.fail(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/?account=%s&printed=%d", url.QueryEscape(account), number), http.StatusSeeOther)
}

// Reprint is "No, void it and reprint": the check is voided with the reason
// given, and the form comes back with it on the next number.
func (a *App) Reprint(w http.ResponseWriter, r *http.Request) {
	account, number, ok := a.key(w, r)
	if !ok {
		return
	}

	reason := r.PostFormValue("reason")
	if reason == "other" {
		reason = r.PostFormValue("other")
	}

	if err := a.cfg.Checks.Void(r.Context(), account, number, reason); err != nil {
		a.fail(w, r, err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/?account=%s&reprint=%d", url.QueryEscape(account), number), http.StatusSeeOther)
}

// stateText is a job's state in words. The reasons are the printer's own
// keywords (media-jam, media-empty), which say more than a translation would.
func stateText(st ipp.JobStatus) string {
	var s string

	switch st.State {
	case "pending", "pending-held":
		s = "waiting to print"
	case "processing":
		s = "printing"
	case "processing-stopped":
		s = "stopped"
	case "completed":
		s = "the printer says it has printed it"
	case "canceled":
		s = "cancelled"
	case "aborted":
		s = "the printer gave up on it"
	default:
		s = "asking the printer"
	}

	var reasons []string
	for _, r := range st.Reasons {
		if r != "none" && r != "job-completed-successfully" && r != "job-printing" && r != "job-queued" {
			reasons = append(reasons, r)
		}
	}

	if len(reasons) > 0 {
		s += " (" + strings.Join(reasons, ", ") + ")"
	}

	if st.Message != "" {
		s += ": " + st.Message
	}

	return s
}

// --- the form -----------------------------------------------------------------

func (a *App) form(w http.ResponseWriter, r *http.Request, status int, v formView) {
	ctx := r.Context()

	s, err := a.cfg.Settings.Settings()
	if err != nil {
		v.Error = sentence(err)
	}
	v.Accounts = s.Accounts

	if len(v.Accounts) > 0 {
		// The account asked for, or the one used last, or the first.
		if v.Form.Account == "" {
			v.Form.Account, _ = a.cfg.Checks.LastAccount(ctx)
		}

		i := slices.IndexFunc(v.Accounts, func(x settingsbus.Account) bool { return x.Key == v.Form.Account })
		if i < 0 {
			i = 0
		}

		v.Account = v.Accounts[i]
		v.Form.Account = v.Account.Key

		v.Proposal, _ = a.cfg.Checks.Propose(ctx, v.Account.Key)
		if v.Form.Number == "" && v.Proposal.Next > 0 {
			v.Form.Number = strconv.Itoa(v.Proposal.Next)
		}
	}

	if v.Form.Date == "" {
		v.Form.Date = a.cfg.Now().Format(time.DateOnly)
	}

	v.Payees, _ = a.cfg.Checks.Payees(ctx, "", 200)
	v.Left, _ = a.cfg.Checks.LeftPrinting(ctx)

	v.Summary = "Print check"
	if nc, err := parse(v.Form); err == nil {
		v.Summary = fmt.Sprintf("Print check %d to %s for %s", nc.Number, nc.Payee, nc.Amount)

		if words, err := nc.Amount.Words(); err == nil {
			v.Words = words
		}
	}

	v.Preview, v.PreviewWhole = preview(v.Form, false), preview(v.Form, true)

	a.cfg.Render.Render(w, r, status, "write", v)
}

// preview is the address of the form's preview: built here, from values, so
// that the page's template has nothing to escape in it.
func preview(f fields, whole bool) template.URL {
	q := url.Values{}
	for k, v := range map[string]string{
		"account": f.Account, "number": f.Number, "date": f.Date, "payee": f.Payee,
		"amount": f.Amount, "memo": f.Memo, "note": f.StubNote,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}

	if whole {
		q.Set("whole", "1")
	}

	return template.URL("/preview.png?" + q.Encode())
}

// parse reads the form into the check to write.
func parse(f fields) (checkbus.NewCheck, error) {
	nc := checkbus.NewCheck{
		Account:  f.Account,
		Payee:    f.Payee,
		Memo:     f.Memo,
		StubNote: f.StubNote,
	}

	n, err := strconv.Atoi(strings.TrimSpace(f.Number))
	if err != nil || n < 1 {
		return nc, checkbus.Invalid{Field: "number", Sentence: "Type the number of the next check in the tray."}
	}
	nc.Number = n

	d, err := time.Parse(time.DateOnly, strings.TrimSpace(f.Date))
	if err != nil {
		return nc, checkbus.Invalid{Field: "date", Sentence: "Give the check a date."}
	}
	nc.WrittenOn = d

	if strings.TrimSpace(f.Payee) == "" {
		return nc, checkbus.Invalid{Field: "payee", Sentence: "Say who the check is to."}
	}

	amount, err := money.Parse(f.Amount)
	if err != nil {
		return nc, checkbus.Invalid{Field: "amount", Sentence: sentence(err)}
	}
	nc.Amount = amount

	if f.ReprintOf != "" {
		nc.ReprintOf, _ = strconv.Atoi(f.ReprintOf)
	}

	return nc, nil
}

// formOf is a check to write as the form shows it.
func formOf(nc checkbus.NewCheck) fields {
	f := fields{
		Account:  nc.Account,
		Payee:    nc.Payee,
		Memo:     nc.Memo,
		StubNote: nc.StubNote,
		Amount:   nc.Amount.Figures(),
		Date:     nc.WrittenOn.Format(time.DateOnly),
	}

	if nc.Number > 0 {
		f.Number = strconv.Itoa(nc.Number)
	}

	if nc.ReprintOf > 0 {
		f.ReprintOf = strconv.Itoa(nc.ReprintOf)
	}

	return f
}

// answer is a question answered, with the value it was asked about: a "yes"
// to $12,000 is not a "yes" to $21,000, so an answer holds only while what it
// was about is unchanged.
func answer(code checkbus.Code, nc checkbus.NewCheck) string {
	return string(code) + "=" + about(code, nc)
}

func about(code checkbus.Code, nc checkbus.NewCheck) string {
	switch code {
	case checkbus.AskOldDate, checkbus.AskPostDated:
		return nc.WrittenOn.Format(time.DateOnly)
	case checkbus.AskOverCeiling:
		return nc.Account + ":" + strconv.FormatInt(int64(nc.Amount), 10)
	case checkbus.AskNewPayee:
		return strings.ToLower(strings.TrimSpace(nc.Payee))
	}

	return ""
}

// answered is the codes whose answers still hold.
func answered(posted []string, nc checkbus.NewCheck) []checkbus.Code {
	var out []checkbus.Code

	for _, p := range posted {
		code, value, _ := strings.Cut(p, "=")
		if c := checkbus.Code(code); about(c, nc) == value && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}

	return out
}

// --- helpers --------------------------------------------------------------------

func (a *App) key(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.NotFound(w, r)
		return "", 0, false
	}

	return r.PathValue("account"), n, true
}

func (a *App) check(w http.ResponseWriter, r *http.Request) (checkbus.Check, bool) {
	account, number, ok := a.key(w, r)
	if !ok {
		return checkbus.Check{}, false
	}

	c, err := a.cfg.Checks.Get(r.Context(), account, number)
	if err != nil {
		a.fail(w, r, err)
		return checkbus.Check{}, false
	}

	return c, true
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError

	switch {
	case errors.Is(err, checkbus.ErrNotFound):
		status = http.StatusNotFound
	case isTheirs(err):
		status = http.StatusUnprocessableEntity
	}

	a.form(w, r, status, formView{Error: sentence(err)})
}

func isTheirs(err error) bool {
	_, invalid := errors.AsType[checkbus.Invalid](err)
	_, wrong := errors.AsType[checkbus.WrongState](err)

	return invalid || wrong
}

// field is which field an error is about, for the page to mark.
func field(err error) string {
	if inv, ok := errors.AsType[checkbus.Invalid](err); ok {
		return inv.Field
	}

	if tl, ok := errors.AsType[layoutbus.TooLong](err); ok {
		return fieldName(tl.Field)
	}

	if bc, ok := errors.AsType[layoutbus.BadCharacter](err); ok {
		return fieldName(bc.Field)
	}

	if _, ok := errors.AsType[checkbus.NumberUsed](err); ok {
		return "number"
	}

	return ""
}

// fieldName is the form's name for a layout's field: the stub note is
// stub_note here, and the amount in words is the amount's.
func fieldName(layoutField string) string {
	switch {
	case strings.HasPrefix(layoutField, "stub note"):
		return "stub_note"
	case strings.HasPrefix(layoutField, "amount"):
		return "amount"
	}

	return layoutField
}

func printingPage(c checkbus.Check, s printbus.Sent, uncertain string) string {
	q := url.Values{}
	if s.Job > 0 {
		q.Set("printer", s.Printer)
		q.Set("job", strconv.Itoa(s.Job))
	}

	if uncertain != "" {
		q.Set("uncertain", "1")
	}

	return fmt.Sprintf("/checks/%s/%d/printing?%s", url.PathEscape(c.Account), c.Number, q.Encode())
}

// sentence is an error as a person reads it: its first letter capitalised and
// a full stop at the end.
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
