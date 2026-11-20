package signinbus_test

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/signin/signinbus"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile/reconciletest"
	"github.com/jroedel/reconcile-checks/foundation/secretstore"
)

const redirect = "http://127.0.0.1:7461/callback"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type fixture struct {
	srv   *reconciletest.Server
	store secretstore.Store
	clock *clock
	bus   *signinbus.Business
}

func setup(t *testing.T) *fixture {
	t.Helper()

	srv := reconciletest.New(
		reconciletest.Account{ID: "acc1", Name: "Checking A", Last4: "1234", Currency: "USD"},
		reconciletest.Account{ID: "acc2", Name: "Cuenta", Last4: "9876", Currency: "MXN"},
	)
	t.Cleanup(srv.Close)

	c, err := reconcile.New(srv.URL, "v1")
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{srv: srv, store: secretstore.File(filepath.Join(t.TempDir(), "key")), clock: &clock{time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}}
	f.bus = f.make(c)

	return f
}

func (f *fixture) make(c *reconcile.Client) *signinbus.Business {
	return signinbus.New(signinbus.Config{Client: c, Store: f.store, Device: "Office", Now: f.clock.now})
}

// browse follows the browser to Reconcile and back, and is the query it
// brought back.
func browse(t *testing.T, to string) url.Values {
	t.Helper()

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	return loc.Query()
}

func (f *fixture) signIn(t *testing.T) {
	t.Helper()

	to, err := f.bus.Begin(t.Context(), redirect)
	if err != nil {
		t.Fatal(err)
	}

	h, err := f.bus.Back(t.Context(), reconcile.BackFrom(browse(t, to)))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.bus.Keep(h); err != nil {
		t.Fatal(err)
	}
}

func TestSigningInKeepsTheKeyAndReachesTheDollarAccounts(t *testing.T) {
	f := setup(t)

	if st := f.bus.Status(); !st.Available || st.SignedIn {
		t.Fatalf("before signing in: %+v", st)
	}

	if _, err := f.bus.Accounts(t.Context()); !errors.Is(err, signinbus.ErrSignedOut) {
		t.Fatalf("accounts before signing in: %v", err)
	}

	f.signIn(t)

	st := f.bus.Status()
	if !st.SignedIn || st.Device != "Office" || !st.Expires.Equal(f.clock.t.Add(reconciletest.KeyLife)) || st.EndsSoon {
		t.Fatalf("signed in: %+v", st)
	}

	accts, err := f.bus.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(accts) != 1 || accts[0].ID != "acc1" {
		t.Errorf("got %+v, want only the account in dollars", accts)
	}

	// The program started again reads the same key.
	c, _ := reconcile.New(f.srv.URL, "v1")
	if st := f.make(c).Status(); !st.SignedIn {
		t.Errorf("after a restart: %+v", st)
	}

	// For another Reconcile, it is no key.
	other := reconciletest.New()
	defer other.Close()

	c, _ = reconcile.New(other.URL, "v1")
	if st := f.make(c).Status(); st.SignedIn {
		t.Errorf("a key for one Reconcile was taken for another's: %+v", st)
	}

	if err := f.bus.SignOut(); err != nil {
		t.Fatal(err)
	}

	if st := f.bus.Status(); st.SignedIn {
		t.Errorf("after signing out: %+v", st)
	}

	if _, err := f.store.Get(); !errors.Is(err, secretstore.ErrNone) {
		t.Errorf("the key is still kept after signing out: %v", err)
	}
}

func TestAKeyThatEndsOrIsRevokedAsksToSignInAgain(t *testing.T) {
	f := setup(t)
	f.signIn(t)

	f.clock.t = f.clock.t.Add(reconciletest.KeyLife - signinbus.EndsSoon + time.Hour)
	if st := f.bus.Status(); !st.SignedIn || !st.EndsSoon {
		t.Errorf("two weeks before it ends: %+v", st)
	}

	f.clock.t = f.clock.t.Add(signinbus.EndsSoon)
	if st := f.bus.Status(); st.SignedIn || st.Again == "" {
		t.Errorf("once it ended: %+v", st)
	}

	f.clock.t = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	f.srv.RevokeAll()

	if _, err := f.bus.Accounts(t.Context()); !errors.Is(err, reconcile.ErrSignedOut) {
		t.Fatalf("with the key revoked: %v", err)
	}

	if st := f.bus.Status(); st.SignedIn || st.Again == "" {
		t.Errorf("after a 401: %+v", st)
	}

	// Signing in again clears it.
	f.signIn(t)

	if st := f.bus.Status(); !st.SignedIn || st.Again != "" {
		t.Errorf("signed in again: %+v", st)
	}
}

func TestOnlyASignInThisProgramBeganIsTaken(t *testing.T) {
	f := setup(t)

	to, err := f.bus.Begin(t.Context(), redirect)
	if err != nil {
		t.Fatal(err)
	}

	q := browse(t, to)

	forged := reconcile.BackFrom(q)
	forged.State = "a-state-nobody-made"

	if _, err := f.bus.Back(t.Context(), forged); !errors.Is(err, signinbus.ErrUnknown) {
		t.Errorf("a state this program didn't make: %v", err)
	}

	h, err := f.bus.Back(t.Context(), reconcile.BackFrom(q))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.bus.Back(t.Context(), reconcile.BackFrom(q)); !errors.Is(err, signinbus.ErrUnknown) {
		t.Errorf("the same state twice: %v", err)
	}

	if err := f.bus.Keep("a-handoff-nobody-made"); !errors.Is(err, signinbus.ErrUnknown) {
		t.Errorf("a handoff nobody made: %v", err)
	}

	f.clock.t = f.clock.t.Add(10 * time.Minute)

	if err := f.bus.Keep(h); !errors.Is(err, signinbus.ErrUnknown) {
		t.Errorf("a handoff kept ten minutes later: %v", err)
	}

	// A sign-in begun more than half an hour ago is gone.
	to, _ = f.bus.Begin(t.Context(), redirect)
	q = browse(t, to)
	f.clock.t = f.clock.t.Add(31 * time.Minute)

	if _, err := f.bus.Back(t.Context(), reconcile.BackFrom(q)); !errors.Is(err, signinbus.ErrUnknown) {
		t.Errorf("a sign-in back after 31 minutes: %v", err)
	}
}

func TestWithNoReconcileThereIsNothingToSignInTo(t *testing.T) {
	b := signinbus.New(signinbus.Config{Store: secretstore.File(filepath.Join(t.TempDir(), "key"))})

	if st := b.Status(); st.Available {
		t.Errorf("got %+v", st)
	}

	if _, err := b.Begin(t.Context(), redirect); !errors.Is(err, signinbus.ErrNoReconcile) {
		t.Errorf("got %v", err)
	}
}

func TestTheComputersName(t *testing.T) {
	for give, want := range map[string]string{
		"office":          "office",
		"Office-PC.local": "Office-PC",
		"":                "this computer",
		"  ":              "this computer",
		"a-very-long-host-name-that-goes-on-and-on": "a-very-long-host-name-that-goes-on-and-o",
	} {
		if got := signinbus.DeviceName(give); got != want {
			t.Errorf("%q: got %q, want %q", give, got, want)
		}
	}
}
