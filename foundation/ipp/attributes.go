package ipp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/OpenPrinting/goipp"
)

// Attributes is what a printer says about itself: what Settings shows, and
// what a job is checked against before it is sent.
type Attributes struct {
	Name         string   // printer-name
	MakeAndModel string   // printer-make-and-model
	URIs         []string // printer-uri-supported: the addresses it answers at
	State        string   // idle, processing, stopped
	StateReasons []string // none, media-empty, …

	Formats        []string // document-format-supported
	Sides          []string // sides-supported
	SidesDefault   string
	Sources        []string // media-source-supported: the trays
	Types          []string // media-type-supported
	Media          []string // media-supported, as PWG names: na_letter_8.5x11in
	MediaDefault   string
	Scaling        []string // print-scaling-supported
	ScalingDefault string

	// JobAttributes is job-creation-attributes-supported: the options the
	// printer says a job may carry. Shown, not enforced: Validate-Job is what
	// decides (see the package comment).
	JobAttributes []string

	// Margins is the printer's unprintable edge, in millimetres: the largest
	// of the margins it supports on each side, which is the one a job that
	// names none is printed with.
	Margins struct{ Top, Bottom, Left, Right float64 }

	// Certificate is the SHA-256 of the certificate presented over ipps, in
	// hex, to be kept as the printer's Pin. Empty over plain ipp.
	Certificate string
}

func parseAttributes(attrs goipp.Attributes) Attributes {
	a := Attributes{
		Name:           first(find(attrs, "printer-name")),
		MakeAndModel:   first(find(attrs, "printer-make-and-model")),
		URIs:           strs(find(attrs, "printer-uri-supported")),
		State:          printerStates[integer(find(attrs, "printer-state"))],
		StateReasons:   strs(find(attrs, "printer-state-reasons")),
		Formats:        strs(find(attrs, "document-format-supported")),
		Sides:          strs(find(attrs, "sides-supported")),
		SidesDefault:   first(find(attrs, "sides-default")),
		Sources:        strs(find(attrs, "media-source-supported")),
		Types:          strs(find(attrs, "media-type-supported")),
		Media:          strs(find(attrs, "media-supported")),
		MediaDefault:   first(find(attrs, "media-default")),
		Scaling:        strs(find(attrs, "print-scaling-supported")),
		ScalingDefault: first(find(attrs, "print-scaling-default")),
		JobAttributes:  strs(find(attrs, "job-creation-attributes-supported")),
	}

	a.Margins.Top = margin(find(attrs, "media-top-margin-supported"))
	a.Margins.Bottom = margin(find(attrs, "media-bottom-margin-supported"))
	a.Margins.Left = margin(find(attrs, "media-left-margin-supported"))
	a.Margins.Right = margin(find(attrs, "media-right-margin-supported"))

	return a
}

// printerStates is RFC 8011's printer-state enum.
var printerStates = map[int]string{3: "idle", 4: "processing", 5: "stopped"}

// margin is the largest of a margin's supported values, in hundredths of a
// millimetre, as millimetres.
func margin(vs goipp.Values) float64 {
	most := 0
	for _, v := range vs {
		if n, ok := v.V.(goipp.Integer); ok && int(n) > most {
			most = int(n)
		}
	}

	return float64(most) / 100
}

// Job is everything a print job states. Every field that decides where ink
// lands is sent every time; a printer's defaults are someone else's choices
// (docs/05-printing.md, "Options set by the program, every time").
type Job struct {
	Name    string // job-name, shown on the printer's panel and in its log
	Format  string // document-format: application/pdf
	Sides   string // one-sided
	Source  string // media-col's media-source: tray-1
	Type    string // media-col's media-type: stationery-preprinted
	Media   string // the PWG name of the paper, from which media-col's media-size: na_letter_8.5x11in
	Scaling string // print-scaling: none
	Copies  int
}

// Supports refuses a job that asks for something the printer doesn't list,
// naming what it does offer, so that the account's settings can be put right
// before anything is sent.
func (a Attributes) Supports(j Job) error {
	check := func(what, want string, have []string) error {
		if want == "" || slices.Contains(have, want) {
			return nil
		}

		if len(have) == 0 {
			return fmt.Errorf("the printer doesn't say which %s it offers, so %q can't be checked", what, want)
		}

		return fmt.Errorf("the printer doesn't offer %s %q; it offers %s", what, want, strings.Join(have, ", "))
	}

	for _, c := range []struct {
		what, want string
		have       []string
	}{
		{"the document format", j.Format, a.Formats},
		{"sides", j.Sides, a.Sides},
		{"the tray", j.Source, a.Sources},
		{"the media type", j.Type, a.Types},
		{"the paper size", j.Media, a.Media},
		{"scaling", j.Scaling, a.Scaling},
	} {
		if err := check(c.what, c.want, c.have); err != nil {
			return err
		}
	}

	return nil
}

// encode adds the job's operation and job attributes to a request.
func (j Job) encode(m *goipp.Message) error {
	if j.Name != "" {
		m.Operation.Add(goipp.MakeAttribute("job-name", goipp.TagName, goipp.String(j.Name)))
	}

	m.Operation.Add(goipp.MakeAttribute("ipp-attribute-fidelity", goipp.TagBoolean, goipp.Boolean(true)))

	if j.Format != "" {
		m.Operation.Add(goipp.MakeAttribute("document-format", goipp.TagMimeType, goipp.String(j.Format)))
	}

	if j.Sides != "" {
		m.Job.Add(goipp.MakeAttribute("sides", goipp.TagKeyword, goipp.String(j.Sides)))
	}

	if j.Scaling != "" {
		m.Job.Add(goipp.MakeAttribute("print-scaling", goipp.TagKeyword, goipp.String(j.Scaling)))
	}

	if j.Copies > 0 {
		m.Job.Add(goipp.MakeAttribute("copies", goipp.TagInteger, goipp.Integer(j.Copies)))
	}

	var col goipp.Collection

	if j.Media != "" {
		x, y, err := MediaSize(j.Media)
		if err != nil {
			return err
		}

		col.Add(goipp.MakeAttrCollection("media-size",
			goipp.MakeAttribute("x-dimension", goipp.TagInteger, goipp.Integer(x)),
			goipp.MakeAttribute("y-dimension", goipp.TagInteger, goipp.Integer(y)),
		))
	}

	if j.Source != "" {
		col.Add(goipp.MakeAttribute("media-source", goipp.TagKeyword, goipp.String(j.Source)))
	}

	if j.Type != "" {
		col.Add(goipp.MakeAttribute("media-type", goipp.TagKeyword, goipp.String(j.Type)))
	}

	if len(col) > 0 {
		m.Job.Add(goipp.MakeAttribute("media-col", goipp.TagBeginCollection, col))
	}

	return nil
}

// MediaSize reads a PWG self-describing media name (PWG 5101.1), whose last
// part is its size: na_letter_8.5x11in, iso_a4_210x297mm. The answer is in
// hundredths of a millimetre, as media-size wants it.
func MediaSize(name string) (x, y int, err error) {
	bad := fmt.Errorf("%q is not a paper size this program can read; it should look like na_letter_8.5x11in", name)

	i := strings.LastIndexByte(name, '_')
	if i < 0 {
		return 0, 0, bad
	}

	dims := name[i+1:]

	var perUnit float64
	switch {
	case strings.HasSuffix(dims, "in"):
		perUnit = 2540
	case strings.HasSuffix(dims, "mm"):
		perUnit = 100
	default:
		return 0, 0, bad
	}

	w, h, ok := strings.Cut(dims[:len(dims)-2], "x")
	if !ok {
		return 0, 0, bad
	}

	fw, err1 := strconv.ParseFloat(w, 64)
	fh, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || fw <= 0 || fh <= 0 {
		return 0, 0, bad
	}

	return int(fw*perUnit + 0.5), int(fh*perUnit + 0.5), nil
}
