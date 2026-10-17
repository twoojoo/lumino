package dbtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
)

// RunDocuments is the conformance suite of document databases (MongoDB
// and compatibles): the same guarantees as Run — metadata, streaming,
// cancel, transactions, read-only sessions, errors, normalized values,
// grid edits keeping types — through the connection's Language, with
// mongosh statements.
func RunDocuments(t *testing.T, tg Target) {
	conn := Prepare(t, tg)
	ctx := context.Background()
	caps := conn.Caps()
	cat := conn.Catalog()
	lang := conn.Language()
	ref := func(name string) db.ObjectRef {
		return db.ObjectRef{Schema: tg.Schema, Name: name, Type: db.ObjCollection}
	}
	count := func(t *testing.T, s db.Session, name, filter string) string {
		t.Helper()
		q, err := lang.Count(ref(name), filter)
		if err != nil {
			t.Fatal(err)
		}
		return scalar(t, s, q)
	}

	t.Run("Ping", func(t *testing.T) {
		if err := conn.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		if conn.ServerVersion() == "" || lang.Name() != "mongo" {
			t.Errorf("version %q, language %q", conn.ServerVersion(), lang.Name())
		}
	})

	t.Run("Schemas", func(t *testing.T) {
		ss, err := cat.Schemas(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(ss, func(s db.Schema) bool { return s.Name == tg.Schema })
		if i < 0 || !ss[i].Current || ss[i].System {
			t.Fatalf("schemas %+v", ss)
		}
	})

	t.Run("Objects", func(t *testing.T) {
		objs, err := cat.Objects(ctx, db.ObjectRef{Schema: tg.Schema})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]db.Object{}
		for _, o := range objs {
			got[o.Ref.Name] = o
		}
		for _, n := range []string{"customers", "orders", "all_types", "big"} {
			if got[n].Ref.Type != db.ObjCollection {
				t.Errorf("collection %s missing: %+v", n, objs)
			}
		}
		if !tg.NoViews {
			if got["customer_names"].Ref.Type != db.ObjView {
				t.Errorf("view missing: %+v", objs)
			}
			if got["capped_log"].Extra != "capped" {
				t.Errorf("capped collection: %+v", got["capped_log"])
			}
		}
		for _, o := range objs {
			if strings.HasPrefix(o.Ref.Name, "system.") {
				t.Errorf("system collection listed: %s", o.Ref.Name)
			}
		}
		views, err := cat.Objects(ctx, db.ObjectRef{Schema: tg.Schema}, db.ObjView)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range views {
			if o.Ref.Type != db.ObjView {
				t.Errorf("type filter: %+v", o)
			}
		}
	})

	t.Run("Describe", func(t *testing.T) {
		info, err := cat.Table(ctx, ref("customers"))
		if err != nil {
			t.Fatal(err)
		}
		if len(info.PrimaryKey) != 1 || info.PrimaryKey[0] != "_id" || info.Columns[0].Name != "_id" || info.Columns[0].PrimaryKeyNo != 1 {
			t.Fatalf("key: %+v", info)
		}
		kinds := map[string]db.Kind{}
		for _, c := range info.Columns {
			kinds[c.Name] = c.Kind
		}
		// nested documents are dotted fields
		if kinds["name"] != db.KindText || kinds["age"] != db.KindInt || kinds["tags"] != db.KindArray || kinds["address.city"] != db.KindText {
			t.Errorf("fields %+v", info.Columns)
		}
		if c, _ := info.Column("age"); !c.Nullable || c.Comment == "" {
			t.Errorf("field missing in some documents: %+v", c)
		}
		if info.RowEstimate != 3 {
			t.Errorf("estimate %d", info.RowEstimate)
		}
		if !slices.ContainsFunc(info.Indexes, func(ix db.Index) bool { return ix.Unique && slices.Equal(ix.Columns, []string{"email"}) }) {
			t.Errorf("unique index: %+v", info.Indexes)
		}
		ord, err := cat.Table(ctx, ref("orders"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(ord.Indexes, func(ix db.Index) bool {
			return ix.Name == "orders_status_idx" && slices.Equal(ix.Columns, []string{"status", "customer DESC"})
		}) {
			t.Errorf("compound index: %+v", ord.Indexes)
		}
		if c, _ := ord.Column("_id"); c.Kind != db.KindObjectID {
			t.Errorf("ObjectId key: %+v", c)
		}
		if !tg.NoViews {
			v, err := cat.Table(ctx, db.ObjectRef{Schema: tg.Schema, Name: "customer_names", Type: db.ObjView})
			if err != nil {
				t.Fatal(err)
			}
			if v.Ref.Type != db.ObjView || len(v.PrimaryKey) != 0 || len(v.Columns) != 2 {
				t.Errorf("view: %+v", v)
			}
		}
	})

	t.Run("DDLRoundTrip", func(t *testing.T) {
		r := ref("ddl_rt")
		before, err := cat.Table(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := cat.DDL(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "createCollection") || !strings.Contains(ddl, "ddl_rt_k") {
			t.Fatalf("ddl:\n%s", ddl)
		}
		if err := Load(ctx, conn, "db.ddl_rt.drop()"); err != nil {
			t.Fatal(err)
		}
		if err := Load(ctx, conn, ddl); err != nil {
			t.Fatalf("re-running DDL: %v\n%s", err, ddl)
		}
		after, err := cat.Table(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := describeIndexes(before), describeIndexes(after); a != b {
			t.Errorf("indexes changed:\n%s\n%s\nDDL:\n%s", a, b, ddl)
		}
		if !tg.NoViews {
			vd, err := cat.DDL(ctx, db.ObjectRef{Schema: tg.Schema, Name: "customer_names", Type: db.ObjView})
			if err != nil || !strings.Contains(vd, `db.createView("customer_names", "customers"`) {
				t.Errorf("view DDL %q %v", vd, err)
			}
		}
	})

	t.Run("Completion", func(t *testing.T) {
		m, err := cat.Completion(ctx, db.ObjectRef{Schema: tg.Schema})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(m["customers"], "email") || !slices.Contains(m["customers"], "address.city") {
			t.Errorf("completion: %v", m["customers"])
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		s := session(t, conn)
		q, err := lang.Browse(ref("big"), db.BrowseQuery{Sort: []db.SortKey{{Field: "_id", Desc: true}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Execute(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		rows, more, err := res.Fetch(ctx, 1000)
		if err != nil || len(rows) != 1000 || !more {
			t.Fatalf("first page: %d rows, more=%v, %v", len(rows), more, err)
		}
		if rows[0][0] != int64(tg.BigRows-1) {
			t.Fatalf("sort: first id %v", rows[0][0])
		}
		total := len(rows)
		for more {
			rows, more, err = res.Fetch(ctx, 700)
			if err != nil {
				t.Fatal(err)
			}
			total += len(rows)
		}
		if total != tg.BigRows {
			t.Fatalf("streamed %d documents, want %d", total, tg.BigRows)
		}
		if v := count(t, s, "customers", ""); v != "3" {
			t.Fatalf("count %s", v)
		}
	})

	t.Run("Filters", func(t *testing.T) {
		s := session(t, conn)
		if v := count(t, s, "customers", "{age: {$gt: 40}}"); v != "1" {
			t.Errorf("count filter: %s", v)
		}
		f := lang.EqualsFilter("name", "a;b", db.KindText)
		q, err := lang.Browse(ref("customers"), db.BrowseQuery{Filter: f})
		if err != nil {
			t.Fatal(err)
		}
		_, rows, err := Query(ctx, s, q)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %d rows %v", q, len(rows), err)
		}
		f = lang.EqualsFilter("_id", "65f1c0a2e4b0a1b2c3d4e5f7", db.KindObjectID)
		if v := count(t, s, "orders", f); v != "1" {
			t.Errorf("ObjectId filter %s: %s", f, v)
		}
		for _, bad := range []string{"age > 1", "{age: 1}; db.customers.drop()"} {
			if lang.ValidateFilter(bad) == nil {
				t.Errorf("filter %q accepted", bad)
			}
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		if tg.Sleep == "" || !caps.Cancel {
			t.Skip("no sleep statement")
		}
		s := session(t, conn)
		done := make(chan error, 1)
		go func() {
			res, err := s.Execute(ctx, tg.Sleep)
			if err == nil {
				_, _, err = res.Fetch(ctx, 10)
				res.Close()
			}
			done <- err
		}()
		time.Sleep(700 * time.Millisecond)
		s.Cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled statement reported success")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("cancel didn't stop the statement")
		}
		if v := count(t, s, "customers", ""); v != "3" {
			t.Fatal("session unusable after cancel")
		}
	})

	t.Run("Transactions", func(t *testing.T) {
		if !caps.Transactions {
			t.Skip("no transactions")
		}
		s, other := session(t, conn), session(t, conn)
		if err := s.Begin(ctx); err != nil {
			t.Fatal(err)
		}
		exec(t, s, `db.customers.insertOne({_id: 9001, name: "tx", email: "tx@example.com"})`)
		if v := count(t, s, "customers", ""); v != "4" {
			t.Fatalf("inside the transaction: %s", v)
		}
		if v := count(t, other, "customers", ""); v != "3" {
			t.Fatalf("uncommitted insert visible: %s", v)
		}
		if err := s.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", ""); v != "3" || s.TxActive() {
			t.Fatalf("after rollback: %s", v)
		}
		if err := s.Begin(ctx); err != nil {
			t.Fatal(err)
		}
		exec(t, s, `db.customers.insertOne({_id: 9002, name: "tx2", email: "tx2@example.com"})`)
		if err := s.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if v := count(t, other, "customers", ""); v != "4" {
			t.Fatalf("after commit: %s", v)
		}
		exec(t, s, `db.customers.deleteOne({_id: 9002})`)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		s := session(t, conn)
		if err := s.SetReadOnly(ctx, true); err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{`db.customers.insertOne({_id: 9003})`, `db.customers.drop()`,
			`db.runCommand({delete: "customers", deletes: [{q: {}, limit: 0}]})`, `db.customers.aggregate([{$out: "x"}])`} {
			if res, err := s.Execute(ctx, w); err == nil {
				res.Close()
				t.Errorf("read-only session ran %s", w)
			}
		}
		if _, err := s.(db.ChangeApplier).ApplyChanges(ctx, ref("customers"), []db.Change{{Op: db.OpDelete, Key: []db.ColValue{{Column: "_id", Kind: db.KindInt, Value: int64(1)}}}}); err == nil {
			t.Error("read-only session applied a change")
		}
		if v := count(t, s, "customers", ""); v != "3" {
			t.Fatalf("reads: %s", v)
		}
		if err := s.SetReadOnly(ctx, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Schema", func(t *testing.T) {
		s := session(t, conn)
		exec(t, s, "use admin")
		if cur, _ := s.CurrentSchema(ctx); cur != "admin" {
			t.Fatalf("use: %q", cur)
		}
		if err := s.SetCurrentSchema(ctx, tg.Schema); err != nil {
			t.Fatal(err)
		}
		// unqualified collections resolve in the current database
		q, _ := lang.Count(db.ObjectRef{Name: "customers"}, "")
		if v := scalar(t, s, q); v != "3" {
			t.Fatalf("unqualified count %s", v)
		}
	})

	t.Run("Errors", func(t *testing.T) {
		s := session(t, conn)
		// not a statement of the shell syntax: run as JavaScript, which
		// reports the line and column
		_, err := s.Execute(ctx, "db.customers.find({name: })")
		e := db.AsError(err)
		if err == nil || !strings.HasPrefix(e.Msg, "SyntaxError: Unexpected token") || e.Line != 1 || e.Column != len("db.customers.find({name: ")+1 {
			t.Fatalf("syntax error: %+v", e)
		}
		_, err = s.Execute(ctx, "db.customers.find({name: {$nosuchop: 1}})")
		if err == nil || db.AsError(err).Msg == "" {
			t.Fatalf("server error: %v", err)
		}
		if _, err := s.Execute(ctx, "db.customers.insertOne({_id: 1})"); err == nil {
			t.Fatal("duplicate key accepted")
		}
		if v := count(t, s, "customers", ""); v != "3" {
			t.Fatal("session unusable after errors")
		}
	})

	t.Run("ValueTypes", func(t *testing.T) {
		s := session(t, conn)
		q, _ := lang.Browse(ref("all_types"), db.BrowseQuery{})
		cols, rows, err := Query(ctx, s, q)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v %d", err, len(rows))
		}
		want := map[string]db.Kind{"c_int": db.KindInt, "c_long": db.KindInt, "c_double": db.KindFloat, "c_dec": db.KindDecimal,
			"c_bool": db.KindBool, "c_date": db.KindTimestampTZ, "c_oid": db.KindObjectID, "c_str": db.KindText, "c_bin": db.KindBytes,
			"c_uuid": db.KindUUID, "c_obj.b": db.KindText, "c_obj.n": db.KindInt, "c_obj.a": db.KindArray, "c_arr": db.KindArray}
		vals := map[string]any{}
		for i, c := range cols {
			if k, ok := want[c.Name]; ok && c.Kind != k {
				t.Errorf("%s: kind %v, want %v", c.Name, c.Kind, k)
			}
			v := rows[0][i]
			vals[c.Name] = v
			switch v.(type) {
			case nil, string, int64, uint64, float64, bool, time.Time, []byte, []any, map[string]any:
			default:
				t.Errorf("%s: unnormalized %T", c.Name, v)
			}
			_ = db.CellText(v, c.Kind)
		}
		if vals["c_long"] != int64(9007199254740993) || vals["c_dec"] != "123.4500" || vals["c_oid"] != "507f1f77bcf86cd799439011" ||
			vals["c_uuid"] != "8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b" || vals["c_str"] != "héllo" {
			t.Errorf("values %v", vals)
		}
		if d, ok := vals["c_date"].(time.Time); !ok || !d.Equal(time.Date(2024, 2, 29, 13, 14, 15, 123e6, time.UTC)) {
			t.Errorf("date %v", vals["c_date"])
		}
	})

	t.Run("RowEdit", func(t *testing.T) {
		s := session(t, conn)
		r := ref("customers")
		key := []db.ColValue{{Column: "_id", Kind: db.KindInt, Value: int64(9100)}}
		apply := func(chs ...db.Change) error {
			_, err := db.Apply(ctx, s, conn, r, chs)
			return err
		}
		if err := apply(db.Change{Op: db.OpInsert, Values: []db.ColValue{key[0],
			{Column: "name", Kind: db.KindText, Value: "edit"}, {Column: "email", Kind: db.KindText, Value: "edit@example.com"}, {Column: "age", Kind: db.KindInt, Value: nil}}}); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", `{_id: 9100, age: {$exists: false}}`); v != "1" {
			t.Fatalf("insert (an empty cell is a missing field): %s", v)
		}
		upd := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: "name", Kind: db.KindText, Value: "edited"}, {Column: "email", Kind: db.KindText, Value: nil}}}
		if err := apply(upd); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", `{_id: 9100, name: "edited", email: null}`); v != "1" {
			t.Fatalf("update: %s", v)
		}
		// a batch failing on its second change saves nothing (transactions)
		bad := db.Change{Op: db.OpUpdate, Key: []db.ColValue{{Column: "_id", Kind: db.KindInt, Value: int64(424242)}}, Values: upd.Values}
		first := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: "name", Kind: db.KindText, Value: "partial"}}}
		applied, err := db.Apply(ctx, s, conn, r, []db.Change{first, bad})
		if err == nil {
			t.Fatal("update of a missing document succeeded")
		}
		want := "1"
		if !caps.Transactions {
			want = "0"
			if len(applied) != 1 {
				t.Errorf("without transactions the applied changes are reported: %v", applied)
			}
		}
		if v := count(t, s, "customers", `{_id: 9100, name: "edited"}`); v != want {
			t.Errorf("after the failed batch: %s, want %s", v, want)
		}
		if err := apply(db.Change{Op: db.OpDelete, Key: key}); err != nil {
			t.Fatal(err)
		}
		if err := apply(db.Change{Op: db.OpDelete, Key: key}); err == nil {
			t.Fatal("deleting a missing document succeeded")
		}
		// previews are statements of the language
		if p := lang.RenderChange(r, upd); lang.Classify(p) != db.StmtWrite || !strings.Contains(p, `"edited"`) {
			t.Errorf("preview %q", p)
		}
	})

	t.Run("TypesRoundTrip", func(t *testing.T) {
		s := session(t, conn)
		r := ref("all_types")
		key := []db.ColValue{{Column: "_id", Kind: db.KindInt, Value: int64(1)}}
		set := func(col string, k db.Kind, text string) {
			t.Helper()
			v, err := db.ParseValue(text, k)
			if err != nil {
				t.Fatalf("%s: %v", col, err)
			}
			if _, err := db.Apply(ctx, s, conn, r, []db.Change{{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: col, Kind: k, Value: v}}}}); err != nil {
				t.Fatalf("%s = %s: %v", col, text, err)
			}
		}
		is := func(filter string) {
			t.Helper()
			if v := count(t, s, "all_types", filter); v != "1" {
				t.Errorf("%s: %s", filter, v)
			}
		}
		set("c_int", db.KindInt, "8")
		is(`{c_int: {$eq: 8, $type: "int"}}`)
		set("c_long", db.KindInt, "12")
		is(`{c_long: {$type: "long", $eq: 12}}`)
		set("c_double", db.KindFloat, "2")
		is(`{c_double: {$type: "double", $eq: 2}}`)
		set("c_dec", db.KindDecimal, "0.10")
		is(`{c_dec: NumberDecimal("0.10")}`)
		set("c_date", db.KindTimestampTZ, "2025-01-02 03:04:05.678+00:00")
		is(`{c_date: ISODate("2025-01-02T03:04:05.678Z")}`)
		set("c_oid", db.KindObjectID, "65f1c0a2e4b0a1b2c3d4e5f6")
		is(`{c_oid: ObjectId("65f1c0a2e4b0a1b2c3d4e5f6")}`)
		set("c_uuid", db.KindUUID, "00000000-0000-4000-8000-000000000001")
		is(`{c_uuid: UUID("00000000-0000-4000-8000-000000000001")}`)
		set("c_bin", db.KindBytes, "0x0102")
		is(`{c_bin: BinData(0, "AQI=")}`)
		set("c_obj", db.KindJSON, `{"a": [1, 2, 3], "n": {"$numberLong": "6"}, "when": {"$date": "2024-01-01T00:00:00Z"}}`)
		is(`{"c_obj.n": {$type: "long"}, "c_obj.when": {$type: "date"}, "c_obj.a": {$size: 3}}`)
		set("c_arr", db.KindArray, `[1, {"$oid": "507f1f77bcf86cd799439011"}]`)
		is(`{"c_arr.1": {$type: "objectId"}}`)
		set("c_bool", db.KindBool, "false")
		is(`{c_bool: false}`)
	})

	t.Run("Documents", func(t *testing.T) {
		s := session(t, conn)
		q, _ := lang.Browse(ref("customers"), db.BrowseQuery{Sort: []db.SortKey{{Field: "_id"}}})
		res, err := s.Execute(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		rows, _, err := res.Fetch(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		dr, ok := res.(db.DocumentResult)
		if !ok {
			t.Fatal("no documents")
		}
		docs := dr.Documents()
		if len(docs) != len(rows) || len(rows) != 3 {
			t.Fatalf("%d documents for %d rows", len(docs), len(rows))
		}
		addr, ok := docs[0].Field("address")
		if !ok || !addr.IsObject() || len(addr.Children) != 2 {
			t.Fatalf("nested document %+v", docs[0])
		}
		if note, ok := docs[2].Field("note"); !ok || note.Lit != `"it's; fine"` {
			t.Errorf("field only in document 3: %+v", note)
		}
		dl := lang.(db.DocumentLanguage)
		// editing the text of the document: per-path $set and $unset
		text := dl.FormatDocument(docs[0])
		edited := strings.Replace(text, `"London"`, `"Paris"`, 1)
		edited = strings.Replace(edited, `age: 36,`, `age: 37, since: ISODate("2020-01-01T00:00:00Z"),`, 1)
		edited = strings.Replace(edited, `zip: "N1"`, `zip: "N1", geo: {lat: 48.8}`, 1)
		key, vals, err := dl.DocumentChanges(docs[0], edited)
		if err != nil || len(vals) != 4 {
			t.Fatalf("changes %v %v\n%s", vals, err, edited)
		}
		res.Close()
		// a removed field
		vals = append(vals, db.ColValue{Column: "tags", Value: db.Unset})
		if _, err := db.Apply(ctx, s, conn, ref("customers"), []db.Change{{Op: db.OpUpdate, Key: key, Values: vals}}); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", `{_id: 1, "address.city": "Paris", "address.zip": "N1", "address.geo.lat": 48.8, age: 37,
			since: {$type: "date"}, tags: {$exists: false}}`); v != "1" {
			t.Fatalf("document edit not applied: %s", v)
		}
		// a flattened column is a path
		if _, err := db.Apply(ctx, s, conn, ref("customers"), []db.Change{{Op: db.OpUpdate, Key: key,
			Values: []db.ColValue{{Column: "address.city", Kind: db.KindText, Value: "London"}}}}); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", `{_id: 1, "address.city": "London", "address.zip": "N1"}`); v != "1" {
			t.Fatalf("path edit: %s", v)
		}
		// insert from text and from dotted grid columns
		nd, err := dl.NewDocument(`{_id: 9200, name: "doc", email: "doc@example.com", address: {city: "Rome"}}`)
		if err != nil {
			t.Fatal(err)
		}
		grid := []db.ColValue{{Column: "_id", Kind: db.KindInt, Value: int64(9201)}, {Column: "address.city", Kind: db.KindText, Value: "Oslo"},
			{Column: "email", Kind: db.KindText, Value: "grid@example.com"}}
		if _, err := db.Apply(ctx, s, conn, ref("customers"), []db.Change{{Op: db.OpInsert, Values: nd}, {Op: db.OpInsert, Values: grid}}); err != nil {
			t.Fatal(err)
		}
		if v := count(t, s, "customers", `{_id: {$in: [9200, 9201]}, "address.city": {$in: ["Rome", "Oslo"]}}`); v != "2" {
			t.Fatalf("inserts: %s", v)
		}
		exec(t, s, `db.customers.deleteMany({_id: {$in: [9200, 9201]}})`)
		exec(t, s, `db.customers.updateOne({_id: 1}, {$set: {age: 36, tags: ["a", "b"]}, $unset: {since: "", "address.geo": ""}})`)
		// projection, sort document and limit of the query bar
		q, err = lang.Browse(ref("big"), db.BrowseQuery{Projection: "{label: 1}", Sort: mustSort(t, dl, "{_id: -1}"), Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		cols, rows2, err := Query(ctx, s, q)
		if err != nil || len(rows2) != 5 || len(cols) != 2 || rows2[0][0] != int64(tg.BigRows-1) {
			t.Fatalf("%s: %v %d rows %v", q, cols, len(rows2), err)
		}
	})

	t.Run("Shell", func(t *testing.T) {
		s := session(t, conn)
		rc, ok := lang.(db.RuntimeClassifier)
		if !ok {
			t.Fatal("the language has no dynamic statements")
		}
		run := func(src string) ([]db.Column, []db.Row, []string) {
			t.Helper()
			if !rc.Dynamic(src) {
				t.Fatalf("not dynamic: %s", src)
			}
			res, err := s.Execute(ctx, src)
			if err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			defer res.Close()
			cols := res.Columns()
			var rows []db.Row
			for cols != nil {
				r, more, err := res.Fetch(ctx, 700)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, r...)
				if !more {
					break
				}
			}
			return cols, rows, res.Messages()
		}
		// variables survive between statements; loops write documents
		run(`let base = 9300`)
		_, _, msgs := run(`for (let i = 0; i < 5; i++) { db.shell_t.insertOne({_id: base + i, sq: i * i, at: new Date()}) }
print("inserted", db.shell_t.countDocuments({}))`)
		if len(msgs) != 1 || msgs[0] != "inserted 5" {
			t.Fatalf("print: %v", msgs)
		}
		// cursor iteration in the script
		_, rows, _ := run(`let total = 0; db.shell_t.find({sq: {$gt: 0}}).forEach(d => { total += d.sq }); total`)
		if len(rows) != 1 || rows[0][0] != int64(30) {
			t.Fatalf("forEach: %v", rows)
		}
		_, rows, _ = run(`const c = db.shell_t.find().sort({_id: 1}); const first = c.next(); [first._id, c.hasNext(), db.shell_t.findOne({_id: base + 4}).sq]`)
		if len(rows) != 3 || rows[0][0] != int64(9300) || rows[1][0] != true || rows[2][0] != int64(16) {
			t.Fatalf("next/hasNext/findOne: %v", rows)
		}
		// a cursor as the value streams like a find
		cols, rows, _ := run(`const big = db.big.find({}, {label: 1}).sort({_id: 1}); big`)
		if len(rows) != tg.BigRows || len(cols) != 2 || rows[0][0] != int64(0) {
			t.Fatalf("streamed cursor: %d rows %v", len(rows), cols)
		}
		// write results, getSiblingDB, runCommand
		_, rows, _ = run(`const r = db.shell_t.updateMany({sq: {$lt: 5}}, {$set: {small: true}}); [r.matchedCount, r.modifiedCount,
			db.getSiblingDB("admin").runCommand({ping: 1}).ok, db.getName()]`)
		if len(rows) != 4 || rows[0][0] != int64(3) || rows[1][0] != int64(3) || rows[3][0] != tg.Schema {
			t.Fatalf("results: %v", rows)
		}
		_, rows, _ = run(`db.shell_t.aggregate([{$group: {_id: null, s: {$sum: "$sq"}}}]).toArray()[0].s + NumberLong(1)`)
		if len(rows) != 1 || rows[0][0] != int64(31) {
			t.Fatalf("aggregate: %v", rows)
		}
		// errors: JavaScript and server ones, with their line
		_, err := s.Execute(ctx, "let a = 1\nundefinedFn()")
		if e := db.AsError(err); err == nil || e.Line != 2 {
			t.Fatalf("script error: %+v", e)
		}
		_, err = s.Execute(ctx, `for (const x of [1]) db.shell_t.insertOne({_id: base})`)
		if err == nil || !strings.Contains(db.AsError(err).Msg, "duplicate key") && !strings.Contains(db.AsError(err).Msg, "Duplicate key") {
			t.Fatalf("server error: %v", err)
		}
		// the guard sees every write with its actual arguments
		var seen []string
		s.(db.Guarded).SetGuard(func(_ context.Context, k db.StmtKind, desc string) error {
			seen = append(seen, desc)
			if k == db.StmtDestructive {
				return errors.New("refused by the test guard")
			}
			return nil
		})
		run(`for (const id of [base, base + 1]) db.shell_t.updateOne({_id: id}, {$set: {seen: true}})`)
		if _, err := s.Execute(ctx, `if (true) db.shell_t.deleteMany({})`); err == nil || !strings.Contains(err.Error(), "refused by the test guard") {
			t.Fatalf("guard: %v", err)
		}
		if len(seen) != 3 || seen[0] != `db.shell_t.updateOne({_id: 9300}, {$set: {seen: true}})` || seen[2] != "db.shell_t.deleteMany({})" {
			t.Fatalf("guard calls: %q", seen)
		}
		s.(db.Guarded).SetGuard(nil)
		// a read-only session refuses writes of scripts, reads go on
		if err := s.SetReadOnly(ctx, true); err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{`let n = 1; db.shell_t.insertOne({_id: n})`, `const c2 = db.getCollection("shell_t"); c2.drop()`,
			`db.shell_t.aggregate([{$out: "x"}]).toArray()`, `db.runCommand({drop: "shell_t"})`, `db.shell_t.aggregate([{$merge: {into: "y"}}])`} {
			if res, err := s.Execute(ctx, w); err == nil {
				res.Close()
				t.Errorf("read-only session ran %s", w)
			} else if !strings.Contains(err.Error(), "read-only") {
				t.Errorf("%s: %v", w, err)
			}
		}
		if _, rows, _ := run(`let k = db.shell_t.countDocuments({}); k`); rows[0][0] != int64(5) {
			t.Fatalf("read-only reads: %v", rows)
		}
		if err := s.SetReadOnly(ctx, false); err != nil {
			t.Fatal(err)
		}
		// cancel stops loops and server operations
		done := make(chan error, 1)
		go func() {
			res, err := s.Execute(ctx, "let spins = 0; while (true) { spins++ }")
			if err == nil {
				res.Close()
			}
			done <- err
		}()
		time.Sleep(300 * time.Millisecond)
		s.Cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled loop reported success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("cancel didn't stop the loop")
		}
		// transactions span statements of scripts
		if caps.Transactions {
			if err := s.Begin(ctx); err != nil {
				t.Fatal(err)
			}
			run(`for (let i = 0; i < 3; i++) db.shell_t.insertOne({_id: 9400 + i})`)
			if err := s.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if _, rows, _ := run(`let after = db.shell_t.countDocuments({_id: {$gte: 9400}}); after`); rows[0][0] != int64(0) {
				t.Fatalf("rolled back script writes: %v", rows)
			}
		}
		run(`let cleanup = db.shell_t.drop(); cleanup`)
	})

	t.Run("Script", func(t *testing.T) {
		sc := "// leading comment\ndb.customers.countDocuments({});\n/* block; comment */\ndb.customers.find({name: 'a;b'})\n  .limit(1)\nshow collections"
		stmts := lang.Split(sc)
		if len(stmts) != 3 {
			t.Fatalf("split into %d statements: %+v", len(stmts), stmts)
		}
		s := session(t, conn)
		for _, st := range stmts {
			_, rows, err := Query(ctx, s, st.Text)
			if err != nil || len(rows) == 0 {
				t.Errorf("%s: %d rows %v", st.Text, len(rows), err)
			}
		}
		if lang.Classify(stmts[1].Text) != db.StmtRead {
			t.Error("classification")
		}
		res, err := s.Execute(ctx, `db.customers.updateOne({_id: 3}, {$set: {seen: true}})`)
		if err != nil {
			t.Fatal(err)
		}
		if res.Columns() != nil || res.RowsAffected() != 1 || len(res.Messages()) == 0 {
			t.Errorf("write result: %v %d %v", res.Columns(), res.RowsAffected(), res.Messages())
		}
		res.Close()
	})

	t.Run("Explain", func(t *testing.T) {
		q, err := lang.Explain(`db.customers.find({age: {$gt: 1}})`)
		if err != nil {
			t.Fatal(err)
		}
		s := session(t, conn)
		_, rows, err := Query(ctx, s, q)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v", q, err)
		}
		if _, err := lang.Explain("db.customers.drop()"); !errors.Is(err, db.ErrNotSupported) {
			t.Errorf("explain of a drop: %v", err)
		}
	})
}

func describeIndexes(t *db.TableInfo) string {
	var b strings.Builder
	for _, ix := range t.Indexes {
		b.WriteString(ix.Name + " " + strings.Join(ix.Columns, ",") + " " + boolStr(ix.Unique) + "\n")
	}
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "unique"
	}
	return ""
}

func mustSort(t *testing.T, dl db.DocumentLanguage, s string) []db.SortKey {
	t.Helper()
	k, err := dl.ParseSort(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
