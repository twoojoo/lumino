// Package trino is the Trino (and Presto-compatible) adapter.
package trino

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trinodb/trino-go-client/trino"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens Trino connections.
type Driver struct{}

var caps = db.Capabilities{Catalogs: true, Schemas: true, Cancel: true, Views: true, MatViews: true, TLS: true}

func (Driver) Info() db.Info {
	return db.Info{ID: "trino", Name: "Trino", DefaultPort: 8080, DefaultUser: "trino",
		DatabaseLabel: "Catalog", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"trino", "presto", "tr"}, DialectName: "trino"}
}

var reserved = db.WordSet(`
ALTER AND AS BETWEEN BY CASE CAST CONSTRAINT CREATE CROSS CUBE CURRENT_CATALOG CURRENT_DATE CURRENT_PATH
CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER DEALLOCATE DELETE DESCRIBE DISTINCT DROP
ELSE END ESCAPE EXCEPT EXECUTE EXISTS EXTRACT FALSE FOR FROM FULL GROUP GROUPING HAVING IN INNER INSERT INTERSECT
INTO IS JOIN JSON_ARRAY JSON_EXISTS JSON_OBJECT JSON_QUERY JSON_TABLE JSON_VALUE LEFT LIKE LISTAGG LOCALTIME
LOCALTIMESTAMP NATURAL NORMALIZE NOT NULL ON OR ORDER OUTER PREPARE RECURSIVE RIGHT ROLLUP SELECT SKIP TABLE THEN
TRIM TRUE UESCAPE UNION UNNEST USING VALUES WHEN WHERE WITH
`)

var lexOptions = sqlx.Options{}

// NewDialect builds the Trino dialect.
func NewDialect() *db.BaseDialect {
	return &db.BaseDialect{
		ID: "trino", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower, Params: db.ParamQuestion, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: db.Merge(reserved, db.CommonKeywords, db.WordSet("CATALOGS SCHEMAS TABLES COLUMNS SESSION USE ANALYZE")),
		FuncList:       append(db.CommonFunctions, strings.Fields("approx_distinct approx_percentile array_agg date_add date_diff date_format date_trunc from_unixtime json_extract json_extract_scalar json_format json_parse map_agg regexp_extract regexp_like sequence split to_unixtime transform try try_cast")...),
		TypeList:       strings.Fields("boolean tinyint smallint integer bigint real double decimal varchar char varbinary json date time timestamp interval array map row uuid ipaddress"),
		QualifyCatalog: true,
		BytesLit:       func(b []byte) string { return fmt.Sprintf("X'%X'", b) },
	}
}

// KindOf maps Trino type names: "timestamp(3) with time zone", "time with
// time zone", "decimal(10,2)", "array(integer)", "interval day to second"…
func KindOf(t string) db.Kind {
	u := strings.ToUpper(strings.TrimSpace(t))
	base := u
	if i := strings.IndexAny(base, "( "); i > 0 {
		base = base[:i]
	}
	switch base {
	case "TINYINT", "SMALLINT", "INTEGER", "INT", "BIGINT":
		return db.KindInt
	case "REAL", "DOUBLE":
		return db.KindFloat
	case "DECIMAL", "NUMBER":
		return db.KindDecimal
	case "BOOLEAN":
		return db.KindBool
	case "DATE":
		return db.KindDate
	case "TIME":
		return db.KindTime
	case "TIMESTAMP":
		if strings.Contains(u, "WITH TIME ZONE") {
			return db.KindTimestampTZ
		}
		return db.KindTimestamp
	case "VARCHAR", "CHAR", "IPADDRESS":
		return db.KindText
	case "VARBINARY":
		return db.KindBytes
	case "JSON":
		return db.KindJSON
	case "UUID":
		return db.KindUUID
	case "ARRAY", "MAP", "ROW":
		return db.KindArray
	case "INTERVAL":
		return db.KindInterval
	}
	return db.KindOther
}

// convert turns ROW values (also nested in arrays and maps) into maps of
// their named fields, or lists when the fields are anonymous.
func convert(_ *db.Column, v any) any {
	switch x := v.(type) {
	case trino.Row:
		if !x.Valid {
			return nil
		}
		named := false
		for i := 0; i < x.Len(); i++ {
			if x.Name(i) != "field"+strconv.Itoa(i) {
				named = true
				break
			}
		}
		if named {
			m := make(map[string]any, x.Len())
			for i := 0; i < x.Len(); i++ {
				m[x.Name(i)] = convert(nil, x.Value(i))
			}
			return m
		}
		out := make([]any, x.Len())
		for i := range out {
			out[i] = convert(nil, x.Value(i))
		}
		return out
	case *trino.Row:
		if x == nil {
			return nil
		}
		return convert(nil, *x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = convert(nil, e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = convert(nil, e)
		}
		return out
	}
	return v
}

var clientSeq atomic.Int64

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 8080
	}
	tc, err := cfg.TLS.TLS(host)
	if err != nil {
		return nil, err
	}
	scheme := "http"
	if tc != nil {
		scheme = "https"
	}
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port))}
	if cfg.Password != "" {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	} else {
		u.User = url.User(cfg.User)
	}
	q := url.Values{}
	q.Set("source", "lumino")
	if cfg.Database != "" {
		q.Set("catalog", cfg.Database)
	}
	if s := cfg.Param("schema"); s != "" {
		q.Set("schema", s)
	}
	if tc != nil {
		name := "lumino" + strconv.FormatInt(clientSeq.Add(1), 10)
		if err := trino.RegisterCustomClient(name, &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}); err != nil {
			return nil, err
		}
		q.Set("custom_client", name)
	}
	for k, v := range cfg.Params {
		if k != "schema" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	sdb, err := sql.Open("trino", u.String())
	if err != nil {
		return nil, err
	}
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	ver, err := sqlbase.One(ctx, sdb, "SELECT node_version FROM system.runtime.nodes WHERE coordinator LIMIT 1")
	if err != nil {
		if ver2, err2 := sqlbase.One(ctx, sdb, "SELECT 1"); err2 != nil || ver2 == "" {
			sdb.Close()
			return nil, mapError(err)
		}
	}
	dia := NewDialect()
	h := sqlbase.Hooks{
		Kind:     KindOf,
		Convert:  convert,
		MapError: mapError,
		NoTx:     true,
		ReturnsRows: func(stmt string) bool {
			// every Trino statement returns a result (DDL reports a row)
			w := sqlx.Words(stmt, &lexOptions, 1)
			if len(w) == 0 {
				return false
			}
			switch w[0] {
			case "CREATE", "DROP", "ALTER", "INSERT", "DELETE", "UPDATE", "MERGE", "TRUNCATE", "GRANT", "REVOKE",
				"COMMENT", "CALL", "USE", "SET", "RESET", "ANALYZE", "REFRESH", "PREPARE", "DEALLOCATE":
				return false
			}
			return true
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: "Trino " + ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	return c, nil
}

func mapError(err error) error {
	var te *trino.ErrTrino
	if errors.As(err, &te) {
		msg := te.Message
		// "line 1:8: Column 'x' cannot be resolved"
		if strings.HasPrefix(msg, "line ") {
			if i := strings.Index(msg, ": "); i > 0 {
				msg = msg[i+2:]
			}
		}
		return &db.Error{Msg: msg, Code: te.ErrorName, Line: te.ErrorLocation.LineNumber,
			Position: positionOnFirstLine(te.ErrorLocation), Err: err}
	}
	return err
}

func positionOnFirstLine(l trino.ErrorLocation) int {
	if l.LineNumber == 1 {
		return l.ColumnNumber
	}
	return 0
}

type catalog struct {
	db *sql.DB
	d  *db.BaseDialect
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) {
	out, err := sqlbase.Strings(ctx, c.db, "SHOW CATALOGS")
	return out, mapError(err)
}

func (c *catalog) is(cat string) string { return c.d.Quote(cat) + ".information_schema" }

func (c *catalog) Schemas(ctx context.Context, cat string) ([]db.Schema, error) {
	if cat == "" {
		return nil, errors.New("choose a catalog")
	}
	names, err := sqlbase.Strings(ctx, c.db, "SELECT schema_name FROM "+c.is(cat)+".schemata ORDER BY schema_name")
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]db.Schema, len(names))
	for i, n := range names {
		out[i] = db.Schema{Name: n, System: n == "information_schema"}
	}
	return out, nil
}

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	recs, err := sqlbase.Recs(ctx, c.db, "SELECT table_name, table_type FROM "+c.is(schema.Catalog)+".tables WHERE table_schema = ? ORDER BY table_name", schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	var mviews map[string]bool
	if sqlbase.Wants(types, db.ObjMatView) || sqlbase.Wants(types, db.ObjView) {
		names, err := sqlbase.Strings(ctx, c.db, "SELECT name FROM system.metadata.materialized_views WHERE catalog_name = ? AND schema_name = ?", schema.Catalog, schema.Schema)
		if err == nil {
			mviews = map[string]bool{}
			for _, n := range names {
				mviews[n] = true
			}
		}
	}
	var out []db.Object
	for _, r := range recs {
		t := db.ObjTable
		if r["table_type"] == "VIEW" {
			t = db.ObjView
		}
		if mviews[r["table_name"]] {
			t = db.ObjMatView
		}
		if sqlbase.Wants(types, t) {
			out = append(out, db.Object{Ref: db.ObjectRef{Catalog: schema.Catalog, Schema: schema.Schema, Name: r["table_name"], Type: t}})
		}
	}
	return out, nil
}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	recs, err := sqlbase.Recs(ctx, c.db, "SELECT column_name, data_type, is_nullable, column_default FROM "+c.is(ref.Catalog)+
		".columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position", ref.Schema, ref.Name)
	if err != nil {
		return nil, mapError(err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	for _, r := range recs {
		t.Columns = append(t.Columns, db.TableColumn{Name: r["column_name"], Type: r["data_type"], Kind: KindOf(r["data_type"]),
			Nullable: r.Bool("is_nullable"), Default: r["column_default"], HasDefault: r["column_default"] != ""})
	}
	if cmt, err := sqlbase.One(ctx, c.db, "SELECT comment FROM system.metadata.table_comments WHERE catalog_name = ? AND schema_name = ? AND table_name = ?",
		ref.Catalog, ref.Schema, ref.Name); err == nil {
		t.Comment = cmt
	}
	return t, nil
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	kw := "TABLE"
	switch ref.Type {
	case db.ObjView:
		kw = "VIEW"
	case db.ObjMatView:
		kw = "MATERIALIZED VIEW"
	}
	s, err := sqlbase.One(ctx, c.db, "SHOW CREATE "+kw+" "+c.d.Qualified(ref))
	if err != nil {
		return "", mapError(err)
	}
	return strings.TrimSpace(s) + ";\n", nil
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := sqlbase.Recs(ctx, c.db, "SELECT table_name AS t, column_name AS c FROM "+c.is(schema.Catalog)+
		".columns WHERE table_schema = ? ORDER BY table_name, ordinal_position", schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
