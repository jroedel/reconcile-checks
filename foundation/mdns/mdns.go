// Package mdns asks the local network about .local names and the services
// devices announce (RFC 6762, RFC 6763): enough to reach a printer by the
// name it gives itself, and to list the printers on the network.
//
// It exists because a pure-Go Linux build (CGO_ENABLED=0, docs/10) resolves
// names with Go's own resolver, which doesn't do mDNS: NPI46CBDF.local is not
// found, though every other program on the computer finds it through the
// system's resolver. Windows 10 and later resolve .local names themselves, so
// there the dialler is the system's and this package is used only to list
// printers (docs/05-printing.md, "Finding the printer").
//
// # One-shot queries, and no listening
//
// Every query is a one-shot query (RFC 6762, 5.1): sent to 224.0.0.251:5353
// from an ordinary port, with the unicast-response bit set. A responder
// answers a query from a port other than 5353 directly, by unicast, to the
// port it came from (6.7), so this program never binds 5353, never joins the
// multicast group, and so never makes Windows Firewall ask about it. It
// misses announcements made while it isn't asking, which a program that asks
// when it needs to know doesn't need.
//
// IPv4 only: the printers it is for answer on IPv4, and an IPv6 address on an
// office network is rarely the one a printer is configured by.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// Group is the mDNS IPv4 multicast address and port.
const Group = "224.0.0.251:5353"

// Resolver asks the network.
type Resolver struct {
	// Addr is where queries are sent: Group, unless a test points it at a
	// responder of its own.
	Addr string

	// Wait is how long one round of a query waits for answers; three rounds
	// are sent (RFC 6762 asks a one-shot querier to retry). Zero is a
	// second.
	Wait time.Duration
}

// ErrNotFound is a name or service nothing answered for.
var ErrNotFound = errors.New("nothing on the network answered")

// unicastResponse is the top bit of a question's class: "answer me directly".
const unicastResponse = 1 << 15

// IsLocal reports whether host is a .local name, which only mDNS resolves.
func IsLocal(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local")
}

// Resolve finds a .local name's IPv4 addresses.
func (r *Resolver) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	name, err := fqdn(host)
	if err != nil {
		return nil, err
	}

	var addrs []netip.Addr

	err = r.ask(ctx, []dnsmessage.Question{question(name, dnsmessage.TypeA)}, func(rr dnsmessage.Resource) bool {
		if a, ok := rr.Body.(*dnsmessage.AResource); ok && strings.EqualFold(rr.Header.Name.String(), name.String()) {
			addr := netip.AddrFrom4(a.A)
			if !slices.Contains(addrs, addr) {
				addrs = append(addrs, addr)
			}
		}

		return len(addrs) > 0
	})
	if err != nil {
		return nil, err
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s: %w", host, ErrNotFound)
	}

	return addrs, nil
}

// Service is one instance of a service, as it announces itself.
type Service struct {
	Instance string            // "HP LaserJet M612 [46CBDF]"
	Type     string            // "_ipp._tcp"
	Host     string            // "NPI46CBDF.local"
	Port     int               // 631
	Addrs    []netip.Addr      // its IPv4 addresses
	TXT      map[string]string // its TXT record: rp, ty, pdl, …
}

// Browse lists the instances of a service type, such as "_ipp._tcp", that
// answer within the wait. What doesn't answer isn't listed: a printer asleep
// may need a second look.
func (r *Resolver) Browse(ctx context.Context, service string) ([]Service, error) {
	typ, err := fqdn(service + ".local")
	if err != nil {
		return nil, err
	}

	found := map[string]*Service{} // by instance name
	hosts := map[string][]netip.Addr{}

	collect := func(rr dnsmessage.Resource) {
		owner := strings.ToLower(rr.Header.Name.String())

		switch b := rr.Body.(type) {
		case *dnsmessage.PTRResource:
			if strings.EqualFold(owner, typ.String()) {
				inst := b.PTR.String()
				if found[strings.ToLower(inst)] == nil {
					found[strings.ToLower(inst)] = &Service{Instance: instanceName(inst, typ.String()), Type: service}
				}
			}

		case *dnsmessage.SRVResource:
			s := found[owner]
			if s == nil {
				s = &Service{Instance: instanceName(rr.Header.Name.String(), typ.String()), Type: service}
				found[owner] = s
			}
			s.Host = strings.TrimSuffix(b.Target.String(), ".")
			s.Port = int(b.Port)

		case *dnsmessage.TXTResource:
			s := found[owner]
			if s == nil {
				s = &Service{Instance: instanceName(rr.Header.Name.String(), typ.String()), Type: service}
				found[owner] = s
			}
			s.TXT = txt(b.TXT)

		case *dnsmessage.AResource:
			addr := netip.AddrFrom4(b.A)
			h := strings.TrimSuffix(owner, ".")
			if !slices.Contains(hosts[h], addr) {
				hosts[h] = append(hosts[h], addr)
			}
		}
	}

	err = r.ask(ctx, []dnsmessage.Question{question(typ, dnsmessage.TypePTR)}, func(rr dnsmessage.Resource) bool {
		collect(rr)
		return false // keep listening: there may be more than one printer
	})
	if err != nil {
		return nil, err
	}

	// Most responders send the SRV, TXT and address with the PTR; ask for
	// what one left out.
	for key, s := range found {
		if s.Host != "" && s.TXT != nil {
			continue
		}

		inst, err := dnsmessage.NewName(key)
		if err != nil {
			continue
		}

		_ = r.ask(ctx, []dnsmessage.Question{question(inst, dnsmessage.TypeSRV), question(inst, dnsmessage.TypeTXT)}, func(rr dnsmessage.Resource) bool {
			collect(rr)
			return s.Host != "" && s.TXT != nil
		})
	}

	var out []Service
	for _, s := range found {
		if s.Host == "" || s.Port == 0 {
			continue
		}

		s.Addrs = hosts[strings.ToLower(s.Host)]
		if len(s.Addrs) == 0 {
			if addrs, err := r.Resolve(ctx, s.Host); err == nil {
				s.Addrs = addrs
			}
		}

		out = append(out, *s)
	}

	slices.SortFunc(out, func(a, b Service) int { return strings.Compare(a.Instance, b.Instance) })

	return out, nil
}

// Dialer is a DialContext that resolves .local names with Resolve and every
// other name as base would.
func (r *Resolver) Dialer(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{Timeout: 10 * time.Second}
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || !IsLocal(host) {
			return base.DialContext(ctx, network, addr)
		}

		ips, err := r.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}

		var errs []error
		for _, ip := range ips {
			c, err := base.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			errs = append(errs, err)
		}

		return nil, errors.Join(errs...)
	}
}

// ask sends the questions up to three times, a Wait apart, and hands every
// record in every answer to each, until each says it has enough or the time
// is up.
func (r *Resolver) ask(ctx context.Context, qs []dnsmessage.Question, each func(dnsmessage.Resource) bool) error {
	dst, err := net.ResolveUDPAddr("udp4", r.addr())
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return fmt.Errorf("asking the network: %w", err)
	}
	defer conn.Close()

	msg := dnsmessage.Message{Questions: qs}

	packet, err := msg.Pack()
	if err != nil {
		return err
	}

	send := func() error {
		_, err := conn.WriteToUDP(packet, dst)
		return err
	}

	if dst.IP.IsMulticast() {
		send = multicaster(conn, packet, dst)
	}

	wait := r.Wait
	if wait <= 0 {
		wait = time.Second
	}

	buf := make([]byte, 9000) // the most an mDNS packet may be, RFC 6762, 17

	for range 3 {
		if err := send(); err != nil {
			return fmt.Errorf("asking the network: %w", err)
		}

		deadline := time.Now().Add(wait)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}

		if err := conn.SetReadDeadline(deadline); err != nil {
			return err
		}

		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
					break
				}

				return fmt.Errorf("listening for an answer: %w", err)
			}

			var answer dnsmessage.Message
			if answer.Unpack(buf[:n]) != nil || !answer.Header.Response {
				continue
			}

			done := false
			for _, rr := range slices.Concat(answer.Answers, answer.Additionals) {
				if each(rr) {
					done = true
				}
			}

			if done {
				return nil
			}
		}

		if err := ctx.Err(); err != nil {
			return err
		}
	}

	return nil
}

// multicaster sends the packet out of every interface a printer could be on,
// not only the one the routing table picks for multicast.
//
// The route is no guide: on the development computer it goes into a VPN
// tunnel, where no printer will ever be, and the query never reached the
// office network. avahi and Windows' own resolver ask on every interface for
// the same reason. Skipped: an interface that is down, can't multicast, is
// loopback, or is point-to-point (a tunnel), or has no IPv4 address.
func multicaster(conn *net.UDPConn, packet []byte, dst *net.UDPAddr) func() error {
	pc := ipv4.NewPacketConn(conn)

	// RFC 6762, 11: queries are sent with a hop limit of 255, so a responder
	// can tell they came from the link.
	_ = pc.SetMulticastTTL(255)

	return func() error {
		ifs, err := net.Interfaces()
		if err != nil {
			return err
		}

		var errs []error
		sent := 0

		for _, ifi := range ifs {
			if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 ||
				ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagPointToPoint != 0 || !hasIPv4(ifi) {
				continue
			}

			if err := pc.SetMulticastInterface(&ifi); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", ifi.Name, err))
				continue
			}

			if _, err := conn.WriteToUDP(packet, dst); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", ifi.Name, err))
				continue
			}

			sent++
		}

		if sent == 0 {
			if len(errs) == 0 {
				return errors.New("this computer has no network that can be asked")
			}

			return errors.Join(errs...)
		}

		return nil
	}
}

func hasIPv4(ifi net.Interface) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}

	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return true
		}
	}

	return false
}

func (r *Resolver) addr() string {
	if r.Addr != "" {
		return r.Addr
	}

	return Group
}

func question(name dnsmessage.Name, t dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: name, Type: t, Class: dnsmessage.ClassINET | unicastResponse}
}

func fqdn(s string) (dnsmessage.Name, error) {
	if !strings.HasSuffix(s, ".") {
		s += "."
	}

	n, err := dnsmessage.NewName(s)
	if err != nil {
		return n, fmt.Errorf("%q is not a name that can be asked about: %w", s, err)
	}

	return n, nil
}

// instanceName is an instance's own name, without the service type after it,
// with DNS's escapes undone: "HP LaserJet M612 [46CBDF]".
func instanceName(full, typ string) string {
	name := strings.TrimSuffix(full, "."+typ)
	name = strings.TrimSuffix(name, "."+strings.TrimSuffix(typ, "."))

	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c != '\\' || i+1 >= len(name) {
			b.WriteByte(c)
			continue
		}

		// \DDD is a byte in decimal; \x is x.
		if i+3 < len(name) && isDigit(name[i+1]) && isDigit(name[i+2]) && isDigit(name[i+3]) {
			b.WriteByte((name[i+1]-'0')*100 + (name[i+2]-'0')*10 + (name[i+3] - '0'))
			i += 3
			continue
		}

		b.WriteByte(name[i+1])
		i++
	}

	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func txt(ss []string) map[string]string {
	m := map[string]string{}
	for _, s := range ss {
		k, v, _ := strings.Cut(s, "=")
		if k != "" {
			m[strings.ToLower(k)] = v
		}
	}

	return m
}
