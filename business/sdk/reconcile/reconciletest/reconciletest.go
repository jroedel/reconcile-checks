// Package reconciletest is a Reconcile for tests: its sign-in and the checks
// routes, answering as Reconcile's code does (docs/09-reconcile-api.md, read
// against reconcile#167–#171), on an httptest server.
//
// Its sign-in page has no person behind it: a request that would be shown
// the page is agreed to at once, or turned down when Deny is set, so that a
// test can follow the browser's whole round trip with plain GETs. Everything
// a real Reconcile checks before that page -- the program, its loopback
// redirect, an S256 challenge -- it checks too, so that a client that would
// be refused there is refused here.
package reconciletest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The paths Reconcile serves its sign-in on (its oauthapp).
const (
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
	MetadataPath  = "/.well-known/oauth-authorization-server"
)

// KeyLife is how long a key it gives lasts: a year, as Reconcile's.
const KeyLife = 365 * 24 * time.Hour

// Account is a checking account, as Reconcile lists it.
type Account struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Last4        string   `json:"last4"`
	Currency     string   `json:"currency"`
	Organization string   `json:"organization"`
	Highest      string   `json:"highest"`
	LastNumbers  []string `json:"last_numbers"`
}

// Server is the fake.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	accounts []Account
	grants   map[string]grant  // code → what it was given for
	keys     map[string]string // key → the device it was made for
	deny     bool
	issuer   string // what it says it is, when a test wants it to lie
	devices  []string
}

type grant struct {
	challenge, redirect, device string
}

// New starts one with the accounts given.
func New(accounts ...Account) *Server {
	s := &Server{accounts: accounts, grants: map[string]grant{}, keys: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+MetadataPath, s.metadata)
	mux.HandleFunc("GET "+AuthorizePath, s.authorize)
	mux.HandleFunc("POST "+TokenPath, s.token)
	mux.HandleFunc("GET /api/v1/checks/accounts", s.authed(s.listAccounts))

	s.Server = httptest.NewServer(mux)

	return s
}

// Deny makes the sign-in page turn every request down, as a person saying
// no would.
func (s *Server) Deny(no bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = no
}

// Claim makes it call itself issuer, in its metadata and on the way back.
func (s *Server) Claim(issuer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issuer = issuer
}

// SetAccounts replaces the accounts the keys reach.
func (s *Server) SetAccounts(accounts ...Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts = accounts
}

// RevokeAll revokes every key, as the keys screen does one.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.keys)
}

// Devices is the device_name of each sign-in agreed to, in order.
func (s *Server) Devices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.devices...)
}

func (s *Server) me() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.issuer != "" {
		return s.issuer
	}

	return s.URL
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{
		"issuer":                                         s.me(),
		"authorization_endpoint":                         s.URL + AuthorizePath,
		"token_endpoint":                                 s.URL + TokenPath,
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// authorize is the page a person would agree on. A program or redirect it
// can't trust is answered here, never sent anywhere (RFC 6749 4.1.2.1).
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if q.Get("client_id") != "reconcile-checks" || !loopbackCallback(q.Get("redirect_uri")) {
		http.Error(w, "not a program or a redirect this site knows", http.StatusBadRequest)
		return
	}

	back := url.Values{"state": {q.Get("state")}, "iss": {s.me()}}

	s.mu.Lock()
	deny := s.deny
	s.mu.Unlock()

	switch {
	case q.Get("response_type") != "code":
		back.Set("error", "unsupported_response_type")
	case q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43:
		back.Set("error", "invalid_request")
		back.Set("error_description", "a PKCE code_challenge made with S256 is required")
	case deny:
		back.Set("error", "access_denied")
		back.Set("error_description", "the person said no")
	default:
		code := random()

		s.mu.Lock()
		s.grants[code] = grant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), device: q.Get("device_name")}
		s.devices = append(s.devices, q.Get("device_name"))
		s.mu.Unlock()

		back.Set("code", code)
	}

	http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusSeeOther)
}

// loopbackCallback is Reconcile's rule for this program's redirects:
// http://127.0.0.1/callback or http://[::1]/callback, on any port; not
// localhost.
func loopbackCallback(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" || u.Path != "/callback" || u.RawQuery != "" || u.User != nil {
		return false
	}

	ip := net.ParseIP(u.Hostname())

	return ip != nil && ip.IsLoopback()
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	bad := func(code, desc string) {
		write(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": desc})
	}

	if err := r.ParseForm(); err != nil {
		bad("invalid_request", "the body could not be read as a form")
		return
	}

	f := r.PostForm

	if f.Get("grant_type") != "authorization_code" {
		bad("unsupported_grant_type", "only authorization_code is offered")
		return
	}

	s.mu.Lock()
	g, ok := s.grants[f.Get("code")]
	delete(s.grants, f.Get("code")) // a code is good once
	s.mu.Unlock()

	sum := sha256.Sum256([]byte(f.Get("code_verifier")))

	if !ok || f.Get("client_id") != "reconcile-checks" || f.Get("redirect_uri") != g.redirect ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		bad("invalid_grant", "the code is not valid: it may have been used, have expired, or belong to another request")
		return
	}

	key := "rcn_" + random()

	s.mu.Lock()
	// Signing in again from the same computer replaces that computer's key.
	for k, d := range s.keys {
		if d == g.device {
			delete(s.keys, k)
		}
	}
	s.keys[key] = g.device
	s.mu.Unlock()

	write(w, http.StatusOK, map[string]any{
		"access_token": key, "token_type": "Bearer",
		"expires_in": int64(KeyLife / time.Second), "scope": "checks",
	})
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

		s.mu.Lock()
		_, known := s.keys[key]
		s.mu.Unlock()

		if !ok || !known {
			Refuse(w, http.StatusUnauthorized, "", "", "the key is not valid: it may have expired or been revoked")
			return
		}

		h(w, r)
	}
}

func (s *Server) listAccounts(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	list := append([]Account{}, s.accounts...)
	s.mu.Unlock()

	for i := range list {
		if list[i].LastNumbers == nil {
			list[i].LastNumbers = []string{}
		}
	}

	write(w, http.StatusOK, map[string]any{"accounts": list})
}

// Refuse answers as Reconcile refuses: {"error": {field, problem}}, and the
// code beside it when there is one.
func Refuse(w http.ResponseWriter, status int, code, field, problem string) {
	body := map[string]any{"error": map[string]string{"field": field, "problem": problem}}
	if code != "" {
		body["code"] = code
	}

	write(w, status, body)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)

	return base64.RawURLEncoding.EncodeToString(b)
}
