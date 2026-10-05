// Package clickhouse is the ClickHouse adapter (clickhouse-go v2, native or
// HTTP protocol).
package clickhouse

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens ClickHouse connections.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Cancel: true, Views: true, MatViews: true, Insert: true, Update: true, Delete: true, TLS: true,
	EditNeedsWarning: "ClickHouse applies updates and deletes as mutations (ALTER TABLE … UPDATE/DELETE): they rewrite data parts and every row matching the sorting key is changed.",
}

func (Driver) Info() db.Info {
	return db.Info{ID: "clickhouse", Name: "ClickHouse", DefaultPort: 9000, DefaultUser: "default", DefaultDB: "default",
		DatabaseLabel: "Database", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"clickhouse", "ch"}, DialectName: "clickhouse"}
}

var keywords = db.Merge(db.CommonKeywords, db.WordSet(`
ENGINE MERGETREE REPLACINGMERGETREE SUMMINGMERGETREE AGGREGATINGMERGETREE COLLAPSINGMERGETREE LOG TINYLOG MEMORY
PREWHERE FINAL SAMPLE ARRAY JOIN GLOBAL ANY ALL ASOF SEMI ANTI SETTINGS FORMAT TTL CODEC MATERIALIZED ALIAS
EPHEMERAL POPULATE LIVE DICTIONARY OPTIMIZE DEDUPLICATE ATTACH DETACH SYSTEM KILL QUERY MUTATION ILIKE TOTALS
CUBE ROLLUP LIMIT BY INTERVAL
`))

var functions = append(db.CommonFunctions, strings.Fields(`
toDate toDateTime toDateTime64 toString toInt32 toInt64 toUInt64 toFloat64 toDecimal64 now today yesterday
toStartOfDay toStartOfHour toStartOfMonth toYYYYMM formatDateTime dateDiff dateAdd uniq uniqExact groupArray
groupUniqArray arrayJoin arrayMap arrayFilter has length empty notEmpty if multiIf countIf sumIf avgIf any anyLast
argMax argMin quantile quantiles median topK JSONExtract JSONExtractString visitParamExtractString splitByChar
match extract replaceAll lowerUTF8 upperUTF8 generateUUIDv4 cityHash64 sipHash64 numbers
`)...)

var types = strings.Fields(`
UInt8 UInt16 UInt32 UInt64 UInt128 UInt256 Int8 Int16 Int32 Int64 Int128 Int256 Float32 Float64 Decimal
Decimal32 Decimal64 Decimal128 Bool String FixedString UUID Date Date32 DateTime DateTime64 Enum8 Enum16 Array
Tuple Map Nullable LowCardinality IPv4 IPv6 JSON
`)

var lexOptions = sqlx.Options{Backticks: true, BackslashEscape: true}

type dialect struct{ db.BaseDialect }

func newDialect() *dialect {
	return &dialect{db.BaseDialect{
		ID: "clickhouse", QuoteL: "`", QuoteR: "`", Fold: db.FoldNone, Params: db.ParamQuestion, LexOpts: lexOptions,
		Reserved:   db.WordSet("SELECT FROM WHERE GROUP ORDER BY HAVING LIMIT JOIN AS ON AND OR NOT IN IS NULL TABLE DATABASE FORMAT SETTINGS FINAL"),
		KeywordSet: keywords, FuncList: functions, TypeList: types, StringBackslash: true,
		BytesLit: func(b []byte) string { return "unhex('" + hex.EncodeToString(b) + "')" },
	}}
}

// BuildChange renders updates and deletes as mutations. Times are inlined
// as literals: the driver's binding drops sub-second precision and time
// zones of DateTime64 values.
func (d *dialect) BuildChange(ref db.ObjectRef, chg db.Change, literal bool) (string, []any) {
	var args []any
	val := func(cv db.ColValue) string {
		if _, isTime := cv.Value.(time.Time); literal || cv.Value == nil || isTime {
			return d.Literal(cv.Value, cv.Kind)
		}
		args = append(args, cv.Value)
		return "?"
	}
	table := d.Qualified(ref)
	switch chg.Op {
	case db.OpInsert:
		cols := make([]string, len(chg.Values))
		vals := make([]string, len(chg.Values))
		for i, cv := range chg.Values {
			cols[i], vals[i] = d.Quote(cv.Column), val(cv)
		}
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), strings.Join(vals, ", ")), args
	case db.OpDelete:
		return fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", table, db.KeyWhere(d, chg.Key, val)), args
	}
	sets := make([]string, len(chg.Values))
	for i, cv := range chg.Values {
		sets[i] = d.Quote(cv.Column) + " = " + val(cv)
	}
	return fmt.Sprintf("ALTER TABLE %s UPDATE %s WHERE %s", table, strings.Join(sets, ", "), db.KeyWhere(d, chg.Key, val)), args
}

// Literal renders times with explicit precision and zone.
func (d *dialect) Literal(v any, k db.Kind) string {
	if t, ok := v.(time.Time); ok {
		switch k {
		case db.KindDate:
			return "toDate('" + t.Format("2006-01-02") + "')"
		case db.KindTimestampTZ:
			return "parseDateTime64BestEffort('" + t.UTC().Format("2006-01-02T15:04:05.999999999Z") + "', 9)"
		}
		return "toDateTime64('" + t.Format("2006-01-02 15:04:05.999999999") + "', 9)"
	}
	return d.BaseDialect.Literal(v, k)
}

var querySeq atomic.Int64

func newQueryID() string {
	return fmt.Sprintf("lumino-%d-%d", time.Now().UnixNano(), querySeq.Add(1))
}

// unwrap strips Nullable(…) and LowCardinality(…).
func unwrap(t string) string {
	for {
		switch {
		case strings.HasPrefix(t, "Nullable(") && strings.HasSuffix(t, ")"):
			t = t[len("Nullable(") : len(t)-1]
		case strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")"):
			t = t[len("LowCardinality(") : len(t)-1]
		default:
			return t
		}
	}
}

// KindOf maps ClickHouse type names (case sensitive in ClickHouse; the
// generic layer upper-cases them).
func KindOf(t string) db.Kind {
	u := strings.ToUpper(unwrap(t))
	if strings.HasPrefix(u, "NULLABLE(") || strings.HasPrefix(u, "LOWCARDINALITY(") {
		// upper-cased wrapper
		for _, p := range []string{"NULLABLE(", "LOWCARDINALITY("} {
			for strings.HasPrefix(u, p) && strings.HasSuffix(u, ")") {
				u = u[len(p) : len(u)-1]
			}
		}
	}
	base := u
	if i := strings.IndexByte(base, '('); i > 0 {
		base = base[:i]
	}
	switch {
	case base == "UINT128" || base == "UINT256" || base == "INT128" || base == "INT256":
		return db.KindDecimal // beyond int64/uint64: kept as decimal strings
	case strings.HasPrefix(base, "INTERVAL"):
		return db.KindInterval
	case strings.HasPrefix(base, "UINT") || strings.HasPrefix(base, "INT"):
		return db.KindInt
	case strings.HasPrefix(base, "FLOAT"):
		return db.KindFloat
	case strings.HasPrefix(base, "DECIMAL"):
		return db.KindDecimal
	case base == "BOOL":
		return db.KindBool
	case base == "DATE" || base == "DATE32":
		return db.KindDate
	case base == "DATETIME" || base == "DATETIME64":
		if strings.Contains(u, "'") {
			return db.KindTimestampTZ
		}
		return db.KindTimestamp
	case base == "STRING" || base == "FIXEDSTRING" || strings.HasPrefix(base, "ENUM") || base == "IPV4" || base == "IPV6":
		return db.KindText
	case base == "UUID":
		return db.KindUUID
	case base == "JSON" || base == "OBJECT":
		return db.KindJSON
	case base == "ARRAY" || base == "TUPLE" || base == "MAP":
		return db.KindArray
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
		port = 9000
	}
	opts := &ch.Options{
		Addr:        []string{net.JoinHostPort(host, strconv.Itoa(port))},
		Auth:        ch.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
		DialTimeout: 15 * time.Second,
		ReadTimeout: 24 * time.Hour, // long analytical queries; cancellation is explicit
		Settings:    ch.Settings{"mutations_sync": 1, "date_time_input_format": "best_effort"},
		ClientInfo:  ch.ClientInfo{Products: []struct{ Name, Version string }{{Name: "lumino", Version: "1"}}},
	}
	if strings.EqualFold(cfg.Param("protocol"), "http") {
		opts.Protocol = ch.HTTP
	}
	tc, err := cfg.TLS.TLS(host)
	if err != nil {
		return nil, err
	}
	opts.TLS = tc
	for k, v := range cfg.Params {
		if k != "protocol" {
			opts.Settings[k] = v
		}
	}
	sdb := ch.OpenDB(opts)
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT version()")
	dia := newDialect()
	h := sqlbase.Hooks{
		Kind:     KindOf,
		Convert:  convert,
		MapError: mapError,
		NoTx:     true,
		ReturnsRows: func(stmt string) bool {
			w := sqlx.Words(stmt, &lexOptions, 1)
			if len(w) > 0 && (w[0] == "EXISTS" || w[0] == "CHECK" || w[0] == "DESC") {
				return true
			}
			return sqlx.ReturnsRows(stmt, &lexOptions)
		},
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) {
			return sqlbase.One(ctx, q, "SELECT currentDatabase()")
		},
		// cancelling the context drops the connection: kill by query id
		StatementContext: func(ctx context.Context) (context.Context, string) {
			id := newQueryID()
			return ch.Context(ctx, ch.WithQueryID(id)), id
		},
		Cancel: func(ctx context.Context, sdb *sql.DB, id string) bool {
			_, err := sdb.ExecContext(ctx, "KILL QUERY WHERE query_id = ? ASYNC", id)
			return err == nil
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: "ClickHouse " + ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	return c, nil
}

func convert(col *db.Column, v any) any {
	if b, ok := v.([]byte); ok && col.Kind == db.KindText {
		return string(b)
	}
	return bigInts(v)
}

var bigIntType = reflect.TypeOf(big.Int{})

// bigInts renders (U)Int128/(U)Int256 values, which the driver returns as
// big.Int values (not pointers, so not Stringers), as decimal strings, also
// inside Nullable, arrays, tuples and maps.
func bigInts(v any) any {
	switch x := v.(type) {
	case nil, string, []byte, int64, uint64, float64, bool, time.Time:
		return v
	case big.Int:
		return x.String()
	case *big.Int:
		if x == nil {
			return nil
		}
		return x.String()
	}
	rv := reflect.ValueOf(v)
	if !hasBigInt(rv.Type(), 0) {
		return v
	}
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return bigInts(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = bigInts(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		it := rv.MapRange()
		for it.Next() {
			out[db.FormatValue(db.Normalize(bigInts(it.Key().Interface())), db.KindOther)] = bigInts(it.Value().Interface())
		}
		return out
	case reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return bigInts(rv.Elem().Interface())
	}
	return v
}

// hasBigInt reports whether values of t may contain a big.Int.
func hasBigInt(t reflect.Type, depth int) bool {
	if depth > 8 {
		return false
	}
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return hasBigInt(t.Elem(), depth+1)
	case reflect.Map:
		return hasBigInt(t.Key(), depth+1) || hasBigInt(t.Elem(), depth+1)
	case reflect.Struct:
		return t == bigIntType
	}
	return false
}

var posRx = regexp.MustCompile(`failed at position (\d+)`)

func mapError(err error) error {
	var ex *proto.Exception
	if errors.As(err, &ex) {
		msg := ex.Message
		if i := strings.Index(msg, ". Stack trace"); i > 0 {
			msg = msg[:i]
		}
		e := &db.Error{Msg: msg, Code: strconv.Itoa(int(ex.Code)), Err: err}
		if m := posRx.FindStringSubmatch(msg); m != nil {
			e.Position, _ = strconv.Atoi(m[1])
		}
		return e
	}
	return err
}
