package render

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"golang.org/x/image/font/sfnt"
)

// PDF draws the page as a PDF: one page, one embedded font, the drawing
// commands left uncompressed so that they can be read, and nothing that
// varies from one run to the next (no dates, no ids). The same Doc always
// makes the same bytes.
//
// The font is embedded whole, as a TrueType "simple" font with WinAnsi
// encoding and its widths stated (docs/02-architecture.md): the kind of font
// every PDF interpreter, including a printer's own, reads without surprises.
func PDF(d Doc) ([]byte, error) {
	if d.Width <= 0 || d.Height <= 0 {
		return nil, errEmpty
	}

	content, err := contentStream(d)
	if err != nil {
		return nil, err
	}

	fontObj, descObj, fontFile, err := fontObjects()
	if err != nil {
		return nil, err
	}

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			num(pt(d.Width)), num(pt(d.Height))),
		stream("", content),
		fontObj,
		descObj,
		fontFile,
	}

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	return b.Bytes(), nil
}

// Content is the page's drawing commands, as the PDF holds them: what a
// golden test compares, because it says exactly where every mark lands and
// reads as text in a pull request.
func Content(d Doc) (string, error) {
	c, err := contentStream(d)
	return string(c), err
}

func contentStream(d Doc) ([]byte, error) {
	var b bytes.Buffer

	for _, it := range d.Items {
		switch it := it.(type) {
		case Text:
			s, err := pdfString(it.S)
			if err != nil {
				return nil, err
			}

			fmt.Fprintf(&b, "%s g BT /F1 %s Tf %s %s Td %s Tj ET\n",
				num(it.Gray), num(it.Size), num(pt(it.X)), num(pt(d.Height-it.Baseline)), s)

		case Line:
			fmt.Fprintf(&b, "%s G %s w %s %s m %s %s l S\n",
				num(it.Gray), num(pt(it.Width)),
				num(pt(it.X1)), num(pt(d.Height-it.Y1)), num(pt(it.X2)), num(pt(d.Height-it.Y2)))

		case Rect:
			fmt.Fprintf(&b, "%s G %s w %s %s %s %s re S\n",
				num(it.Gray), num(pt(it.Width)),
				num(pt(it.X)), num(pt(d.Height-it.Y-it.H)), num(pt(it.W)), num(pt(it.H)))

		default:
			return nil, fmt.Errorf("render: %T can't be drawn", it)
		}
	}

	return b.Bytes(), nil
}

// pdfString is s in Windows-1252 as a PDF literal string, with ( ) and \
// escaped, and bytes past ASCII written as octal so the stream stays readable.
func pdfString(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('(')

	for _, r := range s {
		c, ok := winAnsiByte(r)
		if !ok {
			return "", ErrCharacter{Char: r}
		}

		switch {
		case c == '(' || c == ')' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c > 0x7e:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}

	b.WriteByte(')')

	return b.String(), nil
}

// fontObjects is the font dictionary, its descriptor and the font file.
func fontObjects() (fontObj, descObj, fileObj string, err error) {
	f, err := loadFont()
	if err != nil {
		return "", "", "", err
	}

	a, em, err := advances()
	if err != nil {
		return "", "", "", err
	}

	scale := func(units int) int { return units * 1000 / em }

	var widths []string
	for c := 0x20; c <= 0xff; c++ {
		widths = append(widths, strconv.Itoa(scale(a[runeAt(byte(c))])))
	}

	var buf sfnt.Buffer
	ppem := fixed.Int26_6(em << 6)

	bounds, err := f.Bounds(&buf, ppem, font.HintingNone)
	if err != nil {
		return "", "", "", fmt.Errorf("render: the font's bounds: %w", err)
	}

	m, err := f.Metrics(&buf, ppem, font.HintingNone)
	if err != nil {
		return "", "", "", fmt.Errorf("render: the font's metrics: %w", err)
	}

	u := func(v fixed.Int26_6) int { return scale(int(v >> 6)) }

	fontObj = "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FirstChar 32 /LastChar 255 /Widths [" +
		strings.Join(widths, " ") + "] /FontDescriptor 6 0 R /Encoding /WinAnsiEncoding >>"

	// Flags 32 is Nonsymbolic: the font's characters are the standard Latin
	// set, so a reader maps WinAnsi codes to glyphs through the font's own
	// Unicode table. Bounds' Y runs down, so its Min.Y is the top.
	descObj = fmt.Sprintf("<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontBBox [%d %d %d %d] "+
		"/ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 /FontFile2 7 0 R >>",
		u(bounds.Min.X), -u(bounds.Max.Y), u(bounds.Max.X), -u(bounds.Min.Y),
		u(m.Ascent), -u(m.Descent), u(m.CapHeight))

	// FlateDecode is zlib, header and checksum included, not bare deflate.
	var z bytes.Buffer
	w, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	w.Write(FontFile())
	w.Close()

	fileObj = stream(fmt.Sprintf("/Length1 %d /Filter /FlateDecode", len(FontFile())), z.Bytes())

	return fontObj, descObj, fileObj, nil
}

// stream is a PDF stream object.
func stream(extra string, data []byte) string {
	dict := fmt.Sprintf("<< /Length %d", len(data))
	if extra != "" {
		dict += " " + extra
	}

	return dict + " >>\nstream\n" + string(data) + "\nendstream"
}

// pt converts millimetres to points.
func pt(mm float64) float64 { return mm / mmPerInch * ptPerInch }

// num writes a number with at most three decimals and no trailing zeros:
// readable, and the same every time.
func num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")

	if s == "-0" {
		return "0"
	}

	return s
}
