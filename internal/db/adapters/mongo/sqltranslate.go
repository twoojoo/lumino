package mongo

// SQL to MongoDB: SELECT (WHERE, GROUP BY with aggregates, HAVING, ORDER
// BY, LIMIT, OFFSET, DISTINCT), INSERT, UPDATE and DELETE on one
// collection become find / countDocuments / distinct / aggregate /
// insertMany / updateMany / deleteMany statements.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

type sqlTok struct {
	text string
	kind sqlx.TokKind
}

type sqlParser struct {
	toks []sqlTok
	i    int
}

func newSQLParser(src string) *sqlParser {
	p := &sqlParser{}
	o := &sqlx.Options{}
	for _, t := range sqlx.Tokens(src, o) {
		if t.Kind == sqlx.TSpace || t.Kind == sqlx.TComment {
			continue
		}
		p.toks = append(p.toks, sqlTok{src[t.Start:t.End], t.Kind})
	}
	// "<", "=" lexed apart: join two-character operators
	var out []sqlTok
	for i := 0; i < len(p.toks); i++ {
		t := p.toks[i]
		if i+1 < len(p.toks) && t.kind == sqlx.TOperator && p.toks[i+1].kind == sqlx.TOperator {
			if j := t.text + p.toks[i+1].text; j == "<=" || j == ">=" || j == "<>" || j == "!=" {
				out = append(out, sqlTok{j, sqlx.TOperator})
				i++
				continue
			}
		}
		out = append(out, t)
	}
	p.toks = out
	return p
}

func (p *sqlParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i].text
	}
	return ""
}

func (p *sqlParser) is(words ...string) bool {
	for k, w := range words {
		if p.i+k >= len(p.toks) || !strings.EqualFold(p.toks[p.i+k].text, w) {
			return false
		}
	}
	return true
}

func (p *sqlParser) eat(words ...string) bool {
	if p.is(words...) {
		p.i += len(words)
		return true
	}
	return false
}

func (p *sqlParser) next() sqlTok {
	if p.i < len(p.toks) {
		t := p.toks[p.i]
		p.i++
		return t
	}
	return sqlTok{}
}

func (p *sqlParser) fail(format string, a ...any) error {
	at := "the end"
	if p.i < len(p.toks) {
		at = strconv.Quote(p.toks[p.i].text)
	}
	return fmt.Errorf(format+" (at %s)", append(a, at)...)
}

func (p *sqlParser) expect(w string) error {
	if !p.eat(w) {
		return p.fail("expected %s", strings.ToUpper(w))
	}
	return nil
}

// ident reads a (possibly dotted, quoted) name.
func (p *sqlParser) ident() (string, error) {
	var parts []string
	for {
		t := p.next()
		switch t.kind {
		case sqlx.TWord:
			parts = append(parts, t.text)
		case sqlx.TQuotedIdent:
			parts = append(parts, t.text[1:len(t.text)-1])
		default:
			p.i--
			return "", p.fail("expected a name")
		}
		if p.peek() != "." {
			return strings.Join(parts, "."), nil
		}
		p.i++
	}
}

// value reads a literal.
func (p *sqlParser) value() (any, error) {
	neg := false
	if p.peek() == "-" {
		neg = true
		p.i++
	}
	t := p.next()
	switch {
	case t.kind == sqlx.TNumber:
		return parseNumber(t.text, neg)
	case t.kind == sqlx.TString:
		s := strings.ReplaceAll(t.text[1:len(t.text)-1], "''", "'")
		return s, nil
	case strings.EqualFold(t.text, "true"):
		return true, nil
	case strings.EqualFold(t.text, "false"):
		return false, nil
	case strings.EqualFold(t.text, "null"):
		return nil, nil
	}
	p.i--
	return nil, p.fail("expected a value")
}

func (p *sqlParser) values() (bson.A, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var out bson.A
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.eat(")") {
			return out, nil
		}
		if err := p.expect(","); err != nil {
			return nil, err
		}
	}
}

// ---- conditions ----

func (p *sqlParser) or(alias map[string]string) (bson.D, error) {
	var parts []bson.D
	for {
		d, err := p.and(alias)
		if err != nil {
			return nil, err
		}
		parts = append(parts, d)
		if !p.eat("or") {
			break
		}
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	a := bson.A{}
	for _, d := range parts {
		a = append(a, d)
	}
	return bson.D{{Key: "$or", Value: a}}, nil
}

func (p *sqlParser) and(alias map[string]string) (bson.D, error) {
	var parts []bson.D
	for {
		d, err := p.not(alias)
		if err != nil {
			return nil, err
		}
		parts = append(parts, d)
		if !p.eat("and") {
			break
		}
	}
	return mergeAnd(parts), nil
}

// mergeAnd joins conditions: one document when their fields differ.
func mergeAnd(parts []bson.D) bson.D {
	if len(parts) == 1 {
		return parts[0]
	}
	seen := map[string]bool{}
	var out bson.D
	for _, d := range parts {
		for _, e := range d {
			if seen[e.Key] || strings.HasPrefix(e.Key, "$") {
				a := bson.A{}
				for _, x := range parts {
					a = append(a, x)
				}
				return bson.D{{Key: "$and", Value: a}}
			}
			seen[e.Key] = true
			out = append(out, e)
		}
	}
	return out
}

func (p *sqlParser) not(alias map[string]string) (bson.D, error) {
	if p.eat("not") {
		d, err := p.not(alias)
		if err != nil {
			return nil, err
		}
		return bson.D{{Key: "$nor", Value: bson.A{d}}}, nil
	}
	return p.cmp(alias)
}

// likeRegex converts a LIKE pattern to an anchored regular expression.
func likeRegex(s string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range s {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}

var cmpOps = map[string]string{"=": "", "!=": "$ne", "<>": "$ne", "<": "$lt", "<=": "$lte", ">": "$gt", ">=": "$gte"}

func (p *sqlParser) cmp(alias map[string]string) (bson.D, error) {
	if p.eat("(") {
		d, err := p.or(alias)
		if err != nil {
			return nil, err
		}
		return d, p.expect(")")
	}
	var col string
	if agg, ok, err := p.aggregate(); ok || err != nil {
		if err != nil {
			return nil, err
		}
		name, known := alias[agg.key()]
		if !known {
			return nil, fmt.Errorf("HAVING: %s must also be in the select list", agg.key())
		}
		col = name
	} else {
		c, err := p.ident()
		if err != nil {
			return nil, err
		}
		col = c
		if a, ok := alias[strings.ToLower(c)]; ok {
			col = a
		}
	}
	neg := p.eat("not")
	switch {
	case p.eat("in"):
		vals, err := p.values()
		if err != nil {
			return nil, err
		}
		op := "$in"
		if neg {
			op = "$nin"
		}
		return bson.D{{Key: col, Value: bson.D{{Key: op, Value: vals}}}}, nil
	case p.eat("like"):
		v, err := p.value()
		s, ok := v.(string)
		if err != nil || !ok {
			return nil, p.fail("LIKE needs a string")
		}
		re := bson.Regex{Pattern: likeRegex(s)}
		if neg {
			return bson.D{{Key: col, Value: bson.D{{Key: "$not", Value: re}}}}, nil
		}
		return bson.D{{Key: col, Value: re}}, nil
	case p.eat("between"):
		lo, err := p.value()
		if err != nil {
			return nil, err
		}
		if err := p.expect("and"); err != nil {
			return nil, err
		}
		hi, err := p.value()
		if err != nil {
			return nil, err
		}
		d := bson.D{{Key: col, Value: bson.D{{Key: "$gte", Value: lo}, {Key: "$lte", Value: hi}}}}
		if neg {
			return bson.D{{Key: "$nor", Value: bson.A{d}}}, nil
		}
		return d, nil
	case neg:
		return nil, p.fail("expected IN, LIKE or BETWEEN after NOT")
	case p.eat("is"):
		isNot := p.eat("not")
		if err := p.expect("null"); err != nil {
			return nil, err
		}
		if isNot {
			return bson.D{{Key: col, Value: bson.D{{Key: "$ne", Value: nil}}}}, nil
		}
		return bson.D{{Key: col, Value: nil}}, nil
	}
	op, ok := cmpOps[p.peek()]
	if !ok {
		return nil, p.fail("expected a comparison")
	}
	p.i++
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	if op == "" {
		return bson.D{{Key: col, Value: v}}, nil
	}
	return bson.D{{Key: col, Value: bson.D{{Key: op, Value: v}}}}, nil
}

// ---- select list ----

type sqlAgg struct {
	fn, col  string // col "" = *
	distinct bool
}

func (a sqlAgg) key() string {
	c := a.col
	if c == "" {
		c = "*"
	}
	return strings.ToLower(a.fn) + "(" + c + ")"
}

// accumulator is the $group accumulator of an aggregate.
func (a sqlAgg) accumulator() (any, error) {
	switch strings.ToLower(a.fn) {
	case "count":
		if a.col == "" {
			return bson.D{{Key: "$sum", Value: int32(1)}}, nil
		}
		return bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
			bson.D{{Key: "$ne", Value: bson.A{"$" + a.col, nil}}}, int32(1), int32(0)}}}}}, nil
	case "sum", "avg", "min", "max":
		if a.col == "" {
			return nil, fmt.Errorf("%s(*) is not valid", strings.ToUpper(a.fn))
		}
		return bson.D{{Key: "$" + strings.ToLower(a.fn), Value: "$" + a.col}}, nil
	}
	return nil, fmt.Errorf("unsupported function %s", a.fn)
}

var aggFuncs = map[string]bool{"count": true, "sum": true, "avg": true, "min": true, "max": true}

// aggregate reads COUNT(*), SUM(x)…; ok is false when the next tokens are
// not an aggregate.
func (p *sqlParser) aggregate() (sqlAgg, bool, error) {
	if p.i+1 >= len(p.toks) || !aggFuncs[strings.ToLower(p.peek())] || p.toks[p.i+1].text != "(" {
		return sqlAgg{}, false, nil
	}
	a := sqlAgg{fn: strings.ToLower(p.next().text)}
	p.i++ // (
	if p.eat("distinct") {
		a.distinct = true
	}
	if p.peek() == "*" {
		p.i++
	} else {
		c, err := p.ident()
		if err != nil {
			return a, true, err
		}
		a.col = c
	}
	if a.distinct {
		return a, true, errors.New("aggregates with DISTINCT are not supported")
	}
	return a, true, p.expect(")")
}

type selItem struct {
	col   string
	agg   *sqlAgg
	alias string
}

func (it selItem) name() string {
	if it.alias != "" {
		return it.alias
	}
	if it.agg != nil {
		if it.agg.col == "" {
			return it.agg.fn
		}
		return it.agg.fn + "_" + strings.ReplaceAll(it.agg.col, ".", "_")
	}
	return it.col
}

// ---- statements ----

func (*language) TranslateSQL(sql string) (string, error) {
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	p := newSQLParser(sql)
	var out string
	var err error
	switch {
	case p.eat("select"):
		out, err = p.selectStmt()
	case p.eat("insert", "into"):
		out, err = p.insertStmt()
	case p.eat("update"):
		out, err = p.updateStmt()
	case p.eat("delete", "from"):
		out, err = p.deleteStmt()
	default:
		return "", errors.New("translate SELECT, INSERT, UPDATE or DELETE statements")
	}
	if err != nil {
		return "", err
	}
	if p.i < len(p.toks) {
		return "", p.fail("unsupported SQL")
	}
	return out, nil
}

func (p *sqlParser) collection() (db.ObjectRef, error) {
	name, err := p.ident()
	if err != nil {
		return db.ObjectRef{}, err
	}
	if p.peek() != "" && p.toks[p.i].kind == sqlx.TWord && !isClause(p.peek()) {
		return db.ObjectRef{}, p.fail("aliases and joins are not supported (use $lookup in an aggregation)")
	}
	return db.ObjectRef{Name: name, Type: db.ObjCollection}, nil
}

func isClause(w string) bool {
	switch strings.ToLower(w) {
	case "where", "group", "order", "limit", "offset", "having", "set", "values":
		return true
	}
	return false
}

func (p *sqlParser) where() (bson.D, error) {
	if !p.eat("where") {
		return bson.D{}, nil
	}
	return p.or(nil)
}

func (p *sqlParser) selectStmt() (string, error) {
	distinct := p.eat("distinct")
	var items []selItem
	star := false
	if p.eat("*") {
		star = true
	} else {
		for {
			var it selItem
			agg, ok, err := p.aggregate()
			if err != nil {
				return "", err
			}
			if ok {
				it.agg = &agg
			} else {
				c, err := p.ident()
				if err != nil {
					return "", err
				}
				it.col = c
			}
			if p.eat("as") {
				a, err := p.ident()
				if err != nil {
					return "", err
				}
				it.alias = a
			} else if p.peek() != "" && p.toks[p.i].kind == sqlx.TWord && !strings.EqualFold(p.peek(), "from") {
				a, _ := p.ident()
				it.alias = a
			}
			items = append(items, it)
			if !p.eat(",") {
				break
			}
		}
	}
	if err := p.expect("from"); err != nil {
		return "", err
	}
	ref, err := p.collection()
	if err != nil {
		return "", err
	}
	filter, err := p.where()
	if err != nil {
		return "", err
	}
	var group []string
	if p.eat("group", "by") {
		for {
			c, err := p.ident()
			if err != nil {
				return "", err
			}
			group = append(group, c)
			if !p.eat(",") {
				break
			}
		}
	}
	aliases := map[string]string{}
	for _, it := range items {
		if it.agg != nil {
			aliases[it.agg.key()] = it.name()
		}
		if it.alias != "" {
			aliases[strings.ToLower(it.alias)] = it.name()
		}
	}
	var having bson.D
	if p.eat("having") {
		h, err := p.or(aliases)
		if err != nil {
			return "", err
		}
		having = h
	}
	var sortKeys bson.D
	if p.eat("order", "by") {
		for {
			c, err := p.ident()
			if err != nil {
				return "", err
			}
			if a, ok := aliases[strings.ToLower(c)]; ok {
				c = a
			}
			dir := int32(1)
			if p.eat("desc") {
				dir = -1
			} else {
				p.eat("asc")
			}
			sortKeys = append(sortKeys, bson.E{Key: c, Value: dir})
			if !p.eat(",") {
				break
			}
		}
	}
	var limit, skip int64 = -1, 0
	for {
		switch {
		case p.eat("limit"):
			v, err := p.value()
			n, ok := toFloat(v)
			if err != nil || !ok {
				return "", p.fail("LIMIT needs a number")
			}
			limit = int64(n)
			if p.eat(",") { // LIMIT offset, count
				v, err := p.value()
				m, ok := toFloat(v)
				if err != nil || !ok {
					return "", p.fail("LIMIT needs a number")
				}
				skip, limit = limit, int64(m)
			}
			continue
		case p.eat("offset"):
			v, err := p.value()
			n, ok := toFloat(v)
			if err != nil || !ok {
				return "", p.fail("OFFSET needs a number")
			}
			skip = int64(n)
			p.eat("rows")
			continue
		}
		break
	}
	coll := collExpr(ref)
	hasAgg := false
	for _, it := range items {
		hasAgg = hasAgg || it.agg != nil
	}
	if !hasAgg && len(group) == 0 {
		if having != nil {
			return "", errors.New("HAVING needs GROUP BY or aggregates")
		}
		if distinct {
			if star || len(items) != 1 {
				return "", errors.New("SELECT DISTINCT translates for one field (distinct())")
			}
			return coll + ".distinct(" + strconv.Quote(items[0].col) + ", " + shellLit(filter) + ")", nil
		}
		s := coll + ".find(" + shellLit(filter)
		if !star {
			proj := bson.D{}
			idWanted := false
			for _, it := range items {
				proj = append(proj, bson.E{Key: it.col, Value: int32(1)})
				idWanted = idWanted || it.col == "_id"
			}
			if !idWanted {
				proj = append(proj, bson.E{Key: "_id", Value: int32(0)})
			}
			s += ", " + shellLit(proj)
		}
		s += ")"
		if len(sortKeys) > 0 {
			s += ".sort(" + shellLit(sortKeys) + ")"
		}
		if skip > 0 {
			s += ".skip(" + strconv.FormatInt(skip, 10) + ")"
		}
		if limit >= 0 {
			s += ".limit(" + strconv.FormatInt(limit, 10) + ")"
		}
		return s, nil
	}
	if star {
		return "", errors.New("SELECT * can't be grouped: list the fields and aggregates")
	}
	// SELECT COUNT(*) FROM t WHERE …
	if len(group) == 0 && len(items) == 1 && items[0].agg != nil && items[0].agg.fn == "count" && items[0].agg.col == "" &&
		having == nil && len(sortKeys) == 0 && skip == 0 && limit < 0 {
		return coll + ".countDocuments(" + shellLit(filter) + ")", nil
	}
	var pipe bson.A
	if len(filter) > 0 {
		pipe = append(pipe, bson.D{{Key: "$match", Value: filter}})
	}
	var id any
	switch len(group) {
	case 0:
		id = nil
	case 1:
		id = "$" + group[0]
	default:
		g := bson.D{}
		for _, c := range group {
			g = append(g, bson.E{Key: strings.ReplaceAll(c, ".", "_"), Value: "$" + c})
		}
		id = g
	}
	grp := bson.D{{Key: "_id", Value: id}}
	proj := bson.D{{Key: "_id", Value: int32(0)}}
	inGroup := func(c string) (string, bool) {
		for _, g := range group {
			if g == c {
				if len(group) == 1 {
					return "$_id", true
				}
				return "$_id." + strings.ReplaceAll(c, ".", "_"), true
			}
		}
		return "", false
	}
	for _, it := range items {
		if it.agg != nil {
			acc, err := it.agg.accumulator()
			if err != nil {
				return "", err
			}
			grp = append(grp, bson.E{Key: it.name(), Value: acc})
			proj = append(proj, bson.E{Key: it.name(), Value: int32(1)})
			continue
		}
		ref, ok := inGroup(it.col)
		if !ok {
			return "", fmt.Errorf("%s must be in GROUP BY or inside an aggregate", it.col)
		}
		proj = append(proj, bson.E{Key: it.name(), Value: ref})
	}
	pipe = append(pipe, bson.D{{Key: "$group", Value: grp}})
	if having != nil {
		pipe = append(pipe, bson.D{{Key: "$match", Value: having}})
	}
	pipe = append(pipe, bson.D{{Key: "$project", Value: proj}})
	if len(sortKeys) > 0 {
		pipe = append(pipe, bson.D{{Key: "$sort", Value: sortKeys}})
	}
	if skip > 0 {
		pipe = append(pipe, bson.D{{Key: "$skip", Value: skip}})
	}
	if limit >= 0 {
		pipe = append(pipe, bson.D{{Key: "$limit", Value: limit}})
	}
	return coll + ".aggregate(" + shellLit(pipe) + ")", nil
}

func (p *sqlParser) insertStmt() (string, error) {
	ref, err := p.collection()
	if err != nil {
		return "", err
	}
	if err := p.expect("("); err != nil {
		return "", err
	}
	var cols []string
	for {
		c, err := p.ident()
		if err != nil {
			return "", err
		}
		cols = append(cols, c)
		if p.eat(")") {
			break
		}
		if err := p.expect(","); err != nil {
			return "", err
		}
	}
	if err := p.expect("values"); err != nil {
		return "", err
	}
	docs := bson.A{}
	for {
		vals, err := p.values()
		if err != nil {
			return "", err
		}
		if len(vals) != len(cols) {
			return "", fmt.Errorf("%d values for %d fields", len(vals), len(cols))
		}
		d := bson.D{}
		for i, c := range cols {
			d = setPath(d, c, vals[i])
		}
		docs = append(docs, d)
		if !p.eat(",") {
			break
		}
	}
	if len(docs) == 1 {
		return collExpr(ref) + ".insertOne(" + shellLit(docs[0]) + ")", nil
	}
	return collExpr(ref) + ".insertMany(" + shellLit(docs) + ")", nil
}

func (p *sqlParser) updateStmt() (string, error) {
	ref, err := p.collection()
	if err != nil {
		return "", err
	}
	if err := p.expect("set"); err != nil {
		return "", err
	}
	set, unset := bson.D{}, bson.D{}
	for {
		c, err := p.ident()
		if err != nil {
			return "", err
		}
		if err := p.expect("="); err != nil {
			return "", err
		}
		v, err := p.value()
		if err != nil {
			return "", err
		}
		set = append(set, bson.E{Key: c, Value: v})
		if !p.eat(",") {
			break
		}
	}
	filter, err := p.where()
	if err != nil {
		return "", err
	}
	upd := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		upd = append(upd, bson.E{Key: "$unset", Value: unset})
	}
	return collExpr(ref) + ".updateMany(" + shellLit(filter) + ", " + shellLit(upd) + ")", nil
}

func (p *sqlParser) deleteStmt() (string, error) {
	ref, err := p.collection()
	if err != nil {
		return "", err
	}
	filter, err := p.where()
	if err != nil {
		return "", err
	}
	return collExpr(ref) + ".deleteMany(" + shellLit(filter) + ")", nil
}
