package db

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// FormatTime renders a time for its kind (no locale, ISO-like, lossless).
func FormatTime(t time.Time, k Kind) string {
	frac := func(layout string) string {
		if t.Nanosecond() != 0 {
			s := t.Format(layout + ".000000000")
			return strings.TrimRight(s, "0")
		}
		return t.Format(layout)
	}
	switch k {
	case KindDate:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
			return t.Format("2006-01-02")
		}
		return frac("2006-01-02 15:04:05")
	case KindTime:
		return frac("15:04:05")
	case KindTimestampTZ:
		s := frac("2006-01-02 15:04:05")
		_, off := t.Zone()
		sign := '+'
		if off < 0 {
			sign, off = '-', -off
		}
		return fmt.Sprintf("%s %c%02d:%02d", s, sign, off/3600, off%3600/60)
	}
	return frac("2006-01-02 15:04:05")
}

// FormatFloat renders a float without exponent for usual magnitudes.
func FormatFloat(f float64) string {
	a := math.Abs(f)
	if a != 0 && (a < 1e-6 || a >= 1e15) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// FormatValue renders a normalized value as text (copy, export, editing).
// NULL renders as "NULL"; callers distinguish nil themselves.
func FormatValue(v any, k Kind) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return x
	case []byte:
		if k == KindBytes || !utf8.Valid(x) {
			return "0x" + hex.EncodeToString(x)
		}
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case float64:
		return FormatFloat(x)
	case float32:
		return FormatFloat(float64(x))
	case bool:
		if x {
			return "true"
		}
		return "false"
	case time.Time:
		return FormatTime(x, k)
	case *big.Int:
		return x.String()
	case *big.Rat:
		return x.FloatString(10)
	case []any:
		return formatArray(x)
	case map[string]any:
		b, err := json.Marshal(x)
		if err == nil {
			return string(b)
		}
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

func formatArray(a []any) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, e := range a {
		if i > 0 {
			b.WriteString(", ")
		}
		switch x := e.(type) {
		case string:
			b.WriteString(strconv.Quote(x))
		case nil:
			b.WriteString("null")
		default:
			b.WriteString(FormatValue(x, KindOther))
		}
	}
	b.WriteByte(']')
	return b.String()
}

// maxCell is the longest text shown in a grid cell.
const maxCell = 512

// CellText renders a value for a grid cell: single line, bounded length;
// binary values show their size.
func CellText(v any, k Kind) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		if k == KindBytes || !utf8.Valid(x) {
			if len(x) > 32 {
				return fmt.Sprintf("0x%s… (%s)", hex.EncodeToString(x[:32]), HumanBytes(int64(len(x))))
			}
			return "0x" + hex.EncodeToString(x)
		}
		return oneLine(string(x))
	case string:
		return oneLine(x)
	}
	return oneLine(FormatValue(v, k))
}

func oneLine(s string) string {
	if len(s) > maxCell {
		cut := maxCell
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	if strings.ContainsAny(s, "\n\r\t") {
		s = strings.NewReplacer("\r\n", "⏎", "\n", "⏎", "\r", "⏎", "\t", "  ").Replace(s)
	}
	return s
}

// HumanBytes renders a byte size.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999999999 Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02",
}

// ParseTime parses the formats produced by FormatTime (and ISO 8601).
func ParseTime(s string, k Kind) (time.Time, error) {
	s = strings.TrimSpace(s)
	if k == KindTime {
		for _, l := range []string{"15:04:05.999999999", "15:04"} {
			if t, err := time.Parse(l, s); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("invalid time %q (expected HH:MM:SS)", s)
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date/time %q (expected YYYY-MM-DD[ HH:MM:SS[.ffffff][ ±HH:MM]])", s)
}

var plainNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// IsPlainNumber reports whether s is a decimal number literal (no NaN,
// Inf, hex floats or fractions): safe to send unquoted.
func IsPlainNumber(s string) bool { return plainNumber.MatchString(s) }

// ParseValue converts text typed in the grid to a bind value for a column
// of kind k. It never returns nil: NULL is chosen explicitly in the UI.
func ParseValue(s string, k Kind) (any, error) {
	t := strings.TrimSpace(s)
	switch k {
	case KindInt:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, nil
		}
		if n, err := strconv.ParseUint(t, 10, 64); err == nil {
			return n, nil
		}
		if _, ok := new(big.Int).SetString(t, 10); ok {
			return t, nil // too big for 64 bits: let the server parse it
		}
		return nil, fmt.Errorf("%q is not an integer", s)
	case KindFloat:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", s)
		}
		return f, nil
	case KindDecimal:
		if l := strings.ToLower(strings.TrimLeft(t, "+-")); l == "nan" || l == "infinity" || l == "inf" {
			return t, nil // PostgreSQL numeric accepts them (bound as text, quoted in literals)
		}
		if !IsPlainNumber(t) {
			return nil, fmt.Errorf("%q is not a number", s)
		}
		return t, nil
	case KindBool:
		switch strings.ToLower(t) {
		case "true", "t", "1", "yes", "y", "on":
			return true, nil
		case "false", "f", "0", "no", "n", "off":
			return false, nil
		}
		return nil, fmt.Errorf("%q is not a boolean", s)
	case KindTime:
		// bound as text: drivers reject time.Time values in year 0
		if _, err := ParseTime(t, k); err != nil {
			return nil, err
		}
		return t, nil
	case KindDate, KindTimestamp, KindTimestampTZ:
		return ParseTime(t, k)
	case KindBytes:
		if h, ok := strings.CutPrefix(t, "0x"); ok {
			b, err := hex.DecodeString(h)
			if err != nil {
				return nil, fmt.Errorf("invalid hex: %v", err)
			}
			return b, nil
		}
		if h, ok := strings.CutPrefix(t, `\x`); ok {
			b, err := hex.DecodeString(h)
			if err != nil {
				return nil, fmt.Errorf("invalid hex: %v", err)
			}
			return b, nil
		}
		return []byte(s), nil
	case KindJSON:
		if !json.Valid([]byte(t)) {
			return nil, fmt.Errorf("invalid JSON")
		}
		return t, nil
	case KindObjectID:
		if len(t) != 24 {
			return nil, fmt.Errorf("%q is not an ObjectId (24 hex digits)", s)
		}
		if _, err := hex.DecodeString(t); err != nil {
			return nil, fmt.Errorf("%q is not an ObjectId (24 hex digits)", s)
		}
		return strings.ToLower(t), nil
	}
	return s, nil
}

// Normalize converts common driver values to the Row value set.
func Normalize(v any) any {
	switch x := v.(type) {
	case nil, string, int64, uint64, float64, bool, time.Time, []any, map[string]any:
		return v
	case []byte:
		return append([]byte(nil), x...) // drivers reuse buffers
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return uint64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case float32:
		// shortest decimal of the float32, not of its float64 widening
		// (0.1 → 0.1, not 0.10000000149011612)
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		return f
	case big.Int:
		return x.String()
	case *big.Int:
		if x == nil {
			return nil
		}
		return x.String()
	case *big.Float:
		if x == nil {
			return nil
		}
		return x.Text('f', -1)
	case *big.Rat:
		if x == nil {
			return nil
		}
		return x.FloatString(10)
	case *time.Time:
		if x == nil {
			return nil
		}
		return *x
	case *string:
		if x == nil {
			return nil
		}
		return *x
	case fmt.Stringer:
		if rv := reflect.ValueOf(x); rv.Kind() == reflect.Pointer && rv.IsNil() {
			return nil // typed nil (Nullable elements): String() may panic
		}
		return x.String()
	}
	// typed slices, arrays, maps and pointers (ClickHouse, Cassandra)
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return Normalize(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = Normalize(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		it := rv.MapRange()
		for it.Next() {
			out[FormatValue(Normalize(it.Key().Interface()), KindOther)] = Normalize(it.Value().Interface())
		}
		return out
	case reflect.String:
		return rv.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Bool:
		return rv.Bool()
	}
	return fmt.Sprint(v)
}
