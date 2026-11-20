package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Area is the part of a page to draw, in millimetres.
type Area struct{ X, Y, W, H float64 }

// Whole is the whole page.
func (d Doc) Whole() Area { return Area{0, 0, d.Width, d.Height} }

// PNG draws an area of the page at dpi dots per inch, on white.
//
// Text is drawn glyph by glyph at the same advances as the PDF's (see the
// package comment), with the font's own anti-aliasing. Lines and rectangles
// are filled pixel rectangles, at least one pixel wide, so a hairline never
// vanishes at a small preview.
func PNG(d Doc, dpi float64, area Area) (*image.NRGBA, error) {
	if area.W <= 0 || area.H <= 0 || dpi <= 0 {
		return nil, errEmpty
	}

	px := func(mm float64) float64 { return mm / mmPerInch * dpi }

	w := int(math.Round(px(area.W)))
	h := int(math.Round(px(area.H)))

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)

	// x and y, in pixels within the image, of a point on the page.
	x := func(mm float64) float64 { return px(mm - area.X) }
	y := func(mm float64) float64 { return px(mm - area.Y) }

	faces := make(map[float64]font.Face)
	defer func() {
		for _, f := range faces {
			f.Close()
		}
	}()

	for _, it := range d.Items {
		switch it := it.(type) {
		case Text:
			face, ok := faces[it.Size]
			if !ok {
				f, err := loadFont()
				if err != nil {
					return nil, err
				}

				face, err = opentype.NewFace(f, &opentype.FaceOptions{Size: it.Size, DPI: dpi, Hinting: font.HintingNone})
				if err != nil {
					return nil, fmt.Errorf("render: a face at %g pt: %w", it.Size, err)
				}
				faces[it.Size] = face
			}

			if err := drawText(img, face, it, x(it.X), y(it.Baseline), dpi); err != nil {
				return nil, err
			}

		case Line:
			fillLine(img, x(it.X1), y(it.Y1), x(it.X2), y(it.Y2), px(it.Width), gray(it.Gray))

		case Rect:
			c := gray(it.Gray)
			lw := px(it.Width)
			x0, y0, x1, y1 := x(it.X), y(it.Y), x(it.X+it.W), y(it.Y+it.H)
			fillLine(img, x0, y0, x1, y0, lw, c)
			fillLine(img, x0, y1, x1, y1, lw, c)
			fillLine(img, x0, y0, x0, y1, lw, c)
			fillLine(img, x1, y0, x1, y1, lw, c)

		default:
			return nil, fmt.Errorf("render: %T can't be drawn", it)
		}
	}

	return img, nil
}

func gray(g float64) color.Gray {
	return color.Gray{Y: uint8(math.Round(math.Max(0, math.Min(1, g)) * 255))}
}

// drawText draws t's glyphs one at a time from (x, baseline), in pixels,
// advancing by the same widths as the PDF.
func drawText(img draw.Image, face font.Face, t Text, x, baseline, dpi float64) error {
	a, em, err := advances()
	if err != nil {
		return err
	}

	src := image.NewUniform(gray(t.Gray))
	perUnit := t.Size / ptPerInch * dpi / float64(em) // pixels per font unit

	for _, r := range t.S {
		w, ok := a[r]
		if !ok {
			return ErrCharacter{Char: r}
		}

		dot := fixed.Point26_6{X: fixed.Int26_6(math.Round(x * 64)), Y: fixed.Int26_6(math.Round(baseline * 64))}

		if dr, mask, mp, _, ok := face.Glyph(dot, r); ok {
			draw.DrawMask(img, dr, src, image.Point{}, mask, mp, draw.Over)
		}

		x += float64(w) * perUnit
	}

	return nil
}

// fillLine fills an axis-aligned line of width lw pixels between two points.
// A slanted line is drawn as its bounding box, which nothing here asks for.
func fillLine(img draw.Image, x0, y0, x1, y1, lw float64, c color.Color) {
	half := math.Max(lw, 1) / 2

	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}

	r := image.Rect(
		int(math.Floor(x0-half+0.5)), int(math.Floor(y0-half+0.5)),
		int(math.Floor(x1+half+0.5)), int(math.Floor(y1+half+0.5)),
	)

	if r.Dx() == 0 {
		r.Max.X = r.Min.X + 1
	}
	if r.Dy() == 0 {
		r.Max.Y = r.Min.Y + 1
	}

	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}
