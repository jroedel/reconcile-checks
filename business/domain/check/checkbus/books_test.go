package checkbus_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
)

// books is Reconcile, as a test wants it to answer. checking-a is linked to
// it; checking-b is not.
type books struct {
	highest int
	last    []int
	numbers map[int]checkbus.InBooks
	payees  []string
	err     error // every call fails with it
	asked   []int // the numbers asked about
}

func (b *books) linked(account string) error {
	if account != "checking-a" {
		return checkbus.ErrNotLinked
	}

	return b.err
}

func (b *books) Numbers(_ context.Context, account string) (checkbus.Numbers, error) {
	if err := b.linked(account); err != nil {
		return checkbus.Numbers{}, err
	}

	return checkbus.Numbers{Highest: b.highest, Last: b.last}, nil
}

func (b *books) Number(_ context.Context, account string, number int) (checkbus.InBooks, error) {
	if err := b.linked(account); err != nil {
		return checkbus.InBooks{}, err
	}

	b.asked = append(b.asked, number)

	if in, ok := b.numbers[number]; ok {
		return in, nil
	}

	return checkbus.InBooks{State: checkbus.BooksFree}, nil
}

func (b *books) Payees(_ context.Context, account, q string) ([]string, error) {
	if err := b.linked(account); err != nil {
		return nil, err
	}

	var out []string
	for _, p := range b.payees {
		if strings.Contains(strings.ToLower(p), strings.ToLower(q)) {
			out = append(out, p)
		}
	}

	return out, nil
}

func TestTheNumberProposedTakesTheOtherComputersChecks(t *testing.T) {
	b := &books{highest: 1050, last: []int{1050, 1049, 1046}}
	e := setupWith(t, b)

	e.write(t, form(1045), true)
	e.write(t, form(1047), true)

	p, err := e.bus.Propose(t.Context(), "checking-a")
	if err != nil {
		t.Fatal(err)
	}

	if !p.Books || p.Next != 1051 || !slices.Equal(p.Last, []int{1050, 1049, 1047, 1046, 1045}) {
		t.Errorf("with Reconcile: %+v", p)
	}

	// Offline, the register alone.
	b.err = errors.New("Reconcile can't be reached just now")

	p, _ = e.bus.Propose(t.Context(), "checking-a")
	if p.Books || p.Next != 1048 || !slices.Equal(p.Last, []int{1047, 1045}) {
		t.Errorf("offline: %+v", p)
	}
}

func TestANumberReconcileHasIsRefusedBeforePrinting(t *testing.T) {
	b := &books{numbers: map[int]checkbus.InBooks{
		1043: {State: checkbus.BooksSent, Payee: "Payee B", Amount: 120_00, WrittenOn: today.AddDate(0, 0, -2)},
		1044: {State: checkbus.BooksVoid, Payee: "Payee B", Amount: 120_00, WrittenOn: today},
		1045: {State: checkbus.BooksCleared, Paid: []checkbus.Payment{{Amount: 75_00, On: today.AddDate(0, 0, -17)}}},
	}}
	e := setupWith(t, b)

	for n, want := range map[int]string{
		1043: "Check 1043 is already in Reconcile: to Payee B for $120.00, written November 18",
		1044: "Check 1044 was voided in Reconcile",
		1045: "The bank already paid check 1045, $75.00 on November 3",
	} {
		_, _, err := e.bus.Write(t.Context(), form(n))

		inv, ok := errors.AsType[checkbus.Invalid](err)
		if !ok || inv.Field != "number" || !strings.HasPrefix(inv.Sentence, want) {
			t.Errorf("%d: got %v, want %q", n, err, want)
		}

		if _, err := e.bus.Get(t.Context(), "checking-a", n); !errors.Is(err, checkbus.ErrNotFound) {
			t.Errorf("%d: a number Reconcile has was taken in the register: %v", n, err)
		}
	}

	// A free number prints, once Reconcile has been asked.
	e.write(t, form(1046), true)

	if !slices.Contains(b.asked, 1046) {
		t.Errorf("1046 was printed without asking Reconcile: asked %v", b.asked)
	}

	// The register's own number is refused by the register, before
	// Reconcile is asked.
	b.asked = nil

	if _, _, err := e.bus.Write(t.Context(), form(1046)); !isUsed(err) {
		t.Errorf("1046 again: %v", err)
	}

	if len(b.asked) > 0 {
		t.Errorf("Reconcile was asked about a number the register has: %v", b.asked)
	}
}

func TestOfflineANumberPrintsOnceTheWarningIsAnswered(t *testing.T) {
	b := &books{err: errors.New("Reconcile can't be reached just now (it didn't answer in time)")}
	e := setupWith(t, b)

	_, _, err := e.bus.Write(t.Context(), form(1043))

	asked, ok := errors.AsType[checkbus.Asked](err)
	if !ok || len(asked.Questions) != 1 || asked.Questions[0].Code != checkbus.AskUnchecked ||
		!strings.Contains(asked.Questions[0].Sentence, "couldn't be checked against the other computer's checks: Reconcile can't be reached just now") {
		t.Fatalf("offline: %v", err)
	}

	nc := form(1043)
	nc.Answered = append(nc.Answered, checkbus.AskUnchecked)
	e.write(t, nc, true)

	// An account not linked to Reconcile has nothing to check and nothing
	// to warn about.
	nc = form(2001)
	nc.Account = "checking-b"
	nc.Amount = 100_00

	if _, _, err := e.bus.Write(t.Context(), nc); err != nil {
		t.Errorf("an account not linked: %v", err)
	}
}

func TestAPayeeReconcileKnowsIsNotNew(t *testing.T) {
	b := &books{payees: []string{"Payee A Plumbing", "Payee C Hardware"}}
	e := setupWith(t, b)

	nc := form(1043)
	nc.Answered = nil
	nc.Payee = "payee c hardware"

	if _, _, err := e.bus.Write(t.Context(), nc); err != nil {
		t.Errorf("a payee Reconcile has, in another case: %v", err)
	}

	nc = form(1044)
	nc.Answered = nil
	nc.Payee = "Payee D"

	if _, ok := errors.AsType[checkbus.Asked](func() error { _, _, err := e.bus.Write(t.Context(), nc); return err }()); !ok {
		t.Error("a payee neither the register nor Reconcile knows wasn't asked about")
	}

	names, err := e.bus.PayeesFor(t.Context(), "checking-a", "payee", 10)
	if err != nil {
		t.Fatal(err)
	}

	// Reconcile's spellings first, then the register's own; each once.
	if !slices.Equal(names, []string{"Payee A Plumbing", "Payee C Hardware"}) {
		t.Errorf("completing: %q", names)
	}

	// Offline, the register's.
	b.err = errors.New("offline")
	names, _ = e.bus.PayeesFor(t.Context(), "checking-a", "payee", 10)

	if !slices.Equal(names, []string{"payee c hardware"}) {
		t.Errorf("completing offline: %q", names)
	}
}

func TestReconcileIsNotWaitedOnForEver(t *testing.T) {
	e := setupWith(t, hanging{})

	start := time.Now()

	if _, err := e.bus.Propose(t.Context(), "checking-a"); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("a proposal waited %v on a Reconcile that never answers", took)
	}
}

// hanging is a Reconcile that answers only when it is given up on.
type hanging struct{}

func (hanging) Numbers(ctx context.Context, _ string) (checkbus.Numbers, error) {
	<-ctx.Done()
	return checkbus.Numbers{}, ctx.Err()
}

func (hanging) Number(ctx context.Context, _ string, _ int) (checkbus.InBooks, error) {
	<-ctx.Done()
	return checkbus.InBooks{}, ctx.Err()
}

func (hanging) Payees(ctx context.Context, _, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func isUsed(err error) bool {
	_, ok := errors.AsType[checkbus.NumberUsed](err)
	return ok
}
