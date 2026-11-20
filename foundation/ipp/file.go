package ipp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A file: printer writes each job's PDF to a folder, with the job's options
// beside it as JSON, and sends nothing anywhere (docs/05-printing.md,
// "Printing to a file"). It is how development, the browser tests and any
// agent print: the same code path up to the last step, and a file to look at
// instead of a check.

// fileDir is the folder of a file: printer, and whether the URL is one:
// file:/abs/dir, file:///abs/dir, or file:relative/dir.
func fileDir(s string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(s), "file:") {
		return "", false
	}

	u, err := url.Parse(s)
	if err != nil {
		return s[len("file:"):], true
	}

	if u.Opaque != "" {
		return filepath.FromSlash(u.Opaque), true
	}

	p := u.Path
	// file:///C:/printer on Windows parses with a path of /C:/printer.
	if len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}

	return filepath.FromSlash(p), true
}

// fileAttributes is what a file: printer says about itself: the options a
// real check printer offers, so that an account set up against it is set up
// as it would be against the real one.
func fileAttributes(dir string) Attributes {
	a := Attributes{
		Name:           "file",
		MakeAndModel:   "Files in " + dir,
		URIs:           []string{"file:" + filepath.ToSlash(dir)},
		State:          "idle",
		StateReasons:   []string{"none"},
		Formats:        []string{"application/pdf"},
		Sides:          []string{"one-sided", "two-sided-long-edge", "two-sided-short-edge"},
		SidesDefault:   "two-sided-long-edge",
		Sources:        []string{"auto", "tray-1", "tray-2"},
		Types:          []string{"stationery", "stationery-preprinted"},
		Media:          []string{"na_letter_8.5x11in", "na_legal_8.5x14in", "iso_a4_210x297mm"},
		MediaDefault:   "na_letter_8.5x11in",
		Scaling:        []string{"auto", "fit", "none"},
		ScalingDefault: "auto",
		JobAttributes:  []string{"copies", "sides", "media-col", "print-scaling", "ipp-attribute-fidelity", "job-name"},
	}

	a.Margins.Top, a.Margins.Bottom, a.Margins.Left, a.Margins.Right = 4.23, 4.23, 4.23, 4.23

	return a
}

// printToFile writes job-<n>.pdf and job-<n>.json, n one more than the
// highest already there, and returns n as the job's id.
func printToFile(dir string, job Job, pdf []byte) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("making the printer's folder %s: %w", dir, err)
	}

	opts, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return 0, err
	}

	for n := nextJob(dir); ; n++ {
		path := filepath.Join(dir, "job-"+strconv.Itoa(n)+".pdf")

		// O_EXCL, so two jobs at once never share a number.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}

		if err != nil {
			return 0, fmt.Errorf("printing to %s: %w", dir, err)
		}

		_, werr := f.Write(pdf)
		if err := errors.Join(werr, f.Close()); err != nil {
			return 0, fmt.Errorf("printing to %s: %w", path, err)
		}

		if err := os.WriteFile(filepath.Join(dir, "job-"+strconv.Itoa(n)+".json"), append(opts, '\n'), 0o600); err != nil {
			return 0, fmt.Errorf("printing to %s: %w", dir, err)
		}

		return n, nil
	}
}

func nextJob(dir string) int {
	es, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}

	high := 0
	for _, e := range es {
		name, ok := strings.CutPrefix(e.Name(), "job-")
		if !ok {
			continue
		}

		if n, err := strconv.Atoi(strings.TrimSuffix(name, ".pdf")); err == nil && n > high {
			high = n
		}
	}

	return high + 1
}
