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
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jroedel/reconcile-checks/business/types/money"
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

// Check is a check it holds, as Reconcile answers one.
type Check struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	Amount     string `json:"amount"`
	Payee      string `json:"payee"`
	WrittenOn  string `json:"written_on"`
	Memo       string `json:"memo"`
	State      string `json:"state"`
	AttachedTo []Link `json:"attached_to"`
	URL        string `json:"url"`
	Image      []byte `json:"-"`
	Key        string `json:"-"` // which key sent it
}

// Link is a transaction a check is attached to.
type Link struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Paid is the bank paying a number with no check.
type Paid struct {
	Amount string `json:"amount"`
	Date   string `json:"date"`
}

// book is what it holds about one account.
type book struct {
	checks map[string]*Check // by number
	paid   map[string][]Paid // by number
	payees []string
}

// Server is the fake.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	books    map[string]*book // by account id
	down     bool             // answering 503 to the API, as Reconcile being deployed
	next     *answer          // what the next send answers, when a test says
	sends    int              // how many sends reached it
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
	s := &Server{accounts: accounts, grants: map[string]grant{}, keys: map[string]string{}, books: map[string]*book{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+MetadataPath, s.metadata)
	mux.HandleFunc("GET "+AuthorizePath, s.authorize)
	mux.HandleFunc("POST "+TokenPath, s.token)
	mux.HandleFunc("GET /api/v1/checks/accounts", s.authed(s.listAccounts))
	mux.HandleFunc("GET /api/v1/accounts/{account}/checks/{number}", s.authed(s.reaches(s.number)))
	mux.HandleFunc("GET /api/v1/accounts/{account}/payees", s.authed(s.reaches(s.listPayees)))
	mux.HandleFunc("POST /api/v1/accounts/{account}/checks", s.authed(s.reaches(s.send)))
	mux.HandleFunc("POST /api/v1/accounts/{account}/checks/{number}/void", s.authed(s.reaches(s.void)))

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

// book is the account's, made when first asked for. With mu held.
func (s *Server) book(account string) *book {
	b, ok := s.books[account]
	if !ok {
		b = &book{checks: map[string]*Check{}, paid: map[string][]Paid{}}
		s.books[account] = b
	}

	return b
}

// AddCheck puts a check on an account, as if sent from another computer.
func (s *Server) AddCheck(account string, c Check) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c.State == "" {
		c.State = "outstanding"
	}

	if c.ID == "" {
		c.ID = random()
	}

	s.book(account).checks[c.Number] = &c
}

// Pay has the bank pay a number on an account with no check: a check written
// by hand from the same book.
func (s *Server) Pay(account, number, amount, date string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.book(account)
	b.paid[number] = append(b.paid[number], Paid{Amount: amount, Date: date})
}

// SetPayees is an account's payees.
func (s *Server) SetPayees(account string, names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.book(account).payees = names
}

// Down makes the API answer 503, as Reconcile being deployed does.
func (s *Server) Down(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// answer is a refusal a test has the next send answer with.
type answer struct {
	status               int
	code, field, problem string
}

// RefuseNextSend has the next send refused, as Reconcile refuses a role that
// may not add checks (403), an account archived (422 not_checking), and so
// on.
func (s *Server) RefuseNextSend(status int, code, field, problem string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = &answer{status, code, field, problem}
}

// Checks is the account's checks, by number.
func (s *Server) Checks(account string) map[string]Check {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]Check{}
	for n, c := range s.book(account).checks {
		out[n] = *c
	}

	return out
}

// Sends is how many sends have reached it, refused ones included.
func (s *Server) Sends() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sends
}

// Attach has the statement attach the account's check: it has cleared.
func (s *Server) Attach(account, number string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c := s.book(account).checks[number]; c != nil {
		c.State = "attached"
		c.AttachedTo = []Link{{ID: "tx-" + number, URL: s.URL + "/transactions/tx-" + number}}
	}
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

		s.mu.Lock()
		down := s.down
		s.mu.Unlock()

		if down {
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		h(w, r)
	}
}

// reaches answers 404, without a code, for an account the key doesn't
// reach: as Reconcile does for both "no such account" and "not yours".
func (s *Server) reaches(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		ok := slices.ContainsFunc(s.accounts, func(a Account) bool { return a.ID == r.PathValue("account") })
		s.mu.Unlock()

		if !ok {
			Refuse(w, http.StatusNotFound, "", "account", "There is no such account, or it is not one this key reaches. GET /api/v1/checks/accounts lists those it does.")
			return
		}

		h(w, r)
	}
}

// number reads a check number as Reconcile does: digits, without leading
// zeros, at most ten.
func number(s string) (string, bool) {
	s = strings.TrimLeft(strings.TrimSpace(s), "0")
	if s == "" || len(s) > 10 || strings.Trim(s, "0123456789") != "" {
		return "", false
	}

	return s, true
}

func (s *Server) number(w http.ResponseWriter, r *http.Request) {
	n, ok := number(r.PathValue("number"))
	if !ok {
		Refuse(w, http.StatusUnprocessableEntity, "", "number", "The number will not do: a check number is digits, at most ten.")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.book(r.PathValue("account"))
	out := map[string]any{"number": n, "state": "free"}

	switch c, paid := b.checks[n], b.paid[n]; {
	case c != nil:
		out["state"], out["check"] = c.State, c.view()
	case len(paid) > 0:
		out["state"], out["cleared"] = "cleared", paid
	}

	write(w, http.StatusOK, out)
}

// send is POST .../checks: the check's details in the query, its image as
// the body (Reconcile's apiapp/checks.go, sendCheck, and receiptbus's
// SendCheck).
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	s.mu.Lock()
	s.sends++
	next := s.next
	s.next = nil
	s.mu.Unlock()

	if next != nil {
		Refuse(w, next.status, next.code, next.field, next.problem)
		return
	}

	amount, err := money.Parse(q.Get("amount"))

	switch {
	case q.Get("name") == "":
		Refuse(w, http.StatusBadRequest, "", "name", "Give the image file's name as ?name=, such as check-1005.pdf.")
		return
	case err != nil || amount <= 0:
		Refuse(w, http.StatusBadRequest, "", "amount", "Give the check's amount as a number without a sign, such as \"12.34\".")
		return
	case !isDate(q.Get("written_on")):
		Refuse(w, http.StatusBadRequest, "", "written_on", "Give the date printed on the check as YYYY-MM-DD.")
		return
	case strings.TrimSpace(q.Get("payee")) == "":
		Refuse(w, http.StatusBadRequest, "", "payee", "Give whom the check is to, as printed on it.")
		return
	}

	n, ok := number(q.Get("number"))
	if !ok {
		Refuse(w, http.StatusUnprocessableEntity, "", "number", "The number will not do: a check number is digits, at most ten.")
		return
	}

	image, err := io.ReadAll(io.LimitReader(r.Body, 20<<20+1))

	switch {
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case len(image) > 20<<20:
		Refuse(w, http.StatusRequestEntityTooLarge, "", "", "That image is larger than a check's may be, 20 MB. Nothing was kept.")
		return
	case !bytes.HasPrefix(image, []byte("\x89PNG\r\n\x1a\n")):
		Refuse(w, http.StatusUnsupportedMediaType, "", "", "A check's image is a PNG, JPEG, WebP or HEIC picture, or a PDF, and this file is none of them, by its content. Nothing was kept.")
		return
	}

	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.book(r.PathValue("account"))
	sent := Check{
		ID: random(), Number: n, Amount: figures(amount), Payee: q.Get("payee"), WrittenOn: q.Get("written_on"),
		Memo: q.Get("memo"), State: "outstanding", Image: image, Key: key,
	}
	sent.URL = s.URL + "/receipts/" + sent.ID

	// A void check is skipped when looking for a repeat (receiptbus): the
	// same check sent after its void is a new check.
	if have := b.checks[n]; have != nil && have.State != "void" {
		if same(have, sent) {
			write(w, http.StatusOK, map[string]any{"outcome": "already_there", "check": have.view()})
			return
		}

		body := map[string]any{
			"error": map[string]string{"field": "number", "problem": "Check " + n + " in this account says something else: another amount, payee or day. Nothing was kept. If it is the same check, send what it says; if the paper is numbered wrong, void it."},
			"code":  "number_used", "check": have.view(),
		}
		write(w, http.StatusConflict, body)

		return
	}

	outcome := "outstanding"

	if paid := b.paid[n]; len(paid) > 0 {
		bank, _ := money.Parse(paid[0].Amount)
		if bank != amount {
			write(w, http.StatusConflict, map[string]any{
				"error": map[string]string{"field": "amount", "problem": "The bank paid check " + n + " for $" + figures(bank) + ", not $" + figures(amount) + ". Nothing was kept. Check which stock is in the printer, or the amount."},
				"code":  "amount_differs", "bank": figures(bank), "sent": figures(amount),
			})

			return
		}

		outcome = "attached"
		sent.State = "attached"
		sent.AttachedTo = []Link{{ID: "tx-" + n, URL: s.URL + "/transactions/tx-" + n}}
		delete(b.paid, n)
	}

	b.checks[n] = &sent

	write(w, http.StatusCreated, map[string]any{"outcome": outcome, "check": sent.view()})
}

// same is Reconcile's test for a repeat: the number, the amount, the payee
// but for case and spaces, and the day. Not the memo.
func same(a *Check, b Check) bool {
	fold := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

	x, _ := money.Parse(a.Amount)
	y, _ := money.Parse(b.Amount)

	return x == y && fold(a.Payee) == fold(b.Payee) && a.WrittenOn == b.WrittenOn
}

func (s *Server) void(w http.ResponseWriter, r *http.Request) {
	n, ok := number(r.PathValue("number"))
	if !ok {
		Refuse(w, http.StatusUnprocessableEntity, "", "number", "The number will not do: a check number is digits, at most ten.")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	c := s.book(r.PathValue("account")).checks[n]

	switch {
	case c == nil:
		Refuse(w, http.StatusNotFound, "no_check", "number", "No check in this account has that number. Nothing was voided.")
	case c.State == "attached":
		Refuse(w, http.StatusConflict, "cleared", "number", "That check has cleared: the bank paid it, and it is on its transaction. It cannot be voided.")
	default:
		c.State = "void"
		write(w, http.StatusOK, map[string]any{"check": c.view()})
	}
}

// figures is cents as Reconcile writes an amount: 1234.50.
func figures(c money.Cents) string {
	return strconv.FormatInt(int64(c)/100, 10) + "." + fmt.Sprintf("%02d", int64(c)%100)
}

func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func (c *Check) view() Check {
	v := *c
	if v.AttachedTo == nil {
		v.AttachedTo = []Link{}
	}

	return v
}

func (s *Server) listPayees(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	s.mu.Lock()
	all := s.book(r.PathValue("account")).payees
	s.mu.Unlock()

	out := []map[string]string{}
	for _, name := range all {
		if strings.Contains(strings.ToLower(name), q) && len(out) < 20 {
			out = append(out, map[string]string{"name": name})
		}
	}

	write(w, http.StatusOK, map[string]any{"payees": out})
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
