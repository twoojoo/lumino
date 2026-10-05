//go:build integration

package conformance

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func TestMySQLRegress(t *testing.T)   { mysqlRegress(t, "mysql") }
func TestMariaDBRegress(t *testing.T) { mysqlRegress(t, "mariadb") }
func TestTiDBRegress(t *testing.T)    { mysqlRegress(t, "tidb") }

func mysqlRegress(t *testing.T, name string) {
	tg := dbtest.Targets()[name]
	conn := dbtest.Prepare(t, tg)
	t.Run("Params", func(t *testing.T) { mysqlParams(t, tg) })
	t.Run("IPv6", func(t *testing.T) { mysqlIPv6(t, tg) })
	t.Run("Bit", func(t *testing.T) { mysqlBit(t, conn) })
	t.Run("ZeroDates", func(t *testing.T) { mysqlZeroDates(t, conn) })
	t.Run("RowStatements", func(t *testing.T) { mysqlRowStatements(t, conn, name) })
}

func mysqlSession(t *testing.T, conn db.Conn) db.Session {
	t.Helper()
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mysqlExec(t *testing.T, s db.Session, q string) {
	t.Helper()
	res, err := s.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	res.Close()
}

func mysqlScalar(t *testing.T, s db.Session, q string) any {
	t.Helper()
	_, rows, err := dbtest.Query(context.Background(), s, q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s: %d rows", q, len(rows))
	}
	return rows[0][0]
}

// Driver options in the connection parameters configure the driver; the
// others are session variables.
func mysqlParams(t *testing.T, tg dbtest.Target) {
	cfg := tg.Config
	cfg.Params = map[string]string{"charset": "latin1", "parseTime": "true", "loc": "Europe/Rome", "timeout": "5s",
		"readTimeout": "30s", "WriteTimeout": "30s", "tls": "preferred", "allowAllFiles": "true", "wait_timeout": "1234"}
	conn := ping(t, cfg, db.OpenOptions{})
	s := mysqlSession(t, conn)
	if got := db.FormatValue(mysqlScalar(t, s, "SELECT @@character_set_client"), db.KindText); got != "latin1" {
		t.Errorf("character_set_client %q", got)
	}
	if got := db.FormatValue(mysqlScalar(t, s, "SELECT @@session.wait_timeout"), db.KindInt); got != "1234" {
		t.Errorf("wait_timeout %q", got)
	}
	cfg.Params = map[string]string{"timeout": "soon"}
	if _, err := db.Open(context.Background(), &cfg, db.OpenOptions{}); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("invalid timeout accepted: %v", err)
	}
}

// An IPv6 host is joined with brackets (proxied to the IPv4 dev server).
func mysqlIPv6(t *testing.T, tg dbtest.Target) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	defer ln.Close()
	target := tg.Config.Addr(0)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				u, err := net.Dial("tcp", target)
				if err != nil {
					c.Close()
					return
				}
				go func() { io.Copy(u, c); u.Close() }()
				io.Copy(c, u)
				c.Close()
			}()
		}
	}()
	cfg := tg.Config
	cfg.Host = "::1"
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatalf("open [::1]:%d: %v", cfg.Port, err)
	}
	conn.Close()
}

// BIT columns are integers: shown as numbers and written from typed numbers.
func mysqlBit(t *testing.T, conn db.Conn) {
	ctx := context.Background()
	s := mysqlSession(t, conn)
	mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_bit")
	mysqlExec(t, s, "CREATE TABLE lumino_test.rg_bit (id INT PRIMARY KEY, b1 BIT(1), b8 BIT(8), b64 BIT(64))")
	t.Cleanup(func() { mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_bit") })
	mysqlExec(t, s, "INSERT INTO lumino_test.rg_bit VALUES (1, b'1', 200, 18446744073709551615)")
	ref := db.ObjectRef{Schema: "lumino_test", Name: "rg_bit", Type: db.ObjTable}
	info, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range info.Columns {
		if c.Kind != db.KindInt {
			t.Errorf("%s (%s) kind %s", c.Name, c.Type, c.Kind)
		}
	}
	read := func() (db.Row, []db.Column) {
		cols, rows, err := dbtest.Query(ctx, s, "SELECT b1, b8, b64 FROM lumino_test.rg_bit WHERE id = 1")
		if err != nil || len(rows) != 1 {
			t.Fatalf("read: %v", err)
		}
		return rows[0], cols
	}
	row, cols := read()
	want := []string{"1", "200", "18446744073709551615"}
	for i := range want {
		if cols[i].Kind != db.KindInt || db.FormatValue(row[i], cols[i].Kind) != want[i] {
			t.Errorf("%s: %#v (kind %s), want %s", cols[i].Name, row[i], cols[i].Kind, want[i])
		}
	}
	var vals []db.ColValue
	for i, typed := range []string{"0", "5", "9223372036854775808"} {
		v, err := db.ParseValue(typed, info.Columns[i+1].Kind)
		if err != nil {
			t.Fatal(err)
		}
		vals = append(vals, db.ColValue{Column: cols[i].Name, Kind: cols[i].Kind, Value: v})
	}
	key := []db.ColValue{{Column: "id", Kind: db.KindInt, Value: int64(1)}}
	if _, err := db.ApplyChanges(ctx, s, conn.Caps(), conn.Dialect(), ref, []db.Change{{Op: db.OpUpdate, Key: key, Values: vals}}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	row, cols = read()
	want = []string{"0", "5", "9223372036854775808"}
	for i := range want {
		if db.FormatValue(row[i], cols[i].Kind) != want[i] {
			t.Errorf("%s after edit: %#v, want %s", cols[i].Name, row[i], want[i])
		}
	}
	if got := db.FormatValue(mysqlScalar(t, s, "SELECT b8 + 0 FROM lumino_test.rg_bit"), db.KindInt); got != "5" {
		t.Errorf("stored b8 = %s, want 5", got)
	}
}

// Zero dates read back as zero dates, not 0001-01-01.
func mysqlZeroDates(t *testing.T, conn db.Conn) {
	ctx := context.Background()
	s := mysqlSession(t, conn)
	mysqlExec(t, s, "SET SESSION sql_mode = ''")
	mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_zero")
	mysqlExec(t, s, "CREATE TABLE lumino_test.rg_zero (id INT PRIMARY KEY, d DATE, dt DATETIME, ts DATETIME(3))")
	t.Cleanup(func() { mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_zero") })
	mysqlExec(t, s, "INSERT INTO lumino_test.rg_zero VALUES (1, '0000-00-00', '0000-00-00 00:00:00', '0000-00-00 00:00:00'), (2, '2024-01-02', '2024-01-02 03:04:05', NULL)")
	cols, rows, err := dbtest.Query(ctx, s, "SELECT d, dt, ts FROM lumino_test.rg_zero ORDER BY id")
	if err != nil || len(rows) != 2 {
		t.Fatalf("read: %v", err)
	}
	want := [][]string{{"0000-00-00", "0000-00-00 00:00:00", "0000-00-00 00:00:00"}, {"2024-01-02", "2024-01-02 03:04:05", "NULL"}}
	for r := range want {
		for i := range want[r] {
			if got := db.FormatValue(rows[r][i], cols[i].Kind); got != want[r][i] {
				t.Errorf("row %d %s: %q, want %q", r+1, cols[i].Name, got, want[r][i])
			}
		}
	}
}

// XA RECOVER and HANDLER … READ return rows.
func mysqlRowStatements(t *testing.T, conn db.Conn, name string) {
	if name == "tidb" {
		t.Skip("TiDB has neither XA nor HANDLER")
	}
	ctx := context.Background()
	s := mysqlSession(t, conn)
	res, err := s.Execute(ctx, "XA RECOVER")
	if err != nil {
		t.Fatalf("XA RECOVER: %v", err)
	}
	if res.Columns() == nil {
		t.Error("XA RECOVER returned no result set")
	}
	res.Close()
	mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_handler")
	mysqlExec(t, s, "CREATE TABLE lumino_test.rg_handler (id INT PRIMARY KEY, v VARCHAR(10))")
	t.Cleanup(func() { mysqlExec(t, s, "DROP TABLE IF EXISTS lumino_test.rg_handler") })
	mysqlExec(t, s, "INSERT INTO lumino_test.rg_handler VALUES (1, 'a'), (2, 'b')")
	mysqlExec(t, s, "HANDLER lumino_test.rg_handler OPEN AS h")
	for _, q := range []string{"HANDLER h READ FIRST", "HANDLER h READ `PRIMARY` FIRST LIMIT 2"} {
		_, rows, err := dbtest.Query(ctx, s, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if len(rows) == 0 {
			t.Errorf("%s: no rows", q)
		} else if got := db.FormatValue(rows[0][0], db.KindInt); got != strconv.Itoa(1) {
			t.Errorf("%s: first id %s", q, got)
		}
	}
	mysqlExec(t, s, "HANDLER h CLOSE")
}
