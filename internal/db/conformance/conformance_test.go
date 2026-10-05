//go:build integration

// Package conformance runs the dbtest suite against every adapter on the
// development databases (scripts/dbs/dbs.sh up).
package conformance

import (
	"testing"

	"github.com/giovanni/lumino/internal/db"
	_ "github.com/giovanni/lumino/internal/db/adapters/mysql"
	_ "github.com/giovanni/lumino/internal/db/adapters/postgres"
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
