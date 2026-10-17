// Package site generates the static website published on GitHub Pages
// (landing page, one page per database, Lumino vs DBeaver, sitemap.xml,
// llms.txt). Database facts come from the registered adapters so the pages
// follow the code. Build it with `make site`.
package site

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	ttemplate "text/template"

	"github.com/twoojoo/lumino/internal/db"
	_ "github.com/twoojoo/lumino/internal/db/adapters/cassandra"
	_ "github.com/twoojoo/lumino/internal/db/adapters/clickhouse"
	_ "github.com/twoojoo/lumino/internal/db/adapters/cockroach"
	_ "github.com/twoojoo/lumino/internal/db/adapters/firebird"
	_ "github.com/twoojoo/lumino/internal/db/adapters/mssql"
	_ "github.com/twoojoo/lumino/internal/db/adapters/mysql"
	_ "github.com/twoojoo/lumino/internal/db/adapters/oracle"
	_ "github.com/twoojoo/lumino/internal/db/adapters/postgres"
	_ "github.com/twoojoo/lumino/internal/db/adapters/sqlite"
	_ "github.com/twoojoo/lumino/internal/db/adapters/trino"
)

const (
	DefaultURL  = "https://twoojoo.github.io/lumino/"
	RepoURL     = "https://github.com/twoojoo/lumino"
	DownloadURL = "https://github.com/twoojoo/lumino/releases/latest"
	licenseURL  = "https://www.gnu.org/licenses/gpl-3.0.html"
)

//go:embed templates static
var files embed.FS

type Options struct {
	BaseURL string // public URL with the trailing slash (DefaultURL if empty)
}

// Page is one generated HTML page.
type Page struct {
	Path        string // relative to the base URL: "", "databases/", "databases/mysql/"
	Template    string
	Title       string
	Description string
	Crumbs      []Crumb // breadcrumbs after Home
	DB          *DBPage
	jsonLD      []any
}

type Crumb struct{ Name, Path string }

// DBPage is a database page: hand-written text plus the adapter's facts.
type DBPage struct {
	Database
	Port      int
	Schemes   []string
	DBLabel   string
	Supported []string
	Missing   []string
	Related   []*DBPage
}

type site struct {
	base  string
	pages []*Page
	dbs   []*DBPage
}

// data is what templates see.
type data struct {
	*Page
	Base      string
	Name      string
	Tagline   string
	Summary   string
	Repo      string
	Download  string
	Features  []feature
	FAQ       []faq
	Shots     []shot
	DBs       []*DBPage
	Compare   []compareRow
	DBeaverQA []faq
	JSONLD    template.HTML
}

// Build writes the site into dir, replacing its content.
func Build(dir string, o Options) error {
	s, err := newSite(o)
	if err != nil {
		return err
	}
	out, err := s.render()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for name, b := range out {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func newSite(o Options) (*site, error) {
	base := o.BaseURL
	if base == "" {
		base = DefaultURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	s := &site{base: base}
	bySlug := map[string]*DBPage{}
	for _, d := range databases {
		drv, ok := db.Lookup(d.Driver)
		if !ok {
			return nil, fmt.Errorf("database %s: no driver %q", d.Slug, d.Driver)
		}
		inf := drv.Info()
		p := &DBPage{Database: d, Port: inf.DefaultPort, Schemes: inf.Schemes, DBLabel: inf.DatabaseLabel}
		p.Supported, p.Missing = capabilities(inf.Caps)
		s.dbs = append(s.dbs, p)
		bySlug[d.Slug] = p
	}
	// related pages: same driver family first, then neighbours in the list
	for i, p := range s.dbs {
		for _, q := range s.dbs {
			if q != p && sameFamily(p.Driver, q.Driver) {
				p.Related = append(p.Related, q)
			}
		}
		for k := 1; len(p.Related) < 3; k++ {
			q := s.dbs[(i+k)%len(s.dbs)]
			if !contains(p.Related, q) {
				p.Related = append(p.Related, q)
			}
		}
	}

	home := &Page{Template: "index.html", Title: "Lumino DB — native open-source database IDE, DBeaver alternative",
		Description: "Free, native database IDE for PostgreSQL, MySQL, SQLite, SQL Server, Oracle, ClickHouse, Cassandra and more. No Electron, no JVM. Linux, Windows, macOS."}
	home.jsonLD = []any{s.app(home, "", nil), s.website(), faqLD(homeFAQ)}
	list := &Page{Path: "databases/", Template: "databases.html", Title: "Supported databases — Lumino DB",
		Description: "Built-in drivers for PostgreSQL, CockroachDB, MySQL, MariaDB, TiDB, SQLite, SQL Server, Oracle, ClickHouse, Firebird, Cassandra, ScyllaDB and Trino.",
		Crumbs:      []Crumb{{"Databases", "databases/"}}}
	list.jsonLD = []any{s.breadcrumbs(list)}
	vs := &Page{Path: "lumino-vs-dbeaver/", Template: "dbeaver.html", Title: "Lumino vs DBeaver: a native, lighter DBeaver alternative",
		Description: "Lumino vs DBeaver Community Edition: runtime, drivers, supported databases, large results, production safety. What each does better.",
		Crumbs:      []Crumb{{"Lumino vs DBeaver", "lumino-vs-dbeaver/"}}}
	vs.jsonLD = []any{s.breadcrumbs(vs), faqLD(dbeaverFAQ)}
	s.pages = []*Page{home, list, vs}
	for _, d := range s.dbs {
		p := &Page{Path: "databases/" + d.Slug + "/", Template: "database.html", Title: d.Title, Description: d.Description, DB: d,
			Crumbs: []Crumb{{"Databases", "databases/"}, {d.Name, "databases/" + d.Slug + "/"}}}
		p.jsonLD = []any{s.app(p, d.Name+" client", d.Highlights), s.breadcrumbs(p)}
		s.pages = append(s.pages, p)
	}
	return s, nil
}

func sameFamily(a, b string) bool {
	fam := func(id string) string {
		switch id {
		case "postgres", "cockroachdb":
			return "pg"
		case "mysql", "mariadb", "tidb":
			return "my"
		}
		return id
	}
	return fam(a) == fam(b)
}

func contains(pp []*DBPage, p *DBPage) bool {
	for _, x := range pp {
		if x == p {
			return true
		}
	}
	return false
}

// capabilities lists what the navigator and editor offer for a driver.
func capabilities(c db.Capabilities) (yes, no []string) {
	for _, x := range []struct {
		name string
		ok   bool
	}{
		{"Transactions (auto-commit or manual)", c.Transactions},
		{"Server-enforced read-only sessions", c.ReadOnlyTx},
		{"Cancel running statements", c.Cancel},
		{"Row editing in the data grid", c.Insert || c.Update || c.Delete},
		{"Views", c.Views},
		{"Materialized views", c.MatViews},
		{"Functions", c.Functions},
		{"Stored procedures", c.Procedures},
		{"Triggers", c.Triggers},
		{"Sequences", c.Sequences},
		{"Foreign keys", c.ForeignKeys},
		{"Multiple result sets", c.MultiResultSets},
		{"TLS", c.TLS},
	} {
		if x.ok {
			yes = append(yes, x.name)
		} else {
			no = append(no, x.name)
		}
	}
	return yes, no
}

func (s *site) abs(p string) string { return s.base + p }

func (s *site) app(p *Page, suffix string, featureList []string) map[string]any {
	n := name
	if suffix != "" {
		n = name + " — " + suffix
	}
	shotURLs := []string{}
	for _, sh := range shots {
		shotURLs = append(shotURLs, s.abs("img/"+sh.File))
	}
	m := map[string]any{
		"@context":               "https://schema.org",
		"@type":                  "SoftwareApplication",
		"name":                   n,
		"url":                    s.abs(p.Path),
		"description":            p.Description,
		"applicationCategory":    "DeveloperApplication",
		"applicationSubCategory": "Database management tool",
		"operatingSystem":        "Linux, Windows 10+, macOS 12+",
		"offers":                 map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
		"license":                licenseURL,
		"downloadUrl":            DownloadURL,
		"installUrl":             DownloadURL,
		"image":                  s.abs("img/logo.png"),
		"screenshot":             shotURLs,
		"isAccessibleForFree":    true,
		"sameAs":                 []string{RepoURL},
		"author":                 map[string]any{"@type": "Person", "name": "twoojoo", "url": "https://github.com/twoojoo"},
	}
	if len(featureList) > 0 {
		m["featureList"] = featureList
	} else {
		var fl []string
		for _, f := range features {
			fl = append(fl, f.Title)
		}
		m["featureList"] = fl
	}
	return m
}

func (s *site) website() map[string]any {
	return map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": name, "url": s.base}
}

func (s *site) breadcrumbs(p *Page) map[string]any {
	items := []any{map[string]any{"@type": "ListItem", "position": 1, "name": "Home", "item": s.base}}
	for i, c := range p.Crumbs {
		items = append(items, map[string]any{"@type": "ListItem", "position": i + 2, "name": c.Name, "item": s.abs(c.Path)})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": items}
}

func faqLD(qq []faq) map[string]any {
	var items []any
	for _, q := range qq {
		items = append(items, map[string]any{"@type": "Question", "name": q.Q,
			"acceptedAnswer": map[string]any{"@type": "Answer", "text": q.A}})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "FAQPage", "mainEntity": items}
}

// render returns every output file by slash path.
func (s *site) render() (map[string][]byte, error) {
	out := map[string][]byte{}
	// rel/abs are replaced per page (template functions are resolved at execution)
	layout, err := template.New("").Funcs(template.FuncMap{"rel": s.abs, "abs": s.abs}).ParseFS(files, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	for _, p := range s.pages {
		depth := strings.Count(p.Path, "/")
		rel := func(target string) string {
			r := strings.Repeat("../", depth) + target
			if r == "" {
				return "./"
			}
			return r
		}
		t, err := layout.Clone()
		if err != nil {
			return nil, err
		}
		t = t.Funcs(template.FuncMap{"rel": rel, "abs": s.abs})
		if _, err := t.ParseFS(files, "templates/"+p.Template); err != nil {
			return nil, err
		}
		var ld bytes.Buffer
		for _, x := range p.jsonLD {
			j, err := json.MarshalIndent(x, "", "  ") // escapes <, > and &
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&ld, "<script type=\"application/ld+json\">\n%s\n</script>\n", j)
		}
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, "layout", s.data(p, template.HTML(ld.String()))); err != nil {
			return nil, fmt.Errorf("%s: %w", p.Template, err)
		}
		out[p.Path+"index.html"] = b.Bytes()
	}

	// 404: GitHub Pages serves it for any missing path, so links are absolute
	nf, err := layout.Clone()
	if err != nil {
		return nil, err
	}
	nf = nf.Funcs(template.FuncMap{"rel": s.abs, "abs": s.abs})
	if _, err := nf.ParseFS(files, "templates/404.html"); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	notFound := &Page{Path: "404.html", Template: "404.html", Title: "Page not found — Lumino DB", Description: "This page doesn't exist."}
	if err := nf.ExecuteTemplate(&b, "layout", s.data(notFound, "")); err != nil {
		return nil, err
	}
	out["404.html"] = b.Bytes()

	for name, tmpl := range map[string]string{"llms.txt": "templates/llms.txt", "llms-full.txt": "templates/llms-full.txt", "sitemap.xml": "templates/sitemap.xml"} {
		t, err := ttemplate.New(path.Base(tmpl)).Funcs(ttemplate.FuncMap{"abs": s.abs}).ParseFS(files, tmpl)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, struct {
			data
			Pages []*Page
		}{s.data(&Page{}, ""), s.pages}); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = b.Bytes()
	}

	err = fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		out[strings.TrimPrefix(p, "static/")] = b
		return err
	})
	// a .nojekyll file: GitHub Pages serves the files as they are
	out[".nojekyll"] = nil
	return out, err
}

func (s *site) data(p *Page, ld template.HTML) data {
	return data{Page: p, Base: s.base, Name: name, Tagline: tagline, Summary: summary, Repo: RepoURL, Download: DownloadURL,
		Features: features, FAQ: homeFAQ, Shots: shots, DBs: s.dbs, Compare: dbeaverRows, DBeaverQA: dbeaverFAQ, JSONLD: ld}
}
