//go:build integration

// Package conformance runs the dbtest suite against every adapter on the
// development databases (scripts/dbs/dbs.sh up).
package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/db"
	_ "github.com/giovanni/lumino/internal/db/adapters/mssql"
	_ "github.com/giovanni/lumino/internal/db/adapters/mysql"
	_ "github.com/giovanni/lumino/internal/db/adapters/oracle"
	_ "github.com/giovanni/lumino/internal/db/adapters/postgres"
	_ "github.com/giovanni/lumino/internal/db/adapters/sqlite"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func TestPostgres(t *testing.T) {
	dbtest.Run(t, dbtest.Target{
		Name:    "postgres",
		Config:  db.Config{Driver: "postgres", Host: "127.0.0.1", Port: 55432, Database: "lumino", User: "lumino", Password: "lumino"},
		Fixture: dbtest.Fixture("postgres"),
		Schema:  "lumino_test",
		BigRows: 100000,
		Sleep:   "SELECT pg_sleep(30)",
		Samples: map[string]string{"c_arr": "{1,2,3}", "c_interval": "1 day 02:03:04"},
		Expect:  []db.ObjectType{db.ObjMatView, db.ObjSequence, db.ObjFunction, db.ObjProcedure},
	})
}

var mysqlSamples = map[string]string{"c_bool": "1", "c_tstz": "2024-02-29 13:14:15.123456", "c_enum": "b", "c_ubig": "18446744073709551615"}

func TestMySQL(t *testing.T) {
	dbtest.Run(t, dbtest.Target{
		Name:          "mysql",
		Config:        db.Config{Driver: "mysql", Host: "127.0.0.1", Port: 55306, User: "root", Password: "lumino"},
		Fixture:       dbtest.Fixture("mysql"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure},
	})
}

func TestMariaDB(t *testing.T) {
	dbtest.Run(t, dbtest.Target{
		Name:          "mariadb",
		Config:        db.Config{Driver: "mariadb", Host: "127.0.0.1", Port: 55307, User: "root", Password: "lumino"},
		Fixture:       dbtest.Fixture("mariadb"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence},
	})
}

func TestTiDB(t *testing.T) {
	dbtest.Run(t, dbtest.Target{
		Name:          "tidb",
		Config:        db.Config{Driver: "tidb", Host: "127.0.0.1", Port: 55400, User: "root"},
		Fixture:       dbtest.Fixture("tidb"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "SELECT COUNT(*) FROM lumino_test.big a, lumino_test.big b WHERE a.v + b.v < 0",
		Samples:       mysqlSamples,
		NoErrPosition: true,
		Expect:        []db.ObjectType{db.ObjSequence},
	})
}

func TestSQLite(t *testing.T) {
	dir, _ := filepath.Abs("../../../.dev")
	_ = os.MkdirAll(dir, 0o755)
	dbtest.Run(t, dbtest.Target{
		Name:          "sqlite",
		Config:        db.Config{Driver: "sqlite", File: filepath.Join(dir, "conformance.sqlite"), Params: map[string]string{"create": "true"}},
		Fixture:       dbtest.Fixture("sqlite"),
		Schema:        "main",
		BigRows:       100000,
		Sleep:         "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		Samples:       map[string]string{"c_dec": "12345678.1234"},
		NoErrPosition: true,
	})
}

func TestSQLServer(t *testing.T) {
	cfg := db.Config{Driver: "sqlserver", Host: "127.0.0.1", Port: 55433, User: "sa", Password: "Lumino_pass1"}
	dbtest.Run(t, dbtest.Target{
		Name:   "sqlserver",
		Config: withDB(cfg, "lumino_test"),
		Setup: func(t *testing.T) {
			// the test database must exist before connecting to it
			createDB(t, cfg, "IF DB_ID('lumino_test') IS NULL CREATE DATABASE lumino_test")
		},
		Fixture:       dbtest.Fixture("sqlserver"),
		Schema:        "lumino_test",
		BigRows:       100000,
		Sleep:         "WAITFOR DELAY '00:00:30'; SELECT 1",
		Samples:       map[string]string{"c_money": "1234.5678"},
		NoErrPosition: false,
		Expect:        []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence},
	})
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
			err = dbtest.Load(ctx, conn, stmt)
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

func TestOracle(t *testing.T) {
	dbtest.Run(t, dbtest.Target{
		Name:        "oracle",
		Config:      db.Config{Driver: "oracle", Host: "127.0.0.1", Port: 55521, Database: "FREEPDB1", User: "lumino", Password: "lumino"},
		Fixture:     dbtest.Fixture("oracle"),
		Schema:      "LUMINO",
		Upper:       true,
		BigRows:     100000,
		Sleep:       "SELECT COUNT(*) FROM big a, big b WHERE a.v + b.v < 0",
		Samples:     map[string]string{"c_bool": "1", "c_date": "2024-02-29", "c_nvarchar": "ñandú ✓"},
		EmptyIsNull: true,
		Expect:      []db.ObjectType{db.ObjFunction, db.ObjProcedure, db.ObjSequence, db.ObjMatView, db.ObjPackage},
	})
}
