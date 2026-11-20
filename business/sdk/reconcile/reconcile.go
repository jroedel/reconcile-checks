// Package reconcile is the client for Reconcile: its sign-in, as a native
// program (docs/08-signing-in.md), and the routes of its checks API
// (docs/09-reconcile-api.md), which is reconcile#166 as built.
//
// It is a client and nothing more: it knows the shape of each request and
// answer, and turns each refusal into an error the program can tell apart.
// What the program does about each one -- keep a check to send again, ask
// for the number on the paper -- is decided above it. It holds no key; each
// call is given one.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID is how Reconcile knows this program: a client of its own, whose
// key may only do what printing checks needs (Reconcile's oauthapp,
// builtIns), and whose redirects are a loopback address on any port.
const ClientID = "reconcile-checks"

// MetadataPath is where Reconcile describes its sign-in (RFC 8414), so that
// the program finds the endpoints there rather than having them written in.
const MetadataPath = "/.well-known/oauth-authorization-server"

// Client talks to one Reconcile.
type Client struct {
	base string
	http *http.Client
	ua   string
}

// New is a client for the Reconcile at base, such as https://books.example,
// saying it is this version of the program.
func New(base, version string) (*Client, error) {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")

	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		// http is for a Reconcile running on this computer, in development.
		return nil, fmt.Errorf("%q is not the address of a Reconcile: it starts https://", base)
	}

	return &Client{
		base: base,
		ua:   "reconcile-checks/" + version,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A redirect from an API route is a misconfigured site, not an
			// answer, and following one would carry the key with it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Base is the Reconcile this talks to.
func (c *Client) Base() string { return c.base }

// KeysPage is Reconcile's page listing a person's keys, where this
// computer's can be revoked.
func (c *Client) KeysPage() string { return c.base + "/account/keys" }

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// --- what can go wrong --------------------------------------------------------

// ErrSignedOut is a 401: the key ended, was revoked, or is not Reconcile's.
var ErrSignedOut = errors.New("this computer's key is no longer accepted by Reconcile; sign in to Reconcile again")

// Unreachable is Reconcile not answering, or answering that it can't: the
// network, a timeout, a 5xx. Whatever was being done can be done again
// later.
type Unreachable struct{ Err error }

func (e Unreachable) Error() string {
	return "Reconcile can't be reached just now (" + e.Err.Error() + ")"
}

func (e Unreachable) Unwrap() error { return e.Err }

// Refusal is Reconcile saying no: {"error": {"field", "problem"}}, with a
// code beside it when the program must tell the refusal apart
// (docs/09-reconcile-api.md). Problem is Reconcile's sentence, shown as it
// wrote it.
type Refusal struct {
	Status  int
	Code    string
	Field   string
	Problem string

	// Body is the whole answer, for the refusals that carry what the
	// program needs to act on (the check that has the number, the amount
	// the bank paid).
	Body json.RawMessage
}

func (e Refusal) Error() string {
	if e.Problem != "" {
		return e.Problem
	}

	return fmt.Sprintf("Reconcile refused it (%d %s)", e.Status, http.StatusText(e.Status))
}

// --- the calls ------------------------------------------------------------------

// get asks path with key, and reads the answer into out.
func (c *Client) get(ctx context.Context, key, path string, out any) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}

	return c.do(r, key, out)
}

func (c *Client) do(r *http.Request, key string, out any) error {
	r.Header.Set("User-Agent", c.ua)
	r.Header.Set("Accept", "application/json")

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.http.Do(r)
	if err != nil {
		return Unreachable{Err: plain(err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return Unreachable{Err: err}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized && key != "":
		return ErrSignedOut
	case resp.StatusCode >= 500:
		return Unreachable{Err: fmt.Errorf("it answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	case resp.StatusCode >= 300:
		return refusal(resp.StatusCode, body)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("the answer from Reconcile to %s couldn't be read: %w", r.URL.Path, err)
	}

	return nil
}

// maxAnswer is the most of an answer read. The largest is a list of
// checks; a few megabytes is many years of them.
const maxAnswer = 8 << 20

func refusal(status int, body []byte) Refusal {
	var r struct {
		Error struct {
			Field   string `json:"field"`
			Problem string `json:"problem"`
		} `json:"error"`
		Code string `json:"code"`
	}

	_ = json.Unmarshal(body, &r) // a refusal that isn't JSON is still a refusal

	return Refusal{Status: status, Code: r.Code, Field: r.Error.Field, Problem: r.Error.Problem, Body: body}
}

// plain is a network error without the URL Go's client puts in front of it,
// which says nothing a person can use.
func plain(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		if ue.Timeout() {
			return errors.New("it didn't answer in time")
		}

		return ue.Err
	}

	return err
}

// Account is a checking account the key reaches, as GET
// /api/v1/checks/accounts lists it.
type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Last4        string `json:"last4"`
	Currency     string `json:"currency"`
	Organization string `json:"organization"` // "" for an account on its own

	// Highest is the highest check number the account has used, from its
	// check images and the bank's statements both; "" for none.
	// LastNumbers is the five highest, highest first.
	Highest     string   `json:"highest"`
	LastNumbers []string `json:"last_numbers"`
}

// Accounts is every checking account the key reaches: in use, and where the
// person may attach receipts.
func (c *Client) Accounts(ctx context.Context, key string) ([]Account, error) {
	var out struct {
		Accounts []Account `json:"accounts"`
	}

	if err := c.get(ctx, key, "/api/v1/checks/accounts", &out); err != nil {
		return nil, err
	}

	return out.Accounts, nil
}
