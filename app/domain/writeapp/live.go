package writeapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
)

// What the page's script asks as the form is typed (write.mjs): the amount in
// words, a payee's last check, the number to propose, and where a job is.
// Each answer is the server's own, from the same code that prints, so the
// page never carries a second copy of a rule that could disagree with the
// paper.

// Amount is the amount as it will print: the figures and the words, or the
// sentence saying why it can't be.
func (a *App) Amount(w http.ResponseWriter, r *http.Request) {
	var out struct {
		Figures string `json:"figures"`
		Words   string `json:"words"`
		Error   string `json:"error"`
	}

	if s := strings.TrimSpace(r.URL.Query().Get("amount")); s != "" {
		c, err := money.Parse(s)
		if err == nil {
			out.Figures = c.String()
			out.Words, err = c.Words()
		}
		out.Error = sentence(err)
	}

	reply(w, out)
}

// Defaults is the payee's last check, whose amount, memo and stub note are
// offered for this one (docs/06, "Payees").
func (a *App) Defaults(w http.ResponseWriter, r *http.Request) {
	var out struct {
		Found    bool   `json:"found"`
		Payee    string `json:"payee"`
		Amount   string `json:"amount"`
		Memo     string `json:"memo"`
		StubNote string `json:"stub_note"`
	}

	c, ok, err := a.cfg.Checks.Defaults(r.Context(), r.URL.Query().Get("payee"))
	if err == nil && ok {
		out.Found, out.Payee, out.Amount, out.Memo, out.StubNote = true, c.Payee, c.Amount.Figures(), c.Memo, c.StubNote
	}

	reply(w, out)
}

// Payees completes the payee as it is typed: Reconcile's names on the
// account first, then the register's (docs/06, "Payees").
func (a *App) Payees(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	names, err := a.cfg.Checks.PayeesFor(r.Context(), q.Get("account"), q.Get("q"), 20)
	if err != nil || names == nil {
		names = []string{}
	}

	reply(w, map[string][]string{"payees": names})
}

// Proposal is the number proposed on an account, and its last few.
func (a *App) Proposal(w http.ResponseWriter, r *http.Request) {
	var out struct {
		Next  int   `json:"next"`
		Last  []int `json:"last"`
		Books bool  `json:"books"` // the other computer's numbers are in it, from Reconcile
	}

	p, err := a.cfg.Checks.Propose(r.Context(), r.URL.Query().Get("account"))
	if err == nil {
		out.Next, out.Last, out.Books = p.Next, p.Last, p.Books
	}

	if out.Last == nil {
		out.Last = []int{}
	}

	reply(w, out)
}

// Job is where a check's print job is, for the printing page to show without
// reloading itself.
func (a *App) Job(w http.ResponseWriter, r *http.Request) {
	var out struct {
		State string `json:"state"`
		Text  string `json:"text"`
		Done  bool   `json:"done"`
		Error string `json:"error"`
	}

	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("job"))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	st, err := a.cfg.Print.Status(ctx, printbus.Sent{Printer: q.Get("printer"), Job: id})
	if err != nil {
		out.Error = sentence(err)
	} else {
		out.State, out.Text, out.Done = st.State, stateText(st), st.Done()
	}

	reply(w, out)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
