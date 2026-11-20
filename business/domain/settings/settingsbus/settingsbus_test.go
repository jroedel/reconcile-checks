package settingsbus_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jroedel/reconcile-checks/business/domain/check/checkbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/stores/settingsfile"
)

func setup(t *testing.T, existing string) (*settingsbus.Business, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if existing != "" {
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return settingsbus.New(settingsfile.New(path)), path
}

var printer = settingsbus.Printer{Name: "Office M612", URL: "ipps://printer.local/ipp/print", Pin: strings.Repeat("ab", 32)}

// An invented account. Never a real one (CLAUDE.md, "This repository is
// public").
func account() settingsbus.Account {
	return settingsbus.Account{
		Key: "checking-a", Name: "Checking A", Last4: "1234",
		Stock: "walmart-voucher-top", Printer: "Office M612",
		Tray: "tray-1", MediaType: "stationery-preprinted", Feed: "face down, top edge in first",
		Offset: settingsbus.Offset{X: 0.4, Y: -0.6},
	}
}

func TestSettingsAreKeptBesideTheRestOfTheConfig(t *testing.T) {
	b, path := setup(t, "# by hand\nport = 7462\nclose_after = \"5m\"\n")

	if err := b.SavePrinter("", printer); err != nil {
		t.Fatal(err)
	}

	if err := b.SaveAccount(account(), true); err != nil {
		t.Fatal(err)
	}

	s, err := b.Settings()
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Printers) != 1 || s.Printers[0] != printer {
		t.Errorf("printers %+v", s.Printers)
	}

	want := account()
	want.Ceiling = checkbus.DefaultCeiling
	if len(s.Accounts) != 1 || s.Accounts[0] != want {
		t.Errorf("accounts\n %+v\nwant\n %+v", s.Accounts, want)
	}

	// What main reads from the same file is still there.
	var rest struct {
		Port       int    `toml:"port"`
		CloseAfter string `toml:"close_after"`
	}

	if _, err := toml.DecodeFile(path, &rest); err != nil || rest.Port != 7462 || rest.CloseAfter != "5m" {
		t.Errorf("the rest of the config is %+v (%v)", rest, err)
	}

	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 && os.Getenv("OS") != "Windows_NT" {
		t.Errorf("the config is readable by others: %v", info.Mode())
	}
}

func TestAPrinterIsNamedAndReachedByIPP(t *testing.T) {
	b, _ := setup(t, "")

	for _, p := range []settingsbus.Printer{
		{Name: "", URL: "ipp://printer.local/ipp/print"},
		{Name: "A", URL: "socket://192.0.2.1:9100"},
		{Name: "A", URL: "http://printer.local"},
		{Name: "A", URL: "ipp://"},
	} {
		if _, ok := errors.AsType[settingsbus.Invalid](b.SavePrinter("", p)); !ok {
			t.Errorf("%+v was accepted", p)
		}
	}

	if err := b.SavePrinter("", printer); err != nil {
		t.Fatal(err)
	}

	if _, ok := errors.AsType[settingsbus.Invalid](b.SavePrinter("", printer)); !ok {
		t.Error("a second printer by the same name was accepted")
	}
}

func TestAnAccountIsRefusedWhatItCantPrintWith(t *testing.T) {
	cases := map[string]func(*settingsbus.Account){
		"key":        func(a *settingsbus.Account) { a.Key = "Checking A" },
		"name":       func(a *settingsbus.Account) { a.Name = " " },
		"last4":      func(a *settingsbus.Account) { a.Last4 = "123456789" },
		"stock":      func(a *settingsbus.Account) { a.Stock = "no-such-stock" },
		"printer":    func(a *settingsbus.Account) { a.Printer = "Another" },
		"tray":       func(a *settingsbus.Account) { a.Tray = "" },
		"media_type": func(a *settingsbus.Account) { a.MediaType = "" },
		"offset":     func(a *settingsbus.Account) { a.Offset.Y = 12 },
	}

	for field, change := range cases {
		t.Run(field, func(t *testing.T) {
			b, _ := setup(t, "")
			if err := b.SavePrinter("", printer); err != nil {
				t.Fatal(err)
			}

			a := account()
			change(&a)

			inv, ok := errors.AsType[settingsbus.Invalid](b.SaveAccount(a, true))
			if !ok || inv.Field != field {
				t.Errorf("err = %v; want the %s refused", inv, field)
			}
		})
	}
}

func TestAnAccountsKeyIsKept(t *testing.T) {
	b, _ := setup(t, "")
	if err := b.SavePrinter("", printer); err != nil {
		t.Fatal(err)
	}

	a := account()
	if err := b.SaveAccount(a, true); err != nil {
		t.Fatal(err)
	}

	if _, ok := errors.AsType[settingsbus.Invalid](b.SaveAccount(a, true)); !ok {
		t.Error("a second account with the same key was added")
	}

	a.Offset.X = 1.5
	if err := b.SaveAccount(a, false); err != nil {
		t.Fatal(err)
	}

	got, _ := b.Account("checking-a")
	if got.Offset.X != 1.5 {
		t.Errorf("the offset is %g after an edit", got.Offset.X)
	}

	other := account()
	other.Key = "checking-b"
	if err := b.SaveAccount(other, false); !errors.Is(err, settingsbus.ErrNoAccount) {
		t.Errorf("editing an account that isn't there: %v", err)
	}
}

func TestAPrinterInUseIsNotRemovedAndARenameIsFollowed(t *testing.T) {
	b, _ := setup(t, "")
	if err := b.SavePrinter("", printer); err != nil {
		t.Fatal(err)
	}

	if err := b.SaveAccount(account(), true); err != nil {
		t.Fatal(err)
	}

	if _, ok := errors.AsType[settingsbus.Invalid](b.RemovePrinter("Office M612")); !ok {
		t.Error("a printer an account prints on was removed")
	}

	renamed := printer
	renamed.Name = "Front office"
	if err := b.SavePrinter("Office M612", renamed); err != nil {
		t.Fatal(err)
	}

	if a, _ := b.Account("checking-a"); a.Printer != "Front office" {
		t.Errorf("the account prints on %q after the printer was renamed", a.Printer)
	}
}

func TestTheRegisterSeesTheAccount(t *testing.T) {
	b, _ := setup(t, "")
	if err := b.SavePrinter("", printer); err != nil {
		t.Fatal(err)
	}

	a := account()
	a.Ceiling = 2_500_00
	if err := b.SaveAccount(a, true); err != nil {
		t.Fatal(err)
	}

	got, err := b.CheckAccount("checking-a")
	if err != nil || got != (checkbus.Account{Key: "checking-a", Last4: "1234", Stock: "walmart-voucher-top", Ceiling: 2_500_00, OffsetX: 0.4, OffsetY: -0.6}) {
		t.Errorf("CheckAccount: %+v (%v)", got, err)
	}

	if _, err := b.CheckAccount("savings"); !errors.Is(err, checkbus.ErrUnknownAccount) {
		t.Errorf("an unknown account: %v", err)
	}
}
