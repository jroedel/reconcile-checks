package printbus_test

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/domain/print/printbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/stores/settingsfile"
	"github.com/jroedel/reconcile-checks/foundation/ipp"
	"github.com/jroedel/reconcile-checks/foundation/ipp/ipptest"
	"github.com/jroedel/reconcile-checks/foundation/render"
)

func setup(t *testing.T, printerURL string, offset settingsbus.Offset) *printbus.Business {
	t.Helper()

	s := settingsbus.New(settingsfile.New(filepath.Join(t.TempDir(), "config.toml")))

	if err := s.SavePrinter("", settingsbus.Printer{
		Name: "Office", URL: printerURL, Tray: "tray-1", MediaType: "stationery-preprinted", Offset: offset,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveAccount(settingsbus.Account{
		Key: "checking-a", Name: "Checking A", Last4: "1234", Stock: "walmart-voucher-top", Printer: "Office",
	}, true); err != nil {
		t.Fatal(err)
	}

	return printbus.New(s, &ipp.Client{User: "reconcile-checks"})
}

// The calibration page goes out exactly as a check will: the printer's tray
// and media type, one-sided, unscaled, on the stock's paper, and shifted by
// the printer's offset. It needs a printer and a stock, and no account.
func TestTheCalibrationPageIsPrintedAsACheckWouldBe(t *testing.T) {
	printer := ipptest.New()
	defer printer.Close()

	offset := settingsbus.Offset{X: 1.5, Y: -0.5}
	b := setup(t, printer.URL, offset)

	sent, err := b.Calibrate(t.Context(), "Office", "walmart-voucher-top")
	if err != nil {
		t.Fatal(err)
	}

	if sent.Printer != "Office" || sent.Job != 1 {
		t.Errorf("sent %+v", sent)
	}

	got := printer.Printed()
	if len(got) != 1 {
		t.Fatalf("%d jobs printed", len(got))
	}

	for name, want := range map[string]string{
		"sides":                            "one-sided",
		"print-scaling":                    "none",
		"copies":                           "1",
		"media-col/media-source":           "tray-1",
		"media-col/media-type":             "stationery-preprinted",
		"media-col/media-size/x-dimension": "21590",
		"media-col/media-size/y-dimension": "27940",
	} {
		if v := got[0].Job[name]; !slices.Equal(v, []string{want}) {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}

	l, err := layoutbus.Load("walmart-voucher-top")
	if err != nil {
		t.Fatal(err)
	}

	want, err := render.PDF(l.Calibration().Shift(offset.X, offset.Y))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got[0].Document, want) {
		t.Error("the page printed is not the calibration page shifted by the offset")
	}

	st, err := b.Status(t.Context(), sent)
	if err != nil || !st.Done() {
		t.Errorf("status %+v (%v)", st, err)
	}
}

// A printer that won't honour the options prints nothing, and says why.
func TestAPrinterThatRefusesTheOptionsPrintsNothing(t *testing.T) {
	printer := ipptest.New()
	defer printer.Close()

	printer.Refuse("print-scaling")

	b := setup(t, printer.URL, settingsbus.Offset{})

	if _, err := b.Calibrate(t.Context(), "Office", "walmart-voucher-top"); err == nil {
		t.Fatal("the calibration page was sent to a printer that refused its options")
	}

	if n := len(printer.Printed()); n != 0 {
		t.Errorf("%d pages printed", n)
	}
}

// Trust keeps the certificate the printer presents.
func TestTrustPinsThePrinter(t *testing.T) {
	printer := ipptest.NewTLS()
	defer printer.Close()

	b := setup(t, printer.URL, settingsbus.Offset{})

	if _, err := b.Calibrate(t.Context(), "Office", "walmart-voucher-top"); err == nil {
		t.Fatal("printed over ipps with no pin")
	}

	if err := b.Trust(t.Context(), "Office"); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Calibrate(t.Context(), "Office", "walmart-voucher-top"); err != nil {
		t.Errorf("after Trust: %v", err)
	}
}
