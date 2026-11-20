package settingsapp

// Signing in to Reconcile, and the accounts it offers (docs/08-signing-in.md).
//
// The round trip, and why it has four steps rather than two:
//
//  1. Sign in (GET, a link): the program begins a sign-in and sends the
//     browser to Reconcile. A link and not a form, because the page's
//     Content-Security-Policy says form-action 'self', and a browser holds a
//     form's redirect to that too: a form posting here and redirected to
//     Reconcile would be stopped at the redirect.
//  2. The person signs in on Reconcile's page, and agrees.
//  3. /callback: Reconcile sends the browser back with the code. That
//     navigation comes from Reconcile's site, so the page's SameSite=Strict
//     cookie isn't sent with it, and no session can be checked. The program
//     checks the state it made and the issuer instead, trades the code, and
//     holds the key under a handoff no one else can guess.
//  4. The callback's page moves the browser on to Keep, from this site,
//     which now carries the cookie: the key is kept by a request that shows
//     the session, as everything else that changes this program is.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/signin/signinbus"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
)

// askReconcile is how long a page waits for Reconcile before it says
// Reconcile can't be reached.
const askReconcile = 8 * time.Second

// reconcileView is Reconcile, as Settings shows it.
type reconcileView struct {
	Status  signinbus.Status
	Expires string // "November 20, 2027"

	// Offered is Reconcile's accounts no account here is linked to: each
	// can be printed on from this computer.
	Offered []reconcile.Account

	// Linked is Reconcile's account for each account here linked to one,
	// by key; Gone is the accounts here linked to one the key no longer
	// reaches.
	Linked map[string]reconcile.Account
	Gone   map[string]bool

	// Problem is why the accounts couldn't be asked for.
	Problem string
}

// reconcile is where signing in stands, and what the key reaches, for the
// overview. Asking Reconcile is only done signed in.
func (a *App) reconcile(ctx context.Context, s settingsbus.Settings) *reconcileView {
	if a.cfg.SignIn == nil {
		return nil
	}

	st := a.cfg.SignIn.Status()
	if !st.Available {
		return nil
	}

	v := &reconcileView{Status: st, Linked: map[string]reconcile.Account{}, Gone: map[string]bool{}}
	if !st.Expires.IsZero() {
		v.Expires = st.Expires.Local().Format("January 2, 2006")
	}

	if !st.SignedIn {
		return v
	}

	ctx, cancel := context.WithTimeout(ctx, askReconcile)
	defer cancel()

	offered, err := a.cfg.SignIn.Accounts(ctx)
	if err != nil {
		v.Problem = sentence(err)
		v.Status = a.cfg.SignIn.Status() // a 401 has just signed it out

		return v
	}

	for _, acct := range s.Accounts {
		if acct.ReconcileID == "" {
			continue
		}

		if i := slices.IndexFunc(offered, func(o reconcile.Account) bool { return o.ID == acct.ReconcileID }); i >= 0 {
			v.Linked[acct.Key] = offered[i]
		} else {
			v.Gone[acct.Key] = true
		}
	}

	v.Offered = slices.DeleteFunc(offered, func(o reconcile.Account) bool {
		return slices.ContainsFunc(s.Accounts, func(acct settingsbus.Account) bool { return acct.ReconcileID == o.ID })
	})

	return v
}

// SignIn begins a sign-in, and sends the browser to Reconcile.
func (a *App) SignIn(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SignIn == nil {
		a.notFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), askReconcile)
	defer cancel()

	to, err := a.cfg.SignIn.Begin(ctx, "http://127.0.0.1:"+a.cfg.Port()+"/callback")
	if err != nil {
		a.fail(w, r, settingsbus.Invalid{Field: "reconcile", Sentence: sentence(err)})
		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// callbackView is the page the browser comes back to.
type callbackView struct {
	Keep  string // where to go on to, signed in
	Error string
}

// Callback is the browser come back from Reconcile, with no session (step 3
// above). Nothing it is sent is logged: the query holds the code.
func (a *App) Callback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SignIn == nil {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), askReconcile)
	defer cancel()

	h, err := a.cfg.SignIn.Back(ctx, reconcile.BackFrom(r.URL.Query()))
	if err != nil {
		status := http.StatusBadGateway

		switch {
		case errors.Is(err, signinbus.ErrUnknown):
			status = http.StatusBadRequest
		case errors.Is(err, reconcile.ErrDenied):
			status = http.StatusOK
		}

		a.cfg.Render.Render(w, r, status, "callback", callbackView{Error: sentence(err)})

		return
	}

	keep := "/settings/reconcile/keep?h=" + url.QueryEscape(h)

	// Moved on by the page rather than redirected: a redirect would still
	// be part of the navigation that came from Reconcile, and arrive
	// without the cookie. A refresh is a navigation of this page's own.
	w.Header().Set("Refresh", "0; url="+keep)
	a.cfg.Render.Render(w, r, http.StatusOK, "callback", callbackView{Keep: keep})
}

// Keep keeps the key the callback handed off, now that the session is
// shown (step 4 above).
func (a *App) Keep(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SignIn == nil {
		a.notFound(w, r)
		return
	}

	if err := a.cfg.SignIn.Keep(r.URL.Query().Get("h")); err != nil {
		a.fail(w, r, settingsbus.Invalid{Field: "reconcile", Sentence: sentence(err)})
		return
	}

	http.Redirect(w, r, "/settings?saved=signed-in", http.StatusSeeOther)
}

// SignOut forgets the key on this computer.
func (a *App) SignOut(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SignIn == nil {
		a.notFound(w, r)
		return
	}

	if err := a.cfg.SignIn.SignOut(); err != nil {
		a.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/settings?saved=signed-out", http.StatusSeeOther)
}

// offered is the accounts the account form may link acct to: those no
// other account here is linked to; and, when they couldn't be asked for,
// why. Signed out, there are none, and nothing to say.
func (a *App) offered(ctx context.Context, acct settingsbus.Account, s settingsbus.Settings) ([]reconcile.Account, string) {
	if a.cfg.SignIn == nil || !a.cfg.SignIn.Status().SignedIn {
		return nil, ""
	}

	ctx, cancel := context.WithTimeout(ctx, askReconcile)
	defer cancel()

	all, err := a.cfg.SignIn.Accounts(ctx)
	if err != nil {
		return nil, sentence(err)
	}

	return slices.DeleteFunc(all, func(o reconcile.Account) bool {
		return slices.ContainsFunc(s.Accounts, func(x settingsbus.Account) bool { return x.ReconcileID == o.ID && x.Key != acct.Key })
	}), ""
}

// reachable checks that an account is linked only to one of Reconcile's the
// key reaches. One already linked is left alone, so that an account can be
// edited signed out, or with Reconcile away.
func (a *App) reachable(ctx context.Context, acct settingsbus.Account, was string) error {
	if acct.ReconcileID == "" || acct.ReconcileID == was {
		return nil
	}

	if a.cfg.SignIn == nil {
		return settingsbus.Invalid{Field: "reconcile_id", Sentence: "Sign in to Reconcile to choose its account."}
	}

	ctx, cancel := context.WithTimeout(ctx, askReconcile)
	defer cancel()

	all, err := a.cfg.SignIn.Accounts(ctx)
	if err != nil {
		return settingsbus.Invalid{Field: "reconcile_id", Sentence: sentence(err)}
	}

	if !slices.ContainsFunc(all, func(o reconcile.Account) bool { return o.ID == acct.ReconcileID }) {
		return settingsbus.Invalid{Field: "reconcile_id", Sentence: "Reconcile doesn't offer that account to this computer's key."}
	}

	return nil
}
