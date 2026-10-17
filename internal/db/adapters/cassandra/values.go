package cassandra

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/twoojoo/lumino/internal/db"
)

// typeName renders a driver type as CQL (int, map<text, int>,
// frozen<tuple<int, text>>, …). nested: inside a collection, tuple or UDT,
// where non-native types are frozen.
func typeName(ti gocql.TypeInfo, nested bool) string {
	frozen := func(s string) string {
		if nested {
			return "frozen<" + s + ">"
		}
		return s
	}
	switch t := ti.(type) {
	case nil:
		return "unknown"
	case gocql.CollectionType:
		switch t.Type() {
		case gocql.TypeList:
			return frozen("list<" + typeName(t.Elem, true) + ">")
		case gocql.TypeSet:
			return frozen("set<" + typeName(t.Elem, true) + ">")
		case gocql.TypeMap:
			return frozen("map<" + typeName(t.Key, true) + ", " + typeName(t.Elem, true) + ">")
		}
	case gocql.TupleTypeInfo:
		el := make([]string, len(t.Elems))
		for i, e := range t.Elems {
			el[i] = typeName(e, true)
		}
		// tuples are always frozen
		return "frozen<tuple<" + strings.Join(el, ", ") + ">>"
	case gocql.UDTTypeInfo:
		return frozen(NewDialect().Quote(t.Name))
	case gocql.VectorType:
		return fmt.Sprintf("vector<%s, %d>", typeName(t.SubType, true), t.Dimensions)
	}
	if n, ok := nativeNames[ti.Type()]; ok {
		return n
	}
	if s, ok := ti.(fmt.Stringer); ok {
		return s.String()
	}
	return "custom"
}

var nativeNames = map[gocql.Type]string{
	gocql.TypeAscii: "ascii", gocql.TypeBigInt: "bigint", gocql.TypeBlob: "blob", gocql.TypeBoolean: "boolean",
	gocql.TypeCounter: "counter", gocql.TypeDecimal: "decimal", gocql.TypeDouble: "double", gocql.TypeFloat: "float",
	gocql.TypeInt: "int", gocql.TypeText: "text", gocql.TypeTimestamp: "timestamp", gocql.TypeUUID: "uuid",
	gocql.TypeVarchar: "text", gocql.TypeVarint: "varint", gocql.TypeTimeUUID: "timeuuid", gocql.TypeInet: "inet",
	gocql.TypeDate: "date", gocql.TypeTime: "time", gocql.TypeSmallInt: "smallint", gocql.TypeTinyInt: "tinyint",
	gocql.TypeDuration: "duration",
}

// value converts a scanned native CQL value to the Row value set
// (composites are decoded by decodeCQL).
func value(ti gocql.TypeInfo, v any) any {
	if v == nil {
		return nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	switch x := v.(type) {
	case time.Time:
		return x
	case time.Duration: // time of day
		if ti != nil && ti.Type() == gocql.TypeTime {
			return formatTimeOfDay(x)
		}
		return x.String()
	case gocql.UUID:
		return x.String()
	case []byte:
		return append([]byte{}, x...)
	case gocql.Duration:
		return formatDuration(x)
	}
	return db.Normalize(v)
}

func formatTimeOfDay(x time.Duration) string {
	h := x / time.Hour
	m := x % time.Hour / time.Minute
	sec := x % time.Minute / time.Second
	ns := x % time.Second
	s := fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	if ns != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
	}
	return s
}

// formatDuration renders a duration as a CQL duration literal (1mo2d3ns,
// -1mo2d3ns: the components of a duration share their sign).
func formatDuration(x gocql.Duration) string {
	if x.Months < 0 || x.Days < 0 || x.Nanoseconds < 0 {
		return "-" + formatDuration(gocql.Duration{Months: -x.Months, Days: -x.Days, Nanoseconds: -x.Nanoseconds})
	}
	return fmt.Sprintf("%dmo%dd%dns", x.Months, x.Days, x.Nanoseconds)
}

var (
	intRx      = regexp.MustCompile(`^[+-]?\d+$`)
	decimalRx  = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
	timeOfDay  = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2})(?:\.(\d{1,9}))?)?$`)
	durationRx = regexp.MustCompile(`^-?(\d+(y|mo|w|d|h|ms|us|µs|ns|m|s))+$`)
	isoDurRx   = regexp.MustCompile(`^-?P(\d+Y)?(\d+M)?(\d+W)?(\d+D)?(T(\d+H)?(\d+M)?(\d+S)?)?$`)
	ourDurRx   = regexp.MustCompile(`^(-?\d+)mo(-?\d+)d(-?\d+)ns$`)
)

// parseTimeOfDay parses HH:MM[:SS[.fffffffff]] as a CQL time.
func parseTimeOfDay(s string) (time.Duration, error) {
	m := timeOfDay.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM:SS[.fffffffff])", s)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec := 0
	if m[3] != "" {
		sec, _ = strconv.Atoi(m[3])
	}
	if h > 23 || mi > 59 || sec > 59 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	var ns int
	if m[4] != "" {
		ns, _ = strconv.Atoi(m[4] + strings.Repeat("0", 9-len(m[4])))
	}
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second + time.Duration(ns), nil
}

// floatLit renders a float as a CQL literal (valid for float and double).
func floatLit(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// badValue is a grid value that can't be written: Execute refuses it and
// it never reaches the statement text.
type badValue struct{ msg string }

func (b badValue) Error() string { return b.msg }

func (b badValue) MarshalCQL(gocql.TypeInfo) ([]byte, error) { return nil, errors.New(b.msg) }

// prepare validates and converts an edited value for column kind k: the
// result is bound, or a db.Raw literal built only from validated text.
func prepare(cv db.ColValue) any {
	v := cv.Value
	if v == nil {
		return nil
	}
	if _, ok := v.(db.Raw); ok {
		return v
	}
	col := cv.Column
	bad := func(format string, a ...any) any {
		return badValue{msg: col + ": " + fmt.Sprintf(format, a...)}
	}
	switch cv.Kind {
	case db.KindInt:
		switch x := v.(type) {
		case uint64:
			return db.Raw(strconv.FormatUint(x, 10))
		case string:
			t := strings.TrimSpace(x)
			if !intRx.MatchString(t) {
				return bad("not an integer")
			}
			return db.Raw(t)
		case float64:
			if x != math.Trunc(x) || math.IsInf(x, 0) {
				return bad("not an integer")
			}
			return db.Raw(strconv.FormatFloat(x, 'f', -1, 64))
		}
	case db.KindFloat:
		switch x := v.(type) {
		case float64:
			return db.Raw(floatLit(x))
		case float32:
			return db.Raw(floatLit(float64(x)))
		case int64:
			return db.Raw(strconv.FormatInt(x, 10))
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return bad("not a number")
			}
			return db.Raw(floatLit(f))
		}
	case db.KindDecimal:
		switch x := v.(type) {
		case string:
			t := strings.TrimSpace(x)
			if !decimalRx.MatchString(t) {
				return bad("not a decimal number")
			}
			return db.Raw(t)
		case int64:
			return db.Raw(strconv.FormatInt(x, 10))
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return bad("not a decimal number")
			}
			return db.Raw(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case db.KindUUID:
		switch x := v.(type) {
		case string:
			u, err := gocql.ParseUUID(strings.TrimSpace(x))
			if err != nil {
				return bad("not a UUID")
			}
			return db.Raw(u.String())
		case gocql.UUID:
			return db.Raw(x.String())
		case [16]byte:
			return db.Raw(gocql.UUID(x).String())
		}
		return bad("not a UUID")
	case db.KindTime:
		switch x := v.(type) {
		case string:
			d, err := parseTimeOfDay(x)
			if err != nil {
				return bad("%v", err)
			}
			return d
		case time.Time:
			return time.Duration(x.Hour())*time.Hour + time.Duration(x.Minute())*time.Minute +
				time.Duration(x.Second())*time.Second + time.Duration(x.Nanosecond())
		}
	case db.KindInterval:
		if x, ok := v.(string); ok {
			d, err := parseDuration(x)
			if err != nil {
				return bad("%v", err)
			}
			return d
		}
	case db.KindArray:
		if _, ok := v.(string); ok {
			return bad("editing collection, tuple and UDT values in the grid isn't supported: use a CQL statement")
		}
	}
	return v
}

// parseDuration parses our display format (1mo2d3ns) or a CQL duration
// literal. Literals other than ours are returned as validated Raw text.
func parseDuration(s string) (any, error) {
	t := strings.TrimSpace(s)
	if m := ourDurRx.FindStringSubmatch(t); m != nil {
		mo, e1 := strconv.ParseInt(m[1], 10, 32)
		d, e2 := strconv.ParseInt(m[2], 10, 32)
		ns, e3 := strconv.ParseInt(m[3], 10, 64)
		if e1 == nil && e2 == nil && e3 == nil {
			return gocql.Duration{Months: int32(mo), Days: int32(d), Nanoseconds: ns}, nil
		}
	}
	if durationRx.MatchString(t) || isoDurRx.MatchString(t) && t != "P" && t != "-P" && !strings.HasSuffix(t, "T") {
		return db.Raw(t), nil
	}
	return nil, fmt.Errorf("invalid duration %q (e.g. 1h30m, 2mo3d, P1DT2H)", s)
}

// literal renders a value as CQL, or ok=false for the base rendering.
func literal(d *db.BaseDialect, v any, k db.Kind) (string, bool) {
	switch x := v.(type) {
	case badValue:
		return "/* invalid value */", true
	case gocql.UUID:
		return x.String(), true
	case float64:
		return floatLit(x), true
	case float32:
		return floatLit(float64(x)), true
	case gocql.Duration:
		return formatDuration(x), true
	case time.Duration:
		if k == db.KindTime {
			return d.QuoteString(formatTimeOfDay(x)), true
		}
	case string:
		switch k {
		case db.KindUUID:
			if u, err := gocql.ParseUUID(strings.TrimSpace(x)); err == nil {
				return u.String(), true
			}
		case db.KindInterval:
			if r, err := parseDuration(x); err == nil {
				if raw, ok := r.(db.Raw); ok {
					return string(raw), true
				}
				return formatDuration(r.(gocql.Duration)), true
			}
		case db.KindDecimal, db.KindInt:
			if decimalRx.MatchString(strings.TrimSpace(x)) {
				return strings.TrimSpace(x), true
			}
		}
	}
	return "", false
}

// rawValue scans a composite value (collection, tuple, UDT) itself: the
// driver panics on NULL elements of nested tuples, so the wire format is
// decoded here and only native elements go through the driver.
type rawValue struct{ v any }

func (r *rawValue) UnmarshalCQL(info gocql.TypeInfo, data []byte) error {
	v, err := decodeCQL(info, data)
	r.v = v
	return err
}

func composite(ti gocql.TypeInfo) bool {
	switch ti.(type) {
	case gocql.CollectionType, gocql.TupleTypeInfo, gocql.UDTTypeInfo:
		return true
	}
	return false
}

// scanDest is the scan destination of a value of type ti: a **T (the *T
// stays nil for NULL), a *rawValue for composites, nil for unknown types
// (skipped by the driver).
func scanDest(ti gocql.TypeInfo) any {
	if composite(ti) {
		return &rawValue{}
	}
	z := ti.Zero()
	if z == nil {
		return nil
	}
	return reflect.New(reflect.PointerTo(reflect.TypeOf(z))).Interface()
}

// takeScanned returns the converted value of a scan destination and resets it.
func takeScanned(ti gocql.TypeInfo, p any) any {
	switch x := p.(type) {
	case nil:
		return nil
	case *rawValue:
		v := x.v
		x.v = nil
		return v
	}
	ptr := reflect.ValueOf(p).Elem() // *T
	if ptr.IsNil() {
		return nil
	}
	v := ptr.Elem().Interface()
	ptr.Set(reflect.Zero(ptr.Type()))
	return value(ti, v)
}

func readCQLBytes(data []byte) (elem, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, errors.New("cql: truncated value")
	}
	n := int32(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
	data = data[4:]
	if n < 0 {
		return nil, data, nil
	}
	if int(n) > len(data) {
		return nil, nil, errors.New("cql: truncated value")
	}
	return data[:n:n], data[n:], nil
}

// decodeCQL decodes a value of type ti from its wire format into the Row
// value set.
func decodeCQL(ti gocql.TypeInfo, data []byte) (any, error) {
	if data == nil {
		return nil, nil
	}
	switch t := ti.(type) {
	case gocql.CollectionType:
		if len(data) < 4 {
			return nil, errors.New("cql: truncated collection")
		}
		n := int(int32(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])))
		data = data[4:]
		if n < 0 {
			return nil, errors.New("cql: invalid collection size")
		}
		if t.Type() == gocql.TypeMap {
			out := make(map[string]any, min(n, 1024))
			for i := 0; i < n; i++ {
				var kb, vb []byte
				var err error
				if kb, data, err = readCQLBytes(data); err != nil {
					return nil, err
				}
				if vb, data, err = readCQLBytes(data); err != nil {
					return nil, err
				}
				k, err := decodeCQL(t.Key, kb)
				if err != nil {
					return nil, err
				}
				v, err := decodeCQL(t.Elem, vb)
				if err != nil {
					return nil, err
				}
				out[db.FormatValue(k, db.KindOther)] = v
			}
			return out, nil
		}
		out := make([]any, 0, min(n, 1024))
		for i := 0; i < n; i++ {
			var eb []byte
			var err error
			if eb, data, err = readCQLBytes(data); err != nil {
				return nil, err
			}
			v, err := decodeCQL(t.Elem, eb)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case gocql.TupleTypeInfo:
		out := make([]any, len(t.Elems))
		for i, e := range t.Elems {
			if len(data) < 4 {
				break // missing trailing elements are NULL
			}
			var eb []byte
			var err error
			if eb, data, err = readCQLBytes(data); err != nil {
				return nil, err
			}
			if out[i], err = decodeCQL(e, eb); err != nil {
				return nil, err
			}
		}
		return out, nil
	case gocql.UDTTypeInfo:
		out := make(map[string]any, len(t.Elements))
		for _, f := range t.Elements {
			if len(data) < 4 {
				out[f.Name] = nil
				continue
			}
			var eb []byte
			var err error
			if eb, data, err = readCQLBytes(data); err != nil {
				return nil, err
			}
			if out[f.Name], err = decodeCQL(f.Type, eb); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	p := scanDest(ti)
	if p == nil {
		return nil, nil
	}
	if err := gocql.Unmarshal(ti, data, p); err != nil {
		return nil, err
	}
	return takeScanned(ti, p), nil
}
