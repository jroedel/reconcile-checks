// Package ipptest is a fake IPP printer for tests: it answers the three
// operations the client uses, records every request it receives, and can be
// told to refuse an option or to walk a job through a sequence of states.
//
// It is what the client is tested against, on both systems, and never a real
// printer (CLAUDE.md, section 6): a test can assert exactly what a printer
// would have been asked to do, without paper.
//
// Its attributes are the M612's own, as it answered Get-Printer-Attributes on
// 2026-11-20 (docs/05-printing.md), trimmed to what the client reads.
package ipptest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/OpenPrinting/goipp"
)

// Request is one request as the printer received it. Attributes are
// flattened to strings, and a collection's members are named by their path:
// "media-col/media-source", "media-col/media-size/x-dimension".
type Request struct {
	Op         string // Print-Job, Validate-Job, …
	Operation  map[string][]string
	Job        map[string][]string
	Document   []byte
	RemoteAddr string
}

// Server is the fake printer.
type Server struct {
	*httptest.Server

	// URL is the printer's IPP address: ipp://127.0.0.1:port/ipp/print, or
	// ipps:// for NewTLS.
	URL string

	mu         sync.Mutex
	requests   []Request
	refuse     map[string]bool
	substitute bool
	states     []int
	jobID      int
	printer    goipp.Attributes
}

// New starts a fake printer over plain IPP.
func New() *Server { return start(false) }

// NewTLS starts a fake printer over ipps, with a certificate it made itself.
func NewTLS() *Server { return start(true) }

func start(tls bool) *Server {
	s := &Server{refuse: map[string]bool{}, printer: m612(), states: []int{9}}

	h := http.HandlerFunc(s.serve)
	if tls {
		s.Server = httptest.NewTLSServer(h)
		s.URL = "ipps://" + strings.TrimPrefix(s.Server.URL, "https://") + "/ipp/print"
	} else {
		s.Server = httptest.NewServer(h)
		s.URL = "ipp://" + strings.TrimPrefix(s.Server.URL, "http://") + "/ipp/print"
	}

	return s
}

// Refuse makes Validate-Job and Print-Job refuse a job carrying the named
// option, as a printer with ipp-attribute-fidelity does with one it won't
// honour: "print-scaling", "media-col".
func (s *Server) Refuse(option string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse[option] = true
}

// Substitute makes the printer answer successful-ok-ignored-or-substituted,
// as one that ignores fidelity would.
func (s *Server) Substitute() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.substitute = true
}

// JobStates is what successive Get-Job-Attributes answer, by RFC 8011's enum
// (5 processing, 6 stopped, 8 aborted, 9 completed); the last repeats.
func (s *Server) JobStates(states ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = states
}

// SetPrinter replaces one of the printer's attributes, as keywords.
func (s *Server) SetPrinter(name string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.printer = slices.DeleteFunc(s.printer, func(a goipp.Attribute) bool { return a.Name == name })
	s.printer.Add(keywords(name, values...))
}

// Requests is every request received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Printed is the documents received with a Print-Job.
func (s *Server) Printed() []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Op == "Print-Job" {
			out = append(out, r)
		}
	}

	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != goipp.ContentType {
		http.Error(w, "IPP only", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// What follows the IPP message is the document. goipp's decoder reads
	// exactly the message and no further, so what it leaves is the document.
	rest := bytes.NewReader(body)

	var req goipp.Message
	if err := req.Decode(rest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	doc, _ := io.ReadAll(rest)

	op := goipp.Op(req.Code)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests = append(s.requests, Request{
		Op:         op.String(),
		Operation:  flatten(req.Operation),
		Job:        flatten(req.Job),
		Document:   doc,
		RemoteAddr: r.RemoteAddr,
	})

	resp := goipp.NewResponse(req.Version, goipp.StatusOk, req.RequestID)
	resp.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	resp.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))

	switch op {
	case goipp.OpGetPrinterAttributes:
		resp.Printer = s.printer

	case goipp.OpValidateJob, goipp.OpPrintJob:
		var refused goipp.Attributes
		for _, a := range req.Job {
			if s.refuse[a.Name] {
				refused.Add(a)
			}
		}

		switch {
		case len(refused) > 0:
			resp.Code = goipp.Code(goipp.StatusErrorAttributesOrValues)
			resp.Operation.Add(goipp.MakeAttribute("status-message", goipp.TagText, goipp.String("Unsupported attributes or values")))
			resp.Unsupported = refused
		case s.substitute:
			resp.Code = goipp.Code(goipp.StatusOkIgnoredOrSubstituted)
		}

		if op == goipp.OpPrintJob && resp.Code == goipp.Code(goipp.StatusOk) {
			s.jobID++
			resp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(s.jobID)))
			resp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(3)))
		}

	case goipp.OpGetJobAttributes:
		state := s.states[0]
		if len(s.states) > 1 {
			s.states = s.states[1:]
		}

		reason := "none"
		switch state {
		case 6:
			reason = "media-jam"
		case 9:
			reason = "job-completed-successfully"
		}

		resp.Job.Add(goipp.MakeAttribute("job-state", goipp.TagEnum, goipp.Integer(state)))
		resp.Job.Add(goipp.MakeAttribute("job-state-reasons", goipp.TagKeyword, goipp.String(reason)))

	default:
		resp.Code = goipp.Code(goipp.StatusErrorOperationNotSupported)
	}

	b, _ := resp.EncodeBytes()
	w.Header().Set("Content-Type", goipp.ContentType)
	w.Write(b)
}

func flatten(attrs goipp.Attributes) map[string][]string {
	out := map[string][]string{}

	var walk func(prefix string, attrs goipp.Attributes)
	walk = func(prefix string, attrs goipp.Attributes) {
		for _, a := range attrs {
			for _, v := range a.Values {
				if col, ok := v.V.(goipp.Collection); ok {
					walk(prefix+a.Name+"/", goipp.Attributes(col))
					continue
				}

				out[prefix+a.Name] = append(out[prefix+a.Name], v.V.String())
			}
		}
	}
	walk("", attrs)

	return out
}

func keywords(name string, values ...string) goipp.Attribute {
	a := goipp.Attribute{Name: name}
	for _, v := range values {
		a.Values.Add(goipp.TagKeyword, goipp.String(v))
	}

	return a
}

func m612() goipp.Attributes {
	var a goipp.Attributes

	a.Add(goipp.MakeAttribute("printer-name", goipp.TagName, goipp.String("NPI46CBDF")))
	a.Add(goipp.MakeAttribute("printer-make-and-model", goipp.TagText, goipp.String("HP LaserJet M612")))
	a.Add(goipp.MakeAttribute("printer-state", goipp.TagEnum, goipp.Integer(3)))
	a.Add(keywords("printer-state-reasons", "none"))
	a.Add(keywords("sides-supported", "one-sided", "two-sided-long-edge", "two-sided-short-edge"))
	a.Add(keywords("sides-default", "two-sided-long-edge"))
	a.Add(keywords("print-scaling-supported", "auto", "auto-fit", "fill", "fit", "none"))
	a.Add(keywords("print-scaling-default", "auto"))
	a.Add(keywords("media-source-supported", "auto", "tray-2", "tray-1"))
	a.Add(keywords("media-supported", "na_letter_8.5x11in", "na_legal_8.5x14in", "iso_a4_210x297mm"))
	a.Add(keywords("media-default", "na_letter_8.5x11in"))
	a.Add(keywords("job-creation-attributes-supported", "copies", "finishings", "sides", "orientation-requested",
		"media", "print-quality", "printer-resolution", "output-bin", "media-col", "output-mode",
		"ipp-attribute-fidelity", "job-name", "multiple-document-handling", "page-ranges", "print-color-mode"))

	types := goipp.Attribute{Name: "media-type-supported"}
	for _, t := range []string{"stationery", "stationery-lightweight", "cardstock", "labels", "stationery-letterhead", "envelope", "stationery-preprinted"} {
		types.Values.Add(goipp.TagName, goipp.String(t))
	}
	a.Add(types)

	formats := goipp.Attribute{Name: "document-format-supported"}
	for _, f := range []string{"application/pdf", "application/postscript", "image/urf", "image/pwg-raster"} {
		formats.Values.Add(goipp.TagMimeType, goipp.String(f))
	}
	a.Add(formats)

	for _, side := range []string{"top", "bottom", "left", "right"} {
		a.Add(goipp.MakeAttribute("media-"+side+"-margin-supported", goipp.TagInteger, goipp.Integer(423)))
	}

	return a
}
