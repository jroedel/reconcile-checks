// Package ipp is a small IPP client (RFC 8011): ask a printer what it can do,
// send it a PDF with every option stated, and follow the job. It knows
// nothing of checks; docs/05-printing.md is why it exists and what it is for.
//
// The wire format is github.com/OpenPrinting/goipp, a dependency because the
// standard library has no IPP encoder. It is pure Go, with no dependencies of
// its own, and encodes the collections media-col needs. This package is the
// rest: the transport, the certificate pinning, the three operations and the
// file printer used in development.
//
// # Nothing is substituted
//
// Every Print-Job is sent with ipp-attribute-fidelity true, and is preceded by
// a Validate-Job of the same request, which every IPP printer must support.
// A printer that would ignore an option, or swap a value for one of its own
// (successful-ok-ignored-or-substituted-attributes), prints anyway with
// whatever it chose; for a check, that is a box in the wrong place. With
// fidelity, it refuses instead, Validate-Job finds that out before anything
// is printed, and the refusal names the options it would not honour.
//
// That matters on the M612 in particular: it offers print-scaling "none" but
// leaves print-scaling out of job-creation-attributes-supported, and its
// default, "auto", would shrink a full Letter page into its margins.
package ipp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenPrinting/goipp"
)

// Printer is where to send a job.
type Printer struct {
	// URL is the printer's IPP address: ipp://host[:port]/path, ipps://… for
	// TLS, or file:<dir> for a printer that writes PDFs to a folder.
	URL string

	// Pin is the SHA-256 of the printer's certificate, in hex, for ipps.
	// Printers make their own certificates, which nothing can verify, so the
	// one seen when the printer was added is remembered and expected
	// afterwards (Attributes.Certificate). Printing over ipps refuses to go
	// without it.
	Pin string
}

// Dialer opens connections. Nil is net.Dialer; on Linux, main gives one that
// resolves .local names itself (foundation/mdns), which a pure-Go build can't.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Client talks to printers.
type Client struct {
	// Dial opens connections; nil is the system's dialer.
	Dial Dialer

	// User is the requesting-user-name sent with every request: the
	// program's name, not the person's.
	User string

	requestID atomic.Uint32
}

// Errors a person may see. Each says what to do, in words that don't name
// this package.

// ErrNotPinned is ipps with no remembered certificate.
var ErrNotPinned = errors.New("the printer's certificate was never remembered; add the printer again in Settings")

// PinChanged is a certificate other than the one remembered: after a firmware
// update, usually, and otherwise someone else answering at the address.
type PinChanged struct{ Want, Got string }

func (e PinChanged) Error() string {
	return "the printer's certificate has changed since it was added. If its firmware was just updated, " +
		"accept the new certificate in Settings; otherwise, check that the address is still the printer's"
}

// Unreachable is a printer that could not be reached at all.
type Unreachable struct {
	URL string
	Err error
}

func (e Unreachable) Error() string {
	return fmt.Sprintf("the printer at %s can't be reached (%v). Check that it is on and on the network", e.URL, e.Err)
}

func (e Unreachable) Unwrap() error { return e.Err }

// Uncertain is a Print-Job that failed after it was sent, so that it may or
// may not have printed. Everything before the Print-Job (the printer's lists,
// Validate-Job) fails certain: nothing was sent to print. The caller asks the
// person rather than guessing, since a page that did print is a check that
// exists.
type Uncertain struct{ Err error }

func (e Uncertain) Error() string {
	return fmt.Sprintf("the printer may or may not have printed it: %v", e.Err)
}

func (e Uncertain) Unwrap() error { return e.Err }

// Refused is the printer saying no.
type Refused struct {
	Op          string   // the operation refused: Validate-Job, Print-Job…
	Status      string   // IPP's name for the status, client-error-…
	Message     string   // the printer's own status-message, if it gave one
	Unsupported []string // the options it would not honour, by name
}

func (e Refused) Error() string {
	var b strings.Builder
	b.WriteString("the printer refused the job")

	if len(e.Unsupported) > 0 {
		fmt.Fprintf(&b, ": it won't honour %s", strings.Join(e.Unsupported, ", "))
	}

	if e.Message != "" {
		fmt.Fprintf(&b, " (it says %q)", e.Message)
	} else {
		fmt.Fprintf(&b, " (%s)", e.Status)
	}

	return b.String()
}

// --- the three operations ---------------------------------------------------

// Attributes asks the printer what it is and what it can do.
func (c *Client) Attributes(ctx context.Context, p Printer) (Attributes, error) {
	if dir, ok := fileDir(p.URL); ok {
		return fileAttributes(dir), nil
	}

	req := c.request(goipp.OpGetPrinterAttributes, p.URL)
	req.Operation.Add(goipp.MakeAttr("requested-attributes", goipp.TagKeyword, goipp.String("all"), goipp.String("media-col-database")))

	// The first contact with a printer is what pins it, so a pin is not
	// required here; the certificate seen is returned instead. One given is
	// still checked.
	resp, cert, err := c.do(ctx, p, req, nil, false)
	if err != nil {
		return Attributes{}, err
	}

	a := parseAttributes(resp.Printer)
	a.Certificate = cert

	return a, nil
}

// Print sends the PDF, once the printer has said twice that it will honour
// every option in job: by listing each value among the ones it offers
// (Attributes.Supports), and by accepting a Validate-Job of the same request.
// It returns the printer's job id.
//
// Both, because neither is enough alone. A printer may leave an option out of
// its lists and still honour it, which only Validate-Job can say; and a
// printer may accept a Validate-Job for a value it doesn't have, which CUPS's
// own ippeveprinter does for a tray that doesn't exist, with fidelity set.
// The lists are asked for afresh each time, since a printer's trays and
// state are not something to remember.
func (c *Client) Print(ctx context.Context, p Printer, job Job, pdf []byte) (int, error) {
	if dir, ok := fileDir(p.URL); ok {
		if err := fileAttributes(dir).Supports(job); err != nil {
			return 0, err
		}

		return printToFile(dir, job, pdf)
	}

	if _, secure, err := httpURL(p.URL); err != nil {
		return 0, err
	} else if secure && p.Pin == "" {
		return 0, ErrNotPinned
	}

	a, err := c.Attributes(ctx, p)
	if err != nil {
		return 0, err
	}

	if err := a.Supports(job); err != nil {
		return 0, err
	}

	validate := c.request(goipp.OpValidateJob, p.URL)
	if err := job.encode(validate); err != nil {
		return 0, err
	}

	if _, _, err := c.do(ctx, p, validate, nil, true); err != nil {
		return 0, err
	}

	job0 := c.request(goipp.OpPrintJob, p.URL)
	if err := job.encode(job0); err != nil {
		return 0, err
	}

	resp, _, err := c.do(ctx, p, job0, pdf, true)
	if err != nil {
		// A refusal is a certain no. Anything else, once the Print-Job was
		// on its way, may have reached the printer before it failed: the
		// answer lost, the connection dropped after the document was sent.
		if _, refused := errors.AsType[Refused](err); !refused {
			return 0, Uncertain{Err: err}
		}

		return 0, err
	}

	id := integer(find(resp.Job, "job-id"))
	if id == 0 {
		return 0, fmt.Errorf("the printer accepted the job but gave it no number, so it can't be followed")
	}

	return id, nil
}

// JobState asks where a job is.
func (c *Client) JobState(ctx context.Context, p Printer, id int) (JobStatus, error) {
	if _, ok := fileDir(p.URL); ok {
		return JobStatus{State: "completed"}, nil
	}

	req := c.request(goipp.OpGetJobAttributes, p.URL)
	req.Operation.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(id)))
	req.Operation.Add(goipp.MakeAttr("requested-attributes", goipp.TagKeyword,
		goipp.String("job-state"), goipp.String("job-state-reasons"), goipp.String("job-state-message")))

	resp, _, err := c.do(ctx, p, req, nil, true)
	if err != nil {
		return JobStatus{}, err
	}

	return JobStatus{
		State:   jobStates[integer(find(resp.Job, "job-state"))],
		Reasons: strs(find(resp.Job, "job-state-reasons")),
		Message: first(find(resp.Job, "job-state-message")),
	}, nil
}

// JobStatus is where a job is.
type JobStatus struct {
	State   string // pending, pending-held, processing, processing-stopped, canceled, aborted, completed
	Reasons []string
	Message string
}

// Done reports whether the job will change no more.
func (s JobStatus) Done() bool {
	return s.State == "completed" || s.State == "canceled" || s.State == "aborted"
}

// jobStates is RFC 8011's job-state enum.
var jobStates = map[int]string{
	3: "pending", 4: "pending-held", 5: "processing", 6: "processing-stopped",
	7: "canceled", 8: "aborted", 9: "completed",
}

// --- the transport ----------------------------------------------------------

func (c *Client) request(op goipp.Op, printerURL string) *goipp.Message {
	m := goipp.NewRequest(goipp.MakeVersion(2, 0), op, c.requestID.Add(1))
	m.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	m.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-us")))
	m.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(printerURL)))

	if c.User != "" {
		m.Operation.Add(goipp.MakeAttribute("requesting-user-name", goipp.TagName, goipp.String(c.User)))
	}

	return m
}

// maxResponse is the most a printer's answer may be. The M612's full
// attributes are about 20 KB; a megabyte is room for any printer and still a
// bound.
const maxResponse = 1 << 20

// do sends one request, with the document after it if there is one, and
// returns the answer and the certificate the printer presented (ipps only).
func (c *Client) do(ctx context.Context, p Printer, req *goipp.Message, doc []byte, needPin bool) (*goipp.Message, string, error) {
	target, secure, err := httpURL(p.URL)
	if err != nil {
		return nil, "", err
	}

	if secure && needPin && p.Pin == "" {
		return nil, "", ErrNotPinned
	}

	head, err := req.EncodeBytes()
	if err != nil {
		return nil, "", fmt.Errorf("encoding the request: %w", err)
	}

	var seen string

	transport := &http.Transport{
		DialContext:         c.Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
		TLSClientConfig: &tls.Config{
			// A printer's certificate is one it made itself: no authority
			// signed it and its name is rarely the one dialled, so ordinary
			// verification would refuse every printer. It is pinned instead,
			// below, which is the stronger check for a device that should
			// never change.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("the printer presented no certificate")
				}

				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				seen = hex.EncodeToString(sum[:])

				if p.Pin != "" && !strings.EqualFold(seen, p.Pin) {
					return PinChanged{Want: p.Pin, Got: seen}
				}

				return nil
			},
		},
	}
	defer transport.CloseIdleConnections()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(append(head, doc...)))
	if err != nil {
		return nil, "", err
	}

	hreq.Header.Set("Content-Type", goipp.ContentType)
	hreq.Header.Set("Accept", goipp.ContentType)

	hresp, err := (&http.Client{Transport: transport}).Do(hreq)
	if err != nil {
		if pc, ok := errors.AsType[PinChanged](err); ok {
			return nil, seen, pc
		}

		return nil, seen, Unreachable{URL: p.URL, Err: err}
	}
	defer hresp.Body.Close()

	if hresp.StatusCode != http.StatusOK {
		return nil, seen, fmt.Errorf("the printer at %s answered %s; check that the address is its IPP address", p.URL, hresp.Status)
	}

	var resp goipp.Message
	if err := resp.Decode(io.LimitReader(hresp.Body, maxResponse)); err != nil {
		return nil, seen, fmt.Errorf("the printer's answer could not be read: %w", err)
	}

	// Ignored or substituted is a failure here, not a success: see the
	// package comment. With fidelity set it shouldn't happen; if a printer
	// does it anyway, the job is not trusted.
	if status := goipp.Status(resp.Code); status != goipp.StatusOk {
		return nil, seen, Refused{
			Op:          goipp.Op(req.Code).String(),
			Status:      status.String(),
			Message:     first(find(resp.Operation, "status-message")),
			Unsupported: names(resp.Unsupported),
		}
	}

	return &resp, seen, nil
}

// httpURL is the HTTP address an IPP URL is reached at: ipp is http, ipps is
// https, and the port is 631 unless another is given (RFC 8010, 4).
func httpURL(s string) (string, bool, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false, fmt.Errorf("%q is not a printer's address; it should look like ipp://printer.local/ipp/print", s)
	}

	secure := false

	switch strings.ToLower(u.Scheme) {
	case "ipp", "http":
		u.Scheme = "http"
	case "ipps", "https":
		u.Scheme = "https"
		secure = true
	default:
		return "", false, fmt.Errorf("%q is not a printer's address: it should start ipp:// or ipps://", s)
	}

	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "631")
	}

	return u.String(), secure, nil
}

// --- reading attributes -----------------------------------------------------

func find(attrs goipp.Attributes, name string) goipp.Values {
	for _, a := range attrs {
		if a.Name == name {
			return a.Values
		}
	}

	return nil
}

func strs(vs goipp.Values) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.V.String())
	}

	return out
}

func first(vs goipp.Values) string {
	if len(vs) == 0 {
		return ""
	}

	return vs[0].V.String()
}

func integer(vs goipp.Values) int {
	if len(vs) == 0 {
		return 0
	}

	if n, ok := vs[0].V.(goipp.Integer); ok {
		return int(n)
	}

	return 0
}

func names(attrs goipp.Attributes) []string {
	var out []string
	for _, a := range attrs {
		out = append(out, a.Name)
	}

	return out
}
