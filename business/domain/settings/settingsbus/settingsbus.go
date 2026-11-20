// Package settingsbus is what this computer prints for, and how: its printers,
// and its accounts with the stock, printer, tray and offsets of each
// (docs/03-the-local-app.md, "Settings"; docs/02, "The config, sketched").
//
// Each computer has its own: the rector's prints from another computer, on
// the same accounts, perhaps through another printer, and each printer is
// off by its own few tenths of a millimetre (docs/04, "Alignment and
// calibration"). So nothing here is shared, or sent to Reconcile.
package settingsbus

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/types/money"
)

// Printer is a printer this computer prints to.
type Printer struct {
	Name string // the person's name for it: "Office M612"
	URL  string // ipp://…, ipps://…, or file:<dir> in development
	Pin  string // the SHA-256 of its certificate, for ipps
}

// Offset is how far this computer's printer puts ink from where the layout
// says, in millimetres: positive x is to the right, positive y is down. It
// corrects the printer, never the layout.
type Offset struct{ X, Y float64 }

// Account is an account this computer prints checks on.
type Account struct {
	Key   string // this computer's key for it (checkbus.ValidKey)
	Name  string // "Checking A"
	Last4 string // for the image: XXXXXX1234

	Stock     string // the layout's name
	Printer   string // a Printer's Name
	Tray      string // the printer's media-source for the checks: tray-1
	MediaType string // the printer's media-type for the stock: stationery-preprinted
	Feed      string // which way up, written once: "face down, top edge in first"

	Ceiling money.Cents // above it, an amount is asked about again
	Offset  Offset

	// ReconcileID is Reconcile's id for the account, once signed in
	// (docs/08). Empty until then; checks are kept under Key either way.
	ReconcileID string
}

// Settings is everything this package keeps.
type Settings struct {
	Printers []Printer
	Accounts []Account
}

// Storer keeps the settings (stores/settingsfile).
type Storer interface {
	Load() (Settings, error)
	Save(Settings) error
}

// Invalid is a field refused, with the sentence to show beside it.
type Invalid struct {
	Field    string
	Sentence string
}

func (e Invalid) Error() string { return e.Sentence }

// ErrNoPrinter and ErrNoAccount are a name or key this computer doesn't have.
var (
	ErrNoPrinter = errors.New("there is no printer by that name in Settings")
	ErrNoAccount = errors.New("that account is not one this computer prints for; add it in Settings")
)

// Business is the settings' rules.
type Business struct {
	store Storer

	// One change at a time: each is a read of the file and a write of it.
	mu sync.Mutex
}

// New makes one.
func New(store Storer) *Business { return &Business{store: store} }

// Settings is everything, as it is now.
func (b *Business) Settings() (Settings, error) {
	return b.store.Load()
}

// Printer is one printer.
func (b *Business) Printer(name string) (Printer, error) {
	s, err := b.store.Load()
	if err != nil {
		return Printer{}, err
	}

	i := slices.IndexFunc(s.Printers, func(p Printer) bool { return p.Name == name })
	if i < 0 {
		return Printer{}, ErrNoPrinter
	}

	return s.Printers[i], nil
}

// Account is one account.
func (b *Business) Account(key string) (Account, error) {
	s, err := b.store.Load()
	if err != nil {
		return Account{}, err
	}

	i := slices.IndexFunc(s.Accounts, func(a Account) bool { return a.Key == key })
	if i < 0 {
		return Account{}, ErrNoAccount
	}

	return s.Accounts[i], nil
}

// CheckAccount is the account as the register's rules need it: what makes
// this package a checkbus.Accounts.
func (b *Business) CheckAccount(key string) (checkbus.Account, error) {
	a, err := b.Account(key)
	if errors.Is(err, ErrNoAccount) {
		return checkbus.Account{}, checkbus.ErrUnknownAccount
	}

	if err != nil {
		return checkbus.Account{}, err
	}

	return checkbus.Account{Key: a.Key, Last4: a.Last4, Stock: a.Stock, Ceiling: a.Ceiling}, nil
}

// SavePrinter adds a printer, or replaces the one called was. A printer
// renamed takes its accounts with it.
func (b *Business) SavePrinter(was string, p Printer) error {
	p.Name = strings.TrimSpace(p.Name)
	p.URL = strings.TrimSpace(p.URL)

	switch {
	case p.Name == "":
		return Invalid{Field: "name", Sentence: "Give the printer a name, such as Office M612."}
	case !printerURL(p.URL):
		return Invalid{Field: "url", Sentence: "A printer's address starts ipp:// or ipps://, such as ipps://printer.local/ipp/print."}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.store.Load()
	if err != nil {
		return err
	}

	at := slices.IndexFunc(s.Printers, func(q Printer) bool { return q.Name == was })
	if was != "" && at < 0 {
		return ErrNoPrinter
	}

	if slices.ContainsFunc(s.Printers, func(q Printer) bool { return q.Name == p.Name && q.Name != was }) {
		return Invalid{Field: "name", Sentence: fmt.Sprintf("There is already a printer called %s.", p.Name)}
	}

	if at < 0 {
		s.Printers = append(s.Printers, p)
	} else {
		s.Printers[at] = p
	}

	for i := range s.Accounts {
		if was != "" && s.Accounts[i].Printer == was {
			s.Accounts[i].Printer = p.Name
		}
	}

	return b.store.Save(s)
}

// RemovePrinter removes a printer no account uses.
func (b *Business) RemovePrinter(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.store.Load()
	if err != nil {
		return err
	}

	if i := slices.IndexFunc(s.Accounts, func(a Account) bool { return a.Printer == name }); i >= 0 {
		return Invalid{Field: "printer", Sentence: fmt.Sprintf("%s prints the checks of %s. Choose another printer for it first.", name, s.Accounts[i].Name)}
	}

	n := len(s.Printers)
	s.Printers = slices.DeleteFunc(s.Printers, func(p Printer) bool { return p.Name == name })

	if len(s.Printers) == n {
		return ErrNoPrinter
	}

	return b.store.Save(s)
}

// last4 is four digits, and only four: the program never holds more of an
// account number than Reconcile does (docs/04, "Account numbers").
var last4 = regexp.MustCompile(`^[0-9]{4}$`)

// maxOffset is the largest correction taken: more than a centimetre is a
// misfed sheet or a wrong layout, not a printer a little off.
const maxOffset = 10.0

// SaveAccount adds an account, or replaces the one with its key. The key is
// given once and kept: it names the account's checks in the register and
// their folder.
func (b *Business) SaveAccount(a Account, isNew bool) error {
	a.Name = strings.TrimSpace(a.Name)
	a.Last4 = strings.TrimSpace(a.Last4)
	a.Feed = strings.TrimSpace(a.Feed)

	if a.Ceiling <= 0 {
		a.Ceiling = checkbus.DefaultCeiling
	}

	switch {
	case !checkbus.ValidKey(a.Key):
		return Invalid{Field: "key", Sentence: "A short name for the account, in small letters, digits and hyphens, such as checking-a. It names the account's folder of checks, and can't be changed later."}
	case a.Name == "":
		return Invalid{Field: "name", Sentence: "Give the account a name, such as Checking A."}
	case !last4.MatchString(a.Last4):
		return Invalid{Field: "last4", Sentence: "The last four digits of the account number, and no more."}
	case a.Tray == "":
		return Invalid{Field: "tray", Sentence: "Say which tray holds the checks."}
	case a.MediaType == "":
		return Invalid{Field: "media_type", Sentence: "Say which media type the checks are, as the printer names it: stationery-preprinted on most printers."}
	case abs(a.Offset.X) > maxOffset || abs(a.Offset.Y) > maxOffset:
		return Invalid{Field: "offset", Sentence: fmt.Sprintf("An offset is at most %g mm either way. More than that is a sheet fed the wrong way, not a printer a little off.", maxOffset)}
	}

	if _, err := layoutbus.Load(a.Stock); err != nil {
		return Invalid{Field: "stock", Sentence: "Choose the check stock."}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.store.Load()
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(s.Printers, func(p Printer) bool { return p.Name == a.Printer }) {
		return Invalid{Field: "printer", Sentence: "Choose the printer the checks print on."}
	}

	at := slices.IndexFunc(s.Accounts, func(x Account) bool { return x.Key == a.Key })

	switch {
	case isNew && at >= 0:
		return Invalid{Field: "key", Sentence: fmt.Sprintf("There is already an account called %s on this computer.", a.Key)}
	case !isNew && at < 0:
		return ErrNoAccount
	case isNew:
		s.Accounts = append(s.Accounts, a)
	default:
		a.ReconcileID = s.Accounts[at].ReconcileID // set by signing in, not by this form
		s.Accounts[at] = a
	}

	return b.store.Save(s)
}

func printerURL(s string) bool {
	l := strings.ToLower(s)

	for _, prefix := range []string{"ipp://", "ipps://", "file:"} {
		if strings.HasPrefix(l, prefix) && len(l) > len(prefix) {
			return true
		}
	}

	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}

	return f
}
