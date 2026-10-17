//go:build integration

// Package conformance runs the dbtest suite against every adapter on the
// development databases (scripts/dbs/dbs.sh up).
package conformance

import (
	"testing"

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
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func run(t *testing.T, name string) { dbtest.Run(t, dbtest.Targets()[name]) }

func TestPostgres(t *testing.T)   { run(t, "postgres") }
func TestCockroach(t *testing.T)  { run(t, "cockroach") }
func TestMySQL(t *testing.T)      { run(t, "mysql") }
func TestMariaDB(t *testing.T)    { run(t, "mariadb") }
func TestTiDB(t *testing.T)       { run(t, "tidb") }
func TestSQLite(t *testing.T)     { run(t, "sqlite") }
func TestSQLServer(t *testing.T)  { run(t, "sqlserver") }
func TestOracle(t *testing.T)     { run(t, "oracle") }
func TestClickHouse(t *testing.T) { run(t, "clickhouse") }
func TestFirebird(t *testing.T)   { run(t, "firebird") }
func TestTrino(t *testing.T)      { run(t, "trino") }
func TestScylla(t *testing.T)     { run(t, "scylla") }
func TestCassandra(t *testing.T)  { run(t, "cassandra") }
