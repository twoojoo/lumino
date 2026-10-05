package db

import (
	"strings"

	"github.com/giovanni/lumino/internal/db/sqlx"
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

// Classify inspects the leading words of a statement (and WHERE presence
// for DELETE/UPDATE). Unknown statements count as writes.
func Classify(stmt string, o *sqlx.Options) StmtKind {
	toks := sqlx.Tokens(stmt, o)
	var words []string
	for _, t := range toks {
		if t.Kind == sqlx.TWord {
			words = append(words, strings.ToUpper(stmt[t.Start:t.End]))
		}
	}
	if len(words) == 0 {
		return StmtRead // comments only
	}
	has := func(w string) bool {
		for _, x := range words {
			if x == w {
				return true
			}
		}
		return false
	}
	switch words[0] {
	case "SELECT":
		if has("INTO") && !has("FROM") || has("INTO") && intoBeforeFrom(words) {
			return StmtWrite // SELECT … INTO new_table
		}
		return StmtRead
	case "WITH":
		for _, w := range words {
			switch w {
			case "INSERT", "UPDATE", "DELETE", "MERGE":
				return StmtWrite
			}
		}
		return StmtRead
	case "SHOW", "DESCRIBE", "DESC", "VALUES", "TABLE", "LIST", "HELP":
		return StmtRead
	case "EXPLAIN":
		if has("ANALYZE") {
			for _, w := range words {
				switch w {
				case "INSERT", "UPDATE", "DELETE", "MERGE", "CREATE":
					return StmtWrite
				}
			}
		}
		return StmtRead
	case "PRAGMA":
		if strings.Contains(stmt, "=") {
			return StmtWrite
		}
		return StmtRead
	case "SET", "USE", "RESET":
		if len(words) > 1 && (words[1] == "TRANSACTION" || words[1] == "SESSION" && has("CHARACTERISTICS")) {
			return StmtTx
		}
		return StmtSession
	case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
		if words[0] == "BEGIN" && len(words) > 1 && words[1] != "TRANSACTION" && words[1] != "WORK" && words[1] != "TRAN" {
			return StmtWrite // anonymous PL/SQL block
		}
		return StmtTx
	case "DROP", "TRUNCATE":
		return StmtDestructive
	case "DELETE", "UPDATE":
		if !has("WHERE") {
			return StmtDestructive
		}
		return StmtWrite
	case "ALTER":
		if has("DROP") {
			return StmtDestructive
		}
		return StmtDDL
	case "CREATE", "RENAME", "COMMENT", "GRANT", "REVOKE", "DENY", "REFRESH", "ANALYZE", "VACUUM", "REINDEX", "CLUSTER", "OPTIMIZE":
		return StmtDDL
	}
	return StmtWrite
}

func intoBeforeFrom(words []string) bool {
	for _, w := range words {
		switch w {
		case "INTO":
			return true
		case "FROM":
			return false
		}
	}
	return false
}
