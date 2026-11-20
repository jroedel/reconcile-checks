package checkbus_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
)

// sender is Reconcile taking checks, as a test wants it to answer. Each
// call is written down, in order.
type sender struct {
	calls  []string
	send   map[int]error // what sending each number answers; nil takes it
	void   map[int][]error
	all    error // every call answers it, when set
	images map[int][]byte
}

func newSender() *sender {
	return &sender{send: map[int]error{}, void: map[int][]error{}, images: map[int][]byte{}}
}

func (s *sender) Send(_ context.Context, c checkbus.Check, image []byte) (checkbus.Receipt, error) {
	s.calls = append(s.calls, fmt.Sprintf("send %d", c.Number))

	if s.all != nil {
		return checkbus.Receipt{}, s.all
	}

	if err := s.send[c.Number]; err != nil {
		return checkbus.Receipt{}, err
	}

	s.images[c.Number] = image

	return checkbus.Receipt{Outcome: checkbus.Outstanding, ID: fmt.Sprintf("r%d", c.Number), URL: fmt.Sprintf("https://books.invalid/receipts/r%d", c.Number)}, nil
}

func (s *sender) Void(_ context.Context, _ string, number int) error {
	s.calls = append(s.calls, fmt.Sprintf("void %d", number))

	if s.all != nil {
		return s.all
	}

	if errs := s.void[number]; len(errs) > 0 {
		s.void[number] = errs[1:]
		return errs[0]
	}

	return nil
}

func (e *env) get(t *testing.T, n int) checkbus.Check {
	t.Helper()

	c, err := e.bus.Get(t.Context(), "checking-a", n)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestACheckIsSentWhenItIsConfirmed(t *testing.T) {
	s := newSender()
	e := setupWith(t, nil, s)

	e.write(t, form(1043), true)

	sent := e.bus.SendOne(t.Context(), "checking-a", 1043)
	if sent.Err != nil || sent.Code() != checkbus.SentOutstanding {
		t.Fatalf("got %+v", sent)
	}

	c := e.get(t, 1043)
	if c.Send.SentAt.IsZero() || c.Send.ID != "r1043" || c.Send.URL == "" {
		t.Errorf("the register didn't keep the send: %+v", c.Send)
	}

	if len(s.images[1043]) == 0 || string(s.images[1043][1:4]) != "PNG" {
		t.Error("the image sent isn't the check's PNG")
	}

	if q, _ := e.bus.Unsent(t.Context()); len(q) != 0 {
		t.Errorf("still waiting: %v", q)
	}

	// A check still printing isn't sent.
	e.write(t, form(1044), false)

	if sent := e.bus.SendOne(t.Context(), "checking-a", 1044); sent.Err == nil {
		t.Error("a check nobody confirmed was sent")
	}
}

func TestOfflineACheckWaitsLongerEachTry(t *testing.T) {
	s := newSender()
	s.all = errors.New("Reconcile can't be reached just now")
	e := setupWith(t, nil, s)

	e.write(t, form(1043), true)

	if sent := e.bus.SendOne(t.Context(), "checking-a", 1043); sent.Code() != checkbus.SentLater {
		t.Fatalf("offline: %+v", sent)
	}

	c := e.get(t, 1043)
	if c.Send.Attempts != 1 || !c.Send.NextTry.Equal(e.now.Add(time.Minute)) || c.Send.Refusal != "" {
		t.Fatalf("after one try: %+v", c.Send)
	}

	// Not due: not tried.
	s.calls = nil

	if n, err := e.bus.SendDue(t.Context(), false); n != 0 || err != nil || len(s.calls) != 0 {
		t.Errorf("before it was due: %d, %v, %v", n, err, s.calls)
	}

	*e.now = e.now.Add(time.Minute)

	if _, err := e.bus.SendDue(t.Context(), false); err == nil {
		t.Error("a pass that failed said nothing")
	}

	if c := e.get(t, 1043); c.Send.Attempts != 2 || !c.Send.NextTry.Equal(e.now.Add(2*time.Minute)) {
		t.Errorf("after two tries: %+v", c.Send)
	}

	// Back: it goes, and the tries are forgotten.
	s.all = nil
	*e.now = e.now.Add(2 * time.Minute)

	if n, err := e.bus.SendDue(t.Context(), false); n != 1 || err != nil {
		t.Fatalf("back online: %d, %v", n, err)
	}

	if c := e.get(t, 1043); c.Send.SentAt.IsZero() || c.Send.Attempts != 0 || !c.Send.NextTry.IsZero() {
		t.Errorf("sent: %+v", c.Send)
	}
}

func TestARefusalWaitsForThePerson(t *testing.T) {
	s := newSender()
	s.send[1043] = checkbus.Refused{Sentence: "Check 1043 in this account says something else.", Conflict: "Reconcile's check 1043: to Payee B for $120.00."}
	e := setupWith(t, nil, s)

	e.write(t, form(1043), true)
	e.write(t, form(1044), true)

	if sent := e.bus.SendOne(t.Context(), "checking-a", 1043); sent.Code() != checkbus.SentRefused {
		t.Fatalf("got %+v", sent)
	}

	c := e.get(t, 1043)
	if c.Send.Refusal == "" || c.Send.Conflict == "" || !c.Send.NextTry.IsZero() {
		t.Fatalf("the refusal wasn't kept: %+v", c.Send)
	}

	// The pass leaves it, and sends the next.
	s.calls = nil
	*e.now = e.now.Add(time.Hour)

	if n, err := e.bus.SendDue(t.Context(), false); n != 1 || err != nil || !slices.Equal(s.calls, []string{"send 1044"}) {
		t.Errorf("the pass: %d, %v, %v", n, err, s.calls)
	}

	// The number corrected to the one on the paper: sent afresh.
	if err := e.bus.Renumber(t.Context(), "checking-a", 1043, 1045); err != nil {
		t.Fatal(err)
	}

	if n, err := e.bus.SendDue(t.Context(), false); n != 1 || err != nil {
		t.Errorf("after correcting the number: %d, %v", n, err)
	}
}

func TestSignedOutNothingIsTriedOrCounted(t *testing.T) {
	s := newSender()
	s.all = checkbus.ErrSignedOut
	e := setupWith(t, nil, s)

	e.write(t, form(1043), true)

	if sent := e.bus.SendOne(t.Context(), "checking-a", 1043); sent.Code() != checkbus.SentSignedOut {
		t.Errorf("got %+v", sent)
	}

	if c := e.get(t, 1043); c.Send.Attempts != 0 || !c.Send.NextTry.IsZero() {
		t.Errorf("a try was counted: %+v", c.Send)
	}
}

func TestAVoidIsSentAfterItsCheckAndOnlyThen(t *testing.T) {
	t.Run("a check sent, voided later", func(t *testing.T) {
		s := newSender()
		e := setupWith(t, nil, s)

		e.write(t, form(1043), true)
		e.bus.SendOne(t.Context(), "checking-a", 1043)

		if err := e.bus.Void(t.Context(), "checking-a", 1043, "lost in the mail"); err != nil {
			t.Fatal(err)
		}

		if n, err := e.bus.SendDue(t.Context(), false); n != 1 || err != nil {
			t.Fatalf("%d, %v", n, err)
		}

		if !slices.Equal(s.calls, []string{"send 1043", "void 1043"}) {
			t.Errorf("calls: %v", s.calls)
		}

		if c := e.get(t, 1043); c.Send.VoidSentAt.IsZero() {
			t.Error("the void wasn't kept as sent")
		}

		// Never sent again.
		if q, _ := e.bus.Unsent(t.Context()); len(q) != 0 {
			t.Errorf("a check whose void was sent is waiting again: %v", q)
		}
	})

	t.Run("a jam, never sent: sent, then voided", func(t *testing.T) {
		s := newSender()
		e := setupWith(t, nil, s)

		e.write(t, form(1043), false)

		if err := e.bus.Void(t.Context(), "checking-a", 1043, "jam"); err != nil {
			t.Fatal(err)
		}

		e.bus.SendDue(t.Context(), false)

		if !slices.Equal(s.calls, []string{"send 1043", "void 1043"}) {
			t.Errorf("calls: %v", s.calls)
		}

		if string(s.images[1043][1:4]) != "PNG" {
			t.Error("no image was sent with the void check")
		}
	})

	t.Run("a void Reconcile has no check for: sent again, then voided", func(t *testing.T) {
		s := newSender()
		s.void[1043] = []error{checkbus.ErrNoCheck}
		e := setupWith(t, nil, s)

		e.write(t, form(1043), true)
		e.bus.SendOne(t.Context(), "checking-a", 1043)
		_ = e.bus.Void(t.Context(), "checking-a", 1043, "written in error")
		e.bus.SendDue(t.Context(), false)

		if !slices.Equal(s.calls, []string{"send 1043", "void 1043", "send 1043", "void 1043"}) {
			t.Errorf("calls: %v", s.calls)
		}

		if c := e.get(t, 1043); c.Send.VoidSentAt.IsZero() {
			t.Error("the void wasn't kept as sent")
		}
	})

	t.Run("a refused send is never followed by a void", func(t *testing.T) {
		// Reconcile voids by number: voiding after "another check has
		// this number" would void the other computer's check.
		s := newSender()
		s.send[1043] = checkbus.Refused{Sentence: "Check 1043 in this account says something else."}
		e := setupWith(t, nil, s)

		e.write(t, form(1043), false)
		_ = e.bus.Void(t.Context(), "checking-a", 1043, "jam")

		e.bus.SendDue(t.Context(), false)
		e.bus.SendDue(t.Context(), true)

		for _, call := range s.calls {
			if call == "void 1043" {
				t.Fatalf("a void was sent for a check Reconcile refused: %v", s.calls)
			}
		}
	})

	t.Run("a check that cleared can't be voided, and isn't tried again", func(t *testing.T) {
		s := newSender()
		s.void[1043] = []error{checkbus.Refused{Sentence: "That check has cleared: the bank paid it, and it is on its transaction. It cannot be voided."}}
		e := setupWith(t, nil, s)

		e.write(t, form(1043), true)
		e.bus.SendOne(t.Context(), "checking-a", 1043)
		_ = e.bus.Void(t.Context(), "checking-a", 1043, "never cashed")
		e.bus.SendDue(t.Context(), false)

		c := e.get(t, 1043)
		if c.State != checkbus.Void || c.Send.Refusal == "" || !c.Send.VoidSentAt.IsZero() {
			t.Errorf("got %s, %+v", c.State, c.Send)
		}

		s.calls = nil
		*e.now = e.now.Add(24 * time.Hour)
		e.bus.SendDue(t.Context(), false)

		if len(s.calls) != 0 {
			t.Errorf("tried again by itself: %v", s.calls)
		}
	})
}

func TestNoSenderKeepsEveryCheckHere(t *testing.T) {
	e := setup(t)
	e.write(t, form(1043), true)

	if sent := e.bus.SendOne(t.Context(), "checking-a", 1043); sent.Code() != checkbus.SentUnlinked {
		t.Errorf("got %+v", sent)
	}

	if n, err := e.bus.SendDue(t.Context(), true); n != 0 || err != nil {
		t.Errorf("got %d, %v", n, err)
	}
}

func TestRunSendsAtOnceWhenNudged(t *testing.T) {
	s := newSender()
	e := setupWith(t, nil, s)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})
	go func() {
		e.bus.Run(ctx, func(string, ...any) {})
		close(done)
	}()

	// Not e.write, which moves the clock Run reads.
	if _, _, err := e.bus.Write(t.Context(), form(1043)); err != nil {
		t.Fatal(err)
	}

	if err := e.bus.Confirm(t.Context(), "checking-a", 1043); err != nil {
		t.Fatal(err)
	}

	e.bus.Nudge()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := e.get(t, 1043); !c.Send.SentAt.IsZero() {
			cancel()
			<-done

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Error("a nudge didn't send the check within five seconds")
}
