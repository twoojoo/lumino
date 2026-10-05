package prefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLooksProduction(t *testing.T) {
	for _, c := range []string{"prod", "eks-prod-eu", "gke_acme_production", "aks-prd", "live-cluster", "PROD"} {
		if !LooksProduction(c) {
			t.Errorf("%q should look like production", c)
		}
	}
	for _, c := range []string{"kind-kubeon-dev", "staging", "product-team-dev", "olive"} {
		if LooksProduction(c) {
			t.Errorf("%q should not look like production", c)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	t.Setenv("KUBEON_CONFIG_DIR", t.TempDir())
	p := Load()
	if p.Theme != "dark" || !p.Protected("prod") || p.Protected("dev") {
		t.Fatal("defaults")
	}
	p.Theme = "light"
	p.SetPolicy("prod", false, true)
	p.SavePortForward(PortForward{Context: "c", GVR: "v1/pods", Path: "ns/p", Local: 8080, Remote: 80, AutoStart: true})
	p.SavePortForward(PortForward{Context: "c", GVR: "v1/pods", Path: "ns/p", Local: 9090, Remote: 80, AutoStart: true})
	p.AddBookmark(Bookmark{Name: "web pods", GVR: "v1/pods", Namespace: "demo", Filter: "web"})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	q := Load()
	if q.Theme != "light" || q.Protected("prod") || !q.ReadOnly("prod") {
		t.Fatalf("policy %+v", q.Contexts)
	}
	if f := q.SavedForwards("c"); len(f) != 1 || f[0].Local != 9090 {
		t.Fatalf("forwards %+v", f)
	}
	if b := q.BookmarksFor("any"); len(b) != 1 || b[0].Filter != "web" {
		t.Fatalf("bookmarks %+v", b)
	}
}

func TestMigrateLegacyDirs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG directories (and k9desk installs) only exist on Linux")
	}
	cfg, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("KUBEON_CONFIG_DIR", "")
	_ = os.MkdirAll(filepath.Join(cfg, "k9desk"), 0o700)
	_ = os.WriteFile(filepath.Join(cfg, "k9desk", "settings.json"), []byte(`{"theme":"light"}`), 0o600)
	_ = os.MkdirAll(filepath.Join(cache, "k9desk"), 0o700)
	_ = os.WriteFile(filepath.Join(cache, "k9desk", "k9desk.log"), []byte("x"), 0o600)
	MigrateLegacyDirs()
	if Load().Theme != "light" {
		t.Fatal("settings not migrated")
	}
	if _, err := os.Stat(filepath.Join(cache, "kubeon", "kubeon.log")); err != nil {
		t.Fatal("log not migrated")
	}
}

func TestBrokenSettingsQuarantined(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBEON_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(path, []byte(`{"contexts": {"prod": {"readOnly": tru`), 0o600)
	p := Load()
	if p.LoadError == nil {
		t.Fatal("expected a load error")
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(path + ".broken-*")
	if len(m) != 1 {
		t.Fatalf("broken file not kept: %v", m)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // no unix modes on Windows
		t.Fatalf("mode %v", st.Mode())
	}
}
