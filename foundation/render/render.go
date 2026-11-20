// Package render draws a page of placed text and lines, in millimetres, two
// ways: as a PDF for a printer, and as a PNG for a screen or an image
// (docs/02-architecture.md, "Rendering: in Go, not Typst").
//
// Both drawings come from the same Doc and measure text with the same font
// file, so text that fits in one fits in the other. That is the property the
// live preview rests on: what a person sees before pressing Print is what the
// paper gets.
//
// What it can draw is deliberately small, because a check needs no more: text
// on a baseline, in one font at any size, in black or a grey; straight lines;
// outlined rectangles. Lines and rectangles are axis-aligned. Text is never
// kerned, in either drawing: a PDF shows a string's glyphs at their advances,
// and the PNG does the same rather than letting the font kern, or the two
// would place the end of a long payee differently.
//
// It knows nothing about checks.
package render

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// MM is millimetres; Pt is points (1/72 inch), the unit of a font size.
const (
	mmPerInch = 25.4
	ptPerInch = 72.0
)

// Doc is one page.
type Doc struct {
	Width, Height float64 // mm
	Items         []Item
}

// Item is something drawn on a page: a Text, a Line or a Rect.
type Item interface{ shift(dx, dy float64) Item }

// Text is a string on a baseline, starting at X.
type Text struct {
	X, Baseline float64 // mm from the page's top-left corner
	Size        float64 // points
	S           string
	Gray        float64 // 0 is black, 1 is white
}

// Line is a straight line, horizontal or vertical.
type Line struct {
	X1, Y1, X2, Y2 float64 // mm
	Width          float64 // mm
	Gray           float64
}

// Rect is an outlined rectangle.
type Rect struct {
	X, Y, W, H float64 // mm
	Width      float64 // mm, of the outline
	Gray       float64
}

func (t Text) shift(dx, dy float64) Item { t.X += dx; t.Baseline += dy; return t }
func (l Line) shift(dx, dy float64) Item {
	l.X1 += dx
	l.X2 += dx
	l.Y1 += dy
	l.Y2 += dy
	return l
}
func (r Rect) shift(dx, dy float64) Item { r.X += dx; r.Y += dy; return r }

// Shift moves everything on the page by dx, dy millimetres: a printer's
// calibration offset, applied to the paper and never to the layout.
func (d Doc) Shift(dx, dy float64) Doc {
	out := Doc{Width: d.Width, Height: d.Height, Items: make([]Item, len(d.Items))}
	for i, it := range d.Items {
		out.Items[i] = it.shift(dx, dy)
	}

	return out
}

// --- the font -------------------------------------------------------------------

// The one font: Go Regular, from golang.org/x/image/font/gofont, under the
// same BSD licence as Go. A proportional font rather than Go Mono, because a
// check's date box is narrow (docs/04-stock-and-layout.md) and a long month
// would not fit in a monospaced one.
var (
	fontOnce sync.Once
	theFont  *sfnt.Font
	fontErr  error
)

func loadFont() (*sfnt.Font, error) {
	fontOnce.Do(func() {
		theFont, fontErr = sfnt.Parse(goregular.TTF)
	})

	return theFont, fontErr
}

// FontFile is the font's TrueType file, as embedded in a PDF.
func FontFile() []byte { return goregular.TTF }

// advances is the advance of each character the font can draw, in font units,
// and the font's units per em. Measured once.
var (
	advOnce sync.Once
	advErr  error
	adv     map[rune]int
	upem    int
)

func advances() (map[rune]int, int, error) {
	advOnce.Do(func() {
		f, err := loadFont()
		if err != nil {
			advErr = err
			return
		}

		upem = int(f.UnitsPerEm())
		ppem := fixed.Int26_6(upem << 6) // one pixel per font unit
		var b sfnt.Buffer

		adv = make(map[rune]int, 224)
		for _, r := range winAnsiRunes() {
			gi, err := f.GlyphIndex(&b, r)
			if err != nil || gi == 0 {
				continue // not in the font
			}

			a, err := f.GlyphAdvance(&b, gi, ppem, font.HintingNone)
			if err != nil {
				advErr = fmt.Errorf("render: measuring %q: %w", r, err)
				return
			}

			adv[r] = int(a >> 6)
		}
	})

	return adv, upem, advErr
}

// ErrCharacter is a character this font, or a PDF's WinAnsi encoding, can't
// show.
type ErrCharacter struct{ Char rune }

func (e ErrCharacter) Error() string {
	return fmt.Sprintf("the character %q can't be printed on a check", e.Char)
}

// Printable reports the first character of s that can't be drawn, if any.
func Printable(s string) error {
	a, _, err := advances()
	if err != nil {
		return err
	}

	for _, r := range s {
		if _, ok := a[r]; !ok {
			return ErrCharacter{Char: r}
		}
	}

	return nil
}

// Width is how wide s is at size points, in millimetres, as both drawings
// place it.
func Width(s string, size float64) (float64, error) {
	a, em, err := advances()
	if err != nil {
		return 0, err
	}

	units := 0
	for _, r := range s {
		w, ok := a[r]
		if !ok {
			return 0, ErrCharacter{Char: r}
		}
		units += w
	}

	return float64(units) / float64(em) * size / ptPerInch * mmPerInch, nil
}

// errEmpty is returned for a page with no size.
var errEmpty = errors.New("render: the page has no size")
