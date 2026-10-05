// Package oracle is the Oracle Database adapter (go-ora, pure Go).
package oracle

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/network"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens Oracle connections.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Transactions: true, Cancel: true, ForeignKeys: true, Sequences: true, Functions: true,
	Procedures: true, Triggers: true, Views: true, MatViews: true, Insert: true, Update: true, Delete: true, TLS: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "oracle", Name: "Oracle", DefaultPort: 1521, DefaultUser: "system", DefaultDB: "FREEPDB1",
		DatabaseLabel: "Service name", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"oracle", "or", "ora", "oci"}, DialectName: "oracle"}
}

var reserved = db.WordSet(`
ACCESS ADD ALL ALTER AND ANY AS ASC AUDIT BETWEEN BY CHAR CHECK CLUSTER COLUMN COMMENT COMPRESS CONNECT CREATE
CURRENT DATE DECIMAL DEFAULT DELETE DESC DISTINCT DROP ELSE EXCLUSIVE EXISTS FILE FLOAT FOR FROM GRANT GROUP HAVING
IDENTIFIED IMMEDIATE IN INCREMENT INDEX INITIAL INSERT INTEGER INTERSECT INTO IS LEVEL LIKE LOCK LONG MAXEXTENTS MINUS
MLSLABEL MODE MODIFY NOAUDIT NOCOMPRESS NOT NOWAIT NULL NUMBER OF OFFLINE ON ONLINE OPTION OR ORDER PCTFREE PRIOR
PUBLIC RAW RENAME RESOURCE REVOKE ROW ROWID ROWNUM ROWS SELECT SESSION SET SHARE SIZE SMALLINT START SUCCESSFUL
SYNONYM SYSDATE TABLE THEN TO TRIGGER UID UNION UNIQUE UPDATE USER VALIDATE VALUES VARCHAR VARCHAR2 VIEW WHENEVER
WHERE WITH
`)

var keywords = db.Merge(reserved, db.CommonKeywords, db.WordSet(`
BEGIN DECLARE EXCEPTION LOOP EXIT ELSIF RAISE RETURN PRAGMA CURSOR OPEN CLOSE FETCH BULK COLLECT FORALL PACKAGE BODY
PROCEDURE FUNCTION TYPE RECORD VARRAY NOCOPY OUT IN EXECUTE IMMEDIATE DUAL CONNECT_BY_ROOT NOCYCLE SIBLINGS
MATERIALIZED REFRESH FAST COMPLETE ENABLE DISABLE PURGE FLASHBACK SAVEPOINT EDITIONABLE NONEDITIONABLE MERGE
CLOB BLOB NCLOB NVARCHAR2 TIMESTAMP INTERVAL BINARY_FLOAT BINARY_DOUBLE BOOLEAN
`))

var functions = append(db.CommonFunctions, strings.Fields(`
nvl nvl2 decode to_char to_date to_number to_timestamp to_clob trunc add_months months_between last_day
next_day sysdate systimestamp current_date instr substr lpad rpad initcap listagg regexp_like regexp_substr
regexp_replace regexp_instr sys_context user uid rownum rowidtohex hextoraw rawtohex dbms_lob dbms_output
json_value json_query json_table json_object json_arrayagg xmlagg
`)...)

var lexOptions = sqlx.Options{
	ColonParams: true, SlashLine: true, BatchBlocks: true, BlockSemicolon: true,
	BlockStart: func(w []string) bool {
		if w[0] == "BEGIN" || w[0] == "DECLARE" {
			return true
		}
		if w[0] != "CREATE" {
			return false
		}
		for _, x := range w[1:] {
			switch x {
			case "PROCEDURE", "FUNCTION", "PACKAGE", "TRIGGER", "TYPE":
				return true
			case "OR", "REPLACE", "EDITIONABLE", "NONEDITIONABLE", "EDITIONING":
				continue
			}
			return false
		}
		return false
	},
}

type dialect struct{ db.BaseDialect }

func newDialect() *dialect {
	d := &dialect{db.BaseDialect{
		ID: "oracle", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldUpper, Params: db.ParamColon, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: keywords, FuncList: functions,
		TypeList: strings.Fields("number varchar2 nvarchar2 char nchar date timestamp clob blob nclob raw long float binary_float binary_double integer interval boolean"),
		BoolTrue: "1", BoolFalse: "0",
		BytesLit: func(b []byte) string { return "HEXTORAW('" + strings.ToUpper(hex.EncodeToString(b)) + "')" },
	}}
	d.TimeLit = func(t time.Time, k db.Kind) string {
		switch k {
		case db.KindDate:
			return "DATE '" + t.Format("2006-01-02") + "'"
		case db.KindTimestampTZ:
			return "TIMESTAMP '" + db.FormatTime(t, k) + "'"
		}
		return "TIMESTAMP '" + db.FormatTime(t, db.KindTimestamp) + "'"
	}
	return d
}

// KindOf maps go-ora type names.
func KindOf(t string) db.Kind {
	switch strings.ToUpper(t) {
	case "NUMBER", "VARNUM":
		return db.KindDecimal
	case "FLOAT", "BFLOAT", "BDOUBLE", "IBFLOAT", "IBDOUBLE", "BINARY_FLOAT", "BINARY_DOUBLE":
		return db.KindFloat
	case "INTEGER", "SB1", "UINT":
		return db.KindInt
	case "CHAR", "NCHAR", "VARCHAR", "VARCHAR2", "NVARCHAR2", "LONG", "LONGVARCHAR", "OCICLOBLOCATOR", "CLOB", "NCLOB",
		"ROWID", "UROWID", "OCISTRING", "XMLTYPE":
		return db.KindText
	case "DATE", "OCIDATE":
		return db.KindTimestamp
	case "TIMESTAMP", "TIMESTAMPDTY":
		return db.KindTimestamp
	case "TIMESTAMPTZ", "TIMESTAMPTZ_DTY", "TIMESTAMPELTZ", "TIMESTAMPLTZ_DTY":
		return db.KindTimestampTZ
	case "RAW", "LONGRAW", "LONGVARRAW", "OCIBLOBLOCATOR", "BLOB", "VARRAW":
		return db.KindBytes
	case "INTERVALYM", "INTERVALDS", "INTERVALYM_DTY", "INTERVALDS_DTY":
		return db.KindInterval
	case "BOOLEAN":
		return db.KindBool
	}
	return db.KindOther
}

// kindOfDef maps data dictionary types (ALL_TAB_COLUMNS.DATA_TYPE).
func kindOfDef(t string, prec, scale int64, hasPrec bool) db.Kind {
	t = strings.ToUpper(t)
	switch {
	case t == "NUMBER" && hasPrec && scale == 0:
		return db.KindInt
	case t == "NUMBER":
		return db.KindDecimal
	case t == "FLOAT" || t == "BINARY_FLOAT" || t == "BINARY_DOUBLE":
		return db.KindFloat
	case t == "DATE":
		return db.KindTimestamp
	case strings.HasPrefix(t, "TIMESTAMP") && strings.Contains(t, "TIME ZONE"):
		return db.KindTimestampTZ
	case strings.HasPrefix(t, "TIMESTAMP"):
		return db.KindTimestamp
	case strings.HasPrefix(t, "INTERVAL"):
		return db.KindInterval
	case t == "RAW" || t == "BLOB" || t == "LONG RAW":
		return db.KindBytes
	case t == "JSON":
		return db.KindJSON
	case t == "BOOLEAN":
		return db.KindBool
	case strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || t == "LONG" || t == "ROWID":
		return db.KindText
	}
	return db.KindOther
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 1521
	}
	service := cfg.Database
	if service == "" {
		service = "FREEPDB1"
	}
	opts := map[string]string{"PROGRAM": "lumino", "CONNECT TIMEOUT": "15"}
	switch cfg.TLS.Mode {
	case "require":
		opts["SSL"], opts["SSL VERIFY"] = "true", "false"
	case "verify-ca", "verify-full":
		opts["SSL"], opts["SSL VERIFY"] = "true", "true"
		if cfg.TLS.CAFile != "" {
			return nil, errors.New("Oracle TLS with a custom CA needs a wallet: set the WALLET parameter to the wallet directory")
		}
	}
	for k, v := range cfg.Params {
		opts[k] = v
	}
	dsn := go_ora.BuildUrl(host, port, service, cfg.User, cfg.Password, opts)
	sdb, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT banner_full FROM v$version WHERE ROWNUM = 1")
	if ver == "" {
		ver, _ = sqlbase.One(ctx, sdb, "SELECT version FROM product_component_version WHERE ROWNUM = 1")
		ver = "Oracle " + ver
	}
	if i := strings.IndexByte(ver, '\n'); i > 0 {
		ver = ver[:i]
	}
	dia := newDialect()
	h := sqlbase.Hooks{
		Kind:     KindOf,
		Column:   adjustColumn,
		Convert:  convert,
		MapError: mapError,
		ReturnsRows: func(stmt string) bool {
			w := sqlx.Words(stmt, &lexOptions, 1)
			if len(w) == 1 && (w[0] == "EXEC" || w[0] == "EXECUTE" || w[0] == "CALL") {
				return false // procedure calls return no rows in Oracle
			}
			return sqlx.ReturnsRows(stmt, &lexOptions)
		},
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) {
			return sqlbase.One(ctx, q, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual")
		},
		SetCurrentSchema: func(ctx context.Context, q sqlbase.Querier, schema string) error {
			_, err := q.ExecContext(ctx, "ALTER SESSION SET CURRENT_SCHEMA = "+dia.QuoteAlways(schema))
			return err
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	return c, nil
}

// adjustColumn: NUMBER(p,0) holds integers.
func adjustColumn(c *db.Column) {
	if c.DBType == "NUMBER" && c.Prec > 0 && c.Scale == 0 {
		c.Kind = db.KindInt
	}
}

func convert(col *db.Column, v any) any {
	switch x := v.(type) {
	case float64:
		if col.Kind == db.KindInt && x == float64(int64(x)) {
			return int64(x)
		}
		if col.Kind == db.KindDecimal {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
	case go_ora.TimeStamp:
		return time.Time(x)
	case go_ora.TimeStampTZ:
		return time.Time(x)
	case go_ora.Clob:
		if !x.Valid {
			return nil
		}
		return x.String
	case go_ora.NClob:
		if !x.Valid {
			return nil
		}
		return x.String
	case go_ora.Blob:
		if x.Data == nil {
			return nil
		}
		return x.Data
	}
	return v
}

func mapError(err error) error {
	var oe *network.OracleError
	if errors.As(err, &oe) {
		msg := strings.TrimSpace(oe.ErrMsg)
		e := &db.Error{Msg: msg, Code: fmt.Sprintf("ORA-%05d", oe.ErrCode), Err: err}
		if p := oe.ErrPos(); p > 0 {
			e.Position = p + 1
		}
		return e
	}
	return err
}
