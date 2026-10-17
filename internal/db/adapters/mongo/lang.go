package mongo

import (
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

var _ db.Language = (*language)(nil)

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
	s := collExpr(ref) + ".find(" + f + ")"
	if len(q.Sort) > 0 {
		d := bson.D{}
		for _, k := range q.Sort {
			dir := int32(1)
			if k.Desc {
				dir = -1
			}
			d = append(d, bson.E{Key: k.Field, Value: dir})
		}
		s += ".sort(" + shellLit(d) + ")"
	}
	return s, nil
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
		if c.Name == "_id" || strings.Contains(c.Name, ".") {
			continue
		}
		doc = append(doc, bson.E{Key: c.Name, Value: sample(c)})
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

func (l *language) RenderChange(ref db.ObjectRef, ch db.Change) string {
	coll := collExpr(ref)
	switch ch.Op {
	case db.OpInsert:
		return coll + ".insertOne(" + shellLit(changeDoc(ch.Values)) + ")"
	case db.OpUpdate:
		// a NULL cell sets null (the field stays)
		upd := bson.D{{Key: "$set", Value: changeDoc(ch.Values)}}
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
