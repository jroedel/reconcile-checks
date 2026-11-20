package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// files is a throwaway secrets.env, its example and release-key.pub. Never
// the repository's own (CLAUDE.md, section 6).
type files struct{ env, example, pub string }

func newFiles(t *testing.T) files {
	t.Helper()

	dir := t.TempDir()
	f := files{
		env:     filepath.Join(dir, "secrets.env"),
		example: filepath.Join(dir, "secrets.env.example"),
		pub:     filepath.Join(dir, "release-key.pub"),
	}

	example := "# Every secret this project needs.\n\n# the release key\n" + envKey + "=\n"
	if err := os.WriteFile(f.example, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}

	return f
}

func (f files) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var out, errs bytes.Buffer
	code := run(append([]string{"-env", f.env, "-example", f.example, "-pub", f.pub}, args...), &out, &errs)

	return out.String(), errs.String(), code
}

func TestAKeyIsMadeOnceAndItsHalvesArePaired(t *testing.T) {
	f := newFiles(t)

	if _, errs, code := f.run(t, "keygen"); code != 0 {
		t.Fatalf("keygen: %d %s", code, errs)
	}

	env, _ := os.ReadFile(f.env)
	if !strings.HasPrefix(string(env), "# Every secret this project needs.") {
		t.Errorf("secrets.env didn't start from the example:\n%s", env)
	}

	if info, err := os.Stat(f.env); err == nil && info.Mode().Perm()&0o077 != 0 && os.Getenv("OS") != "Windows_NT" {
		t.Errorf("secrets.env is readable by others: %v", info.Mode())
	}

	if out, errs, code := f.run(t, "check"); code != 0 || !strings.Contains(out, "one pair") {
		t.Errorf("check: %d %s %s", code, out, errs)
	}

	// Run twice, it refuses, and the key is the same.
	if _, errs, code := f.run(t, "keygen"); code == 0 || !strings.Contains(errs, "already holds a release key") {
		t.Errorf("a second keygen: %d %s", code, errs)
	}

	if again, _ := os.ReadFile(f.env); !bytes.Equal(again, env) {
		t.Error("a second keygen changed secrets.env")
	}

	// The public half can be made again from secrets.env, the same.
	pub, _ := os.ReadFile(f.pub)
	os.Remove(f.pub)

	if _, errs, code := f.run(t, "public"); code != 0 {
		t.Fatalf("public: %s", errs)
	}

	if again, _ := os.ReadFile(f.pub); !bytes.Equal(again, pub) {
		t.Error("release-key.pub made again from secrets.env differs")
	}
}

// What the release job signs, the updater checks: one line of base64, over
// the exact bytes, by the key release-key.pub holds.
func TestWhatIsSignedVerifiesAgainstTheCommittedKey(t *testing.T) {
	f := newFiles(t)
	if _, errs, code := f.run(t, "keygen"); code != 0 {
		t.Fatal(errs)
	}

	secret, _, _ := f.run(t, "secret")
	t.Setenv(secretVar, secret)

	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sums, []byte(strings.Repeat("ab", 32)+"  reconcile-checks-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, errs, code := f.run(t, "sign", sums); code != 0 {
		t.Fatal(errs)
	}

	sigLine, _ := os.ReadFile(sums + ".sig")
	if strings.Count(string(sigLine), "\n") != 1 || !strings.HasSuffix(string(sigLine), "\n") {
		t.Errorf("the signature is not one line: %q", sigLine)
	}

	if out, errs, code := f.run(t, "verify", sums); code != 0 {
		t.Errorf("verify: %s %s", out, errs)
	}

	// As foundation/selfupdate checks it.
	pub, _ := readPublic(f.pub)
	data, _ := os.ReadFile(sums)
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigLine)))
	if err != nil || !ed25519.Verify(pub, data, sig) {
		t.Error("the signature doesn't verify as the updater checks it")
	}

	// Signed by another key: refused.
	_, other, _ := ed25519.GenerateKey(nil)
	t.Setenv(secretVar, base64.StdEncoding.EncodeToString(other))

	if _, errs, code := f.run(t, "sign", sums); code != 0 {
		t.Fatal(errs)
	}

	if _, errs, code := f.run(t, "verify", sums); code == 0 || !strings.Contains(errs, "is not a signature") {
		t.Errorf("a signature by another key was verified: %s", errs)
	}
}

func TestSigningWithNoSecretSaysWhy(t *testing.T) {
	f := newFiles(t)
	t.Setenv(secretVar, "")

	if _, errs, code := f.run(t, "sign", f.example); code == 0 || !strings.Contains(errs, "secret is not set") {
		t.Errorf("signing with no secret: %d %s", code, errs)
	}

	_, damaged, _ := ed25519.GenerateKey(nil)
	damaged[40] ^= 1
	t.Setenv(secretVar, base64.StdEncoding.EncodeToString(damaged))

	if _, errs, code := f.run(t, "sign", f.example); code == 0 || !strings.Contains(errs, "don't belong together") {
		t.Errorf("signing with a damaged key: %d %s", code, errs)
	}
}

// The fingerprint is the one `reconcile-checks version` prints for the same
// key: the release job compares them.
func TestTheFingerprintIsTheProgramsKeyWord(t *testing.T) {
	f := newFiles(t)

	// 32 zero bytes, whose word cmd/reconcile-checks's tests also compute.
	if err := os.WriteFile(f.pub, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errs, code := f.run(t, "fingerprint")
	if code != 0 {
		t.Fatal(errs)
	}

	if got := strings.TrimSpace(out); got != "key:66687aadf862bd77" {
		t.Errorf("fingerprint %q", got)
	}
}
