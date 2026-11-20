package layoutbus_test

import (
	"bytes"
	"errors"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/business/domain/layout/layoutbus"
	"github.com/jroedel/reconcile-checks/foundation/render"
)

// update rewrites the golden files: `make golden`, and only on purpose. A
// golden that changed is a check that prints somewhere else; the pull request
// says what moved and why (CLAUDE.md, section 3).
var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const stock = "walmart-voucher-top"

// margin is the M612's unprintable edge, 4.23 mm, rounded up: nothing this
// program prints may lie in it.
const margin = 4.3

func load(t *testing.T) layoutbus.Layout {
	t.Helper()

	l, err := layoutbus.Load(stock)
	if err != nil {
		t.Fatal(err)
	}

	return l
}

// An invented check. Never a real payee, amount or memo (CLAUDE.md, "This
// repository is public").
func sample() layoutbus.Check {
	return layoutbus.Check{
		Number:   1043,
		Date:     time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Payee:    "Payee A Plumbing",
		Amount:   123450,
		Memo:     "Kitchen drain repair",
		StubNote: "Invoice 8812\nVisits on Nov 3, 10, 17 and 24",
		Last4:    "1234",
	}
}

func TestTheLayoutsLoad(t *testing.T) {
	for _, name := range layoutbus.Names() {
		if _, err := layoutbus.Load(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	if _, err := layoutbus.Load("no-such-stock"); err == nil {
		t.Error("an unknown layout loaded")
	}
}

// Every mark on the paper lies on the page and outside the printer's margin.
func TestEverythingPrintedIsWithinThePrintableArea(t *testing.T) {
	l := load(t)

	paper, err := l.Paper(sample())
	if err != nil {
		t.Fatal(err)
	}

	within(t, "paper", paper)
	within(t, "calibration page", l.Calibration())
}

func within(t *testing.T, what string, d render.Doc) {
	t.Helper()

	ok := func(x, y float64) bool {
		return x >= margin && x <= d.Width-margin && y >= margin && y <= d.Height-margin
	}

	for _, it := range d.Items {
		switch it := it.(type) {
		case render.Text:
			w, err := render.Width(it.S, it.Size)
			if err != nil {
				t.Fatal(err)
			}

			if !ok(it.X, it.Baseline) || !ok(it.X+w, it.Baseline) {
				t.Errorf("%s: %q at %.1f, %.1f (to %.1f) is outside the printable area", what, it.S, it.X, it.Baseline, it.X+w)
			}

		case render.Line:
			if !ok(it.X1, it.Y1) || !ok(it.X2, it.Y2) {
				t.Errorf("%s: a line %+v is outside the printable area", what, it)
			}

		case render.Rect:
			if !ok(it.X, it.Y) || !ok(it.X+it.W, it.Y+it.H) {
				t.Errorf("%s: a box %+v is outside the printable area", what, it)
			}
		}
	}
}

func TestTheAmountIsPaddedAndTheWordsFilled(t *testing.T) {
	l := load(t)

	paper, err := l.Paper(sample())
	if err != nil {
		t.Fatal(err)
	}

	var amount, words string
	for _, it := range paper.Items {
		if tx, ok := it.(render.Text); ok {
			switch {
			case strings.HasSuffix(tx.S, "1,234.50") && strings.HasPrefix(tx.S, "*"):
				amount = tx.S
			case strings.HasPrefix(tx.S, "One thousand two hundred thirty-four and 50/100 *"):
				words = tx.S
			}
		}
	}

	if amount == "" {
		t.Error("no amount padded with * in front of it")
	}

	if words == "" || !strings.HasSuffix(words, "***") {
		t.Errorf("the words are %q; want them followed by a run of *", words)
	}

	// The fill reaches the end of the line, and not past it.
	w, _ := render.Width(words, l.Size)
	if w > l.Fields.Words.Width || w < l.Fields.Words.Width-3 {
		t.Errorf("the filled words are %.1f mm on a %.1f mm line", w, l.Fields.Words.Width)
	}
}

func TestAPayeeTooLongIsRefusedWithHowManyFit(t *testing.T) {
	l := load(t)

	c := sample()
	c.Payee = strings.Repeat("Payee with a very long name ", 8)

	_, err := l.Paper(c)

	tl, ok := errors.AsType[layoutbus.TooLong](err)
	if !ok || tl.Field != "payee" || tl.Fits < 40 || tl.Fits >= len(c.Payee) {
		t.Errorf("err = %v; want the payee refused, with somewhere over 40 characters fitting", err)
	}
}

func TestALongPayeeShrinksBeforeItIsRefused(t *testing.T) {
	l := load(t)

	c := sample()
	c.Payee = strings.Repeat("x", 20)
	for {
		w, _ := render.Width(c.Payee+"x", l.Size)
		if w > l.Fields.Payee.Width {
			break
		}
		c.Payee += "x"
	}
	c.Payee += "xxx" // too wide at the full size, not at the smallest

	paper, err := l.Paper(c)
	if err != nil {
		t.Fatalf("a payee a little too long at full size was refused: %v", err)
	}

	for _, it := range paper.Items {
		// The check's own payee line; the stubs' is wider and may not need to shrink.
		if tx, ok := it.(render.Text); ok && tx.S == c.Payee && tx.X == l.Fields.Payee.X && tx.Size >= l.Size {
			t.Errorf("the payee was printed at %g pt; want it smaller than %g", tx.Size, l.Size)
		}
	}
}

func TestACharacterTheFontLacksIsRefused(t *testing.T) {
	l := load(t)

	c := sample()
	c.Payee = "Łukasz Nowak"

	_, err := l.Paper(c)
	if bc, ok := errors.AsType[layoutbus.BadCharacter](err); !ok || bc.Char != 'Ł' || bc.Field != "payee" {
		t.Errorf("err = %v; want the Ł in the payee refused", err)
	}

	c.Payee = "José Núñez"
	if _, err := l.Paper(c); err != nil {
		t.Errorf("a Spanish name was refused: %v", err)
	}
}

func TestADateTooWideIsWrittenInFigures(t *testing.T) {
	l := load(t)
	l.Fields.Date.Width = 22 // narrower than "September 30, 2026", wide enough for figures

	c := sample()
	c.Date = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	paper, err := l.Paper(c)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, it := range paper.Items {
		if tx, ok := it.(render.Text); ok && tx.S == "09/30/2026" && tx.X == l.Fields.Date.X {
			found = true
		}
	}

	if !found {
		t.Error("the date was not written as 09/30/2026")
	}
}

func TestTooManyStubNoteLinesAreRefused(t *testing.T) {
	l := load(t)

	c := sample()
	c.StubNote = strings.Repeat("a line\n", l.Stub.Note.Lines+1)

	if _, err := l.Paper(c); err == nil {
		t.Error("a stub note with too many lines was accepted")
	}
}

func TestTheImageIsTheFaceAndSaysVoid(t *testing.T) {
	l := load(t)

	c := sample()
	c.Void = true

	doc, area, err := l.Image(c)
	if err != nil {
		t.Fatal(err)
	}

	if area.H != l.Face.Height {
		t.Errorf("the image is %g mm tall, want the face's %g", area.H, l.Face.Height)
	}

	var void, masked bool
	for _, it := range doc.Items {
		if tx, ok := it.(render.Text); ok {
			void = void || tx.S == "VOID"
			masked = masked || tx.S == "XXXXXX1234"
		}
	}

	if !void || !masked {
		t.Errorf("void %v, account masked %v; want both", void, masked)
	}
}

// --- golden files ---------------------------------------------------------------------

func TestGoldenPaper(t *testing.T) {
	l := load(t)

	paper, err := l.Paper(sample())
	if err != nil {
		t.Fatal(err)
	}

	content, err := render.Content(paper)
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "paper.golden.txt", []byte(content))
}

func TestGoldenCalibration(t *testing.T) {
	content, err := render.Content(load(t).Calibration())
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "calibration.golden.txt", []byte(content))
}

// The image is compared by its pixels, not its bytes: the same picture
// encoded by another Go release is still the same picture.
func TestGoldenImage(t *testing.T) {
	doc, area, err := load(t).Image(sample())
	if err != nil {
		t.Fatal(err)
	}

	img, err := render.PNG(doc, 150, area)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "image.golden.png")

	if *update {
		var b bytes.Buffer
		png.Encode(&b, img)
		write(t, path, b.Bytes())

		return
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run make golden to make it)", err)
	}
	defer f.Close()

	want, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	if diff := pixelsDiffer(img, want); diff != "" {
		got := filepath.Join(t.TempDir(), "image.png")
		var b bytes.Buffer
		png.Encode(&b, img)
		os.WriteFile(got, b.Bytes(), 0o600)

		t.Errorf("the check image changed: %s. Look at %s; if the change is meant, run make golden and say in the pull request what moved", diff, got)
	}
}

func pixelsDiffer(a, b image.Image) string {
	if a.Bounds() != b.Bounds() {
		return "its size changed from " + b.Bounds().String() + " to " + a.Bounds().String()
	}

	n := 0
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			r1, g1, b1, a1 := a.At(x, y).RGBA()
			r2, g2, b2, a2 := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				n++
			}
		}
	}

	if n == 0 {
		return ""
	}

	return strconv.Itoa(n) + " pixels differ"
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)

	if *update {
		write(t, path, got)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run make golden to make it)", err)
	}

	if !bytes.Equal(got, want) {
		gotLines := strings.Split(string(got), "\n")
		wantLines := strings.Split(string(want), "\n")

		for i := range max(len(gotLines), len(wantLines)) {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}

			if g != w {
				t.Errorf("%s changed, first at line %d:\n got %s\nwant %s\nIf the change is meant, run make golden and say in the pull request what moved.", name, i+1, g, w)
				return
			}
		}
	}
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
