// Package firebird is the Firebird adapter (nakagami/firebirdsql, pure Go).
package firebird

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nakagami/firebirdsql"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlbase"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens Firebird connections.
type Driver struct{}

var caps = db.Capabilities{
	Transactions: true, Cancel: true, ForeignKeys: true, Sequences: true, Functions: true, Procedures: true,
	Triggers: true, Views: true, Insert: true, Update: true, Delete: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "firebird", Name: "Firebird", DefaultPort: 3050, DefaultUser: "SYSDBA",
		DatabaseLabel: "Database (path or alias)", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"firebird", "fb", "firebirdsql"}, DialectName: "firebird"}
}

var reserved = db.WordSet(`
ADD ADMIN ALL ALTER AND ANY AS AT AVG BEGIN BETWEEN BIGINT BINARY BIT_LENGTH BLOB BOOLEAN BOTH BY CASE CAST CHAR
CHARACTER CHAR_LENGTH CHARACTER_LENGTH CHECK CLOSE COLLATE COLUMN COMMENT COMMIT CONNECT CONSTRAINT CORR COUNT
COVAR_POP COVAR_SAMP CREATE CROSS CURRENT CURRENT_CONNECTION CURRENT_DATE CURRENT_ROLE CURRENT_TIME CURRENT_TIMESTAMP
CURRENT_TRANSACTION CURRENT_USER CURSOR DATE DAY DEC DECFLOAT DECIMAL DECLARE DEFAULT DELETE DELETING DETERMINISTIC
DISCONNECT DISTINCT DOUBLE DROP ELSE END ESCAPE EXECUTE EXISTS EXTERNAL EXTRACT FALSE FETCH FILTER FLOAT FOR FOREIGN
FROM FULL FUNCTION GDSCODE GLOBAL GRANT GROUP HAVING HOUR IN INDEX INNER INSENSITIVE INSERT INSERTING INT INT128
INTEGER INTO IS JOIN LEADING LEFT LIKE LOCAL LOCALTIME LOCALTIMESTAMP LONG LOWER MAX MERGE MIN MINUTE MONTH NATIONAL
NATURAL NCHAR NO NOT NULL NUMERIC OCTET_LENGTH OF OFFSET ON ONLY OPEN OR ORDER OUTER OVER PARAMETER PLAN POSITION
POST_EVENT PRECISION PRIMARY PROCEDURE PUBLICATION RDB$DB_KEY RDB$ERROR RDB$GET_CONTEXT RDB$GET_TRANSACTION_CN
RDB$RECORD_VERSION RDB$ROLE_IN_USE RDB$SET_CONTEXT RDB$SYSTEM_PRIVILEGE REAL RECORD_VERSION RECREATE RECURSIVE
REFERENCES REGR_AVGX REGR_AVGY REGR_COUNT REGR_INTERCEPT REGR_R2 REGR_SLOPE REGR_SXX REGR_SXY REGR_SYY RELEASE
RESETTING RETURN RETURNING_VALUES RETURNS REVOKE RIGHT ROLLBACK ROW ROWS ROW_COUNT SAVEPOINT SCROLL SECOND SELECT
SENSITIVE SET SIMILAR SMALLINT SOME SQLCODE SQLSTATE START STDDEV_POP STDDEV_SAMP SUM TABLE THEN TIME TIMESTAMP
TIMEZONE_HOUR TIMEZONE_MINUTE TO TRAILING TRIGGER TRIM TRUE UNBOUNDED UNION UNIQUE UNKNOWN UPDATE UPDATING UPPER USER
USING VALUE VALUES VAR_POP VAR_SAMP VARBINARY VARCHAR VARIABLE VARYING VIEW WHEN WHERE WHILE WINDOW WITH WITHOUT
YEAR
`)

var lexOptions = sqlx.Options{
	SetTerm: true, BodyRequired: true,
	BlockStart: func(w []string) bool {
		if len(w) >= 2 && w[0] == "EXECUTE" && w[1] == "BLOCK" {
			return true
		}
		if w[0] != "CREATE" && w[0] != "ALTER" && w[0] != "RECREATE" {
			return false
		}
		for _, x := range w[1:] {
			switch x {
			case "PROCEDURE", "FUNCTION", "TRIGGER", "PACKAGE":
				return true
			case "OR", "ALTER", "BODY":
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
		ID: "firebird", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldUpper, Params: db.ParamQuestion, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: db.Merge(reserved, db.CommonKeywords, db.WordSet("SUSPEND EXCEPTION GENERATOR SEQUENCE ACTIVE INACTIVE POSITION TERM BLOCK IDENTITY")),
		FuncList: append(db.CommonFunctions, strings.Fields("gen_id dateadd datediff list iif decode left right lpad rpad hash uuid_to_char gen_uuid char_to_uuid")...),
		TypeList: strings.Fields("smallint integer bigint int128 numeric decimal float double real decfloat date time timestamp char varchar blob boolean binary varbinary"),
		BytesLit: func(b []byte) string { return "x'" + hex.EncodeToString(b) + "'" },
	}}
	d.TimeLit = func(t time.Time, k db.Kind) string {
		switch k {
		case db.KindDate:
			return "DATE '" + t.Format("2006-01-02") + "'"
		case db.KindTime:
			return "TIME '" + t.Format("15:04:05.0000") + "'"
		case db.KindTimestampTZ:
			return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.0000 -07:00") + "'"
		}
		return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.0000") + "'"
	}
	return d
}

// KindOf maps driver type names.
func KindOf(t string) db.Kind {
	switch strings.ToUpper(t) {
	case "SHORT", "LONG", "INT64":
		return db.KindInt
	case "INT128", "DECFLOAT(16)", "DECFLOAT(34)", "DECFIXED":
		return db.KindDecimal
	case "FLOAT", "DOUBLE":
		return db.KindFloat
	case "TEXT", "VARYING":
		return db.KindText
	case "DATE":
		return db.KindDate
	case "TIME", "TIME WITH TIMEZONE":
		return db.KindTime
	case "TIMESTAMP":
		return db.KindTimestamp
	case "TIMESTAMP WITH TIMEZONE":
		return db.KindTimestampTZ
	case "BLOB":
		return db.KindBytes
	case "BOOLEAN":
		return db.KindBool
	}
	return db.KindOther
}

func adjustColumn(c *db.Column) {
	// scaled integers are NUMERIC/DECIMAL values
	if c.Kind == db.KindInt && c.Scale != 0 {
		c.Kind = db.KindDecimal
	}
}

// convert trims the padding of CHAR(n) values. Text columns come back as
// []byte only for CHARACTER SET OCTETS (BINARY/VARBINARY): those stay bytes
// (the connection charset is UTF8, so every other text value is a string).
func convert(col *db.Column, v any) any {
	if x, ok := v.(string); ok && col.DBType == "TEXT" {
		return strings.TrimRight(x, " ") // CHAR(n) padding; VARCHAR keeps its spaces
	}
	return v
}

// buildDSN renders user:pass@host:port/path?params (parsed by the driver
// with net/url).
func buildDSN(cfg *db.Config) (string, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 3050
	}
	if cfg.Database == "" {
		return "", errors.New("set the database path or alias")
	}
	q := url.Values{}
	q.Set("charset", "UTF8")
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	return url.UserPassword(cfg.User, cfg.Password).String() + "@" + net.JoinHostPort(host, strconv.Itoa(port)) +
		dbPath(cfg.Database) + "?" + q.Encode(), nil
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	dsn, err := buildDSN(cfg)
	if err != nil {
		return nil, err
	}
	sdb, err := sql.Open("firebirdsql", dsn)
	if err != nil {
		return nil, err
	}
	// idle attachments keep their transaction context (and metadata locks)
	// open with this driver: DDL from other sessions would wait on them
	sdb.SetMaxIdleConns(0)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT rdb$get_context('SYSTEM', 'ENGINE_VERSION') FROM rdb$database")
	dia := newDialect()
	h := sqlbase.Hooks{
		// cancelling the context closes the connection: cancel through the
		// monitoring tables instead (allowed on the user's own attachments)
		SessionID: func(ctx context.Context, c *sql.Conn) (string, error) {
			return sqlbase.One(ctx, c, "SELECT CURRENT_CONNECTION FROM rdb$database")
		},
		Cancel: func(ctx context.Context, sdb *sql.DB, id string) bool {
			_, err := sdb.ExecContext(ctx, "DELETE FROM MON$STATEMENTS WHERE MON$ATTACHMENT_ID = ? AND MON$STATE = 1", id)
			return err == nil
		},
		Kind:     KindOf,
		Column:   adjustColumn,
		Convert:  convert,
		MapError: mapError,
		ReturnsRows: func(stmt string) bool {
			w := sqlx.Words(stmt, &lexOptions, 2)
			if len(w) == 2 && w[0] == "EXECUTE" && w[1] == "PROCEDURE" {
				return true // may return output parameters
			}
			if len(w) == 2 && w[0] == "EXECUTE" && w[1] == "BLOCK" {
				return blockReturns(stmt)
			}
			if len(w) >= 1 && w[0] == "EXECUTE" {
				return false
			}
			return sqlx.ReturnsRows(stmt, &lexOptions)
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: "Firebird " + ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	return c, nil
}

// dbPath escapes a database path or alias for the DSN, segment by segment
// ('#', '?', '%' and spaces would otherwise be parsed by the driver).
func dbPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	out := strings.Join(segs, "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

// blockReturns reports whether an EXECUTE BLOCK declares output parameters
// (RETURNS before the AS that starts its body).
func blockReturns(stmt string) bool {
	depth := 0
	for _, t := range sqlx.Tokens(stmt, &lexOptions) {
		switch t.Kind {
		case sqlx.TPunct:
			switch stmt[t.Start:t.End] {
			case "(":
				depth++
			case ")":
				depth--
			}
		case sqlx.TWord:
			if depth != 0 {
				continue
			}
			switch strings.ToUpper(stmt[t.Start:t.End]) {
			case "RETURNS":
				return true
			case "AS":
				return false
			}
		}
	}
	return false
}

var lineColRx = regexp.MustCompile(`(?i)line (\d+), column (\d+)`)

func mapError(err error) error {
	var fe *firebirdsql.FbError
	if errors.As(err, &fe) {
		msg := strings.TrimSpace(fe.Message)
		e := &db.Error{Msg: strings.ReplaceAll(msg, "\n", " — "), Code: fe.SQLState, Err: err}
		if m := lineColRx.FindStringSubmatch(msg); m != nil {
			e.Line, _ = strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			e.Column = col
			if e.Line == 1 {
				e.Position = col
			}
		}
		return e
	}
	return err
}
