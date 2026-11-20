// Package booksbus is Reconcile as the register asks it: by this computer's
// account keys, which Settings maps to Reconcile's accounts, through the
// key this computer signed in with (docs/08, docs/09-reconcile-api.md).
//
// It is what makes signinbus a checkbus.Books. It translates and nothing
// more: an account not linked, or no Reconcile at all, is
// checkbus.ErrNotLinked, which the register takes as nothing to ask; every
// other failure is an error whose sentence the register shows when it
// prints without having asked.
package booksbus

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/signin/signinbus"
	"github.com/jroedel/reconcile-checks/business/sdk/reconcile"
	"github.com/jroedel/reconcile-checks/business/types/money"
)

// Config is what it needs.
type Config struct {
	SignIn   *signinbus.Business
	Settings *settingsbus.Business
}

// Business is the books.
type Business struct{ cfg Config }

// New makes one.
func New(cfg Config) *Business { return &Business{cfg: cfg} }

var _ checkbus.Books = (*Business)(nil)

// ErrGone is an account linked to one of Reconcile's the key no longer
// reaches: archived, or the person's role changed (docs/09, "A 404 is never
// a free number").
var ErrGone = errors.New("this account is no longer one Reconcile offers to this computer's key; see Settings")

// errSignedOut is the register's sentence for no key.
var errSignedOut = errors.New("this computer isn't signed in to Reconcile")

// id is Reconcile's id for the account key.
func (b *Business) id(key string) (string, error) {
	if b.cfg.SignIn == nil || !b.cfg.SignIn.Status().Available {
		return "", checkbus.ErrNotLinked
	}

	a, err := b.cfg.Settings.Account(key)
	if err != nil {
		return "", err
	}

	if a.ReconcileID == "" {
		return "", checkbus.ErrNotLinked
	}

	return a.ReconcileID, nil
}

// plain is an error from Reconcile as the register says it.
func plain(err error) error {
	ref, isRefusal := errors.AsType[reconcile.Refusal](err)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, signinbus.ErrSignedOut):
		return errSignedOut
	case isRefusal && ref.Status == 404 && ref.Code == "":
		return ErrGone
	}

	return err
}

// Numbers is the highest number Reconcile knows on the account, and its
// last few.
func (b *Business) Numbers(ctx context.Context, key string) (checkbus.Numbers, error) {
	id, err := b.id(key)
	if err != nil {
		return checkbus.Numbers{}, err
	}

	all, err := b.cfg.SignIn.Accounts(ctx)
	if err != nil {
		return checkbus.Numbers{}, plain(err)
	}

	i := slices.IndexFunc(all, func(a reconcile.Account) bool { return a.ID == id })
	if i < 0 {
		return checkbus.Numbers{}, ErrGone
	}

	n := checkbus.Numbers{Highest: number(all[i].Highest)}
	for _, s := range all[i].LastNumbers {
		if v := number(s); v > 0 {
			n.Last = append(n.Last, v)
		}
	}

	return n, nil
}

// Number is what Reconcile says about one number on the account.
func (b *Business) Number(ctx context.Context, key string, n int) (checkbus.InBooks, error) {
	id, err := b.id(key)
	if err != nil {
		return checkbus.InBooks{}, err
	}

	got, err := b.cfg.SignIn.Number(ctx, id, n)
	if err != nil {
		return checkbus.InBooks{}, plain(err)
	}

	in := checkbus.InBooks{}

	switch got.State {
	case reconcile.NumberFree:
		in.State = checkbus.BooksFree
	case reconcile.NumberOutstanding, reconcile.NumberAttached:
		in.State = checkbus.BooksSent
	case reconcile.NumberVoid:
		in.State = checkbus.BooksVoid
	case reconcile.NumberCleared:
		in.State = checkbus.BooksCleared
	default:
		// A state this version doesn't know is a number someone has.
		in.State = checkbus.BooksSent
	}

	if c := got.Check; c != nil {
		in.Payee, in.Amount, in.WrittenOn = c.Payee, amount(c.Amount), date(c.WrittenOn)
	}

	for _, p := range got.Cleared {
		in.Paid = append(in.Paid, checkbus.Payment{Amount: amount(p.Amount), On: date(p.Date)})
	}

	return in, nil
}

// Payees is the account's payees whose name contains q.
func (b *Business) Payees(ctx context.Context, key, q string) ([]string, error) {
	id, err := b.id(key)
	if err != nil {
		return nil, err
	}

	names, err := b.cfg.SignIn.Payees(ctx, id, q)

	return names, plain(err)
}

func number(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}

	return n
}

func amount(a reconcile.Amount) money.Cents {
	c, err := money.Parse(string(a))
	if err != nil {
		return 0
	}

	return c
}

func date(s string) time.Time {
	d, err := time.ParseInLocation(time.DateOnly, s, time.Local)
	if err != nil {
		return time.Time{}
	}

	return d
}
