package mongo

// The console language: mongosh statements without a JavaScript engine.
// Literals follow mongosh (unquoted keys, single quotes, ObjectId(),
// ISODate(), NumberLong()…, regular expressions); a statement is "use x",
// "show x" or a db.… method chain.

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// lexState is carried between lines: inside a /* */ comment.
type lexState struct{ comment bool }

func identStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }
func identRune(r rune) bool  { return identStart(r) || unicode.IsDigit(r) }

// regexAllowed reports whether "/" after the token prev starts a regular
// expression (it is a division after values).
func regexAllowed(line string, prev *sqlx.Token) bool {
	if prev == nil {
		return true
	}
	switch prev.Kind {
	case sqlx.TPunct, sqlx.TOperator:
		c := line[prev.End-1]
		return c != ')' && c != ']' && c != '}'
	}
	return false
}

// lexLine tokenizes one line.
func lexLine(line string, st lexState) ([]sqlx.Token, lexState) {
	var toks []sqlx.Token
	var prev *sqlx.Token
	add := func(k sqlx.TokKind, s, e int) {
		toks = append(toks, sqlx.Token{Kind: k, Start: s, End: e})
		if k != sqlx.TSpace && k != sqlx.TComment {
			prev = &toks[len(toks)-1]
		}
	}
	i := 0
	if st.comment {
		j := strings.Index(line, "*/")
		if j < 0 {
			if len(line) > 0 {
				add(sqlx.TComment, 0, len(line))
			}
			return toks, st
		}
		add(sqlx.TComment, 0, j+2)
		i, st.comment = j+2, false
	}
	for i < len(line) {
		c := line[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r') {
				i++
			}
			add(sqlx.TSpace, start, i)
		case strings.HasPrefix(line[i:], "//"):
			add(sqlx.TComment, i, len(line))
			i = len(line)
		case strings.HasPrefix(line[i:], "/*"):
			j := strings.Index(line[i+2:], "*/")
			if j < 0 {
				add(sqlx.TComment, i, len(line))
				return toks, lexState{comment: true}
			}
			i += j + 4
			add(sqlx.TComment, start, i)
		case c == '"' || c == '\'' || c == '`':
			i++
			for i < len(line) && line[i] != c {
				if line[i] == '\\' {
					i++
				}
				i++
			}
			i = min(i+1, len(line))
			add(sqlx.TString, start, i)
		case c == '/' && regexAllowed(line, prev):
			i++
			class := false
			for i < len(line) && (line[i] != '/' || class) {
				switch line[i] {
				case '\\':
					i++
				case '[':
					class = true
				case ']':
					class = false
				}
				i++
			}
			i = min(i+1, len(line))
			for i < len(line) && strings.IndexByte("dgimsuyv", line[i]) >= 0 {
				i++
			}
			add(sqlx.TString, start, i)
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9':
			if strings.HasPrefix(line[i:], "0x") || strings.HasPrefix(line[i:], "0X") {
				i += 2
			}
			for i < len(line) {
				d := line[i]
				if d >= '0' && d <= '9' || d == '.' || d >= 'a' && d <= 'f' || d >= 'A' && d <= 'F' || d == '_' {
					i++
				} else if (d == '+' || d == '-') && (line[i-1] == 'e' || line[i-1] == 'E') {
					i++
				} else {
					break
				}
			}
			add(sqlx.TNumber, start, i)
		default:
			r, n := utf8.DecodeRuneInString(line[i:])
			if identStart(r) {
				i += n
				for i < len(line) {
					r, n := utf8.DecodeRuneInString(line[i:])
					if !identRune(r) {
						break
					}
					i += n
				}
				if line[start] == '$' {
					add(sqlx.TParam, start, i) // query operators, $field references
				} else {
					add(sqlx.TWord, start, i)
				}
				continue
			}
			i += n
			if strings.ContainsRune("()[]{},;.:", r) {
				add(sqlx.TPunct, start, i)
			} else {
				add(sqlx.TOperator, start, i)
			}
		}
	}
	return toks, st
}

// tokens lexes a whole text (offsets in the text).
func tokens(text string) []sqlx.Token {
	var out []sqlx.Token
	var st lexState
	off := 0
	for {
		nl := strings.IndexByte(text[off:], '\n')
		end := len(text)
		if nl >= 0 {
			end = off + nl
		}
		var toks []sqlx.Token
		toks, st = lexLine(text[off:end], st)
		for _, t := range toks {
			t.Start += off
			t.End += off
			out = append(out, t)
		}
		if nl < 0 {
			return out
		}
		out = append(out, sqlx.Token{Kind: sqlx.TSpace, Start: end, End: end + 1})
		off = end + 1
	}
}

// split splits a script: ";" ends a statement, and so does a line break
// when the statement is complete (brackets closed) and the next line
// doesn't continue the chain (".sort(…)").
func split(script string) []sqlx.Statement {
	toks := tokens(script)
	var out []sqlx.Statement
	depth, start := 0, -1
	end := 0
	flush := func() {
		if start >= 0 {
			out = append(out, sqlx.Statement{Text: script[start:end], Start: start, End: end})
		}
		start = -1
	}
	for i, t := range toks {
		switch t.Kind {
		case sqlx.TComment:
			continue
		case sqlx.TSpace:
			if depth == 0 && start >= 0 && strings.Contains(script[t.Start:t.End], "\n") {
				// next significant token
				next := ""
				for _, u := range toks[i+1:] {
					if u.Kind != sqlx.TSpace && u.Kind != sqlx.TComment {
						next = script[u.Start:u.End]
						break
					}
				}
				if next != "." && !continues(script[start:end]) {
					flush()
				}
			}
			continue
		}
		s := script[t.Start:t.End]
		if t.Kind == sqlx.TPunct {
			switch s {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth = max(0, depth-1)
			case ";":
				if depth == 0 {
					flush()
					continue
				}
			}
		}
		if start < 0 {
			start = t.Start
		}
		end = t.End
	}
	flush()
	return out
}

// continues reports whether a statement ends with something expecting
// more (a dot, a comma, an operator).
func continues(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, ".") || strings.HasSuffix(s, ",") || strings.HasSuffix(s, ":")
}

// ---- parser ----

type parser struct {
	src  string
	toks []sqlx.Token
	i    int
}

// parseError carries the offset of the failing token.
type parseError struct {
	msg string
	pos int
}

func (e *parseError) Error() string { return e.msg }

func newParser(src string) *parser {
	p := &parser{src: src}
	for _, t := range tokens(src) {
		if t.Kind != sqlx.TSpace && t.Kind != sqlx.TComment {
			p.toks = append(p.toks, t)
		}
	}
	return p
}

func (p *parser) peek() string {
	if p.i < len(p.toks) {
		t := p.toks[p.i]
		return p.src[t.Start:t.End]
	}
	return ""
}

func (p *parser) peekKind() sqlx.TokKind {
	if p.i < len(p.toks) {
		return p.toks[p.i].Kind
	}
	return sqlx.TSpace
}

func (p *parser) pos() int {
	if p.i < len(p.toks) {
		return p.toks[p.i].Start
	}
	return len(p.src)
}

func (p *parser) fail(format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	if p.i < len(p.toks) {
		msg += fmt.Sprintf(" (at %q)", p.peek())
	} else {
		msg += " (at the end)"
	}
	return &parseError{msg: msg, pos: p.pos()}
}

func (p *parser) next() string {
	s := p.peek()
	p.i++
	return s
}

func (p *parser) eat(s string) bool {
	if p.peek() == s && p.i < len(p.toks) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(s string) error {
	if !p.eat(s) {
		return p.fail("expected %q", s)
	}
	return nil
}

func (p *parser) done() bool { return p.i >= len(p.toks) }

// value parses a literal.
func (p *parser) value() (any, error) {
	if p.done() {
		return nil, p.fail("expected a value")
	}
	tok := p.toks[p.i]
	s := p.peek()
	switch {
	case s == "{":
		return p.object()
	case s == "[":
		p.i++
		arr := bson.A{}
		for !p.eat("]") {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			if !p.eat(",") {
				if err := p.expect("]"); err != nil {
					return nil, err
				}
				break
			}
		}
		return arr, nil
	case tok.Kind == sqlx.TString:
		p.i++
		if s[0] == '/' {
			return parseRegex(s)
		}
		return unquote(s)
	case tok.Kind == sqlx.TNumber:
		p.i++
		return parseNumber(s, false)
	case s == "-" || s == "+":
		p.i++
		if p.peek() == "Infinity" {
			p.i++
			if s == "-" {
				return math.Inf(-1), nil
			}
			return math.Inf(1), nil
		}
		if p.peekKind() != sqlx.TNumber {
			return nil, p.fail("expected a number")
		}
		return parseNumber(p.next(), s == "-")
	case tok.Kind == sqlx.TWord:
		switch s {
		case "true":
			p.i++
			return true, nil
		case "false":
			p.i++
			return false, nil
		case "null":
			p.i++
			return nil, nil
		case "undefined":
			p.i++
			return bson.Undefined{}, nil
		case "NaN":
			p.i++
			return math.NaN(), nil
		case "Infinity":
			p.i++
			return math.Inf(1), nil
		case "new":
			p.i++
			if p.peekKind() != sqlx.TWord {
				return nil, p.fail("expected a constructor after new")
			}
			return p.helper(true)
		}
		return p.helper(false)
	}
	return nil, p.fail("unexpected token")
}

func (p *parser) object() (bson.D, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	doc := bson.D{}
	for !p.eat("}") {
		if p.done() {
			return nil, p.fail("unterminated object")
		}
		tok := p.toks[p.i]
		var key string
		switch tok.Kind {
		case sqlx.TWord, sqlx.TParam:
			key = p.next()
		case sqlx.TString:
			k, err := unquote(p.next())
			if err != nil {
				return nil, err
			}
			key = k
		case sqlx.TNumber:
			key = p.next()
		default:
			return nil, p.fail("expected a key")
		}
		// dotted keys: {a.b: 1} is not valid JS, but "a.b" is
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		doc = append(doc, bson.E{Key: key, Value: v})
		if !p.eat(",") {
			if err := p.expect("}"); err != nil {
				return nil, err
			}
			break
		}
	}
	return doc, nil
}

// args parses "(a, b…)".
func (p *parser) args() ([]any, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var out []any
	for !p.eat(")") {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if !p.eat(",") {
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			break
		}
	}
	return out, nil
}

// helper parses the shell's constructors: ObjectId("…"), ISODate("…")…
func (p *parser) helper(isNew bool) (any, error) {
	name := p.next()
	if name == "Date" && p.eat(".") {
		if p.next() != "now" {
			return nil, p.fail("unsupported Date member")
		}
		if _, err := p.args(); err != nil {
			return nil, err
		}
		return float64(time.Now().UnixMilli()), nil
	}
	if p.peek() != "(" {
		p.i--
		return nil, p.fail("unknown name %q (quote strings, call helpers like ObjectId(…))", name)
	}
	a, err := p.args()
	if err != nil {
		return nil, err
	}
	str := func(i int) (string, bool) {
		if i < len(a) {
			s, ok := a[i].(string)
			return s, ok
		}
		return "", false
	}
	num := func(i int) (float64, bool) {
		if i < len(a) {
			switch x := a[i].(type) {
			case int32:
				return float64(x), true
			case int64:
				return float64(x), true
			case float64:
				return x, true
			}
		}
		return 0, false
	}
	switch name {
	case "ObjectId", "ObjectID":
		if len(a) == 0 {
			return bson.NewObjectID(), nil
		}
		s, _ := str(0)
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, fmt.Errorf("ObjectId(%q): not 24 hex digits", s)
		}
		return id, nil
	case "ISODate", "Date":
		if len(a) == 0 {
			if name == "Date" && !isNew {
				return time.Now().UTC().Format(time.RFC1123), nil // Date() is a string in JS
			}
			return bson.NewDateTimeFromTime(time.Now()), nil
		}
		if f, ok := num(0); ok && len(a) == 1 {
			return bson.DateTime(int64(f)), nil
		}
		s, _ := str(0)
		t, err := parseDate(s)
		if err != nil {
			return nil, err
		}
		return bson.NewDateTimeFromTime(t), nil
	case "NumberLong", "Long":
		if f, ok := num(0); ok {
			return int64(f), nil
		}
		s, _ := str(0)
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s(%q): not an integer", name, s)
		}
		return n, nil
	case "NumberInt", "Int32":
		if f, ok := num(0); ok {
			return int32(f), nil
		}
		s, _ := str(0)
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s(%q): not a 32-bit integer", name, s)
		}
		return int32(n), nil
	case "Double":
		if f, ok := num(0); ok {
			return f, nil
		}
		s, _ := str(0)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("Double(%q): not a number", s)
		}
		return f, nil
	case "NumberDecimal", "Decimal128":
		s, ok := str(0)
		if !ok {
			if f, ok := num(0); ok {
				s = strconv.FormatFloat(f, 'g', -1, 64)
			}
		}
		d, err := bson.ParseDecimal128(s)
		if err != nil {
			return nil, fmt.Errorf("%s(%q): not a decimal", name, s)
		}
		return d, nil
	case "UUID":
		if len(a) == 0 {
			return nil, errors.New("UUID() needs a value")
		}
		s, _ := str(0)
		b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if err != nil || len(b) != 16 {
			return nil, fmt.Errorf("UUID(%q): not a UUID", s)
		}
		return bson.Binary{Subtype: 4, Data: b}, nil
	case "BinData", "Binary":
		sub, _ := num(0)
		s, _ := str(1)
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("BinData: invalid base64")
		}
		return bson.Binary{Subtype: byte(sub), Data: b}, nil
	case "HexData":
		sub, _ := num(0)
		s, _ := str(1)
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("HexData: invalid hex")
		}
		return bson.Binary{Subtype: byte(sub), Data: b}, nil
	case "Timestamp":
		if len(a) == 1 {
			if d, ok := a[0].(bson.D); ok {
				var ts bson.Timestamp
				for _, e := range d {
					f, _ := toFloat(e.Value)
					switch e.Key {
					case "t":
						ts.T = uint32(f)
					case "i":
						ts.I = uint32(f)
					}
				}
				return ts, nil
			}
		}
		t, _ := num(0)
		i, _ := num(1)
		return bson.Timestamp{T: uint32(t), I: uint32(i)}, nil
	case "MinKey":
		return bson.MinKey{}, nil
	case "MaxKey":
		return bson.MaxKey{}, nil
	case "RegExp":
		s, _ := str(0)
		f, _ := str(1)
		return bson.Regex{Pattern: s, Options: f}, nil
	case "Code":
		s, _ := str(0)
		return bson.JavaScript(s), nil
	}
	return nil, fmt.Errorf("unknown helper %s()", name)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// parseNumber follows the JavaScript drivers: integers that fit 32 bits
// are Int32, bigger integers Int64, the rest doubles.
func parseNumber(s string, neg bool) (any, error) {
	s = strings.ReplaceAll(s, "_", "")
	if neg {
		s = "-" + s
	}
	if strings.HasPrefix(strings.TrimLeft(s, "-"), "0x") || strings.HasPrefix(strings.TrimLeft(s, "-"), "0X") {
		n, err := strconv.ParseInt(strings.Replace(strings.Replace(s, "0x", "", 1), "0X", "", 1), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", s)
		}
		return intValue(n), nil
	}
	if !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return intValue(n), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q", s)
	}
	return f, nil
}

func intValue(n int64) any {
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		return int32(n)
	}
	return n
}

// unquote decodes a JavaScript string literal.
func unquote(s string) (string, error) {
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return "", fmt.Errorf("unterminated string %s", s)
	}
	body := s[1 : len(s)-1]
	if !strings.ContainsRune(body, '\\') {
		return body, nil
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '0':
			b.WriteByte(0)
		case 'u':
			if i+4 < len(body) {
				if n, err := strconv.ParseUint(body[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(n))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		case 'x':
			if i+2 < len(body) {
				if n, err := strconv.ParseUint(body[i+1:i+3], 16, 8); err == nil {
					b.WriteRune(rune(n))
					i += 2
					continue
				}
			}
			b.WriteByte('x')
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String(), nil
}

func parseRegex(s string) (any, error) {
	j := strings.LastIndexByte(s, '/')
	if j <= 0 {
		return nil, fmt.Errorf("invalid regular expression %s", s)
	}
	return bson.Regex{Pattern: s[1:j], Options: s[j+1:]}, nil
}

func parseDate(s string) (time.Time, error) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	if t, err := db.ParseTime(s, db.KindTimestampTZ); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q", s)
}

// ---- statements ----

// call is a method call of a chain.
type call struct {
	name string
	args []any
}

// statement is a parsed console statement.
type statement struct {
	kind   string // use, show, db (method on the database), coll
	arg    string // use / show argument
	dbName string // getSiblingDB("x") ("" = the session database)
	coll   string
	calls  []call
}

// method is the first call (the operation).
func (s *statement) method() string {
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[0].name
}

// modifier returns the args of a chained call (sort, limit…).
func (s *statement) modifier(name string) ([]any, bool) {
	for _, c := range s.calls[1:] {
		if c.name == name {
			return c.args, true
		}
	}
	return nil, false
}

// parseStatement parses one statement.
func parseStatement(src string) (*statement, error) {
	p := newParser(src)
	p.eat("await")
	switch w := p.peek(); w {
	case "use", "show":
		p.i++
		if p.done() {
			return nil, p.fail("%s needs an argument", w)
		}
		arg := p.next()
		if a, err := unquote(arg); err == nil && (arg[0] == '"' || arg[0] == '\'') {
			arg = a
		}
		p.eat(";")
		if !p.done() {
			return nil, p.fail("unexpected text after %s %s", w, arg)
		}
		return &statement{kind: w, arg: arg}, nil
	case "db":
		p.i++
	case "":
		return nil, errors.New("empty statement")
	default:
		return nil, p.fail("statements start with db., use or show")
	}
	st := &statement{kind: "db"}
	var names []string
	for {
		switch {
		case p.eat("."):
			if p.peekKind() != sqlx.TWord && p.peekKind() != sqlx.TParam {
				return nil, p.fail("expected a name after \".\"")
			}
			name := p.next()
			if p.peek() != "(" {
				names = append(names, name)
				continue
			}
			switch name {
			case "getCollection":
				if len(names) > 0 || st.kind == "coll" {
					return nil, p.fail("getCollection must follow db")
				}
				a, err := p.args()
				if err != nil {
					return nil, err
				}
				s, ok := firstString(a)
				if !ok {
					return nil, errors.New("getCollection needs a collection name")
				}
				st.kind, st.coll = "coll", s
				continue
			case "getSiblingDB", "getDB":
				if len(names) > 0 || st.kind == "coll" {
					return nil, p.fail("%s must follow db", name)
				}
				a, err := p.args()
				if err != nil {
					return nil, err
				}
				s, ok := firstString(a)
				if !ok {
					return nil, fmt.Errorf("%s needs a database name", name)
				}
				st.dbName = s
				continue
			}
			if len(names) > 0 && len(st.calls) == 0 {
				st.kind, st.coll = "coll", strings.Join(names, ".")
				names = nil
			}
			if len(names) > 0 {
				return nil, p.fail("unexpected property %q", names[0])
			}
			a, err := p.args()
			if err != nil {
				return nil, err
			}
			st.calls = append(st.calls, call{name: name, args: a})
		case p.peek() == "[" && len(st.calls) == 0:
			p.i++
			if p.peekKind() != sqlx.TString {
				return nil, p.fail("expected a collection name")
			}
			s, err := unquote(p.next())
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			names = append(names, s)
		default:
			p.eat(";")
			if !p.done() {
				return nil, p.fail("unexpected text")
			}
			if len(st.calls) == 0 {
				if len(names) > 0 {
					return nil, fmt.Errorf("call a method on db.%s (e.g. .find())", strings.Join(names, "."))
				}
				return nil, errors.New("call a method on db (e.g. db.getCollectionNames())")
			}
			if len(names) > 0 {
				return nil, fmt.Errorf("unexpected property %q", names[0])
			}
			return st, nil
		}
	}
}

func firstString(a []any) (string, bool) {
	if len(a) == 0 {
		return "", false
	}
	s, ok := a[0].(string)
	return s, ok
}

// parseDocument parses a filter-like document ("" = {}).
func parseDocument(s string) (bson.D, error) {
	if strings.TrimSpace(s) == "" {
		return bson.D{}, nil
	}
	p := newParser(s)
	if p.peek() != "{" {
		return nil, p.fail("expected a document {…}")
	}
	d, err := p.object()
	if err != nil {
		return nil, err
	}
	if !p.done() {
		return nil, p.fail("unexpected text after the document")
	}
	return d, nil
}

// parseValue parses one literal.
func parseValue(s string) (any, error) {
	p := newParser(s)
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	if !p.done() {
		return nil, p.fail("unexpected text after the value")
	}
	return v, nil
}
