package ipp_test

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/reconcile-checks/foundation/ipp"
)

// TestInterop runs the client against a real IPP implementation that is not a
// printer: CUPS's ippeveprinter, which spools what it is sent to a folder.
// The fake in ipptest decodes with the same library the client encodes with,
// so it can't catch an encoding another implementation rejects; this can.
//
// Skipped unless IPP_INTEROP_URL names one, started for example with
//
//	ippeveprinter -r off -n localhost -p 8631 -d <spool dir> -f application/pdf interop
//	IPP_INTEROP_URL=ipp://127.0.0.1:8631/ipp/print IPP_INTEROP_SPOOL=<spool dir> \
//	    go test ./foundation/ipp -run Interop
//
// -r off keeps it from announcing itself on the network, and -n localhost
// keeps it on loopback. Never point this at a real printer (CLAUDE.md,
// section 6).
func TestInterop(t *testing.T) {
	url, spool := os.Getenv("IPP_INTEROP_URL"), os.Getenv("IPP_INTEROP_SPOOL")
	if url == "" {
		t.Skip("IPP_INTEROP_URL is not set")
	}

	c, p := client(), ipp.Printer{URL: url}

	a, err := c.Attributes(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("%s: sources %q, types %q, scaling %q, sides %q", a.MakeAndModel, a.Sources, a.Types, a.Scaling, a.Sides)

	// The check's job, with the tray and media type this printer has.
	j := job
	j.Source = pick(a.Sources, "tray-1")
	j.Type = pick(a.Types, "stationery-preprinted")
	j.Media = pick(a.Media, "na_letter_8.5x11in")

	// ippeveprinter doesn't offer print-scaling at all; a check printer must.
	if len(a.Scaling) == 0 {
		j.Scaling = ""
	}

	if err := a.Supports(j); err != nil {
		t.Fatal(err)
	}

	pdf, err := os.ReadFile("testdata/one-page.pdf")
	if err != nil {
		t.Fatal(err)
	}

	id, err := c.Print(t.Context(), p, j, pdf)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := c.JobState(t.Context(), p, id)
		if err != nil {
			t.Fatal(err)
		}

		if st.Done() {
			if st.State != "completed" {
				t.Errorf("job %d ended %s %q", id, st.State, st.Reasons)
			}
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("job %d still %s after 30 seconds", id, st.State)
		}

		time.Sleep(200 * time.Millisecond)
	}

	// An option it doesn't offer is refused before anything prints. This
	// printer would accept it: with fidelity set, its Validate-Job answers
	// successful-ok for a tray it doesn't have, which is why Print checks the
	// printer's lists first.
	before, _ := os.ReadDir(spool)

	j.Source = "no-such-tray"
	if _, err := c.Print(t.Context(), p, j, pdf); err == nil {
		t.Error("a job for a tray the printer doesn't have was accepted")
	} else {
		t.Logf("refused as it should be: %v", err)
	}

	if after, _ := os.ReadDir(spool); spool != "" && len(after) != len(before) {
		t.Errorf("the spool went from %d files to %d", len(before), len(after))
	}
}

func pick(have []string, want string) string {
	if slices.Contains(have, want) || len(have) == 0 {
		return want
	}

	return have[0]
}
