// Package signinbus is this computer signed in to Reconcile, or not: the
// sign-ins begun and not yet back, the key kept, and what the key reaches
// (docs/08-signing-in.md).
//
// The key is this computer's own. Each computer signs in for itself, with
// its own name, and signing in again replaces this computer's key and no
// other; so two computers have two keys, and signing in on one never signs
// the other out.
package signinbus

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/foundation/secretstore"
)

// Config is what the business needs.
type Config struct {
	// Client is the Reconcile signed in to; nil when there is none, which
	// is a development config that names no Reconcile.
	Client *reconcile.Client

	// Store keeps the key: the system's keychain or a file
	// (foundation/secretstore).
	Store secretstore.Store

	// Device is this computer's name, which the key is named for.
	Device string

	Log *slog.Logger
	Now func() time.Time
}

// Business is signing in.
type Business struct {
	cfg Config

	mu      sync.Mutex
	pending map[string]begun   // by state: sign-ins sent to Reconcile, not yet back
	handed  map[string]handoff // by handoff: keys back from Reconcile, not yet kept
	key     *Key               // the key kept, once read
	read    bool               // whether the store has been read
	refused bool               // Reconcile answered 401 to the key kept
}

type begun struct {
	p    reconcile.Pending
	when time.Time
}

type handoff struct {
	key  Key
	when time.Time
}

// Key is what is kept: the key, and what Settings says about it.
type Key struct {
	Key     string    `json:"key"`
	Expires time.Time `json:"expires,omitzero"`
	Device  string    `json:"device"`
	Site    string    `json:"site"` // the Reconcile it is for
	Since   time.Time `json:"since"`
}

// How long a sign-in may take: Reconcile's page asks for the emailed code,
// which can take a few minutes to arrive.
const (
	beginLife   = 30 * time.Minute
	handoffLife = 5 * time.Minute
	maxPending  = 8
)

// EndsSoon is how long before a key ends Settings starts saying so.
const EndsSoon = 14 * 24 * time.Hour

// New makes one.
func New(cfg Config) *Business {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}

	return &Business{cfg: cfg, pending: map[string]begun{}, handed: map[string]handoff{}}
}

// DeviceName is the name a key for this computer goes by: its host name,
// as Reconcile takes it (at most 40 characters), or "this computer".
func DeviceName(host string) string {
	host = strings.TrimSpace(host)
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i] // office.local is Office's computer, not .local's
	}

	if r := []rune(host); len(r) > 40 {
		host = string(r[:40])
	}

	if host == "" {
		return "this computer"
	}

	return host
}

// --- where it stands ----------------------------------------------------------

// Status is signing in, as Settings shows it.
type Status struct {
	// Available is whether there is a Reconcile to sign in to.
	Available bool
	Site      string
	KeysPage  string

	SignedIn bool
	Device   string
	Expires  time.Time // zero when Reconcile said no end
	EndsSoon bool

	// Again is why signing in again is needed, when it is: the key ended,
	// or Reconcile no longer takes it.
	Again string
}

// Status is where signing in stands.
func (b *Business) Status() Status {
	if b.cfg.Client == nil {
		return Status{}
	}

	st := Status{Available: true, Site: b.cfg.Client.Base(), KeysPage: b.cfg.Client.KeysPage()}

	k, err := b.kept()
	if err != nil {
		b.cfg.Log.Warn("the Reconcile key couldn't be read", "error", err)
		st.Again = "The key this computer kept couldn't be read, so it has to sign in again."

		return st
	}

	if k == nil {
		return st
	}

	b.mu.Lock()
	refused := b.refused
	b.mu.Unlock()

	now := b.cfg.Now()

	switch {
	case refused:
		st.Again = "Reconcile no longer accepts this computer's key: it ended, or was revoked on Reconcile's keys page."
	case !k.Expires.IsZero() && !now.Before(k.Expires):
		st.Again = "This computer's key ended on " + k.Expires.Local().Format("January 2, 2006") + "."
	default:
		st.SignedIn = true
		st.EndsSoon = !k.Expires.IsZero() && k.Expires.Sub(now) < EndsSoon
	}

	st.Device, st.Expires = k.Device, k.Expires

	return st
}

// kept is the key kept for this Reconcile, nil for none.
func (b *Business) kept() (*Key, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.read {
		return b.key, nil
	}

	s, err := b.cfg.Store.Get()
	if errors.Is(err, secretstore.ErrNone) {
		b.read = true
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var k Key
	if err := json.Unmarshal([]byte(s), &k); err != nil || k.Key == "" {
		return nil, errors.New("what is kept isn't a key this program wrote")
	}

	b.read = true

	// A key for another Reconcile -- the config changed which -- is no key
	// for this one.
	if k.Site == b.cfg.Client.Base() {
		b.key = &k
	}

	return b.key, nil
}

// --- signing in -----------------------------------------------------------------

// ErrNoReconcile is a config naming no Reconcile.
var ErrNoReconcile = errors.New("this program's settings name no Reconcile to sign in to")

// ErrUnknown is a sign-in this program didn't begin, or began too long ago.
var ErrUnknown = errors.New("this sign-in wasn't begun by this program, or took too long; start it again from Settings")

// Begin starts a sign-in whose browser comes back to redirect, and is the
// address to send the browser to.
func (b *Business) Begin(ctx context.Context, redirect string) (string, error) {
	if b.cfg.Client == nil {
		return "", ErrNoReconcile
	}

	p, to, err := b.cfg.Client.Begin(ctx, redirect, b.cfg.Device)
	if err != nil {
		return "", err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.forget()

	// A person pressing Sign in again and again leaves a sign-in behind
	// each time; the oldest go.
	for len(b.pending) >= maxPending {
		oldest := ""
		for s, x := range b.pending {
			if oldest == "" || x.when.Before(b.pending[oldest].when) {
				oldest = s
			}
		}
		delete(b.pending, oldest)
	}

	b.pending[p.State] = begun{p: p, when: b.cfg.Now()}

	return to, nil
}

// Back is the browser come back from Reconcile: it trades the code for the
// key, and holds the key under a handoff for Keep. The key isn't kept here,
// because the browser comes back without the page's session (its cookie
// stays behind on a navigation from another site), and nothing is kept for
// a request that hasn't shown it.
func (b *Business) Back(ctx context.Context, back reconcile.Back) (string, error) {
	if b.cfg.Client == nil {
		return "", ErrNoReconcile
	}

	b.mu.Lock()
	b.forget()
	x, ok := b.pending[back.State]
	delete(b.pending, back.State) // a state is good once, whatever happens next
	b.mu.Unlock()

	if !ok || back.State == "" {
		return "", ErrUnknown
	}

	now := b.cfg.Now()

	tok, err := b.cfg.Client.Finish(ctx, now, x.p, back)
	if err != nil {
		return "", err
	}

	h := random()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.handed[h] = handoff{
		key:  Key{Key: tok.Key, Expires: tok.Expires, Device: x.p.Device, Site: b.cfg.Client.Base(), Since: now},
		when: now,
	}

	return h, nil
}

// Keep keeps the key handed off under h, by a request with the session.
func (b *Business) Keep(h string) error {
	b.mu.Lock()
	b.forget()
	x, ok := b.handed[h]
	delete(b.handed, h)
	b.mu.Unlock()

	if !ok || h == "" {
		return ErrUnknown
	}

	s, err := json.Marshal(x.key)
	if err != nil {
		return err
	}

	if err := b.cfg.Store.Set(string(s)); err != nil {
		return errors.New("the key from Reconcile couldn't be kept on this computer: " + err.Error())
	}

	b.mu.Lock()
	b.key, b.read, b.refused = &x.key, true, false
	b.mu.Unlock()

	b.cfg.Log.Info("signed in to Reconcile", "device", x.key.Device, "expires", x.key.Expires)

	return nil
}

// SignOut forgets the key here. It stays live in Reconcile until it ends or
// is revoked on Reconcile's keys page, which Settings links to.
func (b *Business) SignOut() error {
	if err := b.cfg.Store.Delete(); err != nil {
		return err
	}

	b.mu.Lock()
	b.key, b.read, b.refused = nil, true, false
	b.mu.Unlock()

	b.cfg.Log.Info("signed out of Reconcile")

	return nil
}

// forget drops the sign-ins and handoffs too old to finish. With mu held.
func (b *Business) forget() {
	now := b.cfg.Now()

	maps.DeleteFunc(b.pending, func(_ string, x begun) bool { return now.Sub(x.when) > beginLife })
	maps.DeleteFunc(b.handed, func(_ string, x handoff) bool { return now.Sub(x.when) > handoffLife })
}

// --- what the key reaches -------------------------------------------------------

// ErrSignedOut is no key, or one that ended.
var ErrSignedOut = errors.New("this computer isn't signed in to Reconcile")

// usable is the key to send, or ErrSignedOut.
func (b *Business) usable() (string, error) {
	if b.cfg.Client == nil {
		return "", ErrNoReconcile
	}

	if st := b.Status(); !st.SignedIn {
		return "", ErrSignedOut
	}

	k, err := b.kept()
	if err != nil || k == nil {
		return "", ErrSignedOut
	}

	return k.Key, nil
}

// Accounts is the accounts this computer may print checks on: the checking
// accounts the key reaches, in dollars, since the amount in words is
// (docs/09-reconcile-api.md, "The stub note, and currency").
func (b *Business) Accounts(ctx context.Context) ([]reconcile.Account, error) {
	key, err := b.usable()
	if err != nil {
		return nil, err
	}

	all, err := b.cfg.Client.Accounts(ctx, key)
	if err != nil {
		return nil, b.answered(err)
	}

	return slices.DeleteFunc(all, func(a reconcile.Account) bool { return !strings.EqualFold(a.Currency, "USD") }), nil
}

// answered notes a 401, after which Settings asks to sign in again.
func (b *Business) answered(err error) error {
	if errors.Is(err, reconcile.ErrSignedOut) {
		b.mu.Lock()
		b.refused = true
		b.mu.Unlock()
	}

	return err
}

func random() string {
	r := make([]byte, 32)
	_, _ = rand.Read(r)

	return base64.RawURLEncoding.EncodeToString(r)
}
