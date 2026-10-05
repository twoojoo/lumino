package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true}, {"1.0.0", "1.0.0-rc1", true}, {"1.0.0-rc2", "1.0.0-rc1", true},
		{"1.0.0-rc.10", "1.0.0-rc.9", true}, {"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false},
		{"1.0.0-rc1", "1.0.0", false}, {"v1.2.3", "1.2.2", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s,%s)=%v", c.a, c.b, got)
		}
	}
	for v, want := range map[string]bool{"0.1.0": true, "1.0.0-rc1": true, "dev": false, "0.1.0-3-gabc1234": false,
		"0.1.0-dirty": false, "abc1234": false} {
		if IsRelease(v) != want {
			t.Errorf("IsRelease(%s)=%v", v, !want)
		}
	}
}

// fakeRelease serves a GitHub-like latest release with signed checksums.
func fakeRelease(t *testing.T, version string, files map[string][]byte, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	var sums strings.Builder
	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	all := map[string][]byte{sumsName: []byte(sums.String())}
	if priv != nil {
		all[sigName] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums.String()))))
	}
	for k, v := range files {
		all[k] = v
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			var aa []asset
			for n := range all {
				aa = append(aa, asset{n, srv.URL + "/dl/" + n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "body": "notes", "html_url": srv.URL, "assets": aa})
			return
		}
		b, ok := all[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LUMINO_UPDATE_URL", srv.URL+"/latest")
	return srv
}

func testKey(t *testing.T) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := PublicKey
	PublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKey = old })
	return priv
}

func tarball(t *testing.T, version, bin string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "lumino-" + version + "/lumino", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(bin))
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func TestInstallTarball(t *testing.T) {
	priv := testKey(t)
	asset := assetName(KindTarball, "9.9.9")
	fakeRelease(t, "9.9.9", map[string][]byte{asset: tarball(t, "9.9.9", "NEW BINARY")}, priv)
	r, err := Check(context.Background(), "0.1.0")
	if err != nil || r == nil || r.Version != "9.9.9" {
		t.Fatalf("check: %v %+v", err, r)
	}
	target := filepath.Join(t.TempDir(), "lumino")
	_ = os.WriteFile(target, []byte("OLD"), 0o755)
	var seen int64
	if err := Install(context.Background(), r, Plan{Kind: KindTarball, Target: target}, func(d, _ int64) { seen = d }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "NEW BINARY" || seen == 0 {
		t.Fatalf("target %q progress %d", b, seen)
	}
	if st, _ := os.Stat(target); runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 { // no exec bit on Windows
		t.Fatal("not executable")
	}
}

func TestInstallRejectsBadSignatures(t *testing.T) {
	asset := assetName(KindAppImage, "9.9.9")
	files := map[string][]byte{asset: []byte("APPIMAGE")}
	target := filepath.Join(t.TempDir(), "lumino.AppImage")

	testKey(t) // trusted key…
	_, other, _ := ed25519.GenerateKey(nil)
	fakeRelease(t, "9.9.9", files, other) // …but signed by another one
	r, _ := Latest(context.Background())
	if err := Install(context.Background(), r, Plan{Kind: KindAppImage, Target: target}, nil); err == nil ||
		!strings.Contains(err.Error(), "not signed by the Lumino release key") {
		t.Fatalf("foreign signature accepted: %v", err)
	}
	fakeRelease(t, "9.9.9", files, nil) // unsigned
	r, _ = Latest(context.Background())
	if err := Install(context.Background(), r, Plan{Kind: KindAppImage, Target: target}, nil); err == nil {
		t.Fatal("unsigned release accepted")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("target written")
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	priv := testKey(t)
	asset := assetName(KindAppImage, "9.9.9")
	srv := fakeRelease(t, "9.9.9", map[string][]byte{asset: []byte("GOOD")}, priv)
	r, _ := Latest(context.Background())
	r.Assets[asset] = srv.URL + "/dl/" + sumsName // serve different bytes
	target := filepath.Join(t.TempDir(), "lumino.AppImage")
	_ = os.WriteFile(target, []byte("OLD"), 0o755)
	if err := Install(context.Background(), r, Plan{Kind: KindAppImage, Target: target}, nil); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "OLD" {
		t.Fatal("target replaced despite a bad checksum")
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate}
		h.SetMode(0o755)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

func TestInstallMacAppAndWindows(t *testing.T) {
	priv := testKey(t)
	mac := assetName(KindMacApp, "9.9.9")
	win := assetName(KindWindows, "9.9.9")
	fakeRelease(t, "9.9.9", map[string][]byte{
		mac: zipOf(t, map[string]string{"Lumino.app/Contents/MacOS/lumino": "NEW MAC", "Lumino.app/Contents/Info.plist": "plist"}),
		win: zipOf(t, map[string]string{"lumino.exe": "NEW EXE", "LICENSE.k9s.txt": "x"}),
	}, priv)
	r, _ := Latest(context.Background())

	dir := t.TempDir()
	app := filepath.Join(dir, "lumino.app")
	_ = os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
	_ = os.WriteFile(filepath.Join(app, "Contents", "MacOS", "lumino"), []byte("OLD"), 0o755)
	if err := Install(context.Background(), r, Plan{Kind: KindMacApp, Target: app}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "lumino")); string(b) != "NEW MAC" {
		t.Fatalf("app %q", b)
	}

	exe := filepath.Join(dir, "lumino.exe")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	if err := Install(context.Background(), r, Plan{Kind: KindWindows, Target: exe}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "NEW EXE" {
		t.Fatalf("exe %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "OLD" {
		t.Fatal("old exe not kept for cleanup")
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	f := filepath.Join(t.TempDir(), "evil.zip")
	_ = os.WriteFile(f, zipOf(t, map[string]string{"../../escape": "x"}), 0o644)
	if err := unzipAll(f, t.TempDir()); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestDevBuildsNeverCheck(t *testing.T) {
	t.Setenv("LUMINO_UPDATE_URL", "http://127.0.0.1:1/never")
	if r, err := Check(context.Background(), "0.1.0-3-gabc1234"); r != nil || err != nil {
		t.Fatalf("dev build checked: %v %v", r, err)
	}
}
