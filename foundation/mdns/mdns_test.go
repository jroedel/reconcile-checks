package mdns_test

import (
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/jroedel/reconcile-checks/foundation/mdns"
)

// responder is a fake mDNS responder on loopback. It answers a one-shot query
// as a real one does, by unicast to the port the query came from, from the
// records it was given; with brief, it answers a PTR with the PTR alone, as
// some printers do, so the querier has to ask for the rest.
type responder struct {
	conn    *net.UDPConn
	records []dnsmessage.Resource
	brief   bool

	mu    sync.Mutex
	asked []dnsmessage.Question
}

func newResponder(t *testing.T, brief bool, records ...dnsmessage.Resource) *responder {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	r := &responder{conn: conn, records: records, brief: brief}
	t.Cleanup(func() { conn.Close() })

	go r.serve()

	return r
}

func (r *responder) addr() string { return r.conn.LocalAddr().String() }

func (r *responder) questions() []dnsmessage.Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

func (r *responder) serve() {
	buf := make([]byte, 9000)

	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		var q dnsmessage.Message
		if q.Unpack(buf[:n]) != nil {
			continue
		}

		r.mu.Lock()
		r.asked = append(r.asked, q.Questions...)
		r.mu.Unlock()

		resp := dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}}

		for _, qq := range q.Questions {
			for _, rr := range r.records {
				if !strings.EqualFold(rr.Header.Name.String(), qq.Name.String()) || rr.Header.Type != qq.Type {
					continue
				}

				resp.Answers = append(resp.Answers, rr)

				if qq.Type == dnsmessage.TypePTR && !r.brief {
					resp.Additionals = append(resp.Additionals, r.about(rr.Body.(*dnsmessage.PTRResource).PTR)...)
				}
			}
		}

		if len(resp.Answers) == 0 {
			continue
		}

		b, err := resp.Pack()
		if err != nil {
			panic(err)
		}

		r.conn.WriteToUDP(b, from)
	}
}

// about is every record owned by the instance, and the address of its host.
func (r *responder) about(inst dnsmessage.Name) []dnsmessage.Resource {
	var out []dnsmessage.Resource
	var host string

	for _, rr := range r.records {
		if strings.EqualFold(rr.Header.Name.String(), inst.String()) {
			out = append(out, rr)
			if srv, ok := rr.Body.(*dnsmessage.SRVResource); ok {
				host = srv.Target.String()
			}
		}
	}

	for _, rr := range r.records {
		if host != "" && strings.EqualFold(rr.Header.Name.String(), host) && rr.Header.Type == dnsmessage.TypeA {
			out = append(out, rr)
		}
	}

	return out
}

func name(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }

func hdr(n string, t dnsmessage.Type) dnsmessage.ResourceHeader {
	// The cache-flush bit, as real responders set it on unique records.
	return dnsmessage.ResourceHeader{Name: name(n), Type: t, Class: dnsmessage.ClassINET | 1<<15, TTL: 120}
}

func a(host string, ip [4]byte) dnsmessage.Resource {
	return dnsmessage.Resource{Header: hdr(host, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: ip}}
}

// printer is the records a printer announces for one service type.
func printer(instance, typ, host string, port uint16, txt ...string) []dnsmessage.Resource {
	full := instance + "." + typ + ".local."

	return []dnsmessage.Resource{
		{Header: hdr(typ+".local.", dnsmessage.TypePTR), Body: &dnsmessage.PTRResource{PTR: name(full)}},
		{Header: hdr(full, dnsmessage.TypeSRV), Body: &dnsmessage.SRVResource{Target: name(host), Port: port}},
		{Header: hdr(full, dnsmessage.TypeTXT), Body: &dnsmessage.TXTResource{TXT: txt}},
	}
}

func resolver(r *responder) *mdns.Resolver {
	return &mdns.Resolver{Addr: r.addr(), Wait: 150 * time.Millisecond}
}

func TestANameIsResolved(t *testing.T) {
	r := newResponder(t, false, a("NPI46CBDF.local.", [4]byte{192, 0, 2, 92}))

	got, err := resolver(r).Resolve(t.Context(), "npi46cbdf.local")
	if err != nil {
		t.Fatal(err)
	}

	if want := []netip.Addr{netip.MustParseAddr("192.0.2.92")}; !slices.Equal(got, want) {
		t.Errorf("resolved %v, want %v", got, want)
	}

	q := r.questions()
	if len(q) == 0 || q[0].Type != dnsmessage.TypeA || q[0].Class&(1<<15) == 0 {
		t.Errorf("asked %+v; want an A question with the unicast-response bit", q)
	}
}

func TestANameNothingAnswersForIsNotFound(t *testing.T) {
	r := newResponder(t, false)

	start := time.Now()
	_, err := resolver(r).Resolve(t.Context(), "absent.local")

	if err == nil || !strings.Contains(err.Error(), "nothing on the network answered") {
		t.Errorf("err = %v", err)
	}

	if len(r.questions()) != 3 {
		t.Errorf("asked %d times, want 3", len(r.questions()))
	}

	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestPrintersAreListed(t *testing.T) {
	recs := slices.Concat(
		printer("HP LaserJet M612 [46CBDF]", "_ipp._tcp", "NPI46CBDF.local.", 631, "rp=ipp/print", "ty=HP LaserJet M612", "TLS=1.2"),
		printer("Office Printer B", "_ipp._tcp", "printer-b.local.", 8631, "rp=ipp/print"),
		[]dnsmessage.Resource{
			a("NPI46CBDF.local.", [4]byte{192, 0, 2, 92}),
			a("printer-b.local.", [4]byte{192, 0, 2, 7}),
		},
	)

	for _, brief := range []bool{false, true} {
		r := newResponder(t, brief, recs...)

		got, err := resolver(r).Browse(t.Context(), "_ipp._tcp")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("brief %v: found %+v; want two printers", brief, got)
		}

		hp := got[0]
		if hp.Instance != "HP LaserJet M612 [46CBDF]" || hp.Host != "NPI46CBDF.local" || hp.Port != 631 ||
			hp.TXT["rp"] != "ipp/print" || hp.TXT["ty"] != "HP LaserJet M612" || hp.TXT["tls"] != "1.2" ||
			!slices.Equal(hp.Addrs, []netip.Addr{netip.MustParseAddr("192.0.2.92")}) {
			t.Errorf("brief %v: the M612 is %+v", brief, hp)
		}

		if got[1].Host != "printer-b.local" || got[1].Port != 8631 {
			t.Errorf("brief %v: the second printer is %+v", brief, got[1])
		}
	}
}

// A .local name is resolved by mDNS and then dialled; any other name is left
// to the system.
func TestTheDialerResolvesLocalNames(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.WriteString(c, "hello")
			c.Close()
		}
	}()

	r := newResponder(t, false, a("printer-b.local.", [4]byte{127, 0, 0, 1}))
	dial := resolver(r).Dialer(nil)

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	for _, addr := range []string{"printer-b.local:" + port, "127.0.0.1:" + port} {
		c, err := dial(t.Context(), "tcp", addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}

		b, _ := io.ReadAll(c)
		c.Close()

		if string(b) != "hello" {
			t.Errorf("%s: read %q", addr, b)
		}
	}

	if n := len(r.questions()); n != 1 {
		t.Errorf("%d questions; want one, for the .local name only", n)
	}
}

func TestIsLocal(t *testing.T) {
	for host, want := range map[string]bool{
		"NPI46CBDF.local": true, "printer.LOCAL.": true,
		"192.0.2.92": false, "printer.example": false, "local": false, "notlocal": false,
	} {
		if mdns.IsLocal(host) != want {
			t.Errorf("IsLocal(%q) = %v", host, !want)
		}
	}
}
