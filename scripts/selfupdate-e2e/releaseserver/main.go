// Command releaseserver pretends to be GitHub, so that a real reconcile-checks
// binary can update itself without a real release existing. Ported from
// sion-backup's scripts/selfupdate-e2e/releaseserver; what is new here is the
// signature, which sion-backup never had.
//
// # Why it serves TLS on the real hostnames
//
// foundation/selfupdate reaches api.github.com over HTTPS and has no seam for
// pointing it somewhere else, deliberately (docs/10, "What was rejected"): a
// configurable update address is a configurable place to be handed a binary
// to run. So this doesn't ask the program to be testable. It makes a CA,
// signs a certificate for GitHub's names, and the harness puts the CA in the
// container's trust store and this server behind those names on a Docker
// network with no way out. The binary under test is the release binary,
// unchanged but for the test key it was built with, doing a real DNS lookup,
// a real TLS handshake and a real download.
//
// # The key
//
// `releaseserver -keygen <dir>` writes a fresh Ed25519 key pair, as the
// release job's would be: key.pub, the base64 of the 32-byte public key, which
// the harness builds into the binaries under test; and key.priv, the base64
// of the 64-byte private key, which this server signs SHA256SUMS with. A
// throwaway, made for each run: never the release key.
//
// # Faults
//
// The point is not the happy path, which the unit tests cover with an
// httptest server. It is that every way a release can be wrong ends with the
// computer still on the program it started with. -fault names one.
package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const faultUsage = `which way this release is broken:

  none          a correct release; the happy path
  wrong-hash    the binary is fine, SHA256SUMS lies about it (and is signed)
  truncated     half the binary, so the hash guard meets a real short read
  corrupt-mid   one flipped byte in the middle of an otherwise correct download
  no-sums       a release with no SHA256SUMS
  no-sig        a release with SHA256SUMS and no SHA256SUMS.sig
  bad-sig       a signature, by the right key, over other bytes
  other-key     a valid signature by a key the program was not built with
  no-asset      a release with no binary for this system
  prerelease    /latest answers with prerelease: true
  draft         /latest answers with draft: true
  rate-limited  403 with GitHub's rate-limit body, as a shared office address gets
  hangup        the connection closed part-way through the binary

A release whose binary reports another version than its tag needs no fault:
the harness offers a binary stamped with the wrong version, and the smoke
test in foundation/selfupdate is what has to catch it.
`

// assetPrefix is what the release's binaries are named, by AssetName.
const assetPrefix = "reconcile-checks-"

func main() {
	var (
		keygen   = flag.String("keygen", "", "write key.pub and key.priv, a fresh Ed25519 pair, into this directory and exit")
		addr     = flag.String("addr", ":443", "listen address")
		repo     = flag.String("repo", "jroedel/reconcile-checks", "owner/name this pretends to be")
		tag      = flag.String("tag", "v9.9.9", "the release tag to offer")
		assetDir = flag.String("assets", "", "directory holding reconcile-checks-<goos>-<goarch> binaries")
		keyFile  = flag.String("key", "", "key.priv, to sign SHA256SUMS with")
		hosts    = flag.String("hosts", "api.github.com,github.com,objects.githubusercontent.com", "comma-separated names the certificate covers")
		caOut    = flag.String("ca-out", "", "write the generated CA certificate here (PEM)")
		fault    = flag.String("fault", "none", faultUsage)
		ready    = flag.String("ready", "", "write this file once listening")
	)

	flag.Parse()

	if *keygen != "" {
		if err := writeKeys(*keygen); err != nil {
			log.Fatalf("releaseserver: %v", err)
		}

		return
	}

	if *assetDir == "" || *keyFile == "" {
		log.Fatal("releaseserver: -assets and -key are required")
	}

	key, err := readKey(*keyFile)
	if err != nil {
		log.Fatalf("releaseserver: %v", err)
	}

	srv := &server{repo: *repo, tag: *tag, dir: *assetDir, fault: *fault, key: key}

	cert, caPEM, err := selfSigned(strings.Split(*hosts, ","))
	if err != nil {
		log.Fatalf("releaseserver: certificate: %v", err)
	}

	if *caOut != "" {
		if err := os.WriteFile(*caOut, caPEM, 0o644); err != nil {
			log.Fatalf("releaseserver: writing the CA: %v", err)
		}
	}

	ln, err := tls.Listen("tcp", *addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		log.Fatalf("releaseserver: listen: %v", err)
	}

	log.Printf("releaseserver: %s serving %s %s (fault: %s)", ln.Addr(), *repo, *tag, *fault)

	// Written last, so that a harness waiting on it waits for a server that
	// can answer rather than a process that exists.
	if *ready != "" {
		if err := os.WriteFile(*ready, []byte(ln.Addr().String()), 0o644); err != nil {
			log.Fatalf("releaseserver: writing the ready file: %v", err)
		}
	}

	log.Fatal(http.Serve(ln, srv))
}

func writeKeys(dir string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "key.pub"), []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "key.priv"), []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600)
}

func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not an Ed25519 private key", path)
	}

	return key, nil
}

// server answers the requests selfupdate makes: the release, and its files.
type server struct {
	repo, tag, dir, fault string
	key                   ed25519.PrivateKey
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("releaseserver: %s %s%s", r.Method, r.Host, r.URL.Path)

	// Before routing: a rate-limited address is rate-limited for every
	// request, the release JSON and SHA256SUMS alike.
	if s.fault == "rate-limited" {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 203.0.113.7.",`+
			`"documentation_url":"https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting"}`)

		return
	}

	download := "/" + s.repo + "/releases/download/" + s.tag + "/"

	switch {
	case r.URL.Path == "/repos/"+s.repo+"/releases/latest":
		s.latest(w)
	case strings.HasPrefix(r.URL.Path, download):
		s.download(w, r, strings.TrimPrefix(r.URL.Path, download))
	default:
		http.NotFound(w, r)
	}
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// latest answers the release, as GitHub's API does, with the fields the
// updater reads.
func (s *server) latest(w http.ResponseWriter) {
	base := "https://github.com/" + s.repo + "/releases/download/" + s.tag + "/"

	body := struct {
		TagName    string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}{TagName: s.tag, Draft: s.fault == "draft", Prerelease: s.fault == "prerelease"}

	names, err := s.binaries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.fault != "no-asset" {
		for _, n := range names {
			body.Assets = append(body.Assets, asset{Name: n, URL: base + n})
		}
	}

	if s.fault != "no-sums" {
		body.Assets = append(body.Assets, asset{Name: "SHA256SUMS", URL: base + "SHA256SUMS"})
	}

	if s.fault != "no-sig" {
		body.Assets = append(body.Assets, asset{Name: "SHA256SUMS.sig", URL: base + "SHA256SUMS.sig"})
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("releaseserver: encoding the release: %v", err)
	}
}

// binaries is the release's binaries, by name.
func (s *server) binaries() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), assetPrefix) {
			names = append(names, e.Name())
		}
	}

	return names, nil
}

func (s *server) download(w http.ResponseWriter, r *http.Request, name string) {
	switch name {
	case "SHA256SUMS":
		sums, err := s.sums()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(sums)

		return

	case "SHA256SUMS.sig":
		sums, err := s.sums()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		key := s.key

		switch s.fault {
		case "bad-sig":
			// A real signature by the right key, of other bytes: the shape of
			// a SHA256SUMS changed after it was signed.
			sums = append(sums, "tampered\n"...)
		case "other-key":
			_, key, _ = ed25519.GenerateKey(rand.Reader)
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprintln(w, base64.StdEncoding.EncodeToString(ed25519.Sign(key, sums)))

		return
	}

	// Base, so that a path from the request can never leave the asset
	// directory.
	blob, err := os.ReadFile(filepath.Join(s.dir, filepath.Base(name)))
	if err != nil || !strings.HasPrefix(name, assetPrefix) {
		http.NotFound(w, r)
		return
	}

	switch s.fault {
	case "truncated":
		// Content-Length says the whole file and the body is half of it: an
		// unexpected EOF, the shape a dropped link has.
		w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
		w.Write(blob[:len(blob)/2])

		return

	case "corrupt-mid":
		blob[len(blob)/2] ^= 0xff

	case "hangup":
		w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
		w.Write(blob[:len(blob)/2])

		// Take the connection away rather than closing it politely: a reset
		// is what a lost link looks like.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				if tcp, ok := conn.(*net.TCPConn); ok {
					tcp.SetLinger(0)
				}

				conn.Close()
			}
		}

		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(blob)
}

// sums is SHA256SUMS in sha256sum's own format, over the release's binaries.
func (s *server) sums() ([]byte, error) {
	names, err := s.binaries()
	if err != nil {
		return nil, err
	}

	var out strings.Builder

	for _, n := range names {
		blob, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			return nil, err
		}

		sum := sha256.Sum256(blob)
		hexsum := hex.EncodeToString(sum[:])

		if s.fault == "wrong-hash" {
			hexsum = strings.Repeat("de", 32)
		}

		fmt.Fprintf(&out, "%s  %s\n", hexsum, n)
	}

	return []byte(out.String()), nil
}

// selfSigned makes a CA and one leaf certificate covering hosts.
func selfSigned(hosts []string) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "reconcile-checks self-update test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}
