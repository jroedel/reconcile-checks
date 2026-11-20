package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"time"
)

// The two files that vouch for a release, beside its binaries.
//
// SHA256SUMS is sha256sum's own output over every other file in the release:
// "<64 hex>  <name>" a line (a "*" before the name, binary mode, is accepted).
//
// SHA256SUMS.sig is the Ed25519 signature of SHA256SUMS's exact bytes, as one
// line of standard base64 (with padding) of the raw 64-byte signature, and
// nothing else; surrounding whitespace is ignored. Raw rather than signify's
// or minisign's format because there is one key and one signer, so a key id
// and a comment line would be parsed for nothing, and because the release job
// can make it with nothing but OpenSSL:
//
//	openssl pkeyutl -sign -rawin -inkey key.pem -in SHA256SUMS | base64 -w0 > SHA256SUMS.sig
//
// Base64 rather than the 64 bytes themselves so that it is text, which a
// person can look at on the release page and a test can write out.
const (
	sumsAsset = "SHA256SUMS"
	sigAsset  = "SHA256SUMS.sig"
)

// userAgent is sent with every request; GitHub's API refuses one without.
const userAgent = "reconcile-checks-selfupdate"

// errNotFound distinguishes the one status worth a sentence of its own.
var errNotFound = errors.New("not found")

// apiTimeout bounds each small request: the release, SHA256SUMS and its
// signature. Short, because the check at start runs in the background and a
// slow answer is better given up on than waited for.
const apiTimeout = 30 * time.Second

// maxMetadata bounds the release JSON and the two text files, none of which
// is near it.
const maxMetadata = 1 << 20

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`

	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest finds the release to install, if there is one.
//
// It reports false, with no error, when there is nothing to do: this is a
// development build, the latest release is not newer, or it is one this
// computer refused. Otherwise the release it returns has a hash read from a
// SHA256SUMS whose signature has been checked with the key. An updater
// without a key returns [ErrNoKey] before asking anything of anyone.
func (u *Updater) Latest(ctx context.Context) (Release, bool, error) {
	if len(u.key) == 0 {
		return Release{}, false, ErrNoKey
	}

	// A development build never updates, so it need not ask. Checked here as
	// well as by Newer so that a working copy makes no request at all.
	if _, ok := parse(u.current); !ok {
		u.log.Debug("not checking for updates: this is not a release build", "version", u.current)

		return Release{}, false, nil
	}

	var rel ghRelease

	if err := u.fetchJSON(ctx, u.api+"/repos/"+u.repo+"/releases/latest", &rel); err != nil {
		// A repository with no published release answers 404 here, which is
		// an ordinary state (sion-backup#11): said plainly, because the raw
		// message is a bare 404 against an API address and reads like a bug.
		if errors.Is(err, errNotFound) {
			return Release{}, false, fmt.Errorf("%w (%s has published no release yet)", ErrNoRelease, u.repo)
		}

		return Release{}, false, err
	}

	// GitHub leaves drafts and pre-releases out of /latest already. Checked
	// anyway, because a pre-release reaching both computers by accident is not
	// a mistake that announces itself — and a draft is the release workflow
	// still running its gates (docs/10).
	if rel.Draft {
		return Release{}, false, fmt.Errorf("%w (%s is still a draft)", ErrNoRelease, rel.TagName)
	}

	if rel.Prerelease {
		return Release{}, false, fmt.Errorf("%w (%s is a pre-release)", ErrNoRelease, rel.TagName)
	}

	if !Newer(u.current, rel.TagName) {
		return Release{}, false, nil
	}

	// A version this computer already gave up on, by probation or at a
	// restart. Nothing to do rather than an error (sion-backup#22): it stays
	// the latest release until the next one, and saying so at every check
	// would bury what matters. Refused shows it; Forget undoes it.
	if slices.Contains(u.Refused(), rel.TagName) {
		u.log.Info("not installing a version this computer gave up on", "version", rel.TagName)

		return Release{}, false, nil
	}

	want := AssetName(runtime.GOOS, runtime.GOARCH)

	var binary, sums, sig string

	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			binary = a.URL
		case sumsAsset:
			sums = a.URL
		case sigAsset:
			sig = a.URL
		}
	}

	if binary == "" {
		return Release{}, false, fmt.Errorf("%w (%s has no %s)", ErrNoRelease, rel.TagName, want)
	}

	// Both refused rather than installed unchecked. The binary this would be
	// writes checks and holds the key to Reconcile.
	if sums == "" {
		return Release{}, false, fmt.Errorf("%w (%s publishes no %s)", ErrUnverified, rel.TagName, sumsAsset)
	}

	if sig == "" {
		return Release{}, false, fmt.Errorf("%w (%s publishes no %s)", ErrUnverified, rel.TagName, sigAsset)
	}

	sum, err := u.verifiedSum(ctx, rel.TagName, sums, sig, want)
	if err != nil {
		return Release{}, false, err
	}

	return Release{Version: rel.TagName, URL: binary, SHA256: sum}, true, nil
}

// verifiedSum fetches SHA256SUMS and its signature, checks the signature over
// the bytes exactly as they arrived, and only then reads asset's hash out of
// them.
func (u *Updater) verifiedSum(ctx context.Context, version, sumsURL, sigURL, asset string) (string, error) {
	sums, err := u.fetch(ctx, sumsURL, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("%w (%s's %s: %w)", ErrUnverified, version, sumsAsset, err)
	}

	encoded, err := u.fetch(ctx, sigURL, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("%w (%s's %s: %w)", ErrUnverified, version, sigAsset, err)
	}

	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(encoded)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("%w (%s's %s is not a signature)", ErrUnverified, version, sigAsset)
	}

	if !ed25519.Verify(u.key, sums, sig) {
		return "", fmt.Errorf("%w (%s's %s was not signed with this program's release key)",
			ErrUnverified, version, sumsAsset)
	}

	return checksum(sums, asset, version)
}

// checksum pulls one asset's hash out of a sha256sum-format file. Called only
// on a file whose signature has been checked.
func checksum(sums []byte, asset, version string) (string, error) {
	for line := range strings.Lines(string(sums)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}

		sum := strings.ToLower(fields[0])

		if b, err := hex.DecodeString(sum); err != nil || len(b) != 32 {
			return "", fmt.Errorf("%w (%s's %s has no proper hash for %s)", ErrUnverified, version, sumsAsset, asset)
		}

		return sum, nil
	}

	return "", fmt.Errorf("%w (%s's %s does not list %s)", ErrUnverified, version, sumsAsset, asset)
}

func (u *Updater) fetchJSON(ctx context.Context, url string, out any) error {
	body, err := u.fetch(ctx, url, "application/vnd.github+json")
	if err != nil {
		return err
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GitHub's answer about the latest release could not be read; the next check tries again: %w", err)
	}

	return nil
}

func (u *Updater) fetch(ctx context.Context, url, accept string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%q is not an address: %w", url, err)
	}

	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)

	resp, err := u.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach GitHub to check for an update; the next check tries again: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errNotFound, url)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s to %s; the next check tries again", resp.Status, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, fmt.Errorf("couldn't read GitHub's answer; the next check tries again: %w", err)
	}

	// Refused rather than cut short: a truncated SHA256SUMS fails its
	// signature anyway, but would say so less helpfully.
	if len(body) > maxMetadata {
		return nil, fmt.Errorf("GitHub's answer from %s is far larger than it should be", url)
	}

	return body, nil
}
