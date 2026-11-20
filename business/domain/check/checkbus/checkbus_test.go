package checkbus_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/check/stores/checkdb"
	"github.com/jroedel/reconcile-checks/business/domain/check/stores/checkfiles"
	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/sqldb"
)

// today is the clock every test runs at.
var today = time.Date(2026, 11, 20, 15, 4, 0, 0, time.Local)

type accounts map[string]checkbus.Account

func (a accounts) Account(key string) (checkbus.Account, error) {
	if acct, ok := a[key]; ok {
		return acct, nil
	}

	return checkbus.Account{}, checkbus.ErrUnknownAccount
}

type env struct {
	bus   *checkbus.Business
	store *checkdb.Store
	files *checkfiles.Files
	exec  func(q string, args ...any)
	now   *time.Time
}

func setup(t *testing.T) *env {
	t.Helper()

	dir := t.TempDir()

	db, err := sqldb.Open(filepath.Join(dir, "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := checkdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := sqldb.CheckSchema(t.Context(), db, checkdb.Expected); err != nil {
		t.Fatal(err)
	}

	now := today
	e := &env{
		store: checkdb.NewStore(db),
		files: checkfiles.New(filepath.Join(dir, "checks")),
		now:   &now,
		exec: func(q string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
				t.Fatal(err)
			}
		},
	}

	e.bus = checkbus.New(checkbus.Config{
		Store: e.store,
		Files: e.files,
		Accounts: accounts{
			"checking-a": {Key: "checking-a", Last4: "1234", Stock: "walmart-voucher-top"},
			"checking-b": {Key: "checking-b", Last4: "5678", Stock: "walmart-voucher-top", Ceiling: 500_00},
		},
		Now: func() time.Time { return *e.now },
	})

	return e
}

// An invented check. Never a real payee, amount or memo (CLAUDE.md, "This
// repository is public").
func form(number int) checkbus.NewCheck {
	return checkbus.NewCheck{
		Account:   "checking-a",
		Number:    number,
		WrittenOn: today,
		Payee:     "Payee A Plumbing",
		Amount:    123450,
		Memo:      "Kitchen drain repair",
		StubNote:  "Invoice 8812",
		Answered:  []checkbus.Code{checkbus.AskNewPayee},
	}
}

// write prints a check through to the state wanted.
func (e *env) write(t *testing.T, nc checkbus.NewCheck, confirm bool) checkbus.Check {
	t.Helper()

	c, pdf, err := e.bus.Write(t.Context(), nc)
	if err != nil {
		t.Fatalf("writing check %d: %v", nc.Number, err)
	}

	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("Write returned %q…, not a PDF", pdf[:min(len(pdf), 8)])
	}

	if confirm {
		if err := e.bus.Confirm(t.Context(), nc.Account, nc.Number); err != nil {
			t.Fatal(err)
		}
	}

	*e.now = e.now.Add(time.Minute)

	return c
}

func (e *env) file(account string, number int, ext string) string {
	p, _ := e.files.Path(account, number, ext)
	return p
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestTheNumberProposedIsOneMoreThanTheHighest(t *testing.T) {
	e := setup(t)

	p, err := e.bus.Propose(t.Context(), "checking-a")
	if err != nil {
		t.Fatal(err)
	}

	if p.Next != 0 || len(p.Last) != 0 {
		t.Errorf("an empty register proposed %+v; want nothing, for the person to type", p)
	}

	for _, n := range []int{1041, 1042, 1043, 7, 1044, 1045, 1046} {
		e.write(t, form(n), true)
	}

	p, err = e.bus.Propose(t.Context(), "checking-a")
	if err != nil {
		t.Fatal(err)
	}

	// The 7 from a second checkbook shows among the last few, rather than
	// being hidden in one proposed number.
	if p.Next != 1047 || !slices.Equal(p.Last, []int{1046, 1045, 1044, 7, 1043}) {
		t.Errorf("proposed %+v; want 1047 after 1046 1045 1044 7 1043", p)
	}

	if p, _ := e.bus.Propose(t.Context(), "checking-b"); p.Next != 0 {
		t.Errorf("another account was proposed %d; numbers are per account", p.Next)
	}
}

func TestANumberIsTakenOnce(t *testing.T) {
	e := setup(t)

	c := e.write(t, form(1043), false)
	if c.State != checkbus.Printing {
		t.Errorf("a check just written is %q, want printing", c.State)
	}

	if !exists(e.file("checking-a", 1043, "pdf")) || !exists(e.file("checking-a", 1043, "png")) {
		t.Error("the check's PDF and PNG were not kept")
	}

	before, _ := os.ReadFile(e.file("checking-a", 1043, "pdf"))

	again := form(1043)
	again.Payee = "Payee B Gardening"
	again.Answered = append(again.Answered, checkbus.AskNewPayee)

	_, _, err := e.bus.Write(t.Context(), again)
	if used, ok := errors.AsType[checkbus.NumberUsed](err); !ok || used.Number != 1043 || used.State != checkbus.Printing {
		t.Errorf("err = %v; want 1043 refused as being printed", err)
	}

	if after, _ := os.ReadFile(e.file("checking-a", 1043, "pdf")); !bytes.Equal(before, after) {
		t.Error("a refused number overwrote the PDF of the check that has it")
	}

	// The same number on the other account is another check.
	other := form(1043)
	other.Account = "checking-b"
	other.Amount = 100_00
	if _, _, err := e.bus.Write(t.Context(), other); err != nil {
		t.Errorf("1043 on another account was refused: %v", err)
	}
}

// Two presses of Print at once: one has the number, and the other is told so.
func TestTwoPressesAtOnceTakeTheNumberOnce(t *testing.T) {
	e := setup(t)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for range 4 {
		wg.Go(func() {
			_, _, err := e.bus.Write(t.Context(), form(1043))
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		})
	}
	wg.Wait()

	ok := 0
	for _, err := range errs {
		switch _, used := errors.AsType[checkbus.NumberUsed](err); {
		case err == nil:
			ok++
		case !used:
			t.Errorf("a losing press got %v; want NumberUsed", err)
		}
	}

	if ok != 1 {
		t.Errorf("%d presses took 1043; want exactly one", ok)
	}
}

func TestWhatCanNeverPrintIsRefusedAndTakesNoNumber(t *testing.T) {
	cases := map[string]struct {
		change func(*checkbus.NewCheck)
		field  string
	}{
		"no number":      {func(c *checkbus.NewCheck) { c.Number = 0 }, "number"},
		"no payee":       {func(c *checkbus.NewCheck) { c.Payee = "   " }, "payee"},
		"no amount":      {func(c *checkbus.NewCheck) { c.Amount = 0 }, "amount"},
		"negative":       {func(c *checkbus.NewCheck) { c.Amount = -5 }, "amount"},
		"no date":        {func(c *checkbus.NewCheck) { c.WrittenOn = time.Time{} }, "date"},
		"payee too long": {func(c *checkbus.NewCheck) { c.Payee = strings.Repeat("Payee with a long name ", 10) }, "payee"},
		"bad character":  {func(c *checkbus.NewCheck) { c.Payee = "Łukasz" }, "payee"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)

			nc := form(1043)
			tc.change(&nc)

			_, _, err := e.bus.Write(t.Context(), nc)

			var field string
			if inv, ok := errors.AsType[checkbus.Invalid](err); ok {
				field = inv.Field
			} else if tl, ok := errors.AsType[layoutbus.TooLong](err); ok {
				field = tl.Field
			} else if bc, ok := errors.AsType[layoutbus.BadCharacter](err); ok {
				field = bc.Field
			}

			if field != tc.field {
				t.Errorf("err = %v; want the %s refused", err, tc.field)
			}

			if _, err := e.bus.Get(t.Context(), "checking-a", nc.Number); !errors.Is(err, checkbus.ErrNotFound) {
				t.Error("a refused check took its number")
			}
		})
	}
}

func TestAnUnknownAccountIsRefused(t *testing.T) {
	e := setup(t)

	nc := form(1043)
	nc.Account = "savings"

	if _, _, err := e.bus.Write(t.Context(), nc); !errors.Is(err, checkbus.ErrUnknownAccount) {
		t.Errorf("err = %v; want ErrUnknownAccount", err)
	}
}

// Each question is asked until it is answered, and then the check prints.
func TestTheQuestionsAreAskedAgainNotRefused(t *testing.T) {
	cases := map[string]struct {
		change func(*checkbus.NewCheck)
		code   checkbus.Code
		says   string
	}{
		"a new payee": {func(c *checkbus.NewCheck) { c.Answered = nil }, checkbus.AskNewPayee, "Payee A Plumbing is new. Keep it?"},
		"post-dated": {func(c *checkbus.NewCheck) { c.WrittenOn = today.AddDate(0, 0, 1) }, checkbus.AskPostDated,
			"dated November 21, 2026, after today"},
		"more than a week ago": {func(c *checkbus.NewCheck) { c.WrittenOn = today.AddDate(0, 0, -8) }, checkbus.AskOldDate,
			"dated November 12, 2026, more than a week ago"},
		"over the default ceiling": {func(c *checkbus.NewCheck) { c.Amount = 12_000_00 }, checkbus.AskOverCeiling,
			"$12,000.00 is more than this account's usual most, $10,000.00. In words: Twelve thousand and 00/100."},
		"over the account's own ceiling": {func(c *checkbus.NewCheck) { c.Account = "checking-b"; c.Amount = 500_01 }, checkbus.AskOverCeiling,
			"$500.01 is more than this account's usual most, $500.00."},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)

			nc := form(1043)
			tc.change(&nc)

			_, _, err := e.bus.Write(t.Context(), nc)

			asked, ok := errors.AsType[checkbus.Asked](err)
			if !ok || len(asked.Questions) != 1 || asked.Questions[0].Code != tc.code {
				t.Fatalf("err = %v; want only %s asked", err, tc.code)
			}

			if !strings.Contains(asked.Questions[0].Sentence, tc.says) {
				t.Errorf("asked %q; want it to say %q", asked.Questions[0].Sentence, tc.says)
			}

			if _, err := e.bus.Get(t.Context(), nc.Account, nc.Number); !errors.Is(err, checkbus.ErrNotFound) {
				t.Error("a check with a question unanswered took its number")
			}

			nc.Answered = append(nc.Answered, tc.code)
			if _, _, err := e.bus.Write(t.Context(), nc); err != nil {
				t.Errorf("answered, it was still refused: %v", err)
			}
		})
	}
}

func TestDatesWithinAWeekAndAtTheCeilingAreNotAsked(t *testing.T) {
	e := setup(t)

	for _, d := range []int{-7, -1, 0} {
		nc := form(1043)
		nc.WrittenOn = today.AddDate(0, 0, d)
		nc.Amount = checkbus.DefaultCeiling

		qs, err := e.bus.Questions(t.Context(), nc)
		if err != nil {
			t.Fatal(err)
		}

		if len(qs) != 0 {
			t.Errorf("%d days from today at the ceiling, asked %+v", d, qs)
		}
	}
}

func TestAPayeePaidBeforeIsNotNewAndBringsItsLastCheck(t *testing.T) {
	e := setup(t)

	e.write(t, form(1043), true)

	later := form(1044)
	later.Amount = 99_00
	later.Memo = "Kitchen drain, second visit"
	e.write(t, later, true)

	nc := form(1045)
	nc.Payee = "payee a plumbing" // as typed, in lower case
	nc.Answered = nil

	if qs, _ := e.bus.Questions(t.Context(), nc); len(qs) != 0 {
		t.Errorf("a payee already paid was asked about: %+v", qs)
	}

	c, ok, err := e.bus.Defaults(t.Context(), "payee a plumbing")
	if err != nil || !ok {
		t.Fatalf("no defaults: %v", err)
	}

	if c.Number != 1044 || c.Amount != 99_00 || c.Memo != "Kitchen drain, second visit" || c.StubNote != "Invoice 8812" {
		t.Errorf("defaults %+v; want check 1044's", c)
	}

	if _, ok, _ := e.bus.Defaults(t.Context(), "Payee C"); ok {
		t.Error("a payee never paid has defaults")
	}
}

func TestPayeesComplete(t *testing.T) {
	e := setup(t)

	for i, p := range []string{"Payee A Plumbing", "Payee B Gardening", "100% Payee_C", "payee a plumbing"} {
		nc := form(1043 + i)
		nc.Payee = p
		e.write(t, nc, true)
	}

	got, err := e.bus.Payees(t.Context(), "payee", 10)
	if err != nil {
		t.Fatal(err)
	}

	// The most recent first, one plumber however spelled, as last printed.
	if want := []string{"payee a plumbing", "100% Payee_C", "Payee B Gardening"}; !slices.Equal(got, want) {
		t.Errorf("payees %q; want %q", got, want)
	}

	// % and _ are letters in a name, not wildcards.
	if got, _ := e.bus.Payees(t.Context(), "0%", 10); !slices.Equal(got, []string{"100% Payee_C"}) {
		t.Errorf("%q matched %q", "0%", got)
	}

	if got, _ := e.bus.Payees(t.Context(), "e_c", 10); !slices.Equal(got, []string{"100% Payee_C"}) {
		t.Errorf("%q matched %q", "e_c", got)
	}
}

func TestTheLastAccountUsedIsRemembered(t *testing.T) {
	e := setup(t)

	if a, _ := e.bus.LastAccount(t.Context()); a != "" {
		t.Errorf("an empty register remembers %q", a)
	}

	nc := form(1)
	nc.Account = "checking-b"
	nc.Amount = 10_00
	e.write(t, nc, true)

	if a, _ := e.bus.LastAccount(t.Context()); a != "checking-b" {
		t.Errorf("last account %q, want checking-b", a)
	}
}

func TestConfirmingIsOnceAndOnlyWhilePrinting(t *testing.T) {
	e := setup(t)

	e.write(t, form(1043), false)

	if busy, _ := e.bus.Busy(t.Context()); !busy {
		t.Error("a check printing doesn't make the program busy")
	}

	if left, _ := e.bus.LeftPrinting(t.Context()); len(left) != 1 || left[0].Number != 1043 {
		t.Errorf("left printing: %+v", left)
	}

	if err := e.bus.Confirm(t.Context(), "checking-a", 1043); err != nil {
		t.Fatal(err)
	}

	c, _ := e.bus.Get(t.Context(), "checking-a", 1043)
	if c.State != checkbus.Printed || !c.PrintedAt.Equal(*e.now) {
		t.Errorf("confirmed: %s at %v", c.State, c.PrintedAt)
	}

	if busy, _ := e.bus.Busy(t.Context()); busy {
		t.Error("still busy once answered")
	}

	if _, ok := errors.AsType[checkbus.WrongState](e.bus.Confirm(t.Context(), "checking-a", 1043)); !ok {
		t.Error("a check was confirmed twice")
	}

	if err := e.bus.Confirm(t.Context(), "checking-a", 999); !errors.Is(err, checkbus.ErrNotFound) {
		t.Errorf("confirming a check that isn't there: %v", err)
	}
}

func TestAVoidedCheckSaysSoInItsImageAndIsReprintedOnTheNextNumber(t *testing.T) {
	e := setup(t)

	nc := form(1043)
	nc.StubNote = "Invoice 8812\nVisits on Nov 3 and 10"
	e.write(t, nc, false)

	before, _ := os.ReadFile(e.file("checking-a", 1043, "png"))

	if _, ok := errors.AsType[checkbus.Invalid](e.bus.Void(t.Context(), "checking-a", 1043, " ")); !ok {
		t.Error("a void with no reason was accepted")
	}

	if err := e.bus.Void(t.Context(), "checking-a", 1043, "jam"); err != nil {
		t.Fatal(err)
	}

	c, _ := e.bus.Get(t.Context(), "checking-a", 1043)
	if c.State != checkbus.Void || c.VoidReason != "jam" || c.VoidedAt.IsZero() {
		t.Errorf("voided: %+v", c)
	}

	if after, _ := os.ReadFile(e.file("checking-a", 1043, "png")); bytes.Equal(before, after) {
		t.Error("the image was not drawn again with VOID")
	}

	if _, ok := errors.AsType[checkbus.WrongState](e.bus.Void(t.Context(), "checking-a", 1043, "again")); !ok {
		t.Error("a check was voided twice")
	}

	again, err := e.bus.Reprint(t.Context(), "checking-a", 1043)
	if err != nil {
		t.Fatal(err)
	}

	if again.Number != 1044 || again.ReprintOf != 1043 || again.Payee != nc.Payee || again.Amount != nc.Amount ||
		again.Memo != nc.Memo || again.StubNote != nc.StubNote {
		t.Errorf("reprint %+v; want check 1043 again on 1044", again)
	}

	again.Answered = []checkbus.Code{checkbus.AskNewPayee}
	e.write(t, again, true)

	if got, _ := e.bus.Get(t.Context(), "checking-a", 1044); got.ReprintOf != 1043 {
		t.Errorf("check 1044 replaced %d, want 1043", got.ReprintOf)
	}

	if _, err := e.bus.Reprint(t.Context(), "checking-a", 1044); err == nil {
		t.Error("a check that isn't void was offered for reprinting")
	}
}

func TestACheckThatNeverReachedThePrinterCanBeTakenBack(t *testing.T) {
	e := setup(t)

	e.write(t, form(1043), false)

	if err := e.bus.Abandon(t.Context(), "checking-a", 1043); err != nil {
		t.Fatal(err)
	}

	if _, err := e.bus.Get(t.Context(), "checking-a", 1043); !errors.Is(err, checkbus.ErrNotFound) {
		t.Error("the abandoned check is still in the register")
	}

	if exists(e.file("checking-a", 1043, "pdf")) || exists(e.file("checking-a", 1043, "png")) {
		t.Error("the abandoned check's files are still there")
	}

	// A check answered is never taken back.
	e.write(t, form(1043), true)
	if _, ok := errors.AsType[checkbus.WrongState](e.bus.Abandon(t.Context(), "checking-a", 1043)); !ok {
		t.Error("a printed check was taken back")
	}
}

type brokenFiles struct{ checkbus.Files }

func (brokenFiles) Save(string, int, []byte, []byte) error { return errors.New("the disk is full") }

func TestACheckWhoseFilesCantBeKeptIsNotLeftInTheRegister(t *testing.T) {
	e := setup(t)

	bus := checkbus.New(checkbus.Config{
		Store:    e.store,
		Files:    brokenFiles{e.files},
		Accounts: accounts{"checking-a": {Key: "checking-a", Last4: "1234", Stock: "walmart-voucher-top"}},
		Now:      func() time.Time { return today },
	})

	if _, _, err := bus.Write(t.Context(), form(1043)); err == nil || !strings.Contains(err.Error(), "not printed") {
		t.Errorf("err = %v; want it to say the check was not printed", err)
	}

	if _, err := e.bus.Get(t.Context(), "checking-a", 1043); !errors.Is(err, checkbus.ErrNotFound) {
		t.Error("the claim was left behind")
	}
}

func TestANumberIsCorrectedToTheOneOnThePaper(t *testing.T) {
	e := setup(t)

	e.write(t, form(1043), false)
	if err := e.bus.Void(t.Context(), "checking-a", 1043, "misaligned"); err != nil {
		t.Fatal(err)
	}

	again := form(1044)
	again.ReprintOf = 1043
	e.write(t, again, true)
	e.write(t, form(1050), true)

	pdf, _ := os.ReadFile(e.file("checking-a", 1043, "pdf"))
	png, _ := os.ReadFile(e.file("checking-a", 1043, "png"))

	// The voided 1043 was really 1042 on the paper.
	if err := e.bus.Renumber(t.Context(), "checking-a", 1043, 1042); err != nil {
		t.Fatal(err)
	}

	if _, err := e.bus.Get(t.Context(), "checking-a", 1043); !errors.Is(err, checkbus.ErrNotFound) {
		t.Error("1043 is still in the register")
	}

	if c, _ := e.bus.Get(t.Context(), "checking-a", 1042); c.State != checkbus.Void || c.VoidReason != "misaligned" {
		t.Errorf("1042 is %+v; want the voided check", c)
	}

	if c, _ := e.bus.Get(t.Context(), "checking-a", 1044); c.ReprintOf != 1042 {
		t.Errorf("1044 replaced %d; want the corrected 1042", c.ReprintOf)
	}

	if b, _ := os.ReadFile(e.file("checking-a", 1042, "pdf")); !bytes.Equal(b, pdf) {
		t.Error("the PDF changed; the number is pre-printed, so it should only move")
	}

	if b, _ := os.ReadFile(e.file("checking-a", 1042, "png")); len(b) == 0 || bytes.Equal(b, png) {
		t.Error("the image was not drawn again with its new number")
	}

	if exists(e.file("checking-a", 1043, "pdf")) {
		t.Error("the old number's PDF is still there")
	}

	// Onto a number already used: refused, and nothing moves.
	_, used := errors.AsType[checkbus.NumberUsed](e.bus.Renumber(t.Context(), "checking-a", 1044, 1050))
	if !used {
		t.Error("a check was renumbered onto one already printed")
	}

	if _, err := e.bus.Get(t.Context(), "checking-a", 1044); err != nil {
		t.Error("the refused correction moved the check")
	}

	// A check Reconcile has keeps its number.
	e.exec(`UPDATE checks SET sent_at = 1 WHERE number = 1050`)
	if _, ok := errors.AsType[checkbus.WrongState](e.bus.Renumber(t.Context(), "checking-a", 1050, 1051)); !ok {
		t.Error("a check already sent was renumbered")
	}
}

func TestTheUnsentQueue(t *testing.T) {
	e := setup(t)

	e.write(t, form(1), true)  // printed, never sent: waits
	e.write(t, form(2), false) // printing: not yet a check or a void
	e.write(t, form(3), true)  // printed and sent: done
	e.write(t, form(4), true)  // voided after it was sent: the void waits
	e.write(t, form(5), true)  // voided and both sent: done

	for _, n := range []int{4, 5} {
		if err := e.bus.Void(t.Context(), "checking-a", n, "written in error"); err != nil {
			t.Fatal(err)
		}
	}

	e.exec(`UPDATE checks SET sent_at = 1 WHERE number IN (3, 4, 5)`)
	e.exec(`UPDATE checks SET void_sent_at = 1 WHERE number = 5`)

	got, err := e.bus.Unsent(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var nums []int
	for _, c := range got {
		nums = append(nums, c.Number)
	}

	if !slices.Equal(nums, []int{1, 4}) {
		t.Errorf("unsent %v; want 1 and 4, oldest first", nums)
	}
}

func TestTheRegisterKeepsWhatWasWritten(t *testing.T) {
	e := setup(t)

	nc := form(1043)
	nc.Payee = "  José Núñez  "
	nc.StubNote = "Invoice 8812\r\nVisits on Nov 3\r\n"
	nc.Answered = []checkbus.Code{checkbus.AskNewPayee}
	e.write(t, nc, true)

	c, err := e.bus.Get(t.Context(), "checking-a", 1043)
	if err != nil {
		t.Fatal(err)
	}

	want := checkbus.Check{
		Account:   "checking-a",
		Number:    1043,
		WrittenOn: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Payee:     "José Núñez",
		Amount:    money.Cents(123450),
		Memo:      "Kitchen drain repair",
		StubNote:  "Invoice 8812\nVisits on Nov 3",
		State:     checkbus.Printed,
	}

	if c.Account != want.Account || c.Number != want.Number || !c.WrittenOn.Equal(want.WrittenOn) ||
		c.Payee != want.Payee || c.Amount != want.Amount || c.Memo != want.Memo ||
		c.StubNote != want.StubNote || c.State != want.State {
		t.Errorf("kept\n %+v\nwant\n %+v", c, want)
	}

	if !c.CreatedAt.Equal(today) {
		t.Errorf("created at %v, want %v", c.CreatedAt, today)
	}
}

func TestAnAccountKeyNamesAFolder(t *testing.T) {
	for key, ok := range map[string]bool{
		"checking-a": true, "a": true, "2nd-account": true,
		"": false, "Checking": false, "../x": false, "a/b": false, "-a": false, "a b": false, "con.": false,
		"con": false, "nul": false, "com1": false, "lpt9": false, "console": true,
	} {
		if checkbus.ValidKey(key) != ok {
			t.Errorf("ValidKey(%q) = %v", key, !ok)
		}
	}
}
