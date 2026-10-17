package db

import (
	"slices"
	"strings"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// StmtKind classifies a statement for safety policies.
type StmtKind int

const (
	StmtRead        StmtKind = iota // SELECT, SHOW, EXPLAIN…
	StmtSession                     // SET, USE: session settings only
	StmtTx                          // BEGIN, COMMIT, ROLLBACK, SAVEPOINT
	StmtWrite                       // INSERT, UPDATE … WHERE, CALL…
	StmtDDL                         // CREATE, ALTER, GRANT…
	StmtDestructive                 // DROP, TRUNCATE, DELETE/UPDATE without WHERE
)

func (k StmtKind) String() string {
	switch k {
	case StmtRead:
		return "read"
	case StmtSession:
		return "session"
	case StmtTx:
		return "transaction"
	case StmtWrite:
		return "write"
	case StmtDDL:
		return "DDL"
	}
	return "destructive"
}

// Modifies reports whether the statement can change data or schema.
func (k StmtKind) Modifies() bool { return k >= StmtWrite }

// Classify inspects a statement for the safety policies. It is
// deliberately conservative: unknown statements count as writes, and a
// read statement using a data-changing verb anywhere (SQL Server runs
// "SELECT 1 DELETE FROM t" as two statements) is a write. Words used as
// names (t.update, AS load, a column "lock" in a select list) are not verbs.
func Classify(stmt string, o *sqlx.Options) StmtKind {
	toks := classifyTokens(stmt, o, 0)
	var ws []cword
	for i, t := range toks {
		if t.kind != sqlx.TWord {
			continue
		}
		w := cword{text: t.text, depth: t.depth}
		if i > 0 {
			w.prev = toks[i-1].text
		}
		if i+1 < len(toks) {
			w.next, w.nextKind = toks[i+1].text, toks[i+1].kind
		}
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		return StmtRead // comments only
	}
	words := make([]string, len(ws))
	for i, w := range ws {
		words[i] = w.text
	}
	k := classifyFirst(words, ws, stmt)
	switch words[0] {
	case "SHOW", "LIST", "HELP":
		return k // SHOW CREATE TABLE … only reports
	case "EXPLAIN", "DESCRIBE", "DESC":
		if !slices.Contains(words, "ANALYZE") && !slices.Contains(words, "ANALYSE") {
			return k // a plain plan doesn't run the statement
		}
	}
	definer := words[0] == "CREATE" || words[0] == "ALTER" || words[0] == "GRANT" || words[0] == "REVOKE" || words[0] == "DENY"
	// any further verb raises the class (batches without separators,
	// verbs hidden in subqueries or CTEs)
	for _, w := range ws[1:] {
		switch {
		case w.prev == "." || w.next == "." || w.prev == "AS":
			continue // a qualified name or an alias
		case w.text == "UPDATE" && w.prev == "FOR":
			k = max(k, StmtWrite) // SELECT … FOR UPDATE takes row locks
		case sideEffectFuncs[w.text]:
			if w.next == "(" {
				k = max(k, StmtWrite)
			}
		case !w.verbLike():
		case w.text == "DROP" || w.text == "TRUNCATE":
			if !definer {
				k = max(k, StmtDestructive)
			}
		case dmlVerbs[w.text]:
			if !definer {
				k = max(k, StmtWrite)
			}
		case ddlVerbs[w.text]:
			k = max(k, StmtWrite)
		}
	}
	return k
}

// verbLike reports whether a word is used as a statement verb: followed by
// a name or keyword that continues a statement, not by a comma, an
// operator or a clause keyword (SELECT lock, load FROM t).
func (w cword) verbLike() bool {
	if w.nextKind == sqlx.TQuotedIdent || w.nextKind == sqlx.TString && w.text == "COPY" {
		return true
	}
	if w.nextKind != sqlx.TWord {
		return false
	}
	switch w.next {
	case "FROM":
		return w.text == "DELETE" // DELETE FROM t
	case "AS", "WHERE", "AND", "OR", "ORDER", "GROUP", "LIMIT", "IS", "IN", "NOT", "LIKE", "BETWEEN", "THEN",
		"ELSE", "END", "WHEN", "ON", "JOIN", "UNION", "HAVING", "DESC", "ASC", "NULLS", "OFFSET", "FETCH",
		"LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "FULL", "USING", "INTERSECT", "EXCEPT", "WINDOW", "OVER":
		return false
	}
	return true
}

// dmlVerbs change data wherever they appear in a statement.
var dmlVerbs = map[string]bool{"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "UPSERT": true,
	"CALL": true, "EXEC": true, "EXECUTE": true, "COPY": true, "LOAD": true, "HANDLER": true, "LOCK": true}

// ddlVerbs found after the first word of a read statement.
var ddlVerbs = map[string]bool{"CREATE": true, "ALTER": true, "GRANT": true, "REVOKE": true, "RENAME": true}

// sideEffectFuncs are functions with effects outside the query, callable
// from a plain SELECT.
var sideEffectFuncs = map[string]bool{
	"PG_TERMINATE_BACKEND": true, "PG_CANCEL_BACKEND": true, "PG_RELOAD_CONF": true, "PG_ROTATE_LOGFILE": true,
	"PG_PROMOTE": true, "PG_SWITCH_WAL": true, "PG_CREATE_RESTORE_POINT": true, "PG_ADVISORY_LOCK": true,
	"PG_ADVISORY_XACT_LOCK": true, "PG_TRY_ADVISORY_LOCK": true, "PG_FILE_WRITE": true, "PG_FILE_UNLINK": true,
	"PG_FILE_RENAME": true, "LO_IMPORT": true, "LO_EXPORT": true, "LO_UNLINK": true, "LO_CREATE": true,
	"DBLINK_EXEC": true, "DBLINK": true, "SET_CONFIG": true, "NEXTVAL": true, "SETVAL": true,
	"PG_CREATE_LOGICAL_REPLICATION_SLOT": true, "PG_CREATE_PHYSICAL_REPLICATION_SLOT": true, "PG_DROP_REPLICATION_SLOT": true,
	"GET_LOCK": true, "SYS_EXEC": true, "SYS_EVAL": true, "XP_CMDSHELL": true, "OPENROWSET": true, "OPENQUERY": true,
	"LOAD_EXTENSION": true, "WRITEFILE": true,
}

// readPragmas take an argument but only report.
var readPragmas = map[string]bool{"TABLE_INFO": true, "TABLE_XINFO": true, "INDEX_LIST": true, "INDEX_INFO": true,
	"INDEX_XINFO": true, "FOREIGN_KEY_LIST": true, "FOREIGN_KEY_CHECK": true, "INTEGRITY_CHECK": true,
	"QUICK_CHECK": true, "TABLE_LIST": true}

type cword struct {
	text       string
	depth      int    // parenthesis depth
	prev, next string // neighbor tokens (words upper-cased)
	nextKind   sqlx.TokKind
}

// ctok is a significant token (no spaces or comments).
type ctok struct {
	text  string
	kind  sqlx.TokKind
	depth int
}

// classifyTokens returns the significant tokens of stmt, words
// upper-cased, with their parenthesis depth. MySQL executable comments
// (/*! … */, /*M! … */) are code there: their content is included.
func classifyTokens(stmt string, o *sqlx.Options, level int) []ctok {
	var out []ctok
	depth := 0
	mysql := o != nil && o.DashSpace // MySQL rules
	for _, t := range sqlx.Tokens(stmt, o) {
		txt := stmt[t.Start:t.End]
		switch t.Kind {
		case sqlx.TSpace:
			continue
		case sqlx.TWord:
			out = append(out, ctok{strings.ToUpper(txt), t.Kind, depth})
			continue
		case sqlx.TPunct:
			switch txt {
			case "(":
				depth++
			case ")":
				depth = max(0, depth-1)
			}
		case sqlx.TComment:
			inner, ok := strings.CutPrefix(txt, "/*!")
			if !ok {
				inner, ok = strings.CutPrefix(txt, "/*M!")
			}
			if ok && mysql && level < 4 {
				inner = strings.TrimSuffix(inner, "*/")
				inner = strings.TrimLeft(inner, "0123456789") // version
				for _, x := range classifyTokens(inner, o, level+1) {
					x.depth += depth
					out = append(out, x)
				}
			}
			continue
		case sqlx.TParam:
			// MySQL system variables: @@global.x
			if u := strings.ToUpper(txt); strings.HasPrefix(u, "@@GLOBAL") || strings.HasPrefix(u, "@@PERSIST") {
				out = append(out, ctok{"GLOBAL", sqlx.TWord, depth})
				continue
			}
		}
		out = append(out, ctok{txt, t.Kind, depth})
	}
	return out
}

func classifyFirst(words []string, ws []cword, stmt string) StmtKind {
	has := func(w string) bool {
		for _, x := range words {
			if x == w {
				return true
			}
		}
		return false
	}
	topWhere := func() bool {
		for _, w := range ws {
			if w.text == "WHERE" && w.depth == 0 {
				return true
			}
		}
		return false
	}
	analyze := has("ANALYZE") || has("ANALYSE")
	switch words[0] {
	case "SELECT", "WITH", "VALUES", "TABLE":
		if has("INTO") {
			return StmtWrite // SELECT … INTO new_table / OUTFILE / variables
		}
		return StmtRead
	case "SHOW", "LIST", "HELP":
		return StmtRead
	case "EXPLAIN", "DESCRIBE", "DESC":
		if analyze && (has("EXECUTE") || has("EXEC")) {
			return StmtWrite
		}
		return StmtRead // the verbs of an analyzed statement raise it in Classify
	case "PRAGMA":
		name := ""
		if len(words) > 1 {
			name = words[len(words)-1]
			if len(words) > 2 && words[1] != "" {
				name = words[1]
				if strings.Contains(stmt, ".") && len(words) > 2 {
					name = words[2] // PRAGMA schema.name
				}
			}
		}
		switch {
		case strings.Contains(stmt, "="):
			return StmtWrite
		case strings.Contains(stmt, "("):
			if readPragmas[name] {
				return StmtRead
			}
			return StmtWrite
		case name == "OPTIMIZE" || name == "WAL_CHECKPOINT" || name == "INCREMENTAL_VACUUM" || name == "SHRINK_MEMORY":
			return StmtWrite
		}
		return StmtRead
	case "SET", "USE", "RESET":
		for i, w := range words {
			switch w {
			case "GLOBAL", "PERSIST", "PERSIST_ONLY", "PASSWORD", "QUERY_ONLY", "DEFAULT_TRANSACTION_READ_ONLY",
				"TRANSACTION_READ_ONLY", "TX_READ_ONLY", "READ_ONLY", "SUPER_READ_ONLY", "AUTHORIZATION", "IDENTITY_INSERT":
				return StmtWrite // server-wide settings or lifting read-only mode
			case "READ":
				if i+1 < len(words) && words[i+1] == "WRITE" {
					return StmtWrite
				}
			}
		}
		if len(words) > 1 && (words[1] == "TRANSACTION" || words[1] == "SESSION" && has("CHARACTERISTICS")) {
			return StmtTx
		}
		return StmtSession
	case "START":
		if len(words) > 1 && words[1] == "TRANSACTION" && !(has("READ") && has("WRITE")) {
			return StmtTx
		}
		return StmtWrite // START REPLICA, START SLAVE…
	case "BEGIN":
		if len(words) == 1 {
			return StmtTx
		}
		switch words[1] {
		case "TRANSACTION", "WORK", "TRAN", "DEFERRED", "IMMEDIATE", "EXCLUSIVE", "ISOLATION", "READ", "NOT", "DISTRIBUTED":
			if has("READ") && has("WRITE") {
				return StmtWrite
			}
			return StmtTx
		}
		return StmtWrite // anonymous PL/SQL block
	case "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE", "ABORT":
		return StmtTx
	case "DROP", "TRUNCATE":
		return StmtDestructive
	case "DELETE", "UPDATE":
		if !topWhere() {
			return StmtDestructive
		}
		return StmtWrite
	case "ALTER":
		if has("DROP") {
			return StmtDestructive
		}
		return StmtDDL
	case "CREATE", "RENAME", "COMMENT", "GRANT", "REVOKE", "DENY", "REFRESH", "ANALYZE", "ANALYSE", "VACUUM", "REINDEX", "CLUSTER", "OPTIMIZE":
		return StmtDDL
	}
	return StmtWrite
}
