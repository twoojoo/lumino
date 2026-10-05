// Package mssql is the Microsoft SQL Server adapter (go-mssqldb).
package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens SQL Server connections.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Transactions: true, Cancel: true, ForeignKeys: true, Sequences: true, Functions: true,
	Procedures: true, Triggers: true, Views: true, Insert: true, Update: true, Delete: true, TLS: true,
	MultiResultSets: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "sqlserver", Name: "SQL Server", DefaultPort: 1433, DefaultUser: "sa", DefaultDB: "master",
		DatabaseLabel: "Database", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"sqlserver", "mssql", "ms", "azuresql"}, DialectName: "sqlserver"}
}

var reserved = db.WordSet(`
ADD ALL ALTER AND ANY AS ASC AUTHORIZATION BACKUP BEGIN BETWEEN BREAK BROWSE BULK BY CASCADE CASE CHECK CHECKPOINT
CLOSE CLUSTERED COALESCE COLLATE COLUMN COMMIT COMPUTE CONSTRAINT CONTAINS CONTAINSTABLE CONTINUE CONVERT CREATE
CROSS CURRENT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR DATABASE DBCC DEALLOCATE DECLARE
DEFAULT DELETE DENY DESC DISK DISTINCT DISTRIBUTED DOUBLE DROP DUMP ELSE END ERRLVL ESCAPE EXCEPT EXEC EXECUTE
EXISTS EXIT EXTERNAL FETCH FILE FILLFACTOR FOR FOREIGN FREETEXT FREETEXTTABLE FROM FULL FUNCTION GOTO GRANT GROUP
HAVING HOLDLOCK IDENTITY IDENTITY_INSERT IDENTITYCOL IF IN INDEX INNER INSERT INTERSECT INTO IS JOIN KEY KILL LEFT
LIKE LINENO LOAD MERGE NATIONAL NOCHECK NONCLUSTERED NOT NULL NULLIF OF OFF OFFSETS ON OPEN OPENDATASOURCE
OPENQUERY OPENROWSET OPENXML OPTION OR ORDER OUTER OVER PERCENT PIVOT PLAN PRECISION PRIMARY PRINT PROC PROCEDURE
PUBLIC RAISERROR READ READTEXT RECONFIGURE REFERENCES REPLICATION RESTORE RESTRICT RETURN REVERT REVOKE RIGHT
ROLLBACK ROWCOUNT ROWGUIDCOL RULE SAVE SCHEMA SECURITYAUDIT SELECT SEMANTICKEYPHRASETABLE
SEMANTICSIMILARITYDETAILSTABLE SEMANTICSIMILARITYTABLE SESSION_USER SET SETUSER SHUTDOWN SOME STATISTICS SYSTEM_USER
TABLE TABLESAMPLE TEXTSIZE THEN TO TOP TRAN TRANSACTION TRIGGER TRUNCATE TRY_CONVERT TSEQUAL UNION UNIQUE UNPIVOT
UPDATE UPDATETEXT USE USER VALUES VARYING VIEW WAITFOR WHEN WHERE WHILE WITH WITHIN WRITETEXT
`)

var functions = append(db.CommonFunctions, strings.Fields(`
getdate getutcdate sysdatetime sysutcdatetime sysdatetimeoffset dateadd datediff datename datepart eomonth
format isnull iif choose len charindex patindex stuff string_agg string_split concat_ws newid newsequentialid
scope_identity object_id object_name db_name schema_name suser_sname try_cast try_convert json_value json_query
openjson isjson row_number count_big checksum hashbytes
`)...)

var types = strings.Fields(`
bigint int smallint tinyint bit decimal numeric money smallmoney float real date time datetime datetime2
datetimeoffset smalldatetime char varchar nchar nvarchar text ntext binary varbinary image uniqueidentifier xml
sql_variant hierarchyid geography geometry rowversion
`)

var lexOptions = sqlx.Options{
	Brackets: true, AtParams: true, GoBatches: true, BatchBlocks: true,
	BlockStart: func(w []string) bool {
		if len(w) < 2 || w[0] != "CREATE" && w[0] != "ALTER" {
			return false
		}
		for _, x := range w[1:min(3, len(w))] {
			switch x {
			case "PROCEDURE", "PROC", "FUNCTION", "TRIGGER":
				return true
			}
		}
		// CREATE OR ALTER PROCEDURE
		return len(w) >= 4 && w[1] == "OR" && w[2] == "ALTER" && (w[3] == "PROCEDURE" || w[3] == "PROC" || w[3] == "FUNCTION" || w[3] == "TRIGGER")
	},
}

// dialect renders N'…' literals.
type dialect struct{ db.BaseDialect }

func newDialect() *dialect {
	return &dialect{db.BaseDialect{
		ID: "sqlserver", QuoteL: "[", QuoteR: "]", Fold: db.FoldNone, Params: db.ParamAtP, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: db.Merge(reserved, db.CommonKeywords, db.WordSet("NOCOUNT TRY CATCH THROW OUTPUT INSERTED DELETED NVARCHAR VARCHAR INT")),
		FuncList: functions, TypeList: types, BoolTrue: "1", BoolFalse: "0",
		BytesLit: func(b []byte) string { return fmt.Sprintf("0x%X", b) },
	}}
}

func (d *dialect) QuoteString(s string) string { return "N" + d.BaseDialect.QuoteString(s) }

func (d *dialect) Literal(v any, k db.Kind) string {
	switch x := v.(type) {
	case string:
		if k == db.KindDecimal || k == db.KindInt || k == db.KindFloat {
			return d.BaseDialect.Literal(v, k)
		}
		return d.QuoteString(x)
	case time.Time:
		return d.QuoteString(db.FormatTime(x, k))
	}
	return d.BaseDialect.Literal(v, k)
}

// KindOf maps SQL Server type names.
func KindOf(t string) db.Kind {
	t = strings.ToUpper(t)
	if i := strings.IndexByte(t, '('); i > 0 {
		t = t[:i]
	}
	switch t {
	case "INT", "BIGINT", "SMALLINT", "TINYINT":
		return db.KindInt
	case "BIT":
		return db.KindBool
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		return db.KindDecimal
	case "FLOAT", "REAL":
		return db.KindFloat
	case "DATE":
		return db.KindDate
	case "TIME":
		return db.KindTime
	case "DATETIME", "DATETIME2", "SMALLDATETIME":
		return db.KindTimestamp
	case "DATETIMEOFFSET":
		return db.KindTimestampTZ
	case "CHAR", "VARCHAR", "NCHAR", "NVARCHAR", "TEXT", "NTEXT", "XML", "SYSNAME":
		return db.KindText
	case "BINARY", "VARBINARY", "IMAGE", "TIMESTAMP", "ROWVERSION":
		return db.KindBytes
	case "UNIQUEIDENTIFIER":
		return db.KindUUID
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
		port = 1433
	}
	q := url.Values{}
	dbName := cfg.Database
	if dbName == "" {
		dbName = "master"
	}
	q.Set("database", dbName)
	q.Set("app name", "lumino")
	q.Set("dial timeout", "15")
	q.Set("keepAlive", "30")
	switch cfg.TLS.Mode {
	case "disable":
		q.Set("encrypt", "disable")
	case "require":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	case "verify-ca", "verify-full":
		q.Set("encrypt", "true")
		if cfg.TLS.CAFile != "" {
			q.Set("certificate", cfg.TLS.CAFile)
		}
		if cfg.TLS.Mode == "verify-full" {
			name := host
			if cfg.TLS.ServerName != "" {
				name = cfg.TLS.ServerName
			}
			q.Set("hostNameInCertificate", name)
		} else {
			q.Set("TrustServerCertificate", "false")
		}
	default:
		// only the login packet is encrypted: its certificate isn't verified
		q.Set("encrypt", "false")
		q.Set("TrustServerCertificate", "true")
	}
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(cfg.User, cfg.Password), Host: fmt.Sprintf("%s:%d", host, port), RawQuery: q.Encode()}
	connector, err := mssql.NewConnector(u.String())
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(connector)
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(50)) + ' ' + CAST(SERVERPROPERTY('Edition') AS nvarchar(100))")
	dia := newDialect()
	h := sqlbase.Hooks{
		Kind:        KindOf,
		Convert:     convert,
		MapError:    mapError,
		ReturnsRows: returnsRows,
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) {
			return sqlbase.One(ctx, q, "SELECT SCHEMA_NAME()")
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: "SQL Server " + ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	return c, nil
}

// returnsRows: T-SQL batches often mix statements (DECLARE …; SELECT …);
// everything but plain DML/DDL is run as a query.
func returnsRows(stmt string) bool {
	w := sqlx.Words(stmt, &lexOptions, 1)
	if len(w) == 0 {
		return false
	}
	switch w[0] {
	case "INSERT", "UPDATE", "DELETE", "MERGE":
		return sqlx.ReturnsRows(stmt, &lexOptions)
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "DENY", "USE", "SET", "BEGIN", "COMMIT",
		"ROLLBACK", "SAVE", "PRINT", "RAISERROR", "THROW", "DBCC", "BACKUP", "RESTORE", "KILL":
		return false
	}
	return true
}

// convert fixes the driver's raw values.
func convert(col *db.Column, v any) any {
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	switch col.Kind {
	case db.KindUUID:
		if len(b) == 16 {
			var u mssql.UniqueIdentifier
			if err := u.Scan(b); err == nil {
				return u.String()
			}
		}
	case db.KindDecimal, db.KindText, db.KindOther:
		if utf8.Valid(b) {
			return string(b)
		}
	}
	return b
}

func mapError(err error) error {
	var me mssql.Error
	if errors.As(err, &me) {
		return &db.Error{Msg: me.Message, Code: strconv.Itoa(int(me.Number)), Line: int(me.LineNo), Err: err}
	}
	var pe *mssql.Error
	if errors.As(err, &pe) {
		return &db.Error{Msg: pe.Message, Code: strconv.Itoa(int(pe.Number)), Line: int(pe.LineNo), Err: err}
	}
	return err
}
