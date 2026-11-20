package checkbus

// Sending each check to Reconcile, and its void (docs/07-the-image.md,
// "Sending it"; docs/09-reconcile-api.md, "What the program does with each
// answer to a send" and "Voiding").
//
// Three rules shape all of it:
//
//   - A check is sent the moment it is confirmed printed, so that the other
//     computer sees its number within seconds; what can't go then is kept
//     and sent later, oldest first, with longer waits between tries.
//   - A void is sent after its check, never before, and only once
//     Reconcile has taken this computer's send of the check. Reconcile
//     voids by number: a void sent for a check Reconcile refused ("another
//     check has this number") would void the other computer's check.
//   - Once a check's void is sent, the check is never sent again: Reconcile
//     skips voided checks when it looks for a repeat, so the same check
//     sent after its void would be a new check outstanding. The register's
//     queue (Storer.Unsent) leaves such a check out.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Receipt is Reconcile taking a check.
type Receipt struct {
	// Outcome is what became of it: outstanding, attached (the bank had
	// already paid it, and its image is on the charge), or already_there
	// (it was sent before, and the answer was lost).
	Outcome string
	ID, URL string
}

// The outcomes.
const (
	Outstanding  = "outstanding"
	Attached     = "attached"
	AlreadyThere = "already_there"
)

// Sender is what sending needs of Reconcile, beside Books.
type Sender interface {
	Send(ctx context.Context, c Check, image []byte) (Receipt, error)
	Void(ctx context.Context, account string, number int) error
}

// Refused is Reconcile saying no to a check in a way only the person can
// answer: another check has the number, the bank paid it for another
// amount, the account is archived, their role doesn't let them add checks.
// Sentence is Reconcile's, as it wrote it; Conflict is what Reconcile has
// instead, in words.
type Refused struct {
	Sentence string
	Conflict string
}

func (e Refused) Error() string { return e.Sentence }

// ErrSignedOut is this computer not signed in, or its key ended: nothing
// can be sent until someone signs in again.
var ErrSignedOut = errors.New("this computer isn't signed in to Reconcile")

// ErrNoCheck is a void of a number Reconcile has no check for: the send
// never arrived. The check is sent, then voided.
var ErrNoCheck = errors.New("no check in Reconcile has that number")

// Sending is the outcome of sending one check, for the page that asked.
type Sending struct {
	Receipt Receipt
	Err     error // nil, ErrNotLinked, ErrSignedOut, Refused, or Reconcile out of reach
}

// retry is how long to wait after a try that failed for want of Reconcile:
// a minute, doubling, at most an hour.
func retry(attempts int) time.Duration {
	wait := time.Minute << min(max(attempts-1, 0), 6)

	return min(wait, time.Hour)
}

// SendOne sends one check, and its void if it is void: what the press of
// Yes does, and what a void does (docs/06, "After it prints").
func (b *Business) SendOne(ctx context.Context, account string, number int) Sending {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	c, err := b.store.Get(ctx, account, number)
	if err != nil {
		return Sending{Err: err}
	}

	r, err := b.sendCheck(ctx, c)

	return Sending{Receipt: r, Err: err}
}

// SendDue sends what is waiting and due, oldest first: each time the program
// starts, every minute, and at once from the Checks screen's Send now, which
// sends everything waiting, refused or not yet due included (every). It
// stops at the first check that can't go for want of Reconcile, since the
// rest wouldn't either. It reports how many went, and the error that
// stopped it.
func (b *Business) SendDue(ctx context.Context, every bool) (int, error) {
	if b.sender == nil {
		return 0, nil
	}

	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	queue, err := b.store.Unsent(ctx)
	if err != nil {
		return 0, err
	}

	now := b.now()
	sent := 0

	for _, c := range queue {
		if !every && (c.Send.Refusal != "" || c.Send.NextTry.After(now)) {
			continue
		}

		_, err := b.sendCheck(ctx, c)

		_, refused := errors.AsType[Refused](err)

		switch {
		case err == nil:
			sent++
		case errors.Is(err, ErrNotLinked), refused:
			// This check waits for the person; the next may go.
		default:
			return sent, err
		}
	}

	return sent, nil
}

// sendCheck sends c and then its void, as far as each can go, and keeps in
// the register what Reconcile answered. With sendMu held.
func (b *Business) sendCheck(ctx context.Context, c Check) (Receipt, error) {
	if b.sender == nil {
		return Receipt{}, ErrNotLinked
	}

	if c.State == Printing {
		return Receipt{}, WrongState{Number: c.Number, Sentence: fmt.Sprintf("Check %d is sent once someone says whether it printed correctly.", c.Number)}
	}

	var r Receipt

	// Twice at most: a void Reconcile can't place (no check has the
	// number) means the send never arrived, so the check is sent again,
	// then voided.
	for range 2 {
		if c.Send.SentAt.IsZero() {
			img, err := b.files.Read(c.Account, c.Number, "png")
			if err != nil {
				return r, fmt.Errorf("check %d's image couldn't be read to send: %w", c.Number, err)
			}

			r, err = b.sender.Send(ctx, c, img)
			if err != nil {
				return r, b.failed(ctx, c, err)
			}

			if err := b.store.MarkSent(ctx, c.Account, c.Number, r.ID, r.URL, b.now()); err != nil {
				return r, err
			}

			c.Send.SentAt = b.now()
		}

		if c.State != Void || !c.Send.VoidSentAt.IsZero() {
			return r, nil
		}

		err := b.sender.Void(ctx, c.Account, c.Number)

		switch {
		case err == nil:
			return r, b.store.MarkVoidSent(ctx, c.Account, c.Number, b.now())
		case errors.Is(err, ErrNoCheck):
			if err := b.store.Unsend(ctx, c.Account, c.Number); err != nil {
				return r, err
			}

			c.Send.SentAt = time.Time{}

			continue
		default:
			return r, b.failed(ctx, c, err)
		}
	}

	return r, fmt.Errorf("check %d was sent, but Reconcile still has no check with its number to void", c.Number)
}

// failed keeps what Reconcile answered: a refusal, for the person; a try to
// make again later, for Reconcile out of reach. Signed out, or not linked,
// nothing changes: the check waits for that to change.
func (b *Business) failed(ctx context.Context, c Check, err error) error {
	if errors.Is(err, ErrSignedOut) || errors.Is(err, ErrNotLinked) || errors.Is(err, context.Canceled) {
		return err
	}

	attempts := c.Send.Attempts + 1

	if ref, ok := errors.AsType[Refused](err); ok {
		if serr := b.store.MarkFailed(ctx, c.Account, c.Number, attempts, time.Time{}, ref.Sentence, ref.Conflict); serr != nil {
			return errors.Join(err, serr)
		}

		return err
	}

	if serr := b.store.MarkFailed(ctx, c.Account, c.Number, attempts, b.now().Add(retry(attempts)), "", ""); serr != nil {
		return errors.Join(err, serr)
	}

	return err
}

// Run sends what is waiting when the program starts, then every minute,
// and at once when Nudge asks, until ctx ends (docs/07, "Sending it").
func (b *Business) Run(ctx context.Context, log func(msg string, args ...any)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()

	for {
		n, err := b.SendDue(ctx, false)
		if n > 0 || (err != nil && ctx.Err() == nil) {
			args := []any{"sent", n}
			if err != nil {
				args = append(args, "stopped", err.Error())
			}

			log("sending checks to Reconcile", args...)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-b.nudge:
		}
	}
}

// Nudge has Run send what is due now, rather than at the next minute: after
// a void, or signing in.
func (b *Business) Nudge() {
	select {
	case b.nudge <- struct{}{}:
	default: // one is already waiting
	}
}

// What became of a sending, for the page that sent it to word.
const (
	SentOutstanding = "outstanding" // in Reconcile, waiting for the statement
	SentAttached    = "attached"    // in Reconcile, on the charge the bank already made
	SentUnlinked    = "unlinked"    // kept here: the account isn't linked to Reconcile
	SentSignedOut   = "signed-out"  // kept here until someone signs in
	SentRefused     = "refused"     // kept here: Reconcile said no, and the person must act
	SentLater       = "later"       // kept here: Reconcile out of reach, tried again later
)

// Code is what became of the sending, as one of the Sent… words.
func (s Sending) Code() string {
	_, refused := errors.AsType[Refused](s.Err)

	switch {
	case s.Err == nil && s.Receipt.Outcome == Attached:
		return SentAttached
	case s.Err == nil:
		return SentOutstanding
	case errors.Is(s.Err, ErrNotLinked):
		return SentUnlinked
	case errors.Is(s.Err, ErrSignedOut):
		return SentSignedOut
	case refused:
		return SentRefused
	}

	return SentLater
}
