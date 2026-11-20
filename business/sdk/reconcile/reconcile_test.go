package reconcile_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile/reconciletest"
)

const redirect = "http://127.0.0.1:7461/callback"

// signIn follows the browser's round trip: to the authorize address, and
// back to the redirect, whose query it returns.
func signIn(t *testing.T, c *reconcile.Client) (reconcile.Pending, url.Values) {
	t.Helper()

	p, to, err := c.Begin(t.Context(), redirect, "Office")
	if err != nil {
		t.Fatal(err)
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), redirect+"?") {
		t.Fatalf("the sign-in page sent the browser to %q (%d), not back to the program", resp.Header.Get("Location"), resp.StatusCode)
	}

	return p, loc.Query()
}

func TestSigningInGivesAKeyThatReachesTheAccounts(t *testing.T) {
	srv := reconciletest.New(reconciletest.Account{ID: "acc1", Name: "Checking A", Last4: "1234", Currency: "USD", Highest: "1042", LastNumbers: []string{"1042", "1041"}})
	defer srv.Close()

	c, err := reconcile.New(srv.URL+"/", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

	p, q := signIn(t, c)
	if q.Get("state") != p.State {
		t.Fatalf("the state came back as %q, want %q", q.Get("state"), p.State)
	}

	tok, err := c.Finish(t.Context(), now, p, reconcile.BackFrom(q))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(tok.Key, "rcn_") || !tok.Expires.Equal(now.Add(reconciletest.KeyLife)) {
		t.Errorf("got key %.8s… until %v", tok.Key, tok.Expires)
	}

	if d := srv.Devices(); len(d) != 1 || d[0] != "Office" {
		t.Errorf("the device names sent were %q", d)
	}

	accts, err := c.Accounts(t.Context(), tok.Key)
	if err != nil {
		t.Fatal(err)
	}

	if len(accts) != 1 || accts[0].ID != "acc1" || accts[0].Highest != "1042" || len(accts[0].LastNumbers) != 2 {
		t.Errorf("got %+v", accts)
	}

	// A code is good once.
	if _, err := c.Finish(t.Context(), now, p, reconcile.BackFrom(q)); err == nil {
		t.Error("the same code was traded twice")
	}

	srv.RevokeAll()

	if _, err := c.Accounts(t.Context(), tok.Key); !errors.Is(err, reconcile.ErrSignedOut) {
		t.Errorf("with the key revoked: %v, want ErrSignedOut", err)
	}
}

func TestASignInIsRefusedWhenItIsntReconciles(t *testing.T) {
	srv := reconciletest.New()
	defer srv.Close()

	c, _ := reconcile.New(srv.URL, "v1.2.3")

	t.Run("an answer from another issuer", func(t *testing.T) {
		p, q := signIn(t, c)
		q.Set("iss", "https://evil.example")

		if _, err := c.Finish(t.Context(), time.Now(), p, reconcile.BackFrom(q)); err == nil {
			t.Error("a code from another issuer was traded")
		}
	})

	t.Run("a code with the wrong verifier", func(t *testing.T) {
		p, q := signIn(t, c)
		p.Verifier = "another-verifier-another-verifier-another-v"

		if _, err := c.Finish(t.Context(), time.Now(), p, reconcile.BackFrom(q)); err == nil || !strings.Contains(err.Error(), "not valid") {
			t.Errorf("got %v, want Reconcile's invalid_grant sentence", err)
		}
	})

	t.Run("the person saying no", func(t *testing.T) {
		srv.Deny(true)
		defer srv.Deny(false)

		p, q := signIn(t, c)

		if _, err := c.Finish(t.Context(), time.Now(), p, reconcile.BackFrom(q)); !errors.Is(err, reconcile.ErrDenied) {
			t.Errorf("got %v, want ErrDenied", err)
		}
	})

	t.Run("a site calling itself something else", func(t *testing.T) {
		srv.Claim("https://books.example")
		defer srv.Claim("")

		if _, _, err := c.Begin(t.Context(), redirect, "Office"); err == nil {
			t.Error("began a sign-in with a site whose metadata names another issuer")
		}
	})
}

func TestOnlyAnHTTPSAddressOrThisComputersIsReconciles(t *testing.T) {
	for give, ok := range map[string]bool{
		"https://books.example":  true,
		"https://books.example/": true,
		"http://127.0.0.1:8461":  true,
		"http://localhost:8461":  true,
		"http://books.example":   false,
		"books.example":          false,
		"":                       false,
	} {
		if _, err := reconcile.New(give, "v1"); (err == nil) != ok {
			t.Errorf("%q: got %v", give, err)
		}
	}
}

func TestEachAnswerIsTheErrorItShouldBe(t *testing.T) {
	status, body := 0, ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, _ := reconcile.New(srv.URL, "v1")

	status, body = http.StatusForbidden, `{"error":{"field":"","problem":"Your role on this account doesn't let you add checks."}}`
	_, err := c.Accounts(t.Context(), "rcn_x")

	ref, ok := errors.AsType[reconcile.Refusal](err)
	if !ok || ref.Status != 403 || ref.Error() != "Your role on this account doesn't let you add checks." {
		t.Errorf("a 403: got %#v", err)
	}

	status, body = http.StatusConflict, `{"error":{"field":"number","problem":"Check 1043 is already Payee B's."},"code":"number_used","check":{"number":"1043"}}`
	_, err = c.Accounts(t.Context(), "rcn_x")

	if ref, ok := errors.AsType[reconcile.Refusal](err); !ok || ref.Code != "number_used" || ref.Field != "number" || !strings.Contains(string(ref.Body), `"check"`) {
		t.Errorf("a 409 with a code: got %#v", err)
	}

	status, body = http.StatusBadGateway, "<html>proxy</html>"
	_, err = c.Accounts(t.Context(), "rcn_x")

	if _, ok := errors.AsType[reconcile.Unreachable](err); !ok {
		t.Errorf("a 502: got %#v, want Unreachable", err)
	}

	srv.Close()
	_, err = c.Accounts(t.Context(), "rcn_x")

	if _, ok := errors.AsType[reconcile.Unreachable](err); !ok {
		t.Errorf("nothing listening: got %#v, want Unreachable", err)
	}
}
