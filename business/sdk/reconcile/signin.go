package reconcile

// Signing in, as a native program (docs/08-signing-in.md): the
// authorization code flow with PKCE (RFC 7636), the browser sent back to the
// program's own server on a loopback address (RFC 8252), and the issuer
// checked on the way back (RFC 9207). Hand-written, in the style of drop-in
// forms' Google client: three requests are less to read than a library.

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Metadata is what Reconcile says about its sign-in (RFC 8414).
type Metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

// Metadata asks Reconcile for its endpoints, and checks they are its own.
func (c *Client) Metadata(ctx context.Context) (Metadata, error) {
	var m Metadata

	if err := c.get(ctx, "", MetadataPath, &m); err != nil {
		return m, err
	}

	// RFC 8414 3.3: the issuer is the address asked, or the document is
	// somebody else's. And the endpoints are on the same site, since the
	// browser is sent to one with the challenge and the program sends the
	// verifier to the other.
	switch {
	case strings.TrimSuffix(m.Issuer, "/") != c.base:
		return m, fmt.Errorf("the site at %s says it is %q, so it isn't the Reconcile this program signs in to", c.base, m.Issuer)
	case !c.ours(m.AuthorizationEndpoint) || !c.ours(m.TokenEndpoint):
		return m, fmt.Errorf("the site at %s sends its sign-in somewhere else, so this program won't use it", c.base)
	case !slices.Contains(m.CodeChallengeMethods, "S256"):
		return m, fmt.Errorf("the site at %s doesn't offer the sign-in this program needs (PKCE with S256)", c.base)
	}

	return m, nil
}

func (c *Client) ours(endpoint string) bool {
	return endpoint == c.base || strings.HasPrefix(endpoint, c.base+"/")
}

// Pending is a sign-in begun: what the program keeps, in memory and nowhere
// else, until the browser comes back.
type Pending struct {
	State    string // sent and expected back, so that a code is only taken for a sign-in this program began
	Verifier string // the PKCE secret, which never leaves this computer but to the token endpoint
	Redirect string // where the browser comes back, sent again with the code as RFC 6749 asks
	Device   string // the computer's name, which the key will be named for
	Meta     Metadata
}

// Begin starts a sign-in: the address to send the browser to, and what to
// keep until it comes back to redirect. device is this computer's name,
// which Reconcile names the key for and lets the person change.
func (c *Client) Begin(ctx context.Context, redirect, device string) (Pending, string, error) {
	m, err := c.Metadata(ctx)
	if err != nil {
		return Pending{}, "", err
	}

	p := Pending{State: random(), Verifier: random(), Redirect: redirect, Device: device, Meta: m}

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {redirect},
		"state":                 {p.State},
		"code_challenge":        {challenge(p.Verifier)},
		"code_challenge_method": {"S256"},
		"device_name":           {device},
	}

	to := m.AuthorizationEndpoint
	if strings.Contains(to, "?") {
		to += "&"
	} else {
		to += "?"
	}

	return p, to + q.Encode(), nil
}

// Token is the key Reconcile gave, and when it ends.
type Token struct {
	Key     string
	Expires time.Time
	Scope   string
}

// ErrDenied is the person saying no on Reconcile's page.
var ErrDenied = errors.New("the sign-in was turned down on Reconcile's page, so this computer isn't signed in")

// Back is what the browser brought back to the redirect: a code, or an
// error, with the state and the issuer.
type Back struct {
	Code, State, Issuer string
	Error, Description  string
}

// BackFrom reads the query the browser came back with.
func BackFrom(q url.Values) Back {
	return Back{
		Code: q.Get("code"), State: q.Get("state"), Issuer: q.Get("iss"),
		Error: q.Get("error"), Description: q.Get("error_description"),
	}
}

// Finish trades the code the browser came back with for the key. The caller
// has found p by b.State; Finish checks the rest.
func (c *Client) Finish(ctx context.Context, now time.Time, p Pending, b Back) (Token, error) {
	// RFC 9207: the issuer, on every answer, errors included, so that an
	// answer from another site -- sent here by a page that knew our state
	// and our port -- is never taken for Reconcile's.
	if b.Issuer != p.Meta.Issuer {
		return Token{}, errors.New("the sign-in came back from a site that isn't this Reconcile, so it wasn't used")
	}

	switch {
	case b.Error == "access_denied":
		return Token{}, ErrDenied
	case b.Error != "":
		return Token{}, fmt.Errorf("this computer wasn't signed in: %s", cmp.Or(b.Description, b.Error))
	case b.Code == "":
		return Token{}, errors.New("the browser came back from Reconcile without a code, so this computer isn't signed in")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {b.Code},
		"client_id":     {ClientID},
		"redirect_uri":  {p.Redirect},
		"code_verifier": {p.Verifier},
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}

	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var t struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
		Scope       string `json:"scope"`

		// The token endpoint refuses as RFC 6749 5.2 says, not as the API
		// does.
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}

	if err := c.do(r, "", &t); err != nil {
		if ref, ok := errors.AsType[Refusal](err); ok {
			_ = json.Unmarshal(ref.Body, &t)
			return Token{}, fmt.Errorf("no key was given for this computer: %s", cmp.Or(t.Description, t.Error, ref.Error()))
		}

		return Token{}, err
	}

	switch {
	case !strings.EqualFold(t.TokenType, "Bearer") || t.AccessToken == "":
		return Token{}, errors.New("the answer from Reconcile held no key this program can use")
	case !slices.Contains(strings.Fields(t.Scope), "checks"):
		return Token{}, fmt.Errorf("the key from Reconcile may not send checks (it may: %s)", cmp.Or(t.Scope, "nothing"))
	}

	tok := Token{Key: t.AccessToken, Scope: t.Scope}
	if t.ExpiresIn > 0 {
		tok.Expires = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}

	return tok, nil
}

// random is 32 random bytes, as 43 characters: a PKCE verifier (RFC 7636
// asks 43 to 128) or a state no one can guess.
func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails, per its documentation.

	return base64.RawURLEncoding.EncodeToString(b)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}
