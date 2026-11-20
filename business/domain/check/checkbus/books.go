package checkbus

// Reconcile, as the register's rules ask it (docs/06, "Numbering" and
// "Payees"; docs/09-reconcile-api.md). The register works without it: an
// account not linked to Reconcile, or a computer never signed in, prints
// from the register alone, as it did before there was a Reconcile client.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/business/types/money"
)

// Books is Reconcile, by this computer's account keys (booksbus).
type Books interface {
	// Numbers is the highest number Reconcile knows on the account, and
	// its last few: from the checks sent by either computer, and the
	// bank's statements.
	Numbers(ctx context.Context, account string) (Numbers, error)

	// Number is what Reconcile says about one number on the account.
	Number(ctx context.Context, account string, number int) (InBooks, error)

	// Payees is the account's payees whose name contains q.
	Payees(ctx context.Context, account, q string) ([]string, error)
}

// ErrNotLinked is an account with no account in Reconcile: nothing to ask,
// and nothing to warn about.
var ErrNotLinked = errors.New("this account isn't linked to an account in Reconcile")

// Numbers is the numbers Reconcile knows on an account.
type Numbers struct {
	Highest int   // 0 for none
	Last    []int // the highest first
}

// Where a number is in Reconcile.
const (
	BooksFree    = "free"    // no check has it, and the bank hasn't paid it
	BooksSent    = "sent"    // a check has it, outstanding or attached
	BooksVoid    = "void"    // a spoiled check has it
	BooksCleared = "cleared" // the bank paid it, with no check
)

// InBooks is what Reconcile says about a number.
type InBooks struct {
	State string

	// The check that has it (sent, void).
	Payee     string
	Amount    money.Cents
	WrittenOn time.Time

	// What the bank paid (cleared).
	Paid []Payment
}

// Payment is the bank paying a number.
type Payment struct {
	Amount money.Cents
	On     time.Time
}

// AskUnchecked is a number Reconcile couldn't be asked about: printed with
// the warning (docs/06, "Two computers, one stack", decided 2026-11-20),
// since refusing would make the program useless exactly when it should just
// work.
const AskUnchecked Code = "unchecked"

// askBooks is a long enough wait for Reconcile before the page goes on
// without it: a proposal or a press of Print shouldn't hang on a network
// that isn't there.
const askBooks = 5 * time.Second

// fromBooks asks Reconcile with a deadline of its own.
func fromBooks[T any](ctx context.Context, ask func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, askBooks)
	defer cancel()

	return ask(ctx)
}

// numberQuestion asks Reconcile about the check's number before it is
// printed. It is a refusal when Reconcile has the number; a question when
// Reconcile couldn't be asked; nothing when it is free, or the account isn't
// linked.
func (b *Business) numberQuestion(ctx context.Context, nc NewCheck) (*Question, error) {
	if b.books == nil {
		return nil, nil
	}

	in, err := fromBooks(ctx, func(ctx context.Context) (InBooks, error) { return b.books.Number(ctx, nc.Account, nc.Number) })

	switch {
	case errors.Is(err, ErrNotLinked):
		return nil, nil
	case err != nil:
		return &Question{Code: AskUnchecked, Sentence: fmt.Sprintf(
			"Check %d couldn't be checked against the other computer's checks: %s. Make sure %d is the number on the paper in the tray. Print it?",
			nc.Number, strings.TrimSuffix(err.Error(), "."), nc.Number)}, nil
	}

	look := " Look at the number on the paper in the tray, and type that one."

	switch in.State {
	case BooksSent:
		return nil, Invalid{Field: "number", Sentence: fmt.Sprintf("Check %d is already in Reconcile: to %s for %s, written %s, from this computer or the other.",
			nc.Number, in.Payee, in.Amount, in.WrittenOn.Format("January 2"),
		) + look}
	case BooksVoid:
		return nil, Invalid{Field: "number", Sentence: fmt.Sprintf("Check %d was voided in Reconcile, so the paper with that number was voided and isn't the paper in the tray.", nc.Number) + look}
	case BooksCleared:
		paid := "it"
		if len(in.Paid) > 0 {
			paid = fmt.Sprintf("%s on %s", in.Paid[0].Amount, in.Paid[0].On.Format("January 2"))
		}

		return nil, Invalid{Field: "number", Sentence: fmt.Sprintf("The bank already paid check %d, %s, and no check in Reconcile has the number: one written by hand from the same book.", nc.Number, paid) + look}
	}

	return nil, nil
}

// knownToBooks is whether Reconcile already has the payee on the account,
// spelt any way but its case: not a new payee, so not asked about.
func (b *Business) knownToBooks(ctx context.Context, account, payee string) bool {
	if b.books == nil {
		return false
	}

	names, err := fromBooks(ctx, func(ctx context.Context) ([]string, error) { return b.books.Payees(ctx, account, payee) })
	if err != nil {
		return false
	}

	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(strings.TrimSpace(n), payee) })
}

// PayeesFor completes a payee's name on an account: Reconcile's names first,
// as Reconcile spells them, so that one plumber doesn't become three
// spellings; then the register's, for the payees only this computer has
// paid. Offline, or not linked, the register's alone.
func (b *Business) PayeesFor(ctx context.Context, account, q string, n int) ([]string, error) {
	q = strings.TrimSpace(q)

	var names []string

	if b.books != nil {
		if theirs, err := fromBooks(ctx, func(ctx context.Context) ([]string, error) { return b.books.Payees(ctx, account, q) }); err == nil {
			names = theirs
		}
	}

	ours, err := b.store.Payees(ctx, q, n)
	if err != nil {
		return nil, err
	}

	for _, name := range ours {
		if !slices.ContainsFunc(names, func(x string) bool { return strings.EqualFold(x, name) }) {
			names = append(names, name)
		}
	}

	return names[:min(len(names), n)], nil
}

// joinNumbers is the register's numbers and Reconcile's, highest first, each
// once, at most n.
func joinNumbers(ours, theirs []int, n int) []int {
	all := slices.Concat(ours, theirs)
	slices.SortFunc(all, func(x, y int) int { return cmp.Compare(y, x) })
	all = slices.Compact(all)

	return all[:min(len(all), n)]
}
