package mongo

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

// Values: a top-level field becomes a grid cell in the Row value set
// (ObjectId → hex string, Date → time.Time, Decimal128 → decimal string,
// UUID → string, other binary → []byte). Nested documents and arrays are
// Extended JSON text in document order, special values as wrappers
// ({"$oid": …}, {"$date": …}, {"$numberLong": …}…), so editing them as
// JSON keeps their types and the order of the fields.

// typeName is the $type name of a BSON value.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bson.ObjectID:
		return "objectId"
	case string:
		return "string"
	case int32:
		return "int"
	case int64:
		return "long"
	case float64:
		return "double"
	case bson.Decimal128:
		return "decimal"
	case bool:
		return "bool"
	case bson.DateTime:
		return "date"
	case bson.Binary:
		return "binData"
	case bson.D, bson.M:
		return "object"
	case bson.A:
		return "array"
	case bson.Timestamp:
		return "timestamp"
	case bson.Regex:
		return "regex"
	case bson.JavaScript, bson.CodeWithScope:
		return "javascript"
	case bson.MinKey:
		return "minKey"
	case bson.MaxKey:
		return "maxKey"
	case bson.Undefined:
		return "undefined"
	case bson.Symbol:
		return "symbol"
	case bson.DBPointer:
		return "dbPointer"
	}
	return fmt.Sprintf("%T", v)
}

// kindOf maps a BSON value to a grid kind.
func kindOf(v any) db.Kind {
	switch x := v.(type) {
	case bson.ObjectID:
		return db.KindObjectID
	case string, bson.Symbol:
		return db.KindText
	case int32, int64:
		return db.KindInt
	case float64:
		return db.KindFloat
	case bson.Decimal128:
		return db.KindDecimal
	case bool:
		return db.KindBool
	case bson.DateTime:
		return db.KindTimestampTZ
	case bson.Binary:
		if x.Subtype == 4 || x.Subtype == 3 {
			return db.KindUUID
		}
		return db.KindBytes
	case bson.D, bson.M:
		return db.KindJSON
	case bson.A:
		return db.KindArray
	case nil, bson.Undefined:
		return db.KindOther
	}
	return db.KindJSON // timestamps, regexes… as Extended JSON wrappers
}

// cell normalizes a top-level value.
func cell(v any) any {
	switch x := v.(type) {
	case nil, bson.Undefined:
		return nil
	case bson.ObjectID:
		return x.Hex()
	case string:
		return x
	case bson.Symbol:
		return string(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return x
	case bool:
		return x
	case bson.Decimal128:
		return x.String()
	case bson.DateTime:
		return x.Time().UTC()
	case bson.Binary:
		if (x.Subtype == 4 || x.Subtype == 3) && len(x.Data) == 16 {
			return uuidString(x.Data)
		}
		return append([]byte(nil), x.Data...)
	case bson.D, bson.A:
		// Extended JSON text in document order (a map would lose it)
		return ejsonText(x)
	case bson.M:
		return ejsonText(x)
	}
	return ejsonText(v)
}

// ejsonText writes a value as relaxed Extended JSON, keys in document
// order: special values are wrappers ({"$oid": …}, {"$numberLong": …}…)
// that toBSON reads back with their type.
func ejsonText(v any) string {
	var b strings.Builder
	writeEJSON(&b, v)
	return b.String()
}

func writeEJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case bson.D:
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSONString(b, e.Key)
			b.WriteString(": ")
			writeEJSON(b, e.Value)
		}
		b.WriteByte('}')
	case bson.A:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeEJSON(b, e)
		}
		b.WriteByte(']')
	case bson.M:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := make(bson.D, len(keys))
		for i, k := range keys {
			d[i] = bson.E{Key: k, Value: x[k]}
		}
		writeEJSON(b, d)
	case nil:
		b.WriteString("null")
	case string:
		writeJSONString(b, x)
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString(`{"$numberDouble": "` + strconv.FormatFloat(x, 'g', -1, 64) + `"}`)
			return
		}
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0" // stays a double when read back
		}
		b.WriteString(s)
	default:
		j, err := json.Marshal(nested(v))
		if err != nil {
			writeJSONString(b, fmt.Sprint(v))
			return
		}
		b.Write(j)
	}
}

func writeJSONString(b *strings.Builder, s string) {
	j, _ := json.Marshal(s)
	b.Write(j)
}

// nested normalizes a value inside a document or array.
func nested(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string, bool, float64:
		return x
	case int32:
		return int64(x)
	case int64:
		return map[string]any{"$numberLong": strconv.FormatInt(x, 10)}
	case bson.ObjectID:
		return map[string]any{"$oid": x.Hex()}
	case bson.Decimal128:
		return map[string]any{"$numberDecimal": x.String()}
	case bson.DateTime:
		return map[string]any{"$date": x.Time().UTC().Format("2006-01-02T15:04:05.000Z")}
	case bson.Binary:
		if x.Subtype == 4 && len(x.Data) == 16 {
			return map[string]any{"$uuid": uuidString(x.Data)}
		}
		return map[string]any{"$binary": map[string]any{"base64": base64.StdEncoding.EncodeToString(x.Data), "subType": fmt.Sprintf("%02x", x.Subtype)}}
	case bson.D, bson.M, bson.A:
		return cell(x)
	case bson.Timestamp:
		return map[string]any{"$timestamp": map[string]any{"t": int64(x.T), "i": int64(x.I)}}
	case bson.Regex:
		return map[string]any{"$regularExpression": map[string]any{"pattern": x.Pattern, "options": x.Options}}
	case bson.JavaScript:
		return map[string]any{"$code": string(x)}
	case bson.CodeWithScope:
		return map[string]any{"$code": string(x.Code)}
	case bson.Symbol:
		return map[string]any{"$symbol": string(x)}
	case bson.MinKey:
		return map[string]any{"$minKey": int64(1)}
	case bson.MaxKey:
		return map[string]any{"$maxKey": int64(1)}
	case bson.Undefined:
		return map[string]any{"$undefined": true}
	case bson.DBPointer:
		return map[string]any{"$dbPointer": map[string]any{"$ref": x.DB, "$id": map[string]any{"$oid": x.Pointer.Hex()}}}
	}
	return fmt.Sprint(v)
}

func uuidString(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// ---- columns ----

// fieldStats accumulates the types of a field over sampled documents.
type fieldStats struct {
	name    string
	types   map[string]int
	present int
	nulls   int
	first   any
}

// maxFlatten is how many levels of nested documents become columns.
const maxFlatten = 3

// fieldNode is a field path seen in documents, with its sub-fields in
// order of first appearance.
type fieldNode struct {
	stats    *fieldStats
	children []*fieldNode
	byName   map[string]*fieldNode
	objects  int  // values that are non-empty documents with plain keys
	opaque   bool // some value can't be split into columns
}

func (n *fieldNode) child(name, path string) *fieldNode {
	if c := n.byName[name]; c != nil {
		return c
	}
	c := &fieldNode{stats: &fieldStats{name: path, types: map[string]int{}}, byName: map[string]*fieldNode{}}
	n.byName[name] = c
	n.children = append(n.children, c)
	return c
}

// plainKeys reports whether every key of d can be part of a dotted path.
func plainKeys(d bson.D) bool {
	for _, e := range d {
		if e.Key == "" || strings.Contains(e.Key, ".") || strings.HasPrefix(e.Key, "$") {
			return false
		}
	}
	return true
}

// add accumulates the fields of d under the path parent ("" = top level).
func (n *fieldNode) add(parent string, d bson.D, depth int) {
	for _, e := range d {
		path := db.JoinFieldPath(parent, e.Key)
		c := n.child(e.Key, path)
		fs := c.stats
		fs.present++
		if e.Value == nil {
			fs.nulls++
			continue
		}
		if fs.first == nil {
			fs.first = e.Value
		}
		fs.types[typeName(e.Value)]++
		// the key stays whole (rows are identified by it)
		isKey := parent == "" && e.Key == "_id"
		if sub, ok := e.Value.(bson.D); ok && len(sub) > 0 && depth < maxFlatten && !isKey {
			c.objects++
			c.add(path, sub, depth+1)
		} else {
			c.opaque = true
		}
	}
}

// extendColumns adds the columns of the fields of d that cols lack
// (documents read after the first ones): new nested documents become
// dotted columns like in inferColumns.
func extendColumns(cols []db.Column, d bson.D) []db.Column {
	have := make(map[string]bool, len(cols))
	for _, c := range cols {
		have[c.Name] = true
	}
	var walk func(parent string, d bson.D, depth int)
	walk = func(parent string, d bson.D, depth int) {
		for _, e := range d {
			p := db.JoinFieldPath(parent, e.Key)
			if have[p] {
				continue // shown whole (JSON, mixed types)
			}
			sub, isDoc := e.Value.(bson.D)
			isKey := parent == "" && e.Key == "_id"
			if isDoc && len(sub) > 0 && depth < maxFlatten && !isKey {
				walk(p, sub, depth+1)
				continue
			}
			k := kindOf(e.Value)
			cols = append(cols, db.Column{Name: p, Kind: k, DBType: strings.ToUpper(typeName(e.Value)), Nullable: true})
			have[p] = true
		}
	}
	walk("", d, 0)
	return cols
}

// flat reports whether a field is shown as the columns of its sub-fields:
// all its values are documents (or null).
func (n *fieldNode) flat() bool { return n.objects > 0 && !n.opaque }

func (n *fieldNode) collect(out *[]*fieldStats) {
	for _, c := range n.children {
		if c.flat() {
			c.collect(out)
		} else {
			*out = append(*out, c.stats)
		}
	}
}

// inferColumns derives result columns from documents: fields in order of
// first appearance (_id first), nested documents as dotted columns
// ("address.city") when every value of the field is a document; the kind
// is the common kind of the values.
func inferColumns(docs []bson.D) ([]db.Column, []*fieldStats) {
	root := &fieldNode{byName: map[string]*fieldNode{}}
	for _, d := range docs {
		root.add("", d, 0)
	}
	var order []*fieldStats
	root.collect(&order)
	if i := slices.IndexFunc(order, func(f *fieldStats) bool { return f.name == "_id" }); i > 0 {
		id := order[i]
		order = append(order[:i], order[i+1:]...)
		order = append([]*fieldStats{id}, order...)
	}
	cols := make([]db.Column, len(order))
	for i, fs := range order {
		cols[i] = db.Column{Name: fs.name, Kind: fs.kind(), DBType: strings.ToUpper(fs.typeLabel()),
			Nullable: fs.nulls > 0 || fs.present < len(docs)}
	}
	return cols, order
}

// lookup returns the value of a column in a document: a top-level field
// named like the column, else the dotted path.
func lookup(d bson.D, path string) (any, bool) {
	cur := d
	parts := db.SplitFieldPath(path)
	for i, p := range parts {
		v, ok := field(cur, p)
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		sub, ok := v.(bson.D)
		if !ok {
			return nil, false
		}
		cur = sub
	}
	return nil, false
}

// setPath sets a dotted path in a document, creating the parents.
func setPath(d bson.D, path string, v any) bson.D {
	return setSegs(d, db.SplitFieldPath(path), v)
}

// setSegs sets the field of a list of names.
func setSegs(d bson.D, segs []string, v any) bson.D {
	if len(segs) == 0 {
		return d
	}
	head, rest := segs[0], segs[1:]
	for i, e := range d {
		if e.Key == head {
			if len(rest) > 0 {
				sub, _ := e.Value.(bson.D)
				d[i].Value = setSegs(sub, rest, v)
			} else {
				d[i].Value = v
			}
			return d
		}
	}
	if len(rest) > 0 {
		return append(d, bson.E{Key: head, Value: setSegs(bson.D{}, rest, v)})
	}
	return append(d, bson.E{Key: head, Value: v})
}

// docNode is the document tree of a value.
func docNode(name string, v any) db.DocNode {
	n := db.DocNode{Name: name, Type: typeName(v), Kind: kindOf(v)}
	switch x := v.(type) {
	case bson.D:
		n.Kind = db.KindJSON
		for _, e := range x {
			n.Children = append(n.Children, docNode(e.Key, e.Value))
		}
		n.Children = nonNil(n.Children)
		return n
	case bson.A:
		for i, e := range x {
			n.Children = append(n.Children, docNode(strconv.Itoa(i), e))
		}
		n.Children = nonNil(n.Children)
		return n
	}
	n.Value, n.Lit = cell(v), shellLit(v)
	return n
}

func nonNil(c []db.DocNode) []db.DocNode {
	if c == nil {
		return []db.DocNode{}
	}
	return c
}

// kind is the grid kind of a field: the kind of its values, numbers of
// mixed types as the widest one, KindOther when the types differ.
func (fs *fieldStats) kind() db.Kind {
	switch len(fs.types) {
	case 0:
		return db.KindOther
	case 1:
		return kindOf(fs.first)
	}
	onlyNum, hasDouble := true, false
	for t := range fs.types {
		switch t {
		case "int", "long":
		case "double":
			hasDouble = true
		default:
			onlyNum = false
		}
	}
	switch {
	case onlyNum && hasDouble:
		return db.KindFloat
	case onlyNum:
		return db.KindInt
	}
	return db.KindOther
}

// typeLabel names the types of a field ("string", "int|long").
func (fs *fieldStats) typeLabel() string {
	if len(fs.types) == 0 {
		return "null"
	}
	names := make([]string, 0, len(fs.types))
	for t := range fs.types {
		names = append(names, t)
	}
	// most frequent first
	sort.Slice(names, func(i, j int) bool {
		if fs.types[names[i]] != fs.types[names[j]] {
			return fs.types[names[i]] > fs.types[names[j]]
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

// row builds the grid row of a document for cols.
func row(d bson.D, cols []db.Column) db.Row {
	r := make(db.Row, len(cols))
	for i, c := range cols {
		if v, ok := lookup(d, c.Name); ok {
			r[i] = cell(v)
		}
	}
	return r
}

// ---- shell literals ----

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// key renders a document key.
func key(k string) string {
	if identRe.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
}

// shellLit renders a BSON value as a shell literal (parseValue reads it
// back with the same type).
func shellLit(v any) string {
	var b strings.Builder
	writeLit(&b, v)
	return b.String()
}

func writeLit(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		b.WriteString(strconv.Quote(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(`NumberLong("` + strconv.FormatInt(x, 10) + `")`)
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		case x == math.Trunc(x) && math.Abs(x) < 1e15:
			// an integral double must not read back as an integer
			b.WriteString("Double(" + strconv.FormatFloat(x, 'f', -1, 64) + ")")
		default:
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case bson.ObjectID:
		b.WriteString(`ObjectId("` + x.Hex() + `")`)
	case bson.Decimal128:
		b.WriteString(`NumberDecimal("` + x.String() + `")`)
	case bson.DateTime:
		b.WriteString(`ISODate("` + x.Time().UTC().Format("2006-01-02T15:04:05.000Z") + `")`)
	case bson.Binary:
		if x.Subtype == 4 && len(x.Data) == 16 {
			b.WriteString(`UUID("` + uuidString(x.Data) + `")`)
		} else {
			fmt.Fprintf(b, `BinData(%d, "%s")`, x.Subtype, base64.StdEncoding.EncodeToString(x.Data))
		}
	case bson.Timestamp:
		fmt.Fprintf(b, "Timestamp(%d, %d)", x.T, x.I)
	case bson.Regex:
		fmt.Fprintf(b, "RegExp(%s, %s)", strconv.Quote(x.Pattern), strconv.Quote(x.Options))
	case bson.JavaScript:
		b.WriteString("Code(" + strconv.Quote(string(x)) + ")")
	case bson.Symbol:
		b.WriteString(strconv.Quote(string(x)))
	case bson.MinKey:
		b.WriteString("MinKey()")
	case bson.MaxKey:
		b.WriteString("MaxKey()")
	case bson.Undefined:
		b.WriteString("undefined")
	case bson.D:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(key(e.Key) + ": ")
			writeLit(b, e.Value)
		}
		b.WriteString("}")
	case bson.A:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeLit(b, e)
		}
		b.WriteString("]")
	default:
		b.WriteString(strconv.Quote(fmt.Sprint(v)))
	}
}

// ---- grid values to BSON ----

// toBSON converts a grid value of kind k to BSON. like, when not nil, is
// the value currently stored (its numeric/date type is kept).
func toBSON(v any, k db.Kind, like any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if r, ok := v.(db.Raw); ok {
		return parseValue(string(r))
	}
	switch k {
	case db.KindObjectID:
		if s, ok := v.(string); ok {
			id, err := bson.ObjectIDFromHex(s)
			if err != nil {
				return nil, fmt.Errorf("%q is not an ObjectId", s)
			}
			return id, nil
		}
	case db.KindDecimal:
		if s, ok := v.(string); ok {
			if _, isDouble := like.(float64); isDouble {
				return strconv.ParseFloat(s, 64)
			}
			d, err := bson.ParseDecimal128(s)
			if err != nil {
				return nil, fmt.Errorf("%q is not a decimal", s)
			}
			return d, nil
		}
	case db.KindUUID:
		if s, ok := v.(string); ok {
			raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
			if err != nil || len(raw) != 16 {
				return nil, fmt.Errorf("%q is not a UUID", s)
			}
			sub := byte(4)
			if lb, ok := like.(bson.Binary); ok {
				sub = lb.Subtype
			}
			return bson.Binary{Subtype: sub, Data: raw}, nil
		}
	case db.KindJSON, db.KindArray:
		if s, ok := v.(string); ok {
			pv, err := parseValue(s)
			if err != nil {
				return nil, err
			}
			return fromEJSON(pv)
		}
	case db.KindOther:
		// mixed fields: text is read as a shell literal when it is one
		if s, ok := v.(string); ok {
			if pv, err := parseValue(s); err == nil {
				return fromEJSON(pv)
			}
			return s, nil
		}
	}
	switch x := v.(type) {
	case string:
		if k == db.KindInt {
			// too big for 64 bits
			if _, ok := new(big.Int).SetString(x, 10); ok {
				return bson.ParseDecimal128(x)
			}
		}
		return x, nil
	case int64:
		switch like.(type) {
		case int64:
			return x, nil
		case float64:
			return float64(x), nil
		case bson.Decimal128:
			return bson.ParseDecimal128(strconv.FormatInt(x, 10))
		}
		return intValue(x), nil
	case uint64:
		if x <= math.MaxInt64 {
			return toBSON(int64(x), k, like)
		}
		return bson.ParseDecimal128(strconv.FormatUint(x, 10))
	case float64:
		if _, ok := like.(bson.Decimal128); ok {
			return bson.ParseDecimal128(strconv.FormatFloat(x, 'g', -1, 64))
		}
		return x, nil
	case bool:
		return x, nil
	case time.Time:
		return bson.NewDateTimeFromTime(x), nil
	case []byte:
		sub := byte(0)
		if lb, ok := like.(bson.Binary); ok {
			sub = lb.Subtype
		}
		return bson.Binary{Subtype: sub, Data: x}, nil
	case map[string]any, []any:
		return fromNormalized(x)
	}
	return nil, fmt.Errorf("unsupported value %T", v)
}

// fromNormalized converts a normalized nested value (maps and arrays with
// Extended JSON wrappers) back to BSON. Map keys are sorted (Go maps keep
// no order).
func fromNormalized(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := make(bson.D, 0, len(x))
		for _, k := range keys {
			e, err := fromNormalized(x[k])
			if err != nil {
				return nil, err
			}
			d = append(d, bson.E{Key: k, Value: e})
		}
		return fromEJSON(d)
	case []any:
		a := make(bson.A, len(x))
		for i, e := range x {
			n, err := fromNormalized(e)
			if err != nil {
				return nil, err
			}
			a[i] = n
		}
		return a, nil
	case int64:
		return intValue(x), nil
	case uint64:
		return toBSON(x, db.KindInt, nil)
	case time.Time:
		return bson.NewDateTimeFromTime(x), nil
	case []byte:
		return bson.Binary{Data: x}, nil
	}
	return v, nil
}

// fromEJSON replaces Extended JSON wrappers ({"$oid": "…"}…) inside a
// parsed value by the BSON values they describe.
func fromEJSON(v any) (any, error) {
	switch x := v.(type) {
	case bson.D:
		if len(x) >= 1 && strings.HasPrefix(x[0].Key, "$") {
			if w, ok, err := wrapper(x); ok || err != nil {
				return w, err
			}
		}
		out := make(bson.D, len(x))
		for i, e := range x {
			n, err := fromEJSON(e.Value)
			if err != nil {
				return nil, err
			}
			out[i] = bson.E{Key: e.Key, Value: n}
		}
		return out, nil
	case bson.A:
		out := make(bson.A, len(x))
		for i, e := range x {
			n, err := fromEJSON(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	}
	return v, nil
}

func field(d bson.D, k string) (any, bool) {
	for _, e := range d {
		if e.Key == k {
			return e.Value, true
		}
	}
	return nil, false
}

// wrapper decodes one Extended JSON wrapper document.
func wrapper(d bson.D) (any, bool, error) {
	str := func(v any) (string, bool) { s, ok := v.(string); return s, ok }
	k, v := d[0].Key, d[0].Value
	if len(d) == 2 && k == "$regex" {
		// {"$regex": "…", "$options": "…"} is a query operator too: only
		// the canonical $regularExpression form is decoded
		return nil, false, nil
	}
	if len(d) != 1 {
		return nil, false, nil
	}
	bad := func() (any, bool, error) { return nil, true, fmt.Errorf("invalid %s value", k) }
	switch k {
	case "$oid":
		s, _ := str(v)
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return bad()
		}
		return id, true, nil
	case "$numberLong", "$numberInt":
		s, ok := str(v)
		if !ok {
			return bad()
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return bad()
		}
		if k == "$numberInt" {
			return int32(n), true, nil
		}
		return n, true, nil
	case "$numberDouble":
		s, ok := str(v)
		if !ok {
			return bad()
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return bad()
		}
		return f, true, nil
	case "$numberDecimal":
		s, _ := str(v)
		dec, err := bson.ParseDecimal128(s)
		if err != nil {
			return bad()
		}
		return dec, true, nil
	case "$date":
		switch x := v.(type) {
		case string:
			t, err := parseDate(x)
			if err != nil {
				return bad()
			}
			return bson.NewDateTimeFromTime(t), true, nil
		case bson.D:
			if s, ok := field(x, "$numberLong"); ok {
				ss, _ := str(s)
				n, err := strconv.ParseInt(ss, 10, 64)
				if err != nil {
					return bad()
				}
				return bson.DateTime(n), true, nil
			}
		}
		if f, ok := toFloat(v); ok {
			return bson.DateTime(int64(f)), true, nil
		}
		return bad()
	case "$uuid":
		s, _ := str(v)
		raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if err != nil || len(raw) != 16 {
			return bad()
		}
		return bson.Binary{Subtype: 4, Data: raw}, true, nil
	case "$binary":
		x, ok := v.(bson.D)
		if !ok {
			return bad()
		}
		b64, _ := field(x, "base64")
		st, _ := field(x, "subType")
		s, _ := str(b64)
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return bad()
		}
		sub, _ := str(st)
		n, _ := strconv.ParseUint(sub, 16, 8)
		return bson.Binary{Subtype: byte(n), Data: raw}, true, nil
	case "$timestamp":
		x, ok := v.(bson.D)
		if !ok {
			return bad()
		}
		t, _ := field(x, "t")
		i, _ := field(x, "i")
		tf, _ := toFloat(t)
		inc, _ := toFloat(i)
		return bson.Timestamp{T: uint32(tf), I: uint32(inc)}, true, nil
	case "$regularExpression":
		x, ok := v.(bson.D)
		if !ok {
			return bad()
		}
		p, _ := field(x, "pattern")
		o, _ := field(x, "options")
		ps, _ := str(p)
		os, _ := str(o)
		return bson.Regex{Pattern: ps, Options: os}, true, nil
	case "$code":
		s, _ := str(v)
		return bson.JavaScript(s), true, nil
	case "$symbol":
		s, _ := str(v)
		return bson.Symbol(s), true, nil
	case "$minKey":
		return bson.MinKey{}, true, nil
	case "$maxKey":
		return bson.MaxKey{}, true, nil
	case "$undefined":
		return bson.Undefined{}, true, nil
	}
	return nil, false, nil
}
