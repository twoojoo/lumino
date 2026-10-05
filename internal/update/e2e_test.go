//go:build e2e

package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end against real release files (KUBEON_E2E_DIST: a directory with
// the packages, SHA256SUMS.txt and SHA256SUMS.txt.sig; KUBEON_E2E_VERSION).
func TestE2EAppImageAndTarball(t *testing.T) {
	dist, ver := os.Getenv("KUBEON_E2E_DIST"), os.Getenv("KUBEON_E2E_VERSION")
	if dist == "" || ver == "" {
		t.Skip("set KUBEON_E2E_DIST and KUBEON_E2E_VERSION")
	}
	files, _ := os.ReadDir(dist)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			w.Write([]byte(`{"tag_name":"v` + ver + `","body":"e2e","html_url":"` + srv.URL + `","assets":[`))
			for i, f := range files {
				if i > 0 {
					w.Write([]byte(","))
				}
				w.Write([]byte(`{"name":"` + f.Name() + `","browser_download_url":"` + srv.URL + "/dl/" + f.Name() + `"}`))
			}
			w.Write([]byte("]}"))
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, filepath.Base(r.URL.Path)))
	}))
	defer srv.Close()
	t.Setenv("KUBEON_UPDATE_URL", srv.URL+"/latest")

	rel, err := Check(context.Background(), "0.1.0")
	if err != nil || rel == nil {
		t.Fatalf("check: %v %v", rel, err)
	}
	dir := t.TempDir()
	for _, k := range []Kind{KindAppImage, KindTarball} {
		target := filepath.Join(dir, map[Kind]string{KindAppImage: "kubeon.AppImage", KindTarball: "kubeon"}[k])
		_ = os.WriteFile(target, []byte("old 0.1.0"), 0o755)
		if err := Install(context.Background(), rel, Plan{Kind: k, Target: target}, nil); err != nil {
			t.Fatalf("kind %d: %v", k, err)
		}
		out, err := exec.Command(target, "--version").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "kubeon "+ver {
			t.Fatalf("installed %s runs as %q (%v)", target, out, err)
		}
		t.Logf("installed %s → %s", filepath.Base(target), strings.TrimSpace(string(out)))
	}
}

// Against a published release: KUBEON_E2E_LIVE_URL (a GitHub release API
// URL, e.g. …/releases/tags/v0.1.0-rc2), KUBEON_E2E_FROM (installed version).
func TestE2ELive(t *testing.T) {
	url, from := os.Getenv("KUBEON_E2E_LIVE_URL"), os.Getenv("KUBEON_E2E_FROM")
	if url == "" || from == "" {
		t.Skip("set KUBEON_E2E_LIVE_URL and KUBEON_E2E_FROM")
	}
	t.Setenv("KUBEON_UPDATE_URL", url)
	rel, err := Check(context.Background(), from)
	if err != nil || rel == nil {
		t.Fatalf("check from %s: %v %v", from, rel, err)
	}
	t.Logf("found %s (%d assets)", rel.Version, len(rel.Assets))
	dir := t.TempDir()
	for _, k := range []Kind{KindAppImage, KindTarball} {
		target := filepath.Join(dir, map[Kind]string{KindAppImage: "kubeon.AppImage", KindTarball: "kubeon"}[k])
		_ = os.WriteFile(target, []byte("old"), 0o755)
		if err := Install(context.Background(), rel, Plan{Kind: k, Target: target}, nil); err != nil {
			t.Fatalf("kind %d: %v", k, err)
		}
		out, err := exec.Command(target, "--version").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "kubeon "+rel.Version {
			t.Fatalf("installed %s runs as %q (%v)", target, out, err)
		}
		t.Logf("installed %s → %s", filepath.Base(target), strings.TrimSpace(string(out)))
	}
}
