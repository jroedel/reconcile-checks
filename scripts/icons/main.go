// Command icons draws the app's icon at every size it is used, and writes the
// same drawing as an SVG. Run by `make icons`; the results are committed.
//
// The icon is in Reconcile's manner (docs/12-look-and-voice.md): a white glyph
// on the accent blue, in a rounded square. Reconcile's glyph is a tick; this
// one is a check -- the paper kind -- in outline, with a payee line, an amount
// box and a signature stroke, so that the two sit side by side as a pair and
// are still told apart.
//
// The drawing is described once, as shapes in a 512-unit square, and both
// outputs come from that one description: the SVG states the shapes, and the
// PNGs are rendered from them here by supersampling each pixel against the
// shapes' distance functions. Nothing to install, no SVG renderer, and the
// two can't drift apart.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The accent blue and the white, as Reconcile's --accent and its icon.
var (
	accent = color.NRGBA{0x1f, 0x5f, 0x8b, 0xff}
	white  = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// The shapes, in units of a 512-unit square.
const (
	size       = 512.0
	tileRadius = 112.0 // the rounded square, as Reconcile's icon

	// The check: a wide rectangle, outlined.
	checkX, checkY, checkW, checkH, checkR, checkStroke = 88.0, 156.0, 336.0, 200.0, 18.0, 24.0

	// The payee line.
	lineX1, lineX2, lineY, lineStroke = 128.0, 296.0, 236.0, 16.0

	// The amount box.
	boxX, boxY, boxW, boxH, boxR, boxStroke = 328.0, 212.0, 64.0, 40.0, 6.0, 12.0

	// The signature.
	sigStroke = 14.0
)

// signature is the signature stroke, as points of a polyline.
var signature = [][2]float64{
	{276, 316}, {294, 294}, {308, 320}, {324, 292}, {338, 316}, {356, 302}, {380, 308},
}

func main() {
	out := flag.String("out", "app/sdk/page/assets/img", "where to write the icons")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}

	for _, px := range []int{16, 32, 48, 64, 128, 180, 192, 256, 512} {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(px)); err != nil {
			fail(err)
		}

		if err := os.WriteFile(filepath.Join(*out, fmt.Sprintf("icon-%d.png", px)), buf.Bytes(), 0o644); err != nil {
			fail(err)
		}
	}

	if err := os.WriteFile(filepath.Join(*out, "icon.svg"), []byte(svg()), 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icons:", err)
	os.Exit(1)
}

// svg states the shapes.
func svg() string {
	var b bytes.Buffer

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">`+"\n")
	fmt.Fprintf(&b, `<rect width="512" height="512" rx="%g" fill="#1f5f8b"/>`+"\n", tileRadius)
	fmt.Fprintf(&b, `<g fill="none" stroke="#fff" stroke-linecap="round" stroke-linejoin="round">`+"\n")
	fmt.Fprintf(&b, `<rect x="%g" y="%g" width="%g" height="%g" rx="%g" stroke-width="%g"/>`+"\n",
		checkX, checkY, checkW, checkH, checkR, checkStroke)
	fmt.Fprintf(&b, `<path d="M%g %gH%g" stroke-width="%g"/>`+"\n", lineX1, lineY, lineX2, lineStroke)
	fmt.Fprintf(&b, `<rect x="%g" y="%g" width="%g" height="%g" rx="%g" stroke-width="%g"/>`+"\n",
		boxX, boxY, boxW, boxH, boxR, boxStroke)

	fmt.Fprintf(&b, `<path d="M%g %g`, signature[0][0], signature[0][1])
	for _, p := range signature[1:] {
		fmt.Fprintf(&b, `L%g %g`, p[0], p[1])
	}
	fmt.Fprintf(&b, `" stroke-width="%g"/>`+"\n", sigStroke)

	fmt.Fprintf(&b, "</g>\n</svg>\n")

	return b.String()
}

// render draws the icon at px by px, with 4x4 samples per pixel.
func render(px int) *image.NRGBA {
	const samples = 4

	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	scale := size / float64(px)

	for y := range px {
		for x := range px {
			var tile, glyph float64

			for sy := range samples {
				for sx := range samples {
					ux := (float64(x) + (float64(sx)+0.5)/samples) * scale
					uy := (float64(y) + (float64(sy)+0.5)/samples) * scale

					if roundedRect(ux, uy, 0, 0, size, size, tileRadius) <= 0 {
						tile++

						if onGlyph(ux, uy) {
							glyph++
						}
					}
				}
			}

			n := float64(samples * samples)
			a := tile / n
			if a == 0 {
				continue
			}

			// Within the tile, the glyph's share of the samples is white.
			g := glyph / tile
			img.SetNRGBA(x, y, color.NRGBA{
				R: mix(accent.R, white.R, g),
				G: mix(accent.G, white.G, g),
				B: mix(accent.B, white.B, g),
				A: uint8(math.Round(a * 255)),
			})
		}
	}

	return img
}

func mix(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a)*(1-t) + float64(b)*t))
}

// onGlyph reports whether a point is on any white stroke.
func onGlyph(x, y float64) bool {
	if math.Abs(roundedRect(x, y, checkX, checkY, checkW, checkH, checkR)) <= checkStroke/2 {
		return true
	}

	if segment(x, y, lineX1, lineY, lineX2, lineY) <= lineStroke/2 {
		return true
	}

	if math.Abs(roundedRect(x, y, boxX, boxY, boxW, boxH, boxR)) <= boxStroke/2 {
		return true
	}

	for i := 1; i < len(signature); i++ {
		a, b := signature[i-1], signature[i]
		if segment(x, y, a[0], a[1], b[0], b[1]) <= sigStroke/2 {
			return true
		}
	}

	return false
}

// roundedRect is the signed distance from a point to a rounded rectangle:
// negative inside, positive outside.
func roundedRect(px, py, x, y, w, h, r float64) float64 {
	cx, cy := x+w/2, y+h/2
	qx := math.Abs(px-cx) - (w/2 - r)
	qy := math.Abs(py-cy) - (h/2 - r)

	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	inside := math.Min(math.Max(qx, qy), 0)

	return outside + inside - r
}

// segment is the distance from a point to a line segment, which with a
// half-width is a stroke with round caps.
func segment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))

	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
