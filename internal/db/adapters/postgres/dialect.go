package postgres

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// reserved words that must be quoted as identifiers (PostgreSQL reserved
// and "requires AS" keywords).
var reserved = db.WordSet(`
ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC AUTHORIZATION BINARY BOTH CASE CAST CHECK COLLATE COLLATION
COLUMN CONCURRENTLY CONSTRAINT CREATE CROSS CURRENT_CATALOG CURRENT_DATE CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME
CURRENT_TIMESTAMP CURRENT_USER DEFAULT DEFERRABLE DESC DISTINCT DO ELSE END EXCEPT FALSE FETCH FOR FOREIGN FREEZE
FROM FULL GRANT GROUP HAVING ILIKE IN INITIALLY INNER INTERSECT INTO IS ISNULL JOIN LATERAL LEADING LEFT LIKE LIMIT
LOCALTIME LOCALTIMESTAMP NATURAL NOT NOTNULL NULL OFFSET ON ONLY OR ORDER OUTER OVERLAPS PLACING PRIMARY REFERENCES
RETURNING RIGHT SELECT SESSION_USER SIMILAR SOME SYMMETRIC SYSTEM_USER TABLE TABLESAMPLE THEN TO TRAILING TRUE UNION
UNIQUE USER USING VARIADIC VERBOSE WHEN WHERE WINDOW WITH
`)

var keywords = db.Merge(reserved, db.CommonKeywords, db.WordSet(`
ABORT ALSO ANALYZE ATTACH BEFORE CALLED CHAIN CLUSTER COMMENT CONCURRENTLY CONFLICT COPY COST CURSOR CYCLE DEALLOCATE
DECLARE DEFERRED DEFINER DELIMITER DEPENDS DETACH DICTIONARY DISABLE DISCARD DOMAIN EACH ENABLE ENCODING ENUM EVENT
EXCLUDE EXCLUSIVE EXECUTE EXTENSION FAMILY FILTER FORCE FUNCTIONS HANDLER IMMUTABLE IMPLICIT INCLUDE INCREMENT
INHERIT INHERITS INLINE INPUT INSTEAD INVOKER LANGUAGE LEAKPROOF LISTEN LOAD LOCK LOCKED LOGGED MAXVALUE MINVALUE
MODE MOVE NOTIFY NOWAIT OIDS OPERATOR OWNED OWNER PARALLEL PARSER PASSING PLANS POLICY PREPARE PREPARED PROCEDURAL
PROCEDURES PUBLICATION REASSIGN RECHECK REFRESH REINDEX RELATIVE RELEASE RESET RESTART RETURNS ROLE ROUTINE RULE
SCROLL SECURITY SEQUENCES SERIALIZABLE SERVER SESSION SETOF SHARE SKIP SNAPSHOT STABLE STANDALONE START STATEMENT
STATISTICS STDIN STDOUT STORAGE STRICT SUBSCRIPTION SUPPORT SYSID SYSTEM TABLESPACE TRUSTED TYPE TYPES UNENCRYPTED
UNLISTEN UNTIL VACUUM VALID VALIDATE VALIDATOR VERSION VOLATILE WORK WRAPPER XML ABSOLUTE ATOMIC ISOLATION LEVEL
READ WRITE COMMITTED UNCOMMITTED REPEATABLE PLPGSQL RAISE NOTICE EXCEPTION PERFORM RETURN LOOP WHILE ELSIF EXIT
CONTINUE FOREACH QUERY GET DIAGNOSTICS FOUND NEW OLD
`))

var functions = append(db.CommonFunctions, strings.Fields(`
array_agg array_length array_to_string string_agg json_agg jsonb_agg json_build_object jsonb_build_object
jsonb_set jsonb_extract_path jsonb_array_elements json_array_elements to_char to_date to_timestamp to_number
date_trunc date_part age now clock_timestamp generate_series regexp_replace regexp_matches split_part
string_to_array unnest left right initcap md5 gen_random_uuid nextval currval setval pg_size_pretty
pg_total_relation_size pg_relation_size current_setting set_config format quote_ident quote_literal
row_to_json to_json to_jsonb bool_and bool_or every percentile_cont percentile_disc mode
`)...)

var types = strings.Fields(`
bigint bigserial bit boolean box bytea char character cidr circle date double daterange inet integer interval
json jsonb line lseg macaddr money numeric path point polygon real smallint smallserial serial text time
timestamp timestamptz tsquery tsvector uuid varchar xml int int2 int4 int8 float4 float8 int4range int8range
numrange tsrange tstzrange
`)

// LexOptions are PostgreSQL's lexical rules (also used by CockroachDB).
var LexOptions = sqlx.Options{
	DollarQuotes:   true,
	NestedComments: true,
	BlockStart: func(w []string) bool {
		// SQL-standard function bodies: BEGIN ATOMIC … END
		for i := 0; i+1 < len(w); i++ {
			if w[i] == "BEGIN" && w[i+1] == "ATOMIC" {
				return true
			}
		}
		return false
	},
}

// Dialect is the PostgreSQL dialect.
type Dialect struct{ db.BaseDialect }

// NewDialect builds the dialect.
func NewDialect() *Dialect {
	d := &Dialect{db.BaseDialect{
		ID: "postgres", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower, Params: db.ParamDollar,
		LexOpts: LexOptions, Reserved: reserved, KeywordSet: keywords, FuncList: functions, TypeList: types,
		BytesLit: func(b []byte) string { return `'\x` + hex.EncodeToString(b) + `'::bytea` },
	}}
	d.TimeLit = func(t time.Time, k db.Kind) string {
		s := d.QuoteString(db.FormatTime(t, k))
		switch k {
		case db.KindDate:
			return s + "::date"
		case db.KindTime:
			return s + "::time"
		case db.KindTimestampTZ:
			return s + "::timestamptz"
		}
		return s + "::timestamp"
	}
	return d
}

// Literal adds casts for JSON and UUID values.
func (d *Dialect) Literal(v any, k db.Kind) string {
	s := d.BaseDialect.Literal(v, k)
	if v == nil {
		return s
	}
	switch k {
	case db.KindJSON:
		return s + "::jsonb"
	case db.KindUUID:
		return s + "::uuid"
	}
	return s
}

// KindOf maps PostgreSQL type names to kinds.
func KindOf(t string) db.Kind {
	t = strings.ToUpper(t)
	if strings.HasPrefix(t, "_") || strings.HasSuffix(t, "[]") {
		return db.KindArray
	}
	switch t {
	case "INT2", "INT4", "INT8", "SMALLINT", "INTEGER", "BIGINT", "OID", "INT", "SERIAL", "BIGSERIAL", "XID":
		return db.KindInt
	case "FLOAT4", "FLOAT8", "REAL", "DOUBLE PRECISION", "FLOAT":
		return db.KindFloat
	case "NUMERIC", "DECIMAL", "MONEY":
		return db.KindDecimal
	case "BOOL", "BOOLEAN":
		return db.KindBool
	case "DATE":
		return db.KindDate
	case "TIME":
		return db.KindTime
	case "TIMETZ":
		// kept as text: time values can't carry a zone offset
		return db.KindText
	case "TIMESTAMP":
		return db.KindTimestamp
	case "TIMESTAMPTZ":
		return db.KindTimestampTZ
	case "INTERVAL":
		return db.KindInterval
	case "BYTEA":
		return db.KindBytes
	case "JSON", "JSONB":
		return db.KindJSON
	case "UUID":
		return db.KindUUID
	case "TEXT", "VARCHAR", "BPCHAR", "CHAR", "NAME", "CITEXT", "XML", "INET", "CIDR", "MACADDR":
		return db.KindText
	}
	return db.KindOther
}

// KindOfFormatted maps format_type() output (column definitions).
func KindOfFormatted(t string) db.Kind {
	l := strings.ToLower(t)
	if strings.HasSuffix(l, "[]") {
		return db.KindArray
	}
	if i := strings.IndexByte(l, '('); i > 0 {
		l = strings.TrimSpace(l[:i]) + strings.TrimSpace(l[strings.IndexByte(l, ')')+1:])
	}
	switch {
	case l == "smallint" || l == "integer" || l == "bigint" || l == "oid":
		return db.KindInt
	case l == "real" || l == "double precision":
		return db.KindFloat
	case l == "numeric" || l == "money":
		return db.KindDecimal
	case l == "boolean":
		return db.KindBool
	case l == "date":
		return db.KindDate
	case strings.HasPrefix(l, "time") && strings.Contains(l, "with time zone") && strings.HasPrefix(l, "timestamp"):
		return db.KindTimestampTZ
	case strings.HasPrefix(l, "timestamp"):
		return db.KindTimestamp
	case strings.HasPrefix(l, "time") && strings.Contains(l, "with time zone"):
		return db.KindText
	case strings.HasPrefix(l, "time"):
		return db.KindTime
	case strings.HasPrefix(l, "interval"):
		return db.KindInterval
	case l == "bytea":
		return db.KindBytes
	case l == "json" || l == "jsonb":
		return db.KindJSON
	case l == "uuid":
		return db.KindUUID
	case strings.HasPrefix(l, "character") || l == "text" || l == "name" || l == "citext" || l == "inet" || l == "cidr":
		return db.KindText
	}
	return db.KindOther
}
