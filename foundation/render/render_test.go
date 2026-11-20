package render_test

import (
	"errors"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/render"
)

func sample() render.Doc {
	return render.Doc{Width: 215.9, Height: 279.4, Items: []render.Item{
		render.Text{X: 20, Baseline: 30, Size: 11, S: "Pay to Peña (Ñandú) & Co. \\ 50% — “ok”"},
		render.Line{X1: 20, Y1: 32, X2: 120, Y2: 32, Width: 0.3},
		render.Rect{X: 10, Y: 10, W: 100, H: 40, Width: 0.5, Gray: 0.4},
	}}
}

// The PDF is one a strict checker accepts, and its text reads back as written
// -- which proves the font, the WinAnsi encoding and the escapes together.
func TestThePDFIsValidAndItsTextReadsBack(t *testing.T) {
	b, err := render.PDF(sample())
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "page.pdf")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := exec.LookPath("qpdf"); err == nil {
		if out, err := exec.Command("qpdf", "--check", path).CombinedOutput(); err != nil {
			t.Errorf("qpdf --check: %v\n%s", err, out)
		}
	} else {
		t.Log("qpdf is not installed; the PDF's structure was not checked")
	}

	if _, err := exec.LookPath("pdftotext"); err == nil {
		out, err := exec.Command("pdftotext", "-enc", "UTF-8", path, "-").Output()
		if err != nil {
			t.Fatalf("pdftotext: %v", err)
		}

		if want := "Pay to Peña (Ñandú) & Co. \\ 50% — “ok”"; !strings.Contains(string(out), want) {
			t.Errorf("pdftotext read %q, want %q", out, want)
		}
	} else {
		t.Log("pdftotext is not installed; the PDF's text was not read back")
	}
}

func TestTheSameDocMakesTheSameBytes(t *testing.T) {
	a, err := render.PDF(sample())
	if err != nil {
		t.Fatal(err)
	}

	b, err := render.PDF(sample())
	if err != nil {
		t.Fatal(err)
	}

	if string(a) != string(b) {
		t.Error("two PDFs of one page differ")
	}
}

func TestACharacterOutsideWinAnsiIsRefused(t *testing.T) {
	d := render.Doc{Width: 100, Height: 100, Items: []render.Item{render.Text{X: 1, Baseline: 10, Size: 10, S: "Łódź"}}}

	_, err := render.PDF(d)
	if ce, ok := errors.AsType[render.ErrCharacter](err); !ok || ce.Char != 'Ł' {
		t.Errorf("err = %v, want ErrCharacter for Ł", err)
	}

	if err := render.Printable("Łódź"); err == nil {
		t.Error("Printable accepted Ł")
	}

	if err := render.Printable("José Núñez"); err != nil {
		t.Errorf("Printable refused a Spanish name: %v", err)
	}
}

func TestWidthGrowsWithTheTextAndTheSize(t *testing.T) {
	w1, err := render.Width("Payee", 10)
	if err != nil {
		t.Fatal(err)
	}

	w2, _ := render.Width("Payee A", 10)
	w3, _ := render.Width("Payee", 20)

	if !(w1 > 0 && w2 > w1) {
		t.Errorf("widths %g, %g: a longer string should be wider", w1, w2)
	}

	if d := w3 - 2*w1; d > 1e-9 || d < -1e-9 {
		t.Errorf("at twice the size, %g mm; want twice %g", w3, w1)
	}
}

// The PNG puts ink where the Doc says, and nowhere it doesn't: a line drawn at
// 32 mm is dark in its row, and the page's corner stays white.
func TestThePNGDrawsWhereTheDocSays(t *testing.T) {
	d := sample()

	img, err := render.PNG(d, 200, d.Whole())
	if err != nil {
		t.Fatal(err)
	}

	px := func(mm float64) int { return int(mm / 25.4 * 200) }

	if c := color.GrayModel.Convert(img.At(px(70), px(32))).(color.Gray); c.Y > 64 {
		t.Errorf("the line at 32 mm is not drawn: gray %d", c.Y)
	}

	if c := color.GrayModel.Convert(img.At(2, 2)).(color.Gray); c.Y != 255 {
		t.Errorf("the corner is not white: %d", c.Y)
	}

	area := render.Area{X: 10, Y: 10, W: 100, H: 40}
	part, err := render.PNG(d, 200, area)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := part.Bounds().Dx(), px(100); got < want-1 || got > want+1 {
		t.Errorf("an area 100 mm wide is %d px at 200 dpi, want %d", got, want)
	}
}

func TestShiftMovesEverything(t *testing.T) {
	d := sample().Shift(1.5, -2)

	if tx := d.Items[0].(render.Text); tx.X != 21.5 || tx.Baseline != 28 {
		t.Errorf("text at %g, %g", tx.X, tx.Baseline)
	}

	if l := d.Items[1].(render.Line); l.X1 != 21.5 || l.Y2 != 30 {
		t.Errorf("line at %+v", l)
	}

	if orig := sample().Items[0].(render.Text); orig.X != 20 {
		t.Error("Shift changed the original")
	}
}
