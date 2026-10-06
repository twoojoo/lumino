package db

import (
	"testing"

	"github.com/giovanni/lumino/internal/db/sqlx"
)

func TestClassify(t *testing.T) {
	o := &sqlx.Options{}
	cases := map[string]StmtKind{
		"select * from t":            StmtRead,
		"-- c\nSELECT 1":             StmtRead,
		"select a into new_t from t": StmtWrite,
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x": StmtWrite,
		"with x as (select 1) select * from x":                  StmtRead,
		"explain select 1":                                      StmtRead,
		"explain analyze delete from t":                         StmtWrite,
		"delete from t":                                         StmtDestructive,
		"delete from t where id = 1":                            StmtWrite,
		"update t set a = 1":                                    StmtDestructive,
		"drop table t":                                          StmtDestructive,
		"truncate t":                                            StmtDestructive,
		"alter table t add column c int":                        StmtDDL,
		"alter table t drop column c":                           StmtDestructive,
		"create table x (a int)":                                StmtDDL,
		"begin":                                                 StmtTx,
		"commit":                                                StmtTx,
		"begin null; end;":                                      StmtWrite,
		"set search_path to x":                                  StmtSession,
		"insert into t values (1)":                              StmtWrite,
		"call proc()":                                           StmtWrite,
		"pragma table_info(t)":                                  StmtRead,
		"pragma foreign_keys = on":                              StmtWrite,
		// bypasses found in review
		"SELECT 1 DELETE FROM t":                                 StmtWrite,
		"SELECT 1\nDROP TABLE t":                                 StmtDestructive,
		"WITH a AS (SELECT 1) SELECT * INTO t2 FROM a":           StmtWrite,
		"SELECT * FROM t INTO OUTFILE '/tmp/x'":                  StmtWrite,
		"EXPLAIN ANALYSE DELETE FROM t WHERE id = 1":             StmtWrite,
		"DESCRIBE ANALYZE DELETE FROM t WHERE id = 1":            StmtWrite,
		"EXPLAIN ANALYZE EXECUTE p":                              StmtWrite,
		"PRAGMA journal_mode(WAL)":                               StmtWrite,
		"PRAGMA main.table_info(t)":                              StmtRead,
		"PRAGMA wal_checkpoint":                                  StmtWrite,
		"SET GLOBAL read_only = 0":                               StmtWrite,
		"SET @@global.read_only = 0":                             StmtWrite,
		"SET PERSIST max_connections = 10":                       StmtWrite,
		"SET PASSWORD FOR root = 'x'":                            StmtWrite,
		"SET default_transaction_read_only = off":                StmtWrite,
		"SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE":  StmtWrite,
		"SET TRANSACTION READ ONLY":                              StmtTx,
		"START REPLICA":                                          StmtWrite,
		"START TRANSACTION":                                      StmtTx,
		"START TRANSACTION READ WRITE":                           StmtWrite,
		"BEGIN IMMEDIATE":                                        StmtTx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity": StmtWrite,
		"UPDATE t SET a = (SELECT b FROM u WHERE u.id = 1)":      StmtDestructive,
		"UPDATE t SET a = 1 WHERE id IN (SELECT id FROM u)":      StmtWrite,
		"SELECT * FROM t FOR UPDATE":                             StmtWrite,
		"SELECT count(*) FROM orders WHERE status = 'deleted'":   StmtRead,
		"create procedure p as begin delete from t end":          StmtDDL,
	}
	for s, want := range cases {
		if got := Classify(s, o); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}

func TestClassifyDialects(t *testing.T) {
	my := &sqlx.Options{Backticks: true, HashComments: true, BackslashEscape: true, DashSpace: true}
	for q, want := range map[string]StmtKind{
		"/*!50000 DROP TABLE users */":            StmtDestructive,
		"/*!SET SESSION TRANSACTION READ WRITE*/": StmtWrite,
		"SHOW CREATE TABLE t":                     StmtRead,
	} {
		if got := Classify(q, my); got != want {
			t.Errorf("mysql %q: got %v want %v", q, got, want)
		}
	}
	pg := &sqlx.Options{DollarQuotes: true}
	for q, want := range map[string]StmtKind{
		"SELECT 1 /*! DELETE FROM t */":                   StmtRead, // a plain comment outside MySQL
		"SHOW CREATE TABLE t":                             StmtRead,
		"EXPLAIN UPDATE t SET a = 1 WHERE id = 2":         StmtRead,
		"EXPLAIN ANALYZE UPDATE t SET a = 1 WHERE id = 2": StmtWrite,
		"SELECT lock, load, copy, handler FROM t":         StmtRead,
		"SELECT * FROM t WHERE handler = 'x'":             StmtRead,
		"SELECT t.update, t.delete FROM t":                StmtRead,
		"SELECT c AS execute, rename FROM my.load":        StmtRead,
		"SELECT merge FROM t ORDER BY copy":               StmtRead,
		"SELECT 1 DELETE FROM t":                          StmtWrite,
		"SELECT 1 LOCK TABLE t":                           StmtWrite,
		"SELECT setval FROM t":                            StmtRead,
		"SELECT setval('s', 1)":                           StmtWrite,
		"SELECT 1; COPY t TO '/tmp/x'":                    StmtWrite,
	} {
		if got := Classify(q, pg); got != want {
			t.Errorf("pg %q: got %v want %v", q, got, want)
		}
	}
}
