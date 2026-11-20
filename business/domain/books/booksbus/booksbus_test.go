package booksbus_test

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/books/booksbus"
	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/stores/settingsfile"
	"github.com/jroedel/reconcile-checks/business/domain/signin/signinbus"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile/reconciletest"
	"github.com/jroedel/reconcile-checks/foundation/secretstore"
)

type fixture struct {
	srv    *reconciletest.Server
	signIn *signinbus.Business
	books  *booksbus.Business
}

// setup is a Reconcile with one invented account, a computer with two
// accounts (checking-a linked to it, checking-b not), signed in.
func setup(t *testing.T) *fixture {
	t.Helper()

	srv := reconciletest.New(reconciletest.Account{ID: "acc1", Name: "Checking A", Last4: "1234", Currency: "USD", Highest: "1050", LastNumbers: []string{"1050", "1049"}})
	t.Cleanup(srv.Close)

	client, err := reconcile.New(srv.URL, "v1")
	if err != nil {
		t.Fatal(err)
	}

	signIn := signinbus.New(signinbus.Config{Client: client, Store: secretstore.File(filepath.Join(t.TempDir(), "key")), Device: "Office"})

	settings := settingsbus.New(settingsfile.New(filepath.Join(t.TempDir(), "config.toml")))
	if err := settings.SavePrinter("", settingsbus.Printer{Name: "Office", URL: "ipp://printer.invalid/ipp/print"}); err != nil {
		t.Fatal(err)
	}

	for _, a := range []settingsbus.Account{
		{Key: "checking-a", Name: "Checking A", Last4: "1234", Stock: "walmart-voucher-top", Printer: "Office", ReconcileID: "acc1"},
		{Key: "checking-b", Name: "Checking B", Last4: "5678", Stock: "walmart-voucher-top", Printer: "Office"},
	} {
		if err := settings.SaveAccount(a, true); err != nil {
			t.Fatal(err)
		}
	}

	f := &fixture{srv: srv, signIn: signIn, books: booksbus.New(booksbus.Config{SignIn: signIn, Settings: settings})}

	return f
}

func (f *fixture) login(t *testing.T) {
	t.Helper()

	to, err := f.signIn.Begin(t.Context(), "http://127.0.0.1:7461/callback")
	if err != nil {
		t.Fatal(err)
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	back, _ := url.Parse(resp.Header.Get("Location"))

	h, err := f.signIn.Back(t.Context(), reconcile.BackFrom(back.Query()))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.signIn.Keep(h); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileByThisComputersAccounts(t *testing.T) {
	f := setup(t)

	// Signed out: a linked account can't be asked, and says why.
	if _, err := f.books.Numbers(t.Context(), "checking-a"); err == nil || !strings.Contains(err.Error(), "isn't signed in") {
		t.Errorf("signed out: %v", err)
	}

	f.login(t)

	n, err := f.books.Numbers(t.Context(), "checking-a")
	if err != nil || n.Highest != 1050 || !slices.Equal(n.Last, []int{1050, 1049}) {
		t.Errorf("numbers: %+v, %v", n, err)
	}

	if _, err := f.books.Numbers(t.Context(), "checking-b"); !errors.Is(err, checkbus.ErrNotLinked) {
		t.Errorf("an account not linked: %v", err)
	}

	f.srv.AddCheck("acc1", reconciletest.Check{Number: "1043", Amount: "120.00", Payee: "Payee B", WrittenOn: "2026-11-18"})
	f.srv.AddCheck("acc1", reconciletest.Check{Number: "1044", Amount: "50.00", Payee: "Payee B", WrittenOn: "2026-11-19", State: "void"})
	f.srv.Pay("acc1", "1045", "75.00", "2026-11-03")

	for n, want := range map[int]checkbus.InBooks{
		1043: {State: checkbus.BooksSent, Payee: "Payee B", Amount: 120_00, WrittenOn: time.Date(2026, 11, 18, 0, 0, 0, 0, time.Local)},
		1044: {State: checkbus.BooksVoid, Payee: "Payee B", Amount: 50_00, WrittenOn: time.Date(2026, 11, 19, 0, 0, 0, 0, time.Local)},
		1045: {State: checkbus.BooksCleared, Paid: []checkbus.Payment{{Amount: 75_00, On: time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local)}}},
		1046: {State: checkbus.BooksFree},
	} {
		got, err := f.books.Number(t.Context(), "checking-a", n)
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}

		if got.State != want.State || got.Payee != want.Payee || got.Amount != want.Amount || !got.WrittenOn.Equal(want.WrittenOn) ||
			len(got.Paid) != len(want.Paid) || (len(want.Paid) > 0 && (got.Paid[0].Amount != want.Paid[0].Amount || !got.Paid[0].On.Equal(want.Paid[0].On))) {
			t.Errorf("%d: got %+v, want %+v", n, got, want)
		}
	}

	f.srv.SetPayees("acc1", "Payee A Plumbing", "Payee B Hardware")

	if names, err := f.books.Payees(t.Context(), "checking-a", "hard"); err != nil || !slices.Equal(names, []string{"Payee B Hardware"}) {
		t.Errorf("payees: %q, %v", names, err)
	}

	// Reconcile being deployed: an error the register words, not a free number.
	f.srv.Down(true)

	if _, err := f.books.Number(t.Context(), "checking-a", 1046); err == nil || !strings.Contains(err.Error(), "can't be reached") {
		t.Errorf("Reconcile down: %v", err)
	}

	f.srv.Down(false)

	// The account gone from the key: never a free number.
	f.srv.SetAccounts()

	if _, err := f.books.Number(t.Context(), "checking-a", 1046); !errors.Is(err, booksbus.ErrGone) {
		t.Errorf("the account gone: %v", err)
	}
}
