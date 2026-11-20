// Command releasekey makes, keeps and uses the release signing key
// (docs/10-install-update-and-releases.md, "The release signing key").
//
// The private half lives in secrets.env at the repository's root, as
// RELEASE_SIGNING_KEY_B64, which the person keeps in their password vault,
// as every sibling project keeps its secrets.env, so that a lost checkout
// plus that one file is a working setup again. From there it goes to one
// place only, the GitHub Actions secret RELEASE_SIGNING_KEY, which the
// release job signs with. The public half is release-key.pub, committed, and
// built into every release (the Makefile's RELEASE_KEY).
//
//	releasekey keygen       make the pair: the private half into secrets.env, the public into release-key.pub
//	releasekey public       write release-key.pub again from secrets.env
//	releasekey secret       print the private half, for `gh secret set` (make release-send-secret)
//	releasekey check        does secrets.env's key match release-key.pub?
//	releasekey sign FILE    sign FILE with $RELEASE_SIGNING_KEY into FILE.sig (the release job)
//	releasekey verify FILE  check FILE.sig against release-key.pub (the release job, before publishing)
//	releasekey fingerprint  key:<16 hex> for release-key.pub, as `reconcile-checks version` prints it
//
// The commands that read secrets.env are a person's, reached through `make
// release-*` targets that .claude/settings.json denies to agents (CLAUDE.md,
// section 6). sign reads the key from the environment, where the release job
// puts the secret, and never from secrets.env.
package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// envKey is the line in secrets.env that holds the private half: the
// standard base64 of the 64-byte Ed25519 private key.
const envKey = "RELEASE_SIGNING_KEY_B64"

// secretVar is the environment variable the release job puts the GitHub
// secret in.
const secretVar = "RELEASE_SIGNING_KEY"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("releasekey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	env := fs.String("env", "secrets.env", "the secrets file")
	example := fs.String("example", "secrets.env.example", "what a new secrets file starts from")
	pub := fs.String("pub", "release-key.pub", "the public half, committed")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "releasekey: say keygen, public, secret, check, sign FILE, verify FILE or fingerprint")
		return 2
	}

	var err error

	switch cmd, rest := fs.Arg(0), fs.Args()[1:]; {
	case cmd == "keygen":
		err = keygen(*env, *example, *pub, stdout)
	case cmd == "public":
		err = public(*env, *pub, stdout)
	case cmd == "secret":
		err = secret(*env, stdout)
	case cmd == "check":
		err = check(*env, *pub, stdout)
	case cmd == "sign" && len(rest) == 1:
		err = sign(os.Getenv(secretVar), rest[0])
	case cmd == "verify" && len(rest) == 1:
		err = verify(*pub, rest[0], stdout)
	case cmd == "fingerprint":
		err = fingerprint(*pub, stdout)
	default:
		fmt.Fprintf(stderr, "releasekey: %q is not a command this has\n", strings.Join(fs.Args(), " "))
		return 2
	}

	if err != nil {
		fmt.Fprintln(stderr, "releasekey:", err)
		return 1
	}

	return 0
}

// keygen makes a key pair, unless secrets.env already holds one: replacing
// the key is a decision with a cost (every installed copy trusts only the old
// one, docs/10), not something to do by running a target twice.
func keygen(env, example, pub string, stdout io.Writer) error {
	lines, err := readLines(env)

	switch {
	case errors.Is(err, os.ErrNotExist):
		if lines, err = readLines(example); err != nil {
			return fmt.Errorf("there is no %s, and no %s to start one from: %w", env, example, err)
		}
	case err != nil:
		return err
	}

	if v, _ := value(lines, envKey); v != "" {
		return fmt.Errorf("%s already holds a release key. Replacing it would leave every installed copy unable to trust a release, so it is not done by running this again; see docs/10, \"The release signing key\"", env)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	lines = set(lines, envKey, base64.StdEncoding.EncodeToString(private))

	if err := writeLines(env, lines, 0o600); err != nil {
		return err
	}

	if err := writePublic(pub, public); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Made the release key %s.\n", word(public))
	fmt.Fprintf(stdout, "  The private half is in %s: save that file in your password vault now.\n", env)
	fmt.Fprintf(stdout, "  The public half is %s: commit it, by pull request.\n", pub)
	fmt.Fprintln(stdout, "  Then: make release-send-secret, and make release-status.")

	return nil
}

func public(env, pub string, stdout io.Writer) error {
	key, err := fromEnv(env)
	if err != nil {
		return err
	}

	public := key.Public().(ed25519.PublicKey)

	if err := writePublic(pub, public); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Wrote %s, %s.\n", pub, word(public))

	return nil
}

func secret(env string, stdout io.Writer) error {
	key, err := fromEnv(env)
	if err != nil {
		return err
	}

	_, err = fmt.Fprint(stdout, base64.StdEncoding.EncodeToString(key))

	return err
}

func check(env, pub string, stdout io.Writer) error {
	key, err := fromEnv(env)
	if err != nil {
		return err
	}

	committed, err := readPublic(pub)
	if err != nil {
		return err
	}

	if !committed.Equal(key.Public()) {
		return fmt.Errorf("%s's key is %s, and %s is %s: they are not one pair", env, word(key.Public().(ed25519.PublicKey)), pub, word(committed))
	}

	fmt.Fprintf(stdout, "%s and %s are one pair, %s.\n", env, pub, word(committed))

	return nil
}

// sign writes FILE.sig: one line, the standard base64 of the raw signature
// over FILE's exact bytes, which is what foundation/selfupdate checks.
func sign(encoded, file string) error {
	if encoded == "" {
		return fmt.Errorf("$%s is empty: the release job's secret is not set (make release-send-secret)", secretVar)
	}

	key, err := parsePrivate(encoded)
	if err != nil {
		return fmt.Errorf("$%s: %w", secretVar, err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))

	return os.WriteFile(file+".sig", []byte(sig+"\n"), 0o644)
}

// verify checks FILE.sig against the committed public key: in the release
// job, that the secret it signed with is the key every release is built to
// trust.
func verify(pub, file string, stdout io.Writer) error {
	key, err := readPublic(pub)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	encoded, err := os.ReadFile(file + ".sig")
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, data, sig) {
		return fmt.Errorf("%s.sig is not a signature of %s by %s (%s)", file, file, pub, word(key))
	}

	fmt.Fprintf(stdout, "%s.sig is a signature of %s by %s.\n", file, file, word(key))

	return nil
}

func fingerprint(pub string, stdout io.Writer) error {
	key, err := readPublic(pub)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, word(key))

	return err
}

// word is the key's fingerprint as `reconcile-checks version` prints it:
// "key:" and the first 16 hex digits of the SHA-256 of its 32 bytes
// (cmd/reconcile-checks, keyWord).
func word(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return "key:" + hex.EncodeToString(sum[:8])
}

func fromEnv(env string) (ed25519.PrivateKey, error) {
	lines, err := readLines(env)
	if err != nil {
		return nil, err
	}

	v, ok := value(lines, envKey)
	if !ok || v == "" {
		return nil, fmt.Errorf("%s has no %s: make release-keygen makes one, or paste secrets.env back from the vault", env, envKey)
	}

	key, err := parsePrivate(v)
	if err != nil {
		return nil, fmt.Errorf("%s's %s: %w", env, envKey, err)
	}

	return key, nil
}

func parsePrivate(encoded string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("not the base64 of a 64-byte Ed25519 private key")
	}

	key := ed25519.PrivateKey(b)

	// The last 32 bytes are the public half; a key pasted with a byte wrong
	// would sign with one key and claim another.
	if !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) {
		return nil, errors.New("its two halves don't belong together; it was damaged in copying")
	}

	return key, nil
}

func readPublic(pub string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(pub)
	if err != nil {
		return nil, fmt.Errorf("%w; make release-keygen makes it, or make release-public-key from secrets.env", err)
	}

	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s is not the base64 of a 32-byte Ed25519 public key", pub)
	}

	return b, nil
}

func writePublic(pub string, key ed25519.PublicKey) error {
	return os.WriteFile(pub, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o644)
}

// --- secrets.env, as lines -------------------------------------------------------
//
// KEY=value lines, comments and blank lines kept as they are, as reconcile's
// scripts/env.sh reads them: a value is everything after the first =, and a
// pair of surrounding quotes is taken off.

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}

	return lines, sc.Err()
}

func value(lines []string, key string) (string, bool) {
	for _, l := range lines {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(k) == key {
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}

			return v, true
		}
	}

	return "", false
}

// set gives key the value, on its own line if it has one, or at the end.
func set(lines []string, key, v string) []string {
	for i, l := range lines {
		if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == key {
			lines[i] = key + "=" + v
			return lines
		}
	}

	return append(lines, key+"="+v)
}

func writeLines(path string, lines []string, mode os.FileMode) error {
	data := strings.Join(lines, "\n") + "\n"

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), mode); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
