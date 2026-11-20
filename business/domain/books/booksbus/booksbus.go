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
	"encoding/json"
	"errors"
	"fmt"
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

var (
	_ checkbus.Books  = (*Business)(nil)
	_ checkbus.Sender = (*Business)(nil)
)

// ErrGone is an account linked to one of Reconcile's the key no longer
// reaches: archived, or the person's role changed (docs/09, "A 404 is never
// a free number").
var ErrGone = errors.New("this account is no longer one Reconcile offers to this computer's key; see Settings")

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
	case errors.Is(err, signinbus.ErrSignedOut), errors.Is(err, reconcile.ErrSignedOut):
		return checkbus.ErrSignedOut
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

// Send sends a check and its image.
func (b *Business) Send(ctx context.Context, c checkbus.Check, image []byte) (checkbus.Receipt, error) {
	id, err := b.id(c.Account)
	if err != nil {
		return checkbus.Receipt{}, err
	}

	sent, err := b.cfg.SignIn.Send(ctx, id, reconcile.Outgoing{
		Number:    c.Number,
		Amount:    figures(c.Amount),
		Payee:     c.Payee,
		WrittenOn: c.WrittenOn.Format(time.DateOnly),
		Memo:      c.Memo,
		Name:      "check-" + strconv.Itoa(c.Number) + ".png",
	}, image)
	if err != nil {
		return checkbus.Receipt{}, refused(err)
	}

	return checkbus.Receipt{Outcome: sent.Outcome, ID: sent.Check.ID, URL: sent.Check.URL}, nil
}

// Void voids the account's check with the number.
func (b *Business) Void(ctx context.Context, key string, number int) error {
	id, err := b.id(key)
	if err != nil {
		return err
	}

	err = b.cfg.SignIn.Void(ctx, id, number)
	if ref, ok := errors.AsType[reconcile.Refusal](err); ok && ref.Code == "no_check" {
		return checkbus.ErrNoCheck
	}

	return refused(err)
}

// refused is a refusal of a send or a void as the register keeps it: what
// only the person can answer is checkbus.Refused, with Reconcile's sentence
// and what it has instead; Reconcile out of reach stays as it is, to be
// tried again.
func refused(err error) error {
	err = plain(err)

	if errors.Is(err, ErrGone) {
		return checkbus.Refused{Sentence: "Reconcile no longer offers this check's account to this computer's key: it was archived, or your role on it changed. See Settings."}
	}

	ref, ok := errors.AsType[reconcile.Refusal](err)
	if !ok {
		return err
	}

	r := checkbus.Refused{Sentence: ref.Error()}

	switch ref.Code {
	case "number_used":
		var body struct {
			Check reconcile.Check `json:"check"`
		}

		if json.Unmarshal(ref.Body, &body) == nil && body.Check.Number != "" {
			c := body.Check
			r.Conflict = fmt.Sprintf("Reconcile's check %s: to %s for %s, written %s.", c.Number, c.Payee, amount(c.Amount), date(c.WrittenOn).Format("January 2, 2006"))
		}
	case "amount_differs":
		var body struct {
			Bank reconcile.Amount `json:"bank"`
			Sent reconcile.Amount `json:"sent"`
		}

		if json.Unmarshal(ref.Body, &body) == nil {
			r.Conflict = fmt.Sprintf("The bank paid %s; this check says %s.", amount(body.Bank), amount(body.Sent))
		}
	}

	return r
}

// figures is an amount as Reconcile takes it: 1234.50, no sign, no commas.
func figures(c money.Cents) string {
	return fmt.Sprintf("%d.%02d", c.Dollars(), c.CentsPart())
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
