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
	}
	for s, want := range cases {
		if got := Classify(s, o); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}
