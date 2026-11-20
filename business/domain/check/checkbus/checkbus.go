// Package checkbus is the register's rules (docs/06-writing-a-check.md): which
// number a check is proposed, refusing a number twice, what is asked about
// again before printing, and void, reprint and correcting a number.
//
// A check enters the register at the press of Print, in the state "printing",
// and is answered by the person: printed, or void. Nothing is written before
// that press, so a page left half filled leaves nothing behind; and nothing is
// ever deleted after it, but for the one case below where the claim never
// reached the printer.
//
// The register is this computer's alone. What the other computer printed is
// Reconcile's to say, and arrives with the Reconcile client (docs/09); until
// then the paper in the tray is the guard that always works, and every
// proposal here is only a proposal.
package checkbus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"regexp"
	"strings"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/render"
)

// State is where a check is between the press of Print and the books.
type State string

const (
	// Printing is from the press of Print until "Did it print correctly?" is
	// answered. A check left here (the program closed or crashed first) is
	// asked about again at the next start, and an update waits for it.
	Printing State = "printing"

	// Printed is a check the person says came out right.
	Printed State = "printed"

	// Void is a check jammed, misprinted, or voided later. Its number is
	// used for good.
	Void State = "void"
)

// Check is one row of the register.
type Check struct {
	Account   string // this computer's key for the account, never Reconcile's id
	Number    int
	WrittenOn time.Time // the date on the check; only the day means anything
	Payee     string
	Amount    money.Cents
	Memo      string
	StubNote  string

	State      State
	CreatedAt  time.Time // the press of Print
	PrintedAt  time.Time // "Yes, it printed correctly"
	VoidedAt   time.Time
	VoidReason string
	ReprintOf  int // the number this check replaced, or 0

	Send Send
}

// Send is what Reconcile has said about a check (docs/09-reconcile-api.md).
// Every field is here from the first version, empty until the Reconcile
// client fills it, so that the register never has to be rewritten when it
// arrives (docs/06, "The register").
type Send struct {
	SentAt     time.Time
	VoidSentAt time.Time
	ID         string // Reconcile's id for the check
	URL        string
	Attempts   int
	NextTry    time.Time
	Refusal    string // Reconcile's last refusal, as Reconcile wrote it
	Conflict   string // the other check's details, as Reconcile gave them
}

// Account is what the register's rules need to know about an account.
type Account struct {
	Key     string
	Last4   string
	Stock   string      // the layout's name
	Ceiling money.Cents // above it, an amount is asked about again
}

// DefaultCeiling is an account's ceiling unless the config says otherwise
// (docs/04-stock-and-layout.md).
const DefaultCeiling money.Cents = 10_000_00

// keyPattern is what an account key may be. The key names a folder
// (checks/<key>/), so it is held to letters, digits and hyphens: nothing a
// file system on either system reads specially.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// reserved is the names Windows keeps for devices, which no folder may have
// there, whatever follows them.
var reserved = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

// ValidKey reports whether key may name an account.
func ValidKey(key string) bool { return keyPattern.MatchString(key) && !reserved.MatchString(key) }

// --- errors -----------------------------------------------------------------

// ErrNotFound is a check the register doesn't have.
var ErrNotFound = errors.New("there is no such check in the register")

// ErrUnknownAccount is an account this computer doesn't print for.
var ErrUnknownAccount = errors.New("that account is not one this computer prints for; add it in Settings")

// Invalid is a field refused, with the sentence to show beside it.
type Invalid struct {
	Field    string
	Sentence string
}

func (e Invalid) Error() string { return e.Sentence }

// NumberUsed is a number the register already has on that account.
type NumberUsed struct {
	Number int
	State  State
}

func (e NumberUsed) Error() string {
	what := "printed"
	switch e.State {
	case Void:
		what = "voided"
	case Printing:
		what = "being printed"
	}

	return fmt.Sprintf("Check %d was already %s on this account. Look at the number on the paper in the tray, and type that one.", e.Number, what)
}

// WrongState is an action that doesn't fit where the check is: confirming a
// check already answered, voiding one already void, correcting the number of
// one Reconcile already has.
type WrongState struct {
	Number   int
	Sentence string
}

func (e WrongState) Error() string { return e.Sentence }

// --- storage ----------------------------------------------------------------

// Storer is the register's storage (stores/checkdb).
//
// The conditional writes (Printed, Void) change a row only from the states the
// rule allows, in the statement itself, and answer ErrNotFound when no row
// matched; the business reads the row afterwards to say why.
type Storer interface {
	// Claim inserts a check in the state printing. It is the one statement
	// that takes a number: when the number is taken, it answers NumberUsed
	// and changes nothing.
	Claim(ctx context.Context, c Check) error

	// Unclaim removes a check still printing, for the one case where the
	// claim never reached the printer.
	Unclaim(ctx context.Context, account string, number int) error

	Get(ctx context.Context, account string, number int) (Check, error)
	Printed(ctx context.Context, account string, number int, at time.Time) error
	Void(ctx context.Context, account string, number int, reason string, at time.Time) error

	// Renumber moves a check not yet sent to another number, and every check
	// that names it as the one it replaced. NumberUsed when the new number is
	// taken.
	Renumber(ctx context.Context, account string, from, to int) error

	// Highest is the highest number on the account, or 0.
	Highest(ctx context.Context, account string) (int, error)

	// Recent is the account's last n checks, the most recently written first.
	Recent(ctx context.Context, account string, n int) ([]Check, error)

	// LastTo is the most recent check to the payee, matched without regard to
	// case, or ErrNotFound.
	LastTo(ctx context.Context, payee string) (Check, error)

	// Payees is up to n payees whose name contains q, the most recently paid
	// first.
	Payees(ctx context.Context, q string, n int) ([]string, error)

	// LastAccount is the account of the most recent check, or "".
	LastAccount(ctx context.Context) (string, error)

	// InState is every check in the state, oldest first.
	InState(ctx context.Context, s State) ([]Check, error)

	// Unsent is every check Reconcile doesn't have yet: printed or void and
	// never sent, or void and the void never sent. Oldest first.
	Unsent(ctx context.Context) ([]Check, error)
}

// Files keeps each check's PDF and PNG (stores/checkfiles): the only copy of
// the PDF ever, and of the image until Reconcile has it (docs/02).
type Files interface {
	Save(account string, number int, pdf, png []byte) error
	SavePNG(account string, number int, png []byte) error
	Rename(account string, from, to int) error
	Remove(account string, number int) error
}

// Accounts answers what this computer knows about an account: from the
// config today, mapped to Reconcile's accounts once signed in.
type Accounts interface {
	Account(key string) (Account, error) // ErrUnknownAccount
}

// --- the business -----------------------------------------------------------

// Config is what the business needs.
type Config struct {
	Store    Storer
	Files    Files
	Accounts Accounts
	Now      func() time.Time // time.Now unless a test says otherwise
}

// Business is the register's rules.
type Business struct {
	store    Storer
	files    Files
	accounts Accounts
	now      func() time.Time
}

// New makes one.
func New(cfg Config) *Business {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Business{store: cfg.Store, files: cfg.Files, accounts: cfg.Accounts, now: now}
}

// ImageDPI is the image's resolution: enough for a person to read a check on a
// screen, small enough to send in a moment (docs/07-the-image.md).
const ImageDPI = 200

// Recent is how many of an account's last numbers are shown beside the number
// field, so that a gap or a second checkbook is seen rather than hidden in one
// proposed number (docs/06, "Numbering").
const Recent = 5

// Proposal is the number proposed for the next check, and the last few
// numbers it came from.
type Proposal struct {
	Next int   // 0 when the register has nothing on the account yet
	Last []int // the most recent first
}

// Propose is the next number on the account, from the register alone. The
// person confirms it against the paper; Reconcile's numbers join it with the
// Reconcile client.
func (b *Business) Propose(ctx context.Context, account string) (Proposal, error) {
	high, err := b.store.Highest(ctx, account)
	if err != nil {
		return Proposal{}, err
	}

	recent, err := b.store.Recent(ctx, account, Recent)
	if err != nil {
		return Proposal{}, err
	}

	p := Proposal{}
	if high > 0 {
		p.Next = high + 1
	}

	for _, c := range recent {
		p.Last = append(p.Last, c.Number)
	}

	return p, nil
}

// Defaults is the payee's last check, whose amount, memo and stub note are
// offered for the next one (docs/06, "Payees"); false when the register has
// never paid them.
func (b *Business) Defaults(ctx context.Context, payee string) (Check, bool, error) {
	c, err := b.store.LastTo(ctx, strings.TrimSpace(payee))

	switch {
	case errors.Is(err, ErrNotFound):
		return Check{}, false, nil
	case err != nil:
		return Check{}, false, err
	}

	return c, true, nil
}

// Payees completes a payee's name from the register.
func (b *Business) Payees(ctx context.Context, q string, n int) ([]string, error) {
	return b.store.Payees(ctx, strings.TrimSpace(q), n)
}

// LastAccount is the account the form starts on: the one used last.
func (b *Business) LastAccount(ctx context.Context) (string, error) {
	return b.store.LastAccount(ctx)
}

// LeftPrinting is every check whose "Did it print correctly?" was never
// answered: asked about again when the program opens.
func (b *Business) LeftPrinting(ctx context.Context) ([]Check, error) {
	return b.store.InState(ctx, Printing)
}

// Busy reports whether a check is printing, which an update waits for
// (docs/10-install-update-and-releases.md).
func (b *Business) Busy(ctx context.Context) (bool, error) {
	cs, err := b.store.InState(ctx, Printing)
	return len(cs) > 0, err
}

// Unsent is every check waiting for Reconcile, oldest first.
func (b *Business) Unsent(ctx context.Context) ([]Check, error) {
	return b.store.Unsent(ctx)
}

// Get is one check.
func (b *Business) Get(ctx context.Context, account string, number int) (Check, error) {
	return b.store.Get(ctx, account, number)
}

// NewCheck is the form, as typed and confirmed.
type NewCheck struct {
	Account   string
	Number    int
	WrittenOn time.Time
	Payee     string
	Amount    money.Cents
	Memo      string
	StubNote  string
	ReprintOf int

	// Answered is the questions the person has already said yes to.
	Answered []Code
}

// Write claims the number and makes the check's PDF and image: what the press
// of Print does before anything reaches the printer. It returns the PDF to
// print, without the printer's offset, which is the caller's (it describes
// the printer, not the check).
//
// The order is the point. Everything that can refuse the check, the questions
// included, is asked before the number is taken; the number is taken in one
// statement, so two presses of Print can't both have it; and the files are
// written only once the number is this check's, so a refused number never
// overwrites the files of the check that has it.
func (b *Business) Write(ctx context.Context, nc NewCheck) (Check, []byte, error) {
	acct, layout, err := b.account(nc.Account)
	if err != nil {
		return Check{}, nil, err
	}

	c, err := b.validate(nc)
	if err != nil {
		return Check{}, nil, err
	}

	if qs, err := b.Questions(ctx, nc); err != nil {
		return Check{}, nil, err
	} else if len(qs) > 0 {
		return Check{}, nil, Asked{Questions: qs}
	}

	paper, err := layout.Paper(placed(c, acct))
	if err != nil {
		return Check{}, nil, err
	}

	pdf, err := render.PDF(paper)
	if err != nil {
		return Check{}, nil, err
	}

	img, err := image(layout, c, acct)
	if err != nil {
		return Check{}, nil, err
	}

	c.State = Printing
	c.CreatedAt = b.now()

	if err := b.store.Claim(ctx, c); err != nil {
		return Check{}, nil, err
	}

	// The number is this check's now. If its files can't be kept, nothing
	// has gone to the printer, so the claim is taken back rather than left
	// as a check that never existed: the one removal from the register.
	if err := b.files.Save(c.Account, c.Number, pdf, img); err != nil {
		if uerr := b.store.Unclaim(ctx, c.Account, c.Number); uerr != nil {
			return Check{}, nil, fmt.Errorf("%w; and taking back check %d failed too: %w", err, c.Number, uerr)
		}

		return Check{}, nil, fmt.Errorf("the check's files could not be kept, so it was not printed: %w", err)
	}

	return c, pdf, nil
}

// Abandon takes back a check that never reached the printer: the printer
// refused the job outright, or couldn't be reached. Only a check still
// printing can be taken back, and its files go with it.
func (b *Business) Abandon(ctx context.Context, account string, number int) error {
	if err := b.store.Unclaim(ctx, account, number); err != nil {
		if errors.Is(err, ErrNotFound) {
			return b.why(ctx, account, number, "taken back")
		}

		return err
	}

	return b.files.Remove(account, number)
}

// Confirm is "Yes, it printed correctly".
func (b *Business) Confirm(ctx context.Context, account string, number int) error {
	err := b.store.Printed(ctx, account, number, b.now())
	if errors.Is(err, ErrNotFound) {
		return b.why(ctx, account, number, "confirmed")
	}

	return err
}

// Void voids a check, printing or printed, and draws "VOID" across its image.
// The reason is the person's: "jam", "misaligned", or anything typed.
func (b *Business) Void(ctx context.Context, account string, number int, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Invalid{Field: "reason", Sentence: "Say why the check is void: a jam, misaligned, or anything else."}
	}

	if err := b.store.Void(ctx, account, number, reason, b.now()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return b.why(ctx, account, number, "voided")
		}

		return err
	}

	return b.redraw(ctx, account, number)
}

// Reprint is the voided check's form again, on the next number: the same
// payee, amount, memo and stub note (docs/06, "Void and reprint"). The person
// still confirms the number against the paper.
func (b *Business) Reprint(ctx context.Context, account string, number int) (NewCheck, error) {
	c, err := b.store.Get(ctx, account, number)
	if err != nil {
		return NewCheck{}, err
	}

	if c.State != Void {
		return NewCheck{}, WrongState{Number: number, Sentence: fmt.Sprintf("Check %d is not void, so there is nothing to reprint.", number)}
	}

	p, err := b.Propose(ctx, account)
	if err != nil {
		return NewCheck{}, err
	}

	return NewCheck{
		Account:   c.Account,
		Number:    p.Next,
		WrittenOn: c.WrittenOn,
		Payee:     c.Payee,
		Amount:    c.Amount,
		Memo:      c.Memo,
		StubNote:  c.StubNote,
		ReprintOf: c.Number,
	}, nil
}

// Renumber corrects a check's number to the one on the paper: after two
// computers used one number, or one was mistyped (docs/06, "When Reconcile
// refuses the number after printing"). A check Reconcile already has keeps its
// number here; correcting it there is a void and a new check.
//
// The PDF keeps its bytes, since the number is pre-printed and not in it; the
// image is drawn again, since it shows the number.
func (b *Business) Renumber(ctx context.Context, account string, from, to int) error {
	if to < 1 {
		return Invalid{Field: "number", Sentence: "A check number is a whole number, 1 or more."}
	}

	c, err := b.store.Get(ctx, account, from)
	if err != nil {
		return err
	}

	switch {
	case c.State == Printing:
		return WrongState{Number: from, Sentence: fmt.Sprintf("Check %d is still printing. Say whether it printed correctly first.", from)}
	case !c.Send.SentAt.IsZero():
		return WrongState{Number: from, Sentence: fmt.Sprintf("Reconcile already has check %d, so its number can't be corrected here. Void it, and write it again on the right number.", from)}
	}

	if err := b.store.Renumber(ctx, account, from, to); err != nil {
		return err
	}

	if err := b.files.Rename(account, from, to); err != nil {
		// The register moved and the files didn't: move the register back,
		// so that the two never disagree about which check is which.
		if rerr := b.store.Renumber(ctx, account, to, from); rerr != nil {
			return fmt.Errorf("%w; and putting check %d back on %d failed too: %w", err, to, from, rerr)
		}

		return err
	}

	return b.redraw(ctx, account, to)
}

// --- helpers ----------------------------------------------------------------

func (b *Business) account(key string) (Account, layoutbus.Layout, error) {
	acct, err := b.accounts.Account(key)
	if err != nil {
		return Account{}, layoutbus.Layout{}, err
	}

	layout, err := layoutbus.Load(acct.Stock)
	if err != nil {
		return Account{}, layoutbus.Layout{}, err
	}

	return acct, layout, nil
}

// validate refuses what can never be printed, and returns the check as it
// will be kept.
func (b *Business) validate(nc NewCheck) (Check, error) {
	c := Check{
		Account:   nc.Account,
		Number:    nc.Number,
		WrittenOn: day(nc.WrittenOn),
		Payee:     strings.TrimSpace(nc.Payee),
		Amount:    nc.Amount,
		Memo:      strings.TrimSpace(nc.Memo),
		StubNote:  strings.TrimRight(strings.ReplaceAll(nc.StubNote, "\r\n", "\n"), "\n "),
		ReprintOf: nc.ReprintOf,
	}

	switch {
	case c.Number < 1:
		return Check{}, Invalid{Field: "number", Sentence: "A check number is a whole number, 1 or more. Type the number on the paper in the tray."}
	case nc.WrittenOn.IsZero():
		return Check{}, Invalid{Field: "date", Sentence: "Give the check a date."}
	case c.Payee == "":
		return Check{}, Invalid{Field: "payee", Sentence: "Say who the check is to."}
	case c.Amount <= 0:
		return Check{}, Invalid{Field: "amount", Sentence: "The amount must be more than zero."}
	case c.Amount > money.Max:
		return Check{}, Invalid{Field: "amount", Sentence: "That amount is more than this program can write in words."}
	}

	return c, nil
}

// why explains a conditional write that matched no row.
func (b *Business) why(ctx context.Context, account string, number int, verb string) error {
	c, err := b.store.Get(ctx, account, number)
	if err != nil {
		return err
	}

	return WrongState{Number: number, Sentence: fmt.Sprintf("Check %d is already %s, so it can't be %s.", number, c.State, verb)}
}

// redraw draws the check's image again from the register: after a void, or a
// corrected number.
func (b *Business) redraw(ctx context.Context, account string, number int) error {
	c, err := b.store.Get(ctx, account, number)
	if err != nil {
		return err
	}

	acct, layout, err := b.account(account)
	if err != nil {
		return err
	}

	img, err := image(layout, c, acct)
	if err != nil {
		return err
	}

	return b.files.SavePNG(account, number, img)
}

// placed is the check as the layout needs it.
func placed(c Check, acct Account) layoutbus.Check {
	return layoutbus.Check{
		Number:   c.Number,
		Date:     c.WrittenOn,
		Payee:    c.Payee,
		Amount:   c.Amount,
		Memo:     c.Memo,
		StubNote: c.StubNote,
		Last4:    acct.Last4,
		Void:     c.State == Void,
	}
}

// image is the check's PNG, the face only, as Reconcile will be sent it.
func image(l layoutbus.Layout, c Check, acct Account) ([]byte, error) {
	doc, area, err := l.Image(placed(c, acct))
	if err != nil {
		return nil, err
	}

	img, err := render.PNG(doc, ImageDPI, area)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("drawing the check's image: %w", err)
	}

	return buf.Bytes(), nil
}

// day is the date alone, as a day in no particular place: a check is dated a
// day, not a moment.
func day(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}

	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
