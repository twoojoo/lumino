//go:build dev && integration

package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUISiteScreenshots renders the screenshots of the website
// (internal/site/static/img) from a SQLite database. It runs only with
// LUMINO_SITE_SHOTS=<dir> (make site-shots).
func TestUISiteScreenshots(t *testing.T) {
	out := os.Getenv("LUMINO_SITE_SHOTS")
	if out == "" {
		t.Skip("LUMINO_SITE_SHOTS not set (make site-shots)")
	}
	out, _ = filepath.Abs(out)
	_ = os.MkdirAll(devDir(), 0o755)
	t.Setenv("HOME", devDir()) // the navigator shows the file as ~/site-shop.sqlite
	path := devDir("site-shop.sqlite")
	os.Remove(path)
	cfg := db.Config{Driver: "sqlite", File: path, Params: map[string]string{"create": "true"}}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Load(context.Background(), conn, siteFixture()); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	h := newHarness(t, []db.Config{{ID: "shop", Name: "shop", Driver: "sqlite", File: path}}, Options{Connection: "shop"})
	h.dir = out
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("shop").status == stReady })
	a.sidebar.expand(schemaKey("shop", metaKey{schema: "main"}), true)
	a.sidebar.expand(folderKey("shop", metaKey{schema: "main"}, db.ObjTable), true)
	h.waitFor("tree", func() bool { return a.connState("shop").objects[metaKey{schema: "main"}] != nil })

	c := h.console()
	c.ed.SetText(`-- top customers by revenue
SELECT c.name, c.country, count(o.id) AS orders, round(sum(o.total), 2) AS revenue
FROM customers c
JOIN orders o ON o.customer_id = c.id
WHERE o.status <> 'cancelled'
GROUP BY c.id
ORDER BY revenue DESC;`)
	c.ed.SetCaretOffset(len(c.ed.Text()))
	c.run(false)
	h.waitFor("results", func() bool { return !c.running && len(c.results) == 1 })
	if rs := c.results[0]; rs.err != nil || len(rs.rows) == 0 {
		t.Fatalf("query: %v, %d rows", rs.err, len(rs.rows))
	}
	h.settle(300 * time.Millisecond)
	h.shot("console")

	// light theme on the same console
	a.setThemeName("ayu-light")
	h.settle(300 * time.Millisecond)
	h.shot("light")
	a.setThemeName("ayu-dark")

	// completion of a table name
	h.focusEditor(c)
	c.ed.SetText("SELECT * FROM or")
	h.typeText("d")
	// the first keystroke loads the completion metadata: type again
	h.waitFor("completion metadata", func() bool { return c.ed.comp != nil || c.cs().completion[metaKey{schema: "main"}] != nil })
	// columns of an alias
	q := "SELECT c FROM customers c JOIN orders o ON o.customer_id = c.id"
	c.ed.SetText(q)
	c.ed.SetCaretOffset(len("SELECT c"))
	h.typeText(".")
	h.waitFor("completion", func() bool { return c.ed.comp != nil && len(c.ed.comp.items) > 0 })
	h.settle(200 * time.Millisecond)
	h.shot("completion")
	h.press(key.NameEscape)

	// table data with pending edits, then the SQL review
	a.openObject("shop", db.ObjectRef{Schema: "main", Name: "customers", Type: db.ObjTable}, "data")
	tt := a.activeTab().(*TableTab)
	h.waitFor("table", func() bool { return tt.data != nil && tt.info != nil })
	tt.setText(33, 2, "m.stonebraker@example.org", false)
	tt.setText(36, 3, "Norway", false)
	tt.addRow()
	r := tt.data.Rows() - 1
	tt.setText(r, 0, "41", false)
	tt.setText(r, 1, "Barbara Liskov", false)
	tt.setText(r, 2, "barbara@example.com", false)
	tt.setText(r, 3, "USA", false)
	tt.setText(r, 4, "2026-10-17", false)
	h.settle(300 * time.Millisecond)
	h.shot("table")
	tt.review()
	if _, ok := a.overlay.(*SQLReview); !ok {
		t.Fatalf("no review dialog: %T", a.overlay)
	}
	h.settle(300 * time.Millisecond)
	h.shot("review")
	a.overlay = nil

	a.openObject("shop", db.ObjectRef{Schema: "main", Name: "orders", Type: db.ObjTable}, "structure")
	ot := a.activeTab().(*TableTab)
	h.waitFor("structure", func() bool { return ot.info != nil })
	h.settle(300 * time.Millisecond)
	h.shot("structure")
}

// siteFixture is a small shop database with plausible data.
func siteFixture() string {
	names := []string{"Ada Lovelace", "Alan Turing", "Grace Hopper", "Edsger Dijkstra", "Margaret Hamilton",
		"Donald Knuth", "Barbara Liskov", "Ken Thompson", "Frances Allen", "Dennis Ritchie", "Radia Perlman",
		"John McCarthy", "Hedy Lamarr", "Tim Berners-Lee", "Karen Spärck Jones", "Niklaus Wirth", "Sophie Wilson",
		"Bjarne Stroustrup", "Adele Goldberg", "Guido van Rossum", "Shafi Goldwasser", "Leslie Lamport",
		"Anita Borg", "Linus Torvalds", "Jean Sammet", "Rob Pike", "Lynn Conway", "Edgar Codd", "Mary Wilkes",
		"James Gosling", "Kathleen Booth", "Ward Cunningham", "Evelyn Berezin", "Michael Stonebraker",
		"Annie Easley", "Jim Gray", "Joan Clarke", "Charles Bachman", "Ida Rhodes", "Ted Codd"}
	countries := []string{"UK", "USA", "Netherlands", "Switzerland", "Italy", "Germany", "Canada", "France", "Norway", "Japan"}
	statuses := []string{"paid", "paid", "shipped", "paid", "cancelled", "shipped", "pending"}
	var b strings.Builder
	b.WriteString(`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT UNIQUE, country TEXT,
  joined DATE NOT NULL DEFAULT CURRENT_DATE);
CREATE TABLE products (id INTEGER PRIMARY KEY, sku TEXT NOT NULL UNIQUE, title TEXT NOT NULL, price DECIMAL(10,2) NOT NULL);
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(id),
  product_id INTEGER NOT NULL REFERENCES products(id), quantity INTEGER NOT NULL CHECK (quantity > 0),
  total DECIMAL(10,2) NOT NULL, status TEXT NOT NULL DEFAULT 'pending', created_at TIMESTAMP NOT NULL);
CREATE INDEX orders_customer ON orders(customer_id);
CREATE INDEX orders_created ON orders(created_at);
CREATE VIEW revenue_by_country AS
  SELECT c.country, sum(o.total) AS revenue FROM orders o JOIN customers c ON c.id = o.customer_id GROUP BY c.country;
INSERT INTO products (id, sku, title, price) VALUES
  (1, 'KB-01', 'Mechanical keyboard', 129.00), (2, 'MS-02', 'Wireless mouse', 49.90), (3, 'MN-27', '27" monitor', 329.00),
  (4, 'DK-01', 'USB-C dock', 189.50), (5, 'HS-03', 'Headset', 79.00), (6, 'CB-10', 'Cable kit', 19.99);
`)
	for i, n := range names {
		email := strings.ToLower(strings.NewReplacer(" ", ".", "ä", "a", "-", "").Replace(n)) + "@example.com"
		fmt.Fprintf(&b, "INSERT INTO customers VALUES (%d, '%s', '%s', '%s', date('2023-01-01', '+%d days'));\n",
			i+1, n, email, countries[(i*7)%len(countries)], i*17)
	}
	prices := []float64{129.00, 49.90, 329.00, 189.50, 79.00, 19.99}
	seed := uint32(7)
	rnd := func(n int) int { // deterministic LCG: same screenshots on every run
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for i := 1; i <= 600; i++ {
		// a few customers buy a lot more than the others
		cust := rnd(len(names)) + 1
		if rnd(3) == 0 {
			cust = rnd(8) + 1
		}
		prod := rnd(len(prices)) + 1
		q := rnd(4) + 1
		fmt.Fprintf(&b, "INSERT INTO orders VALUES (%d, %d, %d, %d, %.2f, '%s', datetime('2025-01-01', '+%d hours'));\n",
			i, cust, prod, q, prices[prod-1]*float64(q), statuses[rnd(len(statuses))], i*11)
	}
	return b.String()
}
