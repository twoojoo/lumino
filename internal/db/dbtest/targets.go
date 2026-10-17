package dbtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
)

// devDir is the repository's .dev directory.
func devDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", ".dev")
}

// Targets returns the development databases of scripts/dbs, by name.
func Targets() map[string]Target {
	mysqlSamples := map[string]string{"c_bool": "1", "c_tstz": "2024-02-29 13:14:15.123456", "c_enum": "b", "c_ubig": "18446744073709551615"}
	mssql := db.Config{Driver: "sqlserver", Host: "127.0.0.1", Port: 55433, User: "sa", Password: "Lumino_pass1"}
	sqliteDir, _ := filepath.Abs(devDir())
	_ = os.MkdirAll(sqliteDir, 0o755)
	t := map[string]Target{}
	t["postgres"] = Target{
		Name:    "postgres",
		Config:  db.Config{Driver: "postgres", Host: "127.0.0.1", Port: 55432, Database: "lumino", User: "lumino", Password: "lumino"},
		Fixture: Fixture("postgres"),
		Schema:  "lumino_test",
		BigRows: 100000,
		Sleep:   "SELECT pg_sleep(30)",
		Samples: map[string]string{"c_arr": "{1,2,3}", "c_interval": "1 day 02:03:04"},
		Expect:  []db.ObjectType{db.ObjMatView, db.ObjSequence, db.ObjFunction, db.ObjProcedure},
	}
	t["mysql"] = Target{
		Name:          "mysql",
		Config:        db.Config{Driver: "mysql", Host: "127.0.0.1", Port: 55306, User: "root", Password: "lumino"},
		Fixture:       Fixture("mysql"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure},
	}
	t["mariadb"] = Target{
		Name:          "mariadb",
		Config:        db.Config{Driver: "mariadb", Host: "127.0.0.1", Port: 55307, User: "root", Password: "lumino"},
		Fixture:       Fixture("mariadb"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence},
	}
	t["tidb"] = Target{
		Name:          "tidb",
		Config:        db.Config{Driver: "tidb", Host: "127.0.0.1", Port: 55400, User: "root"},
		Fixture:       Fixture("tidb"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjSequence},
	}
	t["sqlite"] = Target{
		Name:          "sqlite",
		Config:        db.Config{Driver: "sqlite", File: filepath.Join(sqliteDir, "conformance.sqlite"), Params: map[string]string{"create": "true"}},
		Fixture:       Fixture("sqlite"),
		Schema:        "main",
		BigRows:       100000,
		Sleep:         "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		Samples:       map[string]string{"c_dec": "12345678.1234"},
		NoErrPosition: true,
	}
	t["sqlserver"] = Target{
		Name:   "sqlserver",
		Config: withDB(mssql, "lumino_test"),
		Setup: func(t *testing.T) {
			// the test database must exist before connecting to it
			createDB(t, mssql, "IF DB_ID('lumino_test') IS NULL CREATE DATABASE lumino_test")
		},
		Fixture:       Fixture("sqlserver"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "WAITFOR DELAY '00:00:30'; SELECT 1",
		Samples:       map[string]string{"c_money": "1234.5678"},
		NoErrPosition: false,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence},
	}
	t["oracle"] = Target{
		Name:        "oracle",
		Config:      db.Config{Driver: "oracle", Host: "127.0.0.1", Port: 55521, Database: "FREEPDB1", User: "lumino", Password: "lumino"},
		Fixture:     Fixture("oracle"),
		Schema:      "LUMINO",
		Upper:       true,
		BigRows:     100000,
		Sleep:       "SELECT COUNT(*) FROM big a, big b WHERE a.v + b.v < 0",
		Samples:     map[string]string{"c_bool": "1", "c_date": "2024-02-29", "c_nvarchar": "ñandú ✓"},
		EmptyIsNull: true,
		Expect:      []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence, db.ObjMatView, db.ObjPackage},
	}
	t["clickhouse"] = Target{
		Name:          "clickhouse",
		Config:        db.Config{Driver: "clickhouse", Host: "127.0.0.1", Port: 55900, User: "lumino", Password: "lumino"},
		Fixture:       Fixture("clickhouse"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT count() FROM numbers(1000000000000) WHERE sipHash64(number) = 0",
		Samples:       map[string]string{"c_arr": "[1,2,3]", "c_ubig": "18446744073709551615"},
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjMatView},
	}
	t["firebird"] = Target{
		Name:    "firebird",
		Config:  db.Config{Driver: "firebird", Host: "127.0.0.1", Port: 55050, Database: "/var/lib/firebird/data/lumino.fdb", User: "lumino", Password: "lumino"},
		Fixture: Fixture("firebird"),
		Upper:   true,
		BigRows: 100000,
		Sleep:   "SELECT COUNT(*) FROM big a, big b WHERE a.v + b.v < 0",
		Samples: map[string]string{"c_int128": "170141183460469231731687303715884105727", "c_char": "abc", "c_ts": "2024-02-29 13:14:15.1234"},
		Expect:  []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence},
	}
	t["cockroach"] = Target{
		Name:          "cockroach",
		Config:        db.Config{Driver: "cockroachdb", Host: "127.0.0.1", Port: 55257, Database: "defaultdb", User: "root", TLS: db.TLSConfig{Mode: "disable"}},
		Fixture:       Fixture("cockroach"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT pg_sleep(30)",
		Samples:       map[string]string{"c_arr": "{1,2,3}", "c_interval": "1 day 02:03:04"},
		Expect:        []db.ObjectType{db.ObjMatView, db.ObjSequence},
		NoErrPosition: true,
	}
	t["trino"] = Target{
		Name:    "trino",
		Config:  db.Config{Driver: "trino", Host: "127.0.0.1", Port: 55080, Database: "memory", User: "lumino"},
		Fixture: Fixture("trino"),
		Catalog: "memory",
		Schema:  "lumino_test",
		BigRows: 100000,
		Sleep:   "SELECT count(*) FROM tpch.sf100.lineitem WHERE comment LIKE '%xyz%'",
		NoKeys:  true,
	}
	for name, cfg := range map[string]db.Config{
		"scylla":    {Driver: "cassandra", Host: "127.0.0.1", Port: 55042},
		"cassandra": {Driver: "cassandra", Host: "127.0.0.1", Port: 55043, User: "cassandra", Password: "cassandra"},
	} {
		t[name] = Target{
			Name:    name,
			Config:  cfg,
			Fixture: Fixture("cassandra"),
			Schema:  "lumino_test",
			BigRows: 100000,
			AfterLoad: func(ctx context.Context, c db.Conn) error {
				// 100k rows in unlogged batches, 8 at a time
				var wg sync.WaitGroup
				errs := make(chan error, 8)
				for w := 0; w < 8; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						s, err := c.Session(ctx)
						if err != nil {
							errs <- err
							return
						}
						defer s.Close()
						for b := w; b < 1000; b += 8 {
							var sb strings.Builder
							sb.WriteString("BEGIN UNLOGGED BATCH\n")
							for i := b*100 + 1; i <= b*100+100; i++ {
								fmt.Fprintf(&sb, "INSERT INTO lumino_test.big (id, label, v) VALUES (%d, 'row %d', %g);\n", i, i, float64(i)*0.5)
							}
							sb.WriteString("APPLY BATCH")
							res, err := s.Execute(ctx, sb.String())
							if err != nil {
								errs <- err
								return
							}
							res.Close()
						}
					}()
				}
				wg.Wait()
				close(errs)
				return <-errs
			},
			Samples:          map[string]string{"c_ts": "2024-02-29 13:14:15.123", "c_list": "-", "c_inet": "10.1.2.3", "c_varint": "123456789012345678901234567890"},
			NoErrPosition:    true,
			NoViews:          true,
			Expect:           []db.ObjectType{db.ObjMatView},
			UnorderedColumns: true,
			NoNotNull:        true,
			NoOrderBy:        true,
			FilterSuffix:     " ALLOW FILTERING",
		}
	}
	return t
}

func withDB(c db.Config, name string) db.Config { c.Database = name; return c }

// createDB runs an admin statement on the server's default database,
// retrying while the server starts.
func createDB(t *testing.T, cfg db.Config, stmt string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		conn, err := db.Open(ctx, &cfg, db.OpenOptions{})
		if err == nil {
			err = Load(ctx, conn, stmt)
			conn.Close()
			if err == nil {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
}
