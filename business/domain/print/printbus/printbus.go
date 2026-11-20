// Package printbus prints: a page goes to a printer, through the tray it
// takes checks in, as its media type for check stock, shifted by its
// offset, with every option that decides where ink lands stated in the job
// (docs/05-printing.md, "Options set by the program, every time"). A check
// goes to its account's printer.
//
// That is the calibration page, and a check: the register (checkbus) claims
// the number and makes the check's PDF, and this package sends it.
package printbus

import (
	"context"
	"fmt"

	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/business/domain/settings/settingsbus"
	"github.com/jroedel/reconcile-checks/foundation/ipp"
	"github.com/jroedel/reconcile-checks/foundation/render"
)

// Printer is the part of the IPP client this package uses.
type Printer interface {
	Attributes(ctx context.Context, p ipp.Printer) (ipp.Attributes, error)
	Print(ctx context.Context, p ipp.Printer, job ipp.Job, pdf []byte) (int, error)
	JobState(ctx context.Context, p ipp.Printer, id int) (ipp.JobStatus, error)
}

// Business prints.
type Business struct {
	settings *settingsbus.Business
	ipp      Printer
}

// New makes one.
func New(settings *settingsbus.Business, client Printer) *Business {
	return &Business{settings: settings, ipp: client}
}

// Sent is a job the printer accepted.
type Sent struct {
	Printer string // the printer's name in Settings
	Job     int    // the printer's id for the job
}

// Job is the print job for a page on a stock, through a printer: the options
// every check, and every calibration page, is printed with.
func Job(p settingsbus.Printer, l layoutbus.Layout, name string) ipp.Job {
	return ipp.Job{
		Name:    name,
		Format:  "application/pdf",
		Sides:   "one-sided",
		Source:  p.Tray,
		Type:    p.MediaType,
		Media:   l.Page.Media,
		Scaling: "none",
		Copies:  1,
	}
}

// Calibrate prints the calibration page for a stock on a printer
// (docs/04-stock-and-layout.md, "Alignment and calibration"): the stock's
// boxes and rules, and rulers, on plain paper, through the same tray with
// the same options as a check, and shifted by the printer's offset, so that
// holding it against a blank check shows where this printer would put the
// ink now. No account is needed: it is the printer being checked.
func (b *Business) Calibrate(ctx context.Context, printer, stock string) (Sent, error) {
	p, err := b.settings.Printer(printer)
	if err != nil {
		return Sent{}, err
	}

	l, err := layoutbus.Load(stock)
	if err != nil {
		return Sent{}, err
	}

	pdf, err := render.PDF(l.Calibration().Shift(p.Offset.X, p.Offset.Y))
	if err != nil {
		return Sent{}, err
	}

	return b.send(ctx, p, l, pdf, "Calibration page")
}

// Check prints a check's PDF as the register made it, the account's offset
// already in it (checkbus.Write). The job is named by the number alone: a
// printer's panel and job log are not where a payee belongs.
//
// An ipp.Uncertain error is a check that may have printed; anything else is
// one that certainly didn't.
func (b *Business) Check(ctx context.Context, key string, number int, pdf []byte) (Sent, error) {
	a, err := b.settings.Account(key)
	if err != nil {
		return Sent{}, err
	}

	l, err := layoutbus.Load(a.Stock)
	if err != nil {
		return Sent{}, err
	}

	p, err := b.settings.Printer(a.Printer)
	if err != nil {
		return Sent{}, err
	}

	return b.send(ctx, p, l, pdf, fmt.Sprintf("Check %d", number))
}

// send prints the PDF on the printer, with its options for checks.
func (b *Business) send(ctx context.Context, p settingsbus.Printer, l layoutbus.Layout, pdf []byte, name string) (Sent, error) {
	id, err := b.ipp.Print(ctx, ipp.Printer{URL: p.URL, Pin: p.Pin}, Job(p, l, name), pdf)
	if err != nil {
		return Sent{}, fmt.Errorf("%s: %w", p.Name, err)
	}

	return Sent{Printer: p.Name, Job: id}, nil
}

// Status is where a job is now.
func (b *Business) Status(ctx context.Context, s Sent) (ipp.JobStatus, error) {
	p, err := b.settings.Printer(s.Printer)
	if err != nil {
		return ipp.JobStatus{}, err
	}

	return b.ipp.JobState(ctx, ipp.Printer{URL: p.URL, Pin: p.Pin}, s.Job)
}

// Attributes is what the printer says about itself.
func (b *Business) Attributes(ctx context.Context, name string) (ipp.Attributes, error) {
	p, err := b.settings.Printer(name)
	if err != nil {
		return ipp.Attributes{}, err
	}

	return b.ipp.Attributes(ctx, ipp.Printer{URL: p.URL, Pin: p.Pin})
}

// Trust asks the printer for its certificate and keeps it as the printer's
// pin: when a printer is added, and when its certificate changed and the
// person says the change is expected.
func (b *Business) Trust(ctx context.Context, name string) error {
	p, err := b.settings.Printer(name)
	if err != nil {
		return err
	}

	attrs, err := b.ipp.Attributes(ctx, ipp.Printer{URL: p.URL})
	if err != nil {
		return err
	}

	p.Pin = attrs.Certificate

	return b.settings.SavePrinter(p.Name, p)
}
