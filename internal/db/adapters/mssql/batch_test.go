package mssql

import "testing"

func TestReturnsRowsLaterSelect(t *testing.T) {
	for q, want := range map[string]bool{
		"UPDATE t SET b = 3\nSELECT @@ROWCOUNT":                         true,
		"SET NOCOUNT ON; SELECT 1":                                      true,
		"PRINT 'x' EXEC sp_who":                                         true,
		"DELETE FROM t; WITH x AS (SELECT 1 a) SELECT * FROM x":         true,
		"INSERT INTO t VALUES (1) SELECT SCOPE_IDENTITY()":              true,
		"INSERT INTO t (a) SELECT a FROM s\nSELECT 1":                   true,
		"UPDATE t SET b = 3":                                            false,
		"UPDATE t SET b = (SELECT MAX(b) FROM s) WHERE a IN (SELECT 1)": false,
		"INSERT INTO t (a) SELECT a FROM s UNION ALL SELECT 2":          false,
		"INSERT INTO t WITH (TABLOCK) (a) SELECT a FROM s":              false,
		"INSERT INTO t EXEC p":                                          false,
		"UPDATE t WITH (ROWLOCK) SET b = 1":                             false,
		"SET @x = (SELECT 1)":                                           false,
		"CREATE PROCEDURE p AS SELECT 1":                                false,
		"DELETE FROM t OUTPUT deleted.a":                                true,
		"SELECT 1":                                                      true,
	} {
		if got := returnsRows(q); got != want {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}
