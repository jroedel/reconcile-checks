// Package layoutbus places a check's fields on its stock's layout
// (docs/04-stock-and-layout.md): what is printed where, at what size, and what
// doesn't fit. It makes three pages from one placement:
//
//   - Paper: the fields alone, for the printer. The stock already carries
//     everything else. The computer's calibration offset is the caller's to
//     apply (render.Doc.Shift): it describes the printer, not the paper.
//   - Image: the check face with the stock drawn in, for Reconcile and the
//     live preview (docs/07-the-image.md). No offset; "VOID" across it when
//     the check was voided.
//   - Calibration: the layout's boxes outlined on plain paper, with corner
//     crosses and rulers, to hold against a check.
//
// The rules about which fields a check must have (a payee, an amount over
// zero) are checkbus's. This package says only whether what it was given fits.
package layoutbus

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jroedel/reconcile-checks/business/types/money"
	"github.com/jroedel/reconcile-checks/foundation/render"
	"github.com/jroedel/reconcile-checks/layouts"
)

// Check is what is written on one check, as this package needs it.
type Check struct {
	Number   int
	Date     time.Time // only the day is used
	Payee    string
	Amount   money.Cents
	Memo     string
	StubNote string // several lines, separated by "\n"
	Last4    string // the account's last four, for the image
	Void     bool
}

// Layout is one stock's layout file (layouts/*.toml).
type Layout struct {
	Name    string  `toml:"name"`
	Size    float64 `toml:"size"`
	MinSize float64 `toml:"min_size"`

	Page struct{ Width, Height float64 } `toml:"page"`
	Face struct {
		X, Y          float64
		Width, Height float64
	} `toml:"face"`

	Fields struct {
		Date, Payee, Amount, Words, Memo Field
	} `toml:"fields"`

	Stubs []struct{ Top float64 } `toml:"stubs"`
	Stub  struct {
		LabelSize                       float64 `toml:"label_size"`
		Date, Amount, Payee, Memo, Note Field
	} `toml:"stub"`

	Stock struct {
		Frame struct {
			X, Y          float64
			Width, Height float64
		} `toml:"frame"`
		Lines      []struct{ X1, Y1, X2, Y2 float64 } `toml:"lines"`
		Labels     []Label                            `toml:"labels"`
		Number     Label                              `toml:"number"`
		StubNumber Label                              `toml:"stub_number"` // on each stub, from its top
		Account    Label                              `toml:"account"`
	} `toml:"stock"`
}

// Field is a place text may be printed: from X, on Baseline, at most Width.
type Field struct {
	X        float64 `toml:"x"`
	Baseline float64 `toml:"baseline"`
	Width    float64 `toml:"width"`

	// On a stub: the label printed before it.
	Label  string  `toml:"label"`
	LabelX float64 `toml:"label_x"`

	// A field of several lines: at most Lines, Gap apart.
	Lines int     `toml:"lines"`
	Gap   float64 `toml:"gap"`
}

// Label is fixed text drawn on the image's stock.
type Label struct {
	S        string  `toml:"s"`
	X        float64 `toml:"x"`
	Baseline float64 `toml:"baseline"`
	Size     float64 `toml:"size"`
}

// Names lists the layouts the program carries.
func Names() []string {
	entries, _ := fs.ReadDir(layouts.FS, ".")

	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
			names = append(names, n)
		}
	}

	return names
}

// Load reads a layout by name, and checks that it describes a usable page.
func Load(name string) (Layout, error) {
	var l Layout

	if !slices.Contains(Names(), name) {
		return l, fmt.Errorf("there is no stock layout called %q; the program has %s", name, strings.Join(Names(), ", "))
	}

	b, err := fs.ReadFile(layouts.FS, path.Join(".", name+".toml"))
	if err != nil {
		return l, err
	}

	md, err := toml.NewDecoder(bytes.NewReader(b)).Decode(&l)
	if err != nil {
		return l, fmt.Errorf("the layout %s: %w", name, err)
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return l, fmt.Errorf("the layout %s has settings this program doesn't know: %v", name, undecoded)
	}

	if l.Page.Width <= 0 || l.Page.Height <= 0 || l.Size <= 0 || l.MinSize <= 0 || l.MinSize > l.Size {
		return l, fmt.Errorf("the layout %s has no usable page size or text size", name)
	}

	return l, nil
}

// --- what doesn't fit ---------------------------------------------------------------

// TooLong is a field whose text doesn't fit, even at the smallest size. Fits is
// how many characters of it would.
type TooLong struct {
	Field string
	Fits  int
}

func (e TooLong) Error() string {
	return fmt.Sprintf("The %s is too long to print: about %d characters fit.", e.Field, e.Fits)
}

// BadCharacter is a character the check's font can't print.
type BadCharacter struct {
	Field string
	Char  rune
}

func (e BadCharacter) Error() string {
	return fmt.Sprintf("The %s has %q, which can't be printed on a check. Leave it out, or use a plain letter instead.", e.Field, e.Char)
}

// fit places s in f, at the layout's size or smaller down to its minimum.
func (l Layout) fit(name string, f Field, s string) (render.Text, error) {
	if err := render.Printable(s); err != nil {
		if ce, ok := errors.AsType[render.ErrCharacter](err); ok {
			return render.Text{}, BadCharacter{Field: name, Char: ce.Char}
		}

		return render.Text{}, err
	}

	for size := l.Size; size >= l.MinSize-1e-9; size -= 0.5 {
		w, err := render.Width(s, size)
		if err != nil {
			return render.Text{}, err
		}

		if w <= f.Width {
			return render.Text{X: f.X, Baseline: f.Baseline, Size: size, S: s}, nil
		}
	}

	return render.Text{}, TooLong{Field: name, Fits: l.fits(f.Width, s)}
}

// fits counts how many characters of s fit in width at the smallest size.
func (l Layout) fits(width float64, s string) int {
	n := 0
	for i := range s {
		if w, err := render.Width(s[:i], l.MinSize); err != nil || w > width {
			break
		}
		n = len([]rune(s[:i]))
	}

	if w, err := render.Width(s, l.MinSize); err == nil && w <= width {
		n = len([]rune(s))
	}

	return n
}

// fill pads s with * up to width at size: on the left for figures (fillLeft),
// on the right, after a space, for words. Nothing can then be written in
// front of an amount, or after its words.
func fill(s string, width, size float64, fillLeft bool) (string, error) {
	star, err := render.Width("*", size)
	if err != nil {
		return "", err
	}

	if !fillLeft {
		s += " "
	}

	w, err := render.Width(s, size)
	if err != nil {
		return "", err
	}

	n := int(math.Floor((width - w) / star))
	if n < 0 {
		n = 0
	}

	if fillLeft {
		return strings.Repeat("*", n) + s, nil
	}

	return s + strings.Repeat("*", n), nil
}

// --- placing the fields -------------------------------------------------------------

// date writes the day in words, "November 20, 2026", or in figures,
// "11/20/2026", when the words don't fit the box.
func (l Layout) date(name string, f Field, d time.Time) (render.Text, error) {
	t, err := l.fit(name, f, d.Format("January 2, 2006"))
	if _, long := errors.AsType[TooLong](err); long {
		return l.fit(name, f, d.Format("01/02/2006"))
	}

	return t, err
}

// face places the fields printed on the check itself.
func (l Layout) face(c Check) ([]render.Item, error) {
	var items []render.Item

	date, err := l.date("date", l.Fields.Date, c.Date)
	if err != nil {
		return nil, err
	}
	items = append(items, date)

	payee, err := l.fit("payee", l.Fields.Payee, c.Payee)
	if err != nil {
		return nil, err
	}
	items = append(items, payee)

	amount, err := l.fit("amount", l.Fields.Amount, c.Amount.Figures())
	if err != nil {
		return nil, err
	}
	amount.S, err = fill(amount.S, l.Fields.Amount.Width, amount.Size, true)
	if err != nil {
		return nil, err
	}
	items = append(items, amount)

	words, err := c.Amount.Words()
	if err != nil {
		return nil, TooLong{Field: "amount in words", Fits: 0}
	}

	// The words must fit with room for a space and at least one star.
	w, err := l.fit("amount in words", l.Fields.Words, words+" *")
	if err != nil {
		return nil, err
	}
	w.S, err = fill(words, l.Fields.Words.Width, w.Size, false)
	if err != nil {
		return nil, err
	}
	items = append(items, w)

	if c.Memo != "" {
		memo, err := l.fit("memo", l.Fields.Memo, c.Memo)
		if err != nil {
			return nil, err
		}
		items = append(items, memo)
	}

	return items, nil
}

// stubs places the fields printed on each stub, with their labels. The
// check number is not among them: it is pre-printed on every stub.
func (l Layout) stubs(c Check) ([]render.Item, error) {
	var lines []string
	if strings.TrimSpace(c.StubNote) != "" {
		lines = strings.Split(strings.TrimRight(c.StubNote, "\n"), "\n")
	}

	if n := l.Stub.Note.Lines; len(lines) > n {
		return nil, TooLong{Field: fmt.Sprintf("stub note (at most %d lines)", n), Fits: 0}
	}

	var items []render.Item

	for _, s := range l.Stubs {
		at := func(f Field) Field { f.Baseline += s.Top; return f }
		label := func(f Field) render.Item {
			return render.Text{X: f.LabelX, Baseline: s.Top + f.Baseline, Size: l.Stub.LabelSize, S: f.Label, Gray: 0.35}
		}

		date, err := l.date("date", at(l.Stub.Date), c.Date)
		if err != nil {
			return nil, err
		}

		amount, err := l.fit("amount", at(l.Stub.Amount), c.Amount.String())
		if err != nil {
			return nil, err
		}

		payee, err := l.fit("payee", at(l.Stub.Payee), c.Payee)
		if err != nil {
			return nil, err
		}

		items = append(items, label(l.Stub.Date), date, label(l.Stub.Amount), amount, label(l.Stub.Payee), payee)

		if c.Memo != "" {
			memo, err := l.fit("memo", at(l.Stub.Memo), c.Memo)
			if err != nil {
				return nil, err
			}
			items = append(items, label(l.Stub.Memo), memo)
		}

		if len(lines) > 0 {
			items = append(items, label(l.Stub.Note))
		}

		for i, line := range lines {
			f := at(l.Stub.Note)
			f.Baseline += float64(i) * l.Stub.Note.Gap

			t, err := l.fit("stub note", f, line)
			if err != nil {
				return nil, err
			}
			items = append(items, t)
		}
	}

	return items, nil
}

// Paper is what the printer prints: the fields alone, on the whole page.
func (l Layout) Paper(c Check) (render.Doc, error) {
	face, err := l.face(c)
	if err != nil {
		return render.Doc{}, err
	}

	stubs, err := l.stubs(c)
	if err != nil {
		return render.Doc{}, err
	}

	return render.Doc{Width: l.Page.Width, Height: l.Page.Height, Items: append(face, stubs...)}, nil
}

// stock is the paper's own printing, drawn for the image and the preview.
func (l Layout) stock(c Check) []render.Item {
	const ink = 0.35 // the stock's grey, lighter than what is written on it

	fr := l.Stock.Frame
	items := []render.Item{render.Rect{X: fr.X, Y: fr.Y, W: fr.Width, H: fr.Height, Width: 0.5, Gray: ink}}

	for _, ln := range l.Stock.Lines {
		items = append(items, render.Line{X1: ln.X1, Y1: ln.Y1, X2: ln.X2, Y2: ln.Y2, Width: 0.25, Gray: ink})
	}

	for _, lb := range l.Stock.Labels {
		items = append(items, render.Text{X: lb.X, Baseline: lb.Baseline, Size: lb.Size, S: lb.S, Gray: ink})
	}

	if c.Number > 0 {
		n := l.Stock.Number
		items = append(items, render.Text{X: n.X, Baseline: n.Baseline, Size: n.Size, S: strconv.Itoa(c.Number), Gray: ink})
	}

	if c.Last4 != "" {
		a := l.Stock.Account
		items = append(items, render.Text{X: a.X, Baseline: a.Baseline, Size: a.Size, S: "XXXXXX" + c.Last4, Gray: ink})
	}

	return items
}

// Image is the check as its image shows it: the face, with the stock drawn
// in, the fields, and "VOID" across a voided check. Area is the face, to
// draw.
func (l Layout) Image(c Check) (render.Doc, render.Area, error) {
	face, err := l.face(c)
	if err != nil {
		return render.Doc{}, render.Area{}, err
	}

	items := append(l.stock(c), face...)

	if c.Void {
		items = append(items, render.Text{X: l.Face.X + l.Face.Width/2 - 45, Baseline: l.Face.Y + l.Face.Height/2 + 12, Size: 72, S: "VOID", Gray: 0.25})
	}

	doc := render.Doc{Width: l.Page.Width, Height: l.Page.Height, Items: items}
	area := render.Area{X: l.Face.X, Y: l.Face.Y, W: l.Face.Width, H: l.Face.Height}

	return doc, area, nil
}

// Preview is the whole page as it will come out: the stock drawn in on the
// face, the fields on the face and on both stubs, and the perforations.
func (l Layout) Preview(c Check) (render.Doc, error) {
	paper, err := l.Paper(c)
	if err != nil {
		return render.Doc{}, err
	}

	items := l.stock(c)
	for _, s := range l.Stubs {
		items = append(items, render.Line{X1: 0, Y1: s.Top, X2: l.Page.Width, Y2: s.Top, Width: 0.2, Gray: 0.7})

		if n := l.Stock.StubNumber; c.Number > 0 && n.Size > 0 {
			items = append(items, render.Text{X: n.X, Baseline: s.Top + n.Baseline, Size: n.Size, S: strconv.Itoa(c.Number), Gray: 0.35})
		}
	}

	paper.Items = append(items, paper.Items...)

	return paper, nil
}

// Calibration is the calibration page, for plain paper: a box where each field
// may print, named; the stock's own rules and the perforations; and rulers
// along the top and left, numbered in millimetres. Held to the light against a
// check, each rule printed here should lie on the check's printed rule; how
// far it is off, read on the rulers, is the printer's offset.
//
// No crosses at the check frame's corners: they fall within about 4 mm of the
// paper's edge, which a printer does not print.
func (l Layout) Calibration() render.Doc {
	const thin = 0.2

	var items []render.Item

	box := func(name string, f Field, size float64) {
		h := size / 72 * 25.4 // one em, in mm
		items = append(items,
			render.Rect{X: f.X, Y: f.Baseline - h*0.8, W: f.Width, H: h, Width: thin},
			render.Text{X: f.X + 0.8, Baseline: f.Baseline - 0.6, Size: 6, S: name, Gray: 0.3},
		)
	}

	box("date", l.Fields.Date, l.Size)
	box("pay to", l.Fields.Payee, l.Size)
	box("amount", l.Fields.Amount, l.Size)
	box("amount in words", l.Fields.Words, l.Size)
	box("memo", l.Fields.Memo, l.Size)

	for _, s := range l.Stubs {
		for _, sf := range []struct {
			name string
			f    Field
		}{{"date", l.Stub.Date}, {"amount", l.Stub.Amount}, {"pay to", l.Stub.Payee}, {"memo", l.Stub.Memo}, {"note", l.Stub.Note}} {
			f := sf.f
			f.Baseline += s.Top
			box(sf.name, f, l.Size)
		}

		items = append(items, render.Line{X1: 8, Y1: s.Top, X2: l.Page.Width - 8, Y2: s.Top, Width: thin, Gray: 0.5})
	}

	for _, ln := range l.Stock.Lines {
		items = append(items, render.Line{X1: ln.X1, Y1: ln.Y1, X2: ln.X2, Y2: ln.Y2, Width: thin, Gray: 0.5})
	}

	// Rulers, inside the printer's margin (about 4.2 mm on the M612): along
	// the top at 8 mm, and down the left at 8 mm.
	const at = 8.0
	for mm := 10; float64(mm) <= l.Page.Width-10; mm++ {
		items = append(items, tick(float64(mm), at, true, mm))
		if mm%10 == 0 {
			items = append(items, render.Text{X: float64(mm) + 0.4, Baseline: at + 4.6, Size: 5, S: strconv.Itoa(mm), Gray: 0.3})
		}
	}
	for mm := 15; float64(mm) <= l.Page.Height-10; mm++ {
		items = append(items, tick(at, float64(mm), false, mm))
		if mm%10 == 0 {
			items = append(items, render.Text{X: at + 3.6, Baseline: float64(mm) + 0.9, Size: 5, S: strconv.Itoa(mm), Gray: 0.3})
		}
	}

	items = append(items,
		render.Text{X: 40, Baseline: 16, Size: 8, Gray: 0.3,
			S: "Calibration page (" + l.Name + "). Print on plain paper, then hold it to the light against a check."},
		render.Text{X: 40, Baseline: 19.5, Size: 8, Gray: 0.3,
			S: "Each rule here should lie on the check's own rule. How far off it is, read on the rulers, is this printer's offset."})

	return render.Doc{Width: l.Page.Width, Height: l.Page.Height, Items: items}
}

// tick is one ruler mark at mm: long every 10, middling every 5.
func tick(x, y float64, horizontal bool, mm int) render.Item {
	n := 1.0
	switch {
	case mm%10 == 0:
		n = 3
	case mm%5 == 0:
		n = 2
	}

	if horizontal {
		return render.Line{X1: x, Y1: y, X2: x, Y2: y + n, Width: 0.15}
	}

	return render.Line{X1: x, Y1: y, X2: x + n, Y2: y, Width: 0.15}
}
