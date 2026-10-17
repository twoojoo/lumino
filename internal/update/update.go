// Package update checks GitHub releases for a newer lumino and installs it
// in place: the AppImage, the tar.gz binary, the portable Windows exe or the
// macOS app bundle is replaced, then lumino restarts. Package-manager
// installs (deb/rpm in /usr) only get a link to the release.
//
// Every download is checked against SHA256SUMS.txt, whose ed25519 signature
// (SHA256SUMS.txt.sig, made in CI) must verify with PublicKey: a release
// that is unsigned, or signed by another key, is never installed.
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is the "latest release" endpoint of the public repository that
// hosts the packages (drafts and prereleases excluded). LUMINO_UPDATE_URL
// overrides it.
const DefaultURL = "https://api.github.com/repos/twoojoo/lumino-releases/releases/latest"

// PublicKey verifies SHA256SUMS.txt.sig (base64 ed25519 public key; its
// private half signs releases in CI, secret UPDATE_SIGNING_KEY).
var PublicKey = "CK1TEM6TZg/WTFIoj/e46uM9J5TtSx7RFQ5P6fUXFGE="

const (
	sumsName = "SHA256SUMS.txt"
	sigName  = "SHA256SUMS.txt.sig"
)

// Release is a published version.
type Release struct {
	Version string            // without "v"
	Notes   string            // markdown
	PageURL string            // human page
	Assets  map[string]string // file name → download URL
}

// URL returns the endpoint to query.
func URL() string {
	if u := os.Getenv("LUMINO_UPDATE_URL"); u != "" {
		return u
	}
	return DefaultURL
}

var releaseRx = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.]+))?$`)

// IsRelease reports whether v is a published version, not a dev or git
// describe build ("dev", "0.1.0-3-gabc123", "0.1.0-dirty", a bare hash).
func IsRelease(v string) bool {
	m := releaseRx.FindStringSubmatch(v)
	return m != nil && !strings.Contains(m[4], "dirty")
}

// Newer reports whether version a is newer than b (semver; a release sorts
// after its prereleases).
func Newer(a, b string) bool {
	ma, mb := releaseRx.FindStringSubmatch(a), releaseRx.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x > y
		}
	}
	switch {
	case ma[4] == mb[4]:
		return false
	case ma[4] == "":
		return true
	case mb[4] == "":
		return false
	}
	return comparePre(ma[4], mb[4]) > 0
}

func comparePre(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, ex := strconv.Atoi(pa[i])
		y, ey := strconv.Atoi(pb[i])
		switch {
		case ex == nil && ey == nil && x != y:
			if x > y {
				return 1
			}
			return -1
		case ex == nil && ey != nil:
			return -1
		case ex != nil && ey == nil:
			return 1
		case pa[i] != pb[i]:
			return strings.Compare(pa[i], pb[i])
		}
	}
	return len(pa) - len(pb)
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// Latest fetches the latest release.
func Latest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lumino-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release check: %s", resp.Status)
	}
	var gh struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&gh); err != nil {
		return nil, err
	}
	r := &Release{Version: strings.TrimPrefix(gh.TagName, "v"), Notes: gh.Body, PageURL: gh.HTMLURL, Assets: map[string]string{}}
	for _, a := range gh.Assets {
		r.Assets[a.Name] = a.URL
	}
	if !releaseRx.MatchString(r.Version) {
		return nil, fmt.Errorf("release check: unexpected tag %q", gh.TagName)
	}
	return r, nil
}

// Check returns the latest release when it is newer than current.
func Check(ctx context.Context, current string) (*Release, error) {
	if !IsRelease(current) {
		return nil, nil // dev builds never update themselves
	}
	r, err := Latest(ctx)
	if err != nil || !Newer(r.Version, current) {
		return nil, err
	}
	return r, nil
}

// verifySums checks the signature of SHA256SUMS.txt and parses it.
func verifySums(sums, sig []byte) (map[string]string, error) {
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("this build has no update signing key")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(pub, sums, s) {
		return nil, errors.New("release checksums are not signed by the Lumino release key")
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(sums), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out, nil
}

// assetName is the release file for this platform and install kind.
func assetName(k Kind, version string) string {
	arch := runtime.GOARCH
	switch k {
	case KindAppImage:
		a := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
		return fmt.Sprintf("lumino-%s-%s.AppImage", version, a)
	case KindTarball:
		return fmt.Sprintf("lumino-%s-linux-%s.tar.gz", version, arch)
	case KindWindows:
		return fmt.Sprintf("lumino-%s-windows-%s.zip", version, arch)
	case KindMacApp:
		return fmt.Sprintf("lumino-%s-macos-universal.zip", version)
	}
	return ""
}
