package mongo

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// ---- classification ----

// readCommands are server commands that change nothing.
var readCommands = map[string]bool{
	"find": true, "count": true, "distinct": true, "listCollections": true, "listIndexes": true,
	"listDatabases": true, "dbStats": true, "collStats": true, "serverStatus": true, "hello": true, "isMaster": true,
	"ismaster": true, "buildInfo": true, "buildinfo": true, "ping": true, "explain": true, "connectionStatus": true,
	"getParameter": true, "hostInfo": true, "top": true, "getLog": true, "replSetGetStatus": true, "usersInfo": true,
	"rolesInfo": true, "getCmdLineOpts": true, "listCommands": true, "dataSize": true, "currentOp": true,
	"getMore": true, "whatsmyuri": true, "features": true, "profile": false, "dbHash": true, "lockInfo": true,
	"getDefaultRWConcern": true, "shardingState": true, "listShards": true, "balancerStatus": true,
}

// ddlCommands change the schema, not the data.
var ddlCommands = map[string]bool{
	"create": true, "createIndexes": true, "collMod": true, "renameCollection": true, "dropIndexes": true,
	"createView": true, "convertToCapped": true, "cloneCollectionAsCapped": true, "shardCollection": true,
}

// destructiveCommands lose data.
var destructiveCommands = map[string]bool{
	"drop": true, "dropDatabase": true, "shutdown": true, "dropAllUsersFromDatabase": true, "compact": false,
}

// emptyFilter reports whether a filter matches every document.
func emptyFilter(v any) bool {
	d, ok := v.(bson.D)
	return v == nil || ok && len(d) == 0
}

// pipelineKind classifies an aggregation pipeline: $out replaces a
// collection, $merge writes into one.
func pipelineKind(p any) db.StmtKind {
	a, _ := p.(bson.A)
	k := db.StmtRead
	for _, st := range a {
		if d, ok := st.(bson.D); ok && len(d) > 0 {
			switch d[0].Key {
			case "$out":
				k = max(k, db.StmtDestructive)
			case "$merge":
				k = max(k, db.StmtWrite)
			}
		}
	}
	return k
}

// commandKind classifies a runCommand document.
func commandKind(cmd bson.D) db.StmtKind {
	if len(cmd) == 0 {
		return db.StmtWrite
	}
	name := cmd[0].Key
	switch {
	case name == "aggregate":
		p, _ := field(cmd, "pipeline")
		return pipelineKind(p)
	case name == "explain":
		return db.StmtRead // explain never applies writes
	case name == "delete":
		if ds, ok := fieldOr(cmd, "deletes").(bson.A); ok {
			for _, d := range ds {
				if dd, ok := d.(bson.D); ok && emptyFilter(fieldOr(dd, "q")) {
					return db.StmtDestructive
				}
			}
		}
		return db.StmtWrite
	case readCommands[name]:
		return db.StmtRead
	case ddlCommands[name]:
		return db.StmtDDL
	case destructiveCommands[name]:
		return db.StmtDestructive
	}
	return db.StmtWrite
}

// dbReadMethods are db.* methods that change nothing.
var dbReadMethods = map[string]bool{
	"getCollectionNames": true, "getCollectionInfos": true, "stats": true, "serverStatus": true, "hello": true,
	"isMaster": true, "version": true, "hostInfo": true, "currentOp": true, "getName": true,
}

// collReadMethods are collection methods that change nothing.
var collReadMethods = map[string]bool{
	"find": true, "findOne": true, "countDocuments": true, "estimatedDocumentCount": true, "count": true,
	"distinct": true, "getIndexes": true, "indexes": true, "stats": true,
}

// classify is the policy kind of a statement (unknown = write).
func classify(st *statement) db.StmtKind {
	switch st.kind {
	case "use":
		return db.StmtSession
	case "show":
		return db.StmtRead
	}
	c := st.calls[0]
	if st.kind == "db" {
		switch {
		case dbReadMethods[c.name]:
			return db.StmtRead
		case c.name == "aggregate" && len(c.args) > 0:
			return pipelineKind(c.args[0])
		case c.name == "runCommand" || c.name == "adminCommand":
			if len(c.args) > 0 {
				if d, ok := c.args[0].(bson.D); ok {
					return commandKind(d)
				}
			}
			return db.StmtWrite
		case c.name == "createCollection" || c.name == "createView":
			return db.StmtDDL
		case c.name == "dropDatabase":
			return db.StmtDestructive
		}
		return db.StmtWrite
	}
	switch c.name {
	case "aggregate":
		if _, ok := hasCall(st, "explain"); ok {
			return db.StmtRead
		}
		if len(c.args) > 0 {
			if a, ok := c.args[0].(bson.A); ok {
				return pipelineKind(a)
			}
			return pipelineKind(bson.A(c.args))
		}
		return db.StmtRead
	case "deleteMany", "remove", "updateMany":
		if len(c.args) == 0 || emptyFilter(c.args[0]) {
			return db.StmtDestructive // like DELETE / UPDATE without WHERE
		}
		return db.StmtWrite
	case "drop":
		return db.StmtDestructive
	case "createIndex", "createIndexes", "ensureIndex", "dropIndex", "dropIndexes", "renameCollection":
		return db.StmtDDL
	}
	if collReadMethods[c.name] {
		return db.StmtRead
	}
	return db.StmtWrite
}

// ---- language ----

// language is the console language of MongoDB connections.
type language struct{}

var _ db.DocumentLanguage = (*language)(nil)

// Language returns the console language (it needs no connection).
func Language() db.DocumentLanguage { return &language{} }

func (*language) Name() string    { return "mongo" }
func (*language) Display() string { return "MongoDB shell" }
func (*language) Fence() string   { return "javascript" }

func (*language) LexLine(line string, st db.LexState) ([]sqlx.Token, db.LexState) {
	s, _ := st.(lexState)
	return lexLine(line, s)
}

func (*language) Tokens(text string) []sqlx.Token { return tokens(text) }

var keywords = map[string]bool{"db": true, "use": true, "show": true, "new": true, "await": true}

func (*language) Keywords() map[string]bool { return keywords }

// shellFunctions are completed and highlighted as calls.
var shellFunctions = []string{
	"find", "findOne", "aggregate", "countDocuments", "estimatedDocumentCount", "distinct",
	"insertOne", "insertMany", "updateOne", "updateMany", "replaceOne", "deleteOne", "deleteMany",
	"findOneAndUpdate", "findOneAndReplace", "findOneAndDelete", "bulkWrite",
	"createIndex", "createIndexes", "dropIndex", "dropIndexes", "getIndexes", "drop", "renameCollection", "stats", "validate",
	"sort", "limit", "skip", "projection", "hint", "collation", "comment", "maxTimeMS", "batchSize", "allowDiskUse", "explain", "count", "toArray",
	"getCollection", "getSiblingDB", "getCollectionNames", "getCollectionInfos", "runCommand", "adminCommand",
	"createCollection", "createView", "dropDatabase", "serverStatus", "currentOp", "killOp", "version",
	"ObjectId", "ISODate", "Date", "NumberLong", "NumberInt", "NumberDecimal", "Decimal128", "UUID", "BinData", "Timestamp",
	"MinKey", "MaxKey", "RegExp", "Double", "Int32", "Long",
}

func (*language) Functions() []string { return shellFunctions }

// operators are the query and aggregation operators (types completion).
var operators = []string{
	"$eq", "$ne", "$gt", "$gte", "$lt", "$lte", "$in", "$nin", "$and", "$or", "$nor", "$not", "$exists", "$type",
	"$regex", "$options", "$expr", "$elemMatch", "$size", "$all", "$text", "$search", "$mod", "$where",
	"$set", "$unset", "$inc", "$mul", "$rename", "$push", "$pull", "$addToSet", "$pop", "$min", "$max", "$currentDate", "$setOnInsert",
	"$match", "$project", "$group", "$sort", "$limit", "$skip", "$unwind", "$lookup", "$addFields", "$count", "$facet",
	"$bucket", "$sortByCount", "$replaceRoot", "$out", "$merge", "$sum", "$avg", "$first", "$last",
}

func (*language) Types() []string { return operators }

func (*language) LineComment() string { return "//" }

func (*language) Split(script string) []sqlx.Statement { return split(script) }

func (*language) Terminate(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || strings.HasSuffix(stmt, ";") {
		return stmt
	}
	return stmt + ";"
}

func (*language) Classify(stmt string) db.StmtKind {
	st, err := parseStatement(strings.TrimSpace(stmt))
	if err != nil {
		return db.StmtWrite // unknown: conservative
	}
	return classify(st)
}

// CompletionContext: "db.|" expects a collection; "db.coll.x|" and words
// inside the arguments reference the collection (its fields complete).
func (*language) CompletionContext(stmt string, caret int) db.CompletionContext {
	caret = max(0, min(caret, len(stmt)))
	var ctx db.CompletionContext
	i := caret
	for i > 0 {
		c := stmt[i-1]
		if c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80 {
			i--
			continue
		}
		break
	}
	ctx.Prefix = stmt[i:caret]
	ctx.QualStart = i
	// the chain before: db.coll.
	j := i
	var chain []string
	for j > 0 && stmt[j-1] == '.' {
		k := j - 1
		s := k
		for s > 0 && (identRune(rune(stmt[s-1])) || stmt[s-1] >= 0x80) {
			s--
		}
		if s == k {
			break
		}
		chain = append([]string{stmt[s:k]}, chain...)
		j = s
	}
	ctx.QualStart = j
	if len(chain) == 1 && chain[0] == "db" {
		ctx.ExpectObject = true // db.|: collections
		return ctx
	}
	// collection referenced by the statement: db.name or db.getCollection("name")
	toks := tokens(stmt)
	var sig []string
	for _, t := range toks {
		if t.Kind != sqlx.TSpace && t.Kind != sqlx.TComment {
			sig = append(sig, stmt[t.Start:t.End])
		}
	}
	for k := 0; k+2 < len(sig); k++ {
		if sig[k] != "db" || sig[k+1] != "." {
			continue
		}
		name := sig[k+2]
		if name == "getCollection" && k+4 < len(sig) && sig[k+3] == "(" {
			if s, err := unquote(sig[k+4]); err == nil {
				name = s
			}
		} else if k+3 < len(sig) && sig[k+3] == "(" {
			continue // a db method
		}
		ctx.Refs = append(ctx.Refs, db.CompletionRef{Name: name})
		break
	}
	return ctx
}

func (*language) QuoteIdent(name string) string { return name }

// collExpr is the shell expression of a collection.
func collExpr(ref db.ObjectRef) string {
	if identRe.MatchString(ref.Name) && !dbProps[ref.Name] {
		return "db." + ref.Name
	}
	return "db.getCollection(" + strconv.Quote(ref.Name) + ")"
}

// dbProps are db members a collection name would shadow.
var dbProps = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range shellFunctions {
		m[f] = true
	}
	for _, f := range []string{"getName", "hello", "isMaster", "hostInfo", "stats", "aggregate"} {
		m[f] = true
	}
	return m
}()

func (l *language) Browse(ref db.ObjectRef, q db.BrowseQuery) (string, error) {
	f, err := filterText(q.Filter)
	if err != nil {
		return "", err
	}
	s := collExpr(ref) + ".find(" + f
	if p := strings.TrimSpace(q.Projection); p != "" {
		if err := l.ValidateProjection(p); err != nil {
			return "", err
		}
		s += ", " + p
	}
	s += ")"
	if len(q.Sort) > 0 {
		s += ".sort(" + l.FormatSort(q.Sort) + ")"
	}
	if q.Limit > 0 {
		s += ".limit(" + strconv.FormatInt(q.Limit, 10) + ")"
	}
	return s, nil
}

func (*language) ValidateProjection(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, err := parseDocument(text); err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	return nil
}

func (*language) ParseSort(text string) ([]db.SortKey, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	d, err := parseDocument(text)
	if err != nil {
		return nil, fmt.Errorf("sort: %w", err)
	}
	var out []db.SortKey
	for _, e := range d {
		n, ok := toFloat(e.Value)
		if !ok || n != 1 && n != -1 {
			return nil, fmt.Errorf("sort: %s must be 1 or -1", e.Key)
		}
		out = append(out, db.SortKey{Field: e.Key, Desc: n < 0})
	}
	return out, nil
}

func (*language) FormatSort(keys []db.SortKey) string {
	d := bson.D{}
	for _, k := range keys {
		dir := int32(1)
		if k.Desc {
			dir = -1
		}
		d = append(d, bson.E{Key: k.Field, Value: dir})
	}
	return shellLit(d)
}

func (*language) Operators() []string { return operators }

func (*language) FilterLabel() string { return "Filter" }

func (*language) MenuLabels() (string, string) {
	return "Find in new console", "Generate insert / update / delete…"
}

// FormatDocument writes a document as indented shell text.
func (*language) FormatDocument(d db.DocNode) string {
	var b strings.Builder
	writeNode(&b, d, 0)
	return b.String()
}

// inline reports whether a node fits on one line (no nested documents,
// short).
func inline(n db.DocNode) bool {
	if len(n.Children) == 0 {
		return true
	}
	size := 0
	for _, c := range n.Children {
		if len(c.Children) > 0 || c.IsObject() {
			return false
		}
		size += len(c.Lit) + len(c.Name) + 2
	}
	return n.IsArray() && size < 60
}

func writeNode(b *strings.Builder, n db.DocNode, depth int) {
	switch {
	case n.IsObject() && len(n.Children) == 0:
		b.WriteString("{}")
	case n.IsArray() && inline(n):
		b.WriteString("[")
		for i, c := range n.Children {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(c.Lit)
		}
		b.WriteString("]")
	case n.IsObject() || n.IsArray():
		open, end := "{", "}"
		if n.IsArray() {
			open, end = "[", "]"
		}
		pad := strings.Repeat("  ", depth+1)
		b.WriteString(open + "\n")
		for i, c := range n.Children {
			b.WriteString(pad)
			if n.IsObject() {
				b.WriteString(key(c.Name) + ": ")
			}
			writeNode(b, c, depth+1)
			if i < len(n.Children)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + end)
	default:
		b.WriteString(n.Lit)
	}
}

// DocumentChanges diffs an edited document with the original, field by
// field into nested documents: changed or new fields are set by path,
// missing ones removed. Arrays and values of other types are replaced
// whole; the _id can't change.
func (l *language) DocumentChanges(orig db.DocNode, edited string) ([]db.ColValue, []db.ColValue, error) {
	id, ok := orig.Field("_id")
	if !ok {
		return nil, nil, errors.New("the document has no _id: it can't be updated")
	}
	key := []db.ColValue{{Column: "_id", Kind: id.Kind, Value: db.Raw(id.Lit)}}
	before, err := parseDocument(l.FormatDocument(orig))
	if err != nil {
		return nil, nil, fmt.Errorf("original document: %w", err)
	}
	after, err := parseDocument(edited)
	if err != nil {
		return nil, nil, err
	}
	if nid, ok := field(after, "_id"); !ok || shellLit(nid) != id.Lit {
		return nil, nil, errors.New("the _id can't be changed (insert a copy and delete the original)")
	}
	var vals []db.ColValue
	var diff func(prefix string, a, b bson.D) error
	diff = func(prefix string, a, b bson.D) error {
		for _, e := range b {
			if prefix == "" && e.Key == "_id" {
				continue
			}
			if !plainKeys(bson.D{e}) {
				return fmt.Errorf("field %q can't be edited by path: change it in the console", prefix+e.Key)
			}
			p := prefix + e.Key
			av, had := field(a, e.Key)
			ad, aIsDoc := av.(bson.D)
			bd, bIsDoc := e.Value.(bson.D)
			switch {
			case !had:
				vals = append(vals, db.ColValue{Column: p, Kind: kindOf(e.Value), Value: db.Raw(shellLit(e.Value))})
			case aIsDoc && bIsDoc && len(ad) > 0 && len(bd) > 0 && plainKeys(ad) && plainKeys(bd):
				if err := diff(p+".", ad, bd); err != nil {
					return err
				}
			case shellLit(av) != shellLit(e.Value):
				vals = append(vals, db.ColValue{Column: p, Kind: kindOf(e.Value), Value: db.Raw(shellLit(e.Value))})
			}
		}
		for _, e := range a {
			if _, ok := field(b, e.Key); !ok {
				vals = append(vals, db.ColValue{Column: prefix + e.Key, Value: db.Unset})
			}
		}
		return nil
	}
	if err := diff("", before, after); err != nil {
		return nil, nil, err
	}
	return key, vals, nil
}

// NewDocument reads a document to insert.
func (*language) NewDocument(text string) ([]db.ColValue, error) {
	d, err := parseDocument(text)
	if err != nil {
		return nil, err
	}
	vals := make([]db.ColValue, 0, len(d))
	for _, e := range d {
		vals = append(vals, db.ColValue{Column: e.Key, Kind: kindOf(e.Value), Value: db.Raw(shellLit(e.Value))})
	}
	return vals, nil
}

// filterText validates a filter and returns its text ("{}" for none).
func filterText(filter string) (string, error) {
	f := strings.TrimSpace(filter)
	if f == "" {
		return "{}", nil
	}
	if _, err := parseDocument(f); err != nil {
		return "", fmt.Errorf("filter: %w", err)
	}
	return f, nil
}

func (l *language) Count(ref db.ObjectRef, filter string) (string, error) {
	f, err := filterText(filter)
	if err != nil {
		return "", err
	}
	return collExpr(ref) + ".countDocuments(" + f + ")", nil
}

// ValidateFilter accepts one filter document ($where and $function run
// server-side JavaScript but can't write).
func (l *language) ValidateFilter(filter string) error {
	_, err := filterText(filter)
	return err
}

func (l *language) EqualsFilter(field string, v any, k db.Kind) string {
	bv, err := toBSON(v, k, nil)
	if err != nil {
		bv = v
	}
	return shellLit(bson.D{{Key: field, Value: bv}})
}

func (*language) FilterHint() string {
	return "filter, e.g. {status: \"A\", qty: {$lt: 30}}  (Enter to apply)"
}

func (*language) SortUnsupported() string { return "" }

func (*language) DisplayName(ref db.ObjectRef) string {
	if ref.Schema == "" {
		return ref.Name
	}
	return ref.Schema + "." + ref.Name
}

func (l *language) OpenTemplate(ref db.ObjectRef) string {
	return collExpr(ref) + ".find({}).limit(100);"
}

func (l *language) DropStatement(ref db.ObjectRef, truncate bool) (string, string, bool) {
	if ref.Type != db.ObjCollection && ref.Type != db.ObjView {
		return "", "", false
	}
	if truncate {
		if ref.Type == db.ObjView {
			return "", "", false
		}
		return collExpr(ref) + ".deleteMany({})", "Delete all documents", true
	}
	return collExpr(ref) + ".drop()", "Drop", true
}

// sample is a placeholder value of a field type for templates.
func sample(c db.TableColumn) any {
	switch strings.ToLower(strings.SplitN(c.Type, "|", 2)[0]) {
	case "objectid":
		return bson.NewObjectID()
	case "int":
		return int32(0)
	case "long":
		return int64(0)
	case "double":
		return float64(0.5)
	case "decimal":
		d, _ := bson.ParseDecimal128("0")
		return d
	case "bool":
		return false
	case "date":
		return bson.DateTime(0)
	case "object":
		return bson.D{}
	case "array":
		return bson.A{}
	}
	return ""
}

func (l *language) Templates(ref db.ObjectRef, info *db.TableInfo) string {
	coll := collExpr(ref)
	doc := bson.D{}
	set := bson.D{}
	for _, c := range info.Columns {
		if c.Name == "_id" {
			continue
		}
		doc = setPath(doc, c.Name, sample(c)) // dotted columns: nested documents
		if len(set) < 3 {
			set = append(set, bson.E{Key: c.Name, Value: sample(c)})
		}
	}
	var id any = bson.NewObjectID()
	if c, ok := info.Column("_id"); ok {
		id = sample(c)
		if id == "" {
			id = "…"
		}
	}
	key := bson.D{{Key: "_id", Value: id}}
	var b strings.Builder
	fmt.Fprintf(&b, "%s.find({}).limit(100);\n\n", coll)
	fmt.Fprintf(&b, "%s.insertOne(%s);\n\n", coll, shellLit(doc))
	if len(set) > 0 {
		fmt.Fprintf(&b, "%s.updateOne(%s, {$set: %s});\n\n", coll, shellLit(key), shellLit(set))
	}
	fmt.Fprintf(&b, "%s.deleteOne(%s);\n", coll, shellLit(key))
	return b.String()
}

// changeDoc renders the values of a change as a document.
func changeDoc(vals []db.ColValue) bson.D {
	d := bson.D{}
	for _, cv := range vals {
		v, err := toBSON(cv.Value, cv.Kind, nil)
		if err != nil {
			v = cv.Value
		}
		d = append(d, bson.E{Key: cv.Column, Value: v})
	}
	return d
}

// insertDoc builds the document of an insert: dotted columns become
// nested documents, empty cells and removed fields are left out.
func insertDoc(vals []db.ColValue, like func(string) any) (bson.D, error) {
	d := bson.D{}
	for _, cv := range vals {
		if cv.Value == nil || cv.Value == db.Unset {
			continue
		}
		v, err := toBSON(cv.Value, cv.Kind, like(cv.Column))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cv.Column, err)
		}
		if _, top := field(d, cv.Column); top || !strings.Contains(cv.Column, ".") {
			d = append(d, bson.E{Key: cv.Column, Value: v})
			continue
		}
		d = setPath(d, cv.Column, v)
	}
	return d, nil
}

// updateDoc builds {$set: …, $unset: …} of an update by path. like
// returns the stored value of a path (its type is kept).
func updateDoc(vals []db.ColValue, like func(string) any) (bson.D, error) {
	set, unset := bson.D{}, bson.D{}
	for _, cv := range vals {
		if cv.Value == db.Unset {
			unset = append(unset, bson.E{Key: cv.Column, Value: ""})
			continue
		}
		v, err := toBSON(cv.Value, cv.Kind, like(cv.Column))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cv.Column, err)
		}
		set = append(set, bson.E{Key: cv.Column, Value: v})
	}
	upd := bson.D{}
	if len(set) > 0 {
		upd = append(upd, bson.E{Key: "$set", Value: set})
	}
	if len(unset) > 0 {
		upd = append(upd, bson.E{Key: "$unset", Value: unset})
	}
	return upd, nil
}

func noLike(string) any { return nil }

func (l *language) RenderChange(ref db.ObjectRef, ch db.Change) string {
	coll := collExpr(ref)
	switch ch.Op {
	case db.OpInsert:
		d, err := insertDoc(ch.Values, noLike)
		if err != nil {
			return "// " + err.Error()
		}
		return coll + ".insertOne(" + shellLit(d) + ")"
	case db.OpUpdate:
		// a NULL cell sets null (the field stays); removed fields are $unset
		upd, err := updateDoc(ch.Values, noLike)
		if err != nil {
			return "// " + err.Error()
		}
		return coll + ".updateOne(" + shellLit(changeDoc(ch.Key)) + ", " + shellLit(upd) + ")"
	}
	return coll + ".deleteOne(" + shellLit(changeDoc(ch.Key)) + ")"
}

func (l *language) InsertText(ref db.ObjectRef, cols []db.Column, r db.Row) string {
	d := bson.D{}
	for i, v := range r {
		if v == nil {
			continue // a missing field, not null
		}
		bv, err := toBSON(v, cols[i].Kind, nil)
		if err != nil {
			return ""
		}
		d = append(d, bson.E{Key: cols[i].Name, Value: bv})
	}
	return collExpr(ref) + ".insertOne(" + shellLit(d) + ");"
}

func (*language) InsertFormat() (string, string) { return "MongoDB insertOne", ".js" }

func (*language) Explain(stmt string) (string, error) {
	st, err := parseStatement(strings.TrimSpace(stmt))
	if err != nil {
		return "", err
	}
	if st.kind != "coll" || st.method() != "find" && st.method() != "aggregate" {
		return "", fmt.Errorf("only find and aggregate can be explained: %w", db.ErrNotSupported)
	}
	if _, ok := hasCall(st, "explain"); ok {
		return strings.TrimSuffix(strings.TrimSpace(stmt), ";"), nil
	}
	return strings.TrimSuffix(strings.TrimSpace(stmt), ";") + `.explain("queryPlanner")`, nil
}

// ColumnNames lists the keys and field references of a statement (the
// assistant redacts results reading sensitive fields).
func (*language) ColumnNames(stmt string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, p := range strings.Split(strings.TrimPrefix(s, "$"), ".") {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, t := range tokens(stmt) {
		w := stmt[t.Start:t.End]
		switch t.Kind {
		case sqlx.TWord, sqlx.TParam:
			add(w)
		case sqlx.TString:
			if s, err := unquote(w); err == nil {
				add(s)
			}
		}
	}
	sort.Strings(out)
	return out
}
