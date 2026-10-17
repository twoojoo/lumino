package site

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
)

func build(t *testing.T) (string, *site) {
	t.Helper()
	dir := t.TempDir()
	if err := Build(dir, Options{}); err != nil {
		t.Fatal(err)
	}
	s, err := newSite(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	reTitle  = regexp.MustCompile(`<title>([^<]+)</title>`)
	reMeta   = regexp.MustCompile(`<meta (?:name|property)="([^"]+)" content="([^"]*)">`)
	reCanon  = regexp.MustCompile(`<link rel="canonical" href="([^"]+)">`)
	reLD     = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
	reLink   = regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	reH1     = regexp.MustCompile(`<h1>`)
	reLLMURL = regexp.MustCompile(`\]\((https?://[^)]+)\)`)
)

func TestPagesHaveSEOMetadata(t *testing.T) {
	dir, s := build(t)
	titles, descs := map[string]string{}, map[string]string{}
	for _, p := range s.pages {
		html := read(t, dir, p.Path+"index.html")
		m := reTitle.FindStringSubmatch(html)
		if m == nil || len(m[1]) > 70 {
			t.Errorf("%q: title missing or longer than 70 chars: %q", p.Path, m)
		}
		meta := map[string]string{}
		for _, x := range reMeta.FindAllStringSubmatch(html, -1) {
			meta[x[1]] = x[2]
		}
		d := meta["description"]
		if n := len([]rune(d)); n < 70 || n > 160 {
			t.Errorf("%q: description is %d chars, want 70–160: %q", p.Path, n, d)
		}
		for _, k := range []string{"og:title", "og:description", "og:url", "og:image", "twitter:card"} {
			if meta[k] == "" {
				t.Errorf("%q: no %s", p.Path, k)
			}
		}
		if c := reCanon.FindStringSubmatch(html); c == nil || c[1] != DefaultURL+p.Path {
			t.Errorf("%q: canonical %v", p.Path, c)
		}
		if n := len(reH1.FindAllString(html, -1)); n != 1 {
			t.Errorf("%q: %d <h1>", p.Path, n)
		}
		if other, dup := titles[m[1]]; dup {
			t.Errorf("%q and %q share the title", p.Path, other)
		}
		if other, dup := descs[d]; dup {
			t.Errorf("%q and %q share the description", p.Path, other)
		}
		titles[m[1]], descs[d] = p.Path, p.Path
	}
}

func TestJSONLD(t *testing.T) {
	dir, s := build(t)
	for _, p := range s.pages {
		html := read(t, dir, p.Path+"index.html")
		types := map[string]map[string]any{}
		for _, x := range reLD.FindAllStringSubmatch(html, -1) {
			var v map[string]any
			if err := json.Unmarshal([]byte(x[1]), &v); err != nil {
				t.Fatalf("%q: invalid JSON-LD: %v\n%s", p.Path, err, x[1])
			}
			if v["@context"] != "https://schema.org" {
				t.Errorf("%q: @context %v", p.Path, v["@context"])
			}
			types[v["@type"].(string)] = v
		}
		if p.Path != "" {
			if _, ok := types["BreadcrumbList"]; !ok {
				t.Errorf("%q: no BreadcrumbList", p.Path)
			}
		}
		if p.Path == "" || p.DB != nil {
			app, ok := types["SoftwareApplication"]
			if !ok {
				t.Fatalf("%q: no SoftwareApplication", p.Path)
			}
			for _, k := range []string{"name", "operatingSystem", "applicationCategory", "offers", "downloadUrl", "screenshot", "license"} {
				if app[k] == nil {
					t.Errorf("%q: SoftwareApplication without %s", p.Path, k)
				}
			}
			if o := app["offers"].(map[string]any); o["price"] != "0" {
				t.Errorf("%q: offers %v", p.Path, o)
			}
		}
	}
}

// TestLinks: every relative link and image of every page exists.
func TestLinks(t *testing.T) {
	dir, s := build(t)
	pages := append([]string{}, "404.html")
	for _, p := range s.pages {
		pages = append(pages, p.Path+"index.html")
	}
	for _, name := range pages {
		html := read(t, dir, name)
		for _, m := range reLink.FindAllStringSubmatch(html, -1) {
			u := m[1]
			switch {
			case strings.HasPrefix(u, DefaultURL):
				u = strings.TrimPrefix(u, DefaultURL)
			case strings.Contains(u, "://"):
				continue
			default:
				u = path.Join(path.Dir(name), u)
				if strings.HasPrefix(u, "..") {
					t.Errorf("%s: link %q leaves the site", name, m[1])
					continue
				}
			}
			p := filepath.Join(dir, filepath.FromSlash(u))
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				p = filepath.Join(p, "index.html")
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: broken link %q", name, m[1])
			}
		}
	}
}

func TestSitemap(t *testing.T) {
	dir, s := build(t)
	var sm struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(read(t, dir, "sitemap.xml")), &sm); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, u := range sm.URLs {
		got[u.Loc] = true
	}
	if len(got) != len(s.pages) {
		t.Errorf("sitemap has %d URLs, %d pages", len(got), len(s.pages))
	}
	for _, p := range s.pages {
		if !got[DefaultURL+p.Path] {
			t.Errorf("sitemap misses %q", p.Path)
		}
	}
}

func TestLLMSTxt(t *testing.T) {
	dir, s := build(t)
	txt := read(t, dir, "llms.txt")
	if !strings.HasPrefix(txt, "# Lumino DB\n\n> ") {
		t.Fatalf("llms.txt must start with an H1 and a blockquote summary:\n%s", txt)
	}
	urls := map[string]bool{}
	for _, m := range reLLMURL.FindAllStringSubmatch(txt, -1) {
		urls[m[1]] = true
	}
	for _, p := range s.pages {
		if !urls[DefaultURL+p.Path] {
			t.Errorf("llms.txt misses %q", p.Path)
		}
	}
	full := read(t, dir, "llms-full.txt")
	for _, d := range s.dbs {
		if !strings.Contains(full, "### "+d.Name+"\n") || !strings.Contains(full, d.Example) {
			t.Errorf("llms-full.txt misses %s", d.Name)
		}
	}
	if strings.Contains(full, "&#") || strings.Contains(full, "&lt;") {
		t.Error("llms-full.txt is HTML-escaped")
	}
}

// TestEveryDriverHasAPage: a new adapter needs a page in databases.go.
func TestEveryDriverHasAPage(t *testing.T) {
	covered := map[string]bool{}
	slugs := map[string]bool{}
	for _, d := range databases {
		covered[d.Driver] = true
		if slugs[d.Slug] {
			t.Errorf("duplicate slug %q", d.Slug)
		}
		slugs[d.Slug] = true
		if len(d.Intro) == 0 || len(d.Highlights) < 3 || d.Example == "" {
			t.Errorf("%s: page too thin", d.Name)
		}
	}
	for _, drv := range db.Drivers() {
		if id := drv.Info().ID; !covered[id] {
			t.Errorf("driver %q has no page in databases.go", id)
		}
	}
}

func TestScreenshotsExist(t *testing.T) {
	for _, sh := range shots {
		if _, err := files.Open("static/img/" + sh.File); err != nil {
			t.Errorf("screenshot %s missing (make site-shots): %v", sh.File, err)
		}
	}
}

func TestCustomBaseURL(t *testing.T) {
	dir := t.TempDir()
	if err := Build(dir, Options{BaseURL: "https://lumino.example"}); err != nil {
		t.Fatal(err)
	}
	if html := read(t, dir, "databases/mysql/index.html"); !strings.Contains(html, `<link rel="canonical" href="https://lumino.example/databases/mysql/">`) {
		t.Error("canonical doesn't use the base URL")
	}
}
