package ipp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jroedel/reconcile-checks/foundation/ipp"
	"github.com/jroedel/reconcile-checks/foundation/ipp/ipptest"
)

var pdf = []byte("%PDF-1.4\n% a page\n%%EOF\n")

// The options a check is printed with (docs/05-printing.md).
var job = ipp.Job{
	Name:    "Check 1043",
	Format:  "application/pdf",
	Sides:   "one-sided",
	Source:  "tray-1",
	Type:    "stationery-preprinted",
	Media:   "na_letter_8.5x11in",
	Scaling: "none",
	Copies:  1,
}

func client() *ipp.Client { return &ipp.Client{User: "reconcile-checks"} }

func TestTheAttributesAreRead(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	a, err := client().Attributes(t.Context(), ipp.Printer{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case a.MakeAndModel != "HP LaserJet M612":
		t.Errorf("make and model %q", a.MakeAndModel)
	case a.State != "idle":
		t.Errorf("state %q", a.State)
	case !slices.Contains(a.Sources, "tray-1"):
		t.Errorf("trays %q", a.Sources)
	case !slices.Contains(a.Types, "stationery-preprinted"):
		t.Errorf("media types %q", a.Types) // sent as names, not keywords, by the M612
	case a.SidesDefault != "two-sided-long-edge" || a.ScalingDefault != "auto":
		t.Errorf("defaults: sides %q, scaling %q", a.SidesDefault, a.ScalingDefault)
	case a.Margins.Left != 4.23 || a.Margins.Top != 4.23:
		t.Errorf("margins %+v", a.Margins)
	case a.Certificate != "":
		t.Errorf("a certificate %q over plain IPP", a.Certificate)
	}

	if err := a.Supports(job); err != nil {
		t.Errorf("the M612 doesn't support a check's job: %v", err)
	}
}

// Every option that decides where ink lands is in the request, and the
// printer is asked whether it will honour them before anything is sent.
func TestAJobIsValidatedAndThenSentWithEveryOption(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	id, err := client().Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf)
	if err != nil {
		t.Fatal(err)
	}

	if id != 1 {
		t.Errorf("job id %d", id)
	}

	reqs := s.Requests()
	if len(reqs) != 3 || reqs[0].Op != "Get-Printer-Attributes" || reqs[1].Op != "Validate-Job" || reqs[2].Op != "Print-Job" {
		t.Fatalf("requests %v; want the printer's attributes, Validate-Job, then Print-Job", ops(reqs))
	}

	reqs = reqs[1:]

	if len(reqs[0].Document) != 0 {
		t.Error("Validate-Job carried the document")
	}

	for _, r := range reqs {
		for name, want := range map[string]string{
			"ipp-attribute-fidelity": "true",
			"document-format":        "application/pdf",
			"job-name":               "Check 1043",
			"requesting-user-name":   "reconcile-checks",
			"printer-uri":            s.URL,
		} {
			if got := r.Operation[name]; !slices.Equal(got, []string{want}) {
				t.Errorf("%s: operation %s = %q, want %q", r.Op, name, got, want)
			}
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
			if got := r.Job[name]; !slices.Equal(got, []string{want}) {
				t.Errorf("%s: %s = %q, want %q", r.Op, name, got, want)
			}
		}
	}

	if string(reqs[1].Document) != string(pdf) {
		t.Errorf("the printer received %q, want the PDF", reqs[1].Document)
	}
}

// A printer that won't honour an option says which, and nothing is printed.
func TestAnOptionThePrinterWontHonourStopsTheJobBeforeItPrints(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	s.Refuse("print-scaling")

	_, err := client().Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf)

	refused, ok := errors.AsType[ipp.Refused](err)
	if !ok || refused.Op != "Validate-Job" || !slices.Equal(refused.Unsupported, []string{"print-scaling"}) {
		t.Fatalf("err = %v; want Validate-Job refused over print-scaling", err)
	}

	if !strings.Contains(err.Error(), "won't honour print-scaling") {
		t.Errorf("the refusal says %q", err)
	}

	if n := len(s.Printed()); n != 0 {
		t.Errorf("%d documents were printed after the refusal", n)
	}
}

// A printer that substitutes values instead of refusing is not trusted.
func TestAPrinterThatSubstitutesIsNotTrusted(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	s.Substitute()

	if _, err := client().Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf); err == nil {
		t.Fatal("a job the printer would substitute options in was printed")
	}

	if n := len(s.Printed()); n != 0 {
		t.Errorf("%d documents were printed", n)
	}
}

// A Print-Job whose answer is lost may have printed; everything before it
// certainly didn't.
func TestAPrintJobThatFailsMidwayIsUncertain(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	s.DropAfter("Print-Job")

	_, err := client().Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf)
	if _, ok := errors.AsType[ipp.Uncertain](err); !ok {
		t.Errorf("err = %v; want Uncertain", err)
	}

	s.DropAfter("Validate-Job")

	_, err = client().Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf)
	if _, ok := errors.AsType[ipp.Uncertain](err); ok || err == nil {
		t.Errorf("a failure before the Print-Job is %v; want a certain failure", err)
	}
}

func TestAJobIsFollowed(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	s.JobStates(5, 6, 9)

	c, p := client(), ipp.Printer{URL: s.URL}

	id, err := c.Print(t.Context(), p, job, pdf)
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	for range 3 {
		st, err := c.JobState(t.Context(), p, id)
		if err != nil {
			t.Fatal(err)
		}

		seen = append(seen, st.State+":"+strings.Join(st.Reasons, ","))

		if st.Done() != (st.State == "completed") {
			t.Errorf("%s: done %v", st.State, st.Done())
		}
	}

	want := []string{"processing:none", "processing-stopped:media-jam", "completed:job-completed-successfully"}
	if !slices.Equal(seen, want) {
		t.Errorf("states %q, want %q", seen, want)
	}

	if got := s.Requests()[3].Operation["job-id"]; !slices.Equal(got, []string{"1"}) {
		t.Errorf("Get-Job-Attributes asked about job %q", got)
	}
}

// Over ipps, the certificate seen when the printer is added is the one
// expected afterwards.
func TestTheCertificateIsPinned(t *testing.T) {
	s := ipptest.NewTLS()
	defer s.Close()

	c := client()

	a, err := c.Attributes(t.Context(), ipp.Printer{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Certificate) != 64 {
		t.Fatalf("the certificate seen is %q; want a SHA-256 in hex", a.Certificate)
	}

	if _, err := c.Print(t.Context(), ipp.Printer{URL: s.URL}, job, pdf); !errors.Is(err, ipp.ErrNotPinned) {
		t.Errorf("printing over ipps with no pin: %v", err)
	}

	if _, err := c.Print(t.Context(), ipp.Printer{URL: s.URL, Pin: strings.ToUpper(a.Certificate)}, job, pdf); err != nil {
		t.Errorf("printing with the pin: %v", err)
	}

	wrong := strings.Repeat("0", 64)
	_, err = c.Print(t.Context(), ipp.Printer{URL: s.URL, Pin: wrong}, job, pdf)
	if pc, ok := errors.AsType[ipp.PinChanged](err); !ok || pc.Got != a.Certificate {
		t.Errorf("err = %v; want PinChanged", err)
	}

	if n := len(s.Printed()); n != 1 {
		t.Errorf("%d documents printed; want only the one sent with the right pin", n)
	}
}

func TestAPrinterThatIsntThereIsUnreachable(t *testing.T) {
	s := ipptest.New()
	url := s.URL
	s.Close()

	_, err := client().Attributes(t.Context(), ipp.Printer{URL: url})
	if _, ok := errors.AsType[ipp.Unreachable](err); !ok {
		t.Errorf("err = %v; want Unreachable", err)
	}
}

func TestAnAddressThatIsntIPPIsRefused(t *testing.T) {
	for _, u := range []string{"http//x", "socket://192.0.2.1:9100", "lpd://printer/queue", ""} {
		if _, err := client().Attributes(t.Context(), ipp.Printer{URL: u}); err == nil {
			t.Errorf("%q was taken for a printer", u)
		}
	}
}

func TestAFilePrinterWritesThePDFAndItsOptions(t *testing.T) {
	dir := t.TempDir()
	p := ipp.Printer{URL: "file:" + filepath.ToSlash(dir)}
	c := client()

	a, err := c.Attributes(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Supports(job); err != nil {
		t.Errorf("a file printer doesn't support a check's job: %v", err)
	}

	for want := 1; want <= 2; want++ {
		id, err := c.Print(t.Context(), p, job, pdf)
		if err != nil {
			t.Fatal(err)
		}

		if id != want {
			t.Errorf("job %d, want %d", id, want)
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, "job-2.pdf"))
	if err != nil || string(b) != string(pdf) {
		t.Errorf("job-2.pdf is %q (%v)", b, err)
	}

	var got ipp.Job
	b, _ = os.ReadFile(filepath.Join(dir, "job-2.json"))
	if err := json.Unmarshal(b, &got); err != nil || got != job {
		t.Errorf("job-2.json is %s (%v); want the job's options", b, err)
	}

	if st, err := c.JobState(t.Context(), p, 2); err != nil || !st.Done() {
		t.Errorf("a file job is %+v (%v); want it done", st, err)
	}
}

// A value the printer doesn't list is refused before the printer is asked to
// validate it: a printer may accept one it doesn't have.
func TestAJobForATrayThePrinterDoesntHaveIsNeverSent(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	j := job
	j.Source = "tray-3"

	if _, err := client().Print(t.Context(), ipp.Printer{URL: s.URL}, j, pdf); err == nil || !strings.Contains(err.Error(), "tray-3") {
		t.Errorf("err = %v; want tray-3 refused", err)
	}

	if got := ops(s.Requests()); !slices.Equal(got, []string{"Get-Printer-Attributes"}) {
		t.Errorf("requests %v; want only the printer's attributes", got)
	}

	dir := t.TempDir()
	if _, err := client().Print(t.Context(), ipp.Printer{URL: "file:" + filepath.ToSlash(dir)}, j, pdf); err == nil {
		t.Error("a file printer printed for tray-3")
	}
}

func TestSupportsNamesWhatThePrinterOffers(t *testing.T) {
	s := ipptest.New()
	defer s.Close()

	a, err := client().Attributes(t.Context(), ipp.Printer{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}

	j := job
	j.Source = "tray-3"

	err = a.Supports(j)
	if err == nil || !strings.Contains(err.Error(), `"tray-3"`) || !strings.Contains(err.Error(), "tray-1") {
		t.Errorf("err = %v; want tray-3 refused, naming tray-1", err)
	}
}

func TestMediaSize(t *testing.T) {
	for name, want := range map[string][2]int{
		"na_letter_8.5x11in":       {21590, 27940},
		"iso_a4_210x297mm":         {21000, 29700},
		"na_index-3x5_3x5in":       {7620, 12700},
		"om_small-photo_100x150mm": {10000, 15000},
	} {
		x, y, err := ipp.MediaSize(name)
		if err != nil || x != want[0] || y != want[1] {
			t.Errorf("%s: %d x %d (%v), want %v", name, x, y, err, want)
		}
	}

	for _, bad := range []string{"letter", "na_letter_8.5x11", "na_letter_x11in", "na_letter_0x11in"} {
		if _, _, err := ipp.MediaSize(bad); err == nil {
			t.Errorf("%q was read as a size", bad)
		}
	}
}

func ops(rs []ipptest.Request) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Op)
	}

	return out
}
