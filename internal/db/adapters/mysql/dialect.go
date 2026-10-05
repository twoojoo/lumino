package mysql

import (
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

var reserved = db.WordSet(`
ACCESSIBLE ADD ALL ALTER ANALYZE AND AS ASC ASENSITIVE BEFORE BETWEEN BIGINT BINARY BLOB BOTH BY CALL CASCADE CASE
CHANGE CHAR CHARACTER CHECK COLLATE COLUMN CONDITION CONSTRAINT CONTINUE CONVERT CREATE CROSS CUBE CUME_DIST
CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR DATABASE DATABASES DAY_HOUR DAY_MICROSECOND
DAY_MINUTE DAY_SECOND DEC DECIMAL DECLARE DEFAULT DELAYED DELETE DENSE_RANK DESC DESCRIBE DETERMINISTIC DISTINCT
DISTINCTROW DIV DOUBLE DROP DUAL EACH ELSE ELSEIF EMPTY ENCLOSED ESCAPED EXCEPT EXISTS EXIT EXPLAIN FALSE FETCH
FIRST_VALUE FLOAT FLOAT4 FLOAT8 FOR FORCE FOREIGN FROM FULLTEXT FUNCTION GENERATED GET GRANT GROUP GROUPING GROUPS
HAVING HIGH_PRIORITY HOUR_MICROSECOND HOUR_MINUTE HOUR_SECOND IF IGNORE IN INDEX INFILE INNER INOUT INSENSITIVE
INSERT INT INT1 INT2 INT3 INT4 INT8 INTEGER INTERSECT INTERVAL INTO IO_AFTER_GTIDS IO_BEFORE_GTIDS IS ITERATE JOIN
JSON_TABLE KEY KEYS KILL LAG LAST_VALUE LATERAL LEAD LEADING LEAVE LEFT LIKE LIMIT LINEAR LINES LOAD LOCALTIME
LOCALTIMESTAMP LOCK LONG LONGBLOB LONGTEXT LOOP LOW_PRIORITY MASTER_BIND MASTER_SSL_VERIFY_SERVER_CERT MATCH
MAXVALUE MEDIUMBLOB MEDIUMINT MEDIUMTEXT MIDDLEINT MINUTE_MICROSECOND MINUTE_SECOND MOD MODIFIES NATURAL NOT
NO_WRITE_TO_BINLOG NTH_VALUE NTILE NULL NUMERIC OF ON OPTIMIZE OPTIMIZER_COSTS OPTION OPTIONALLY OR ORDER OUT OUTER
OUTFILE OVER PARTITION PERCENT_RANK PRECISION PRIMARY PROCEDURE PURGE RANGE RANK READ READS READ_WRITE REAL RECURSIVE
REFERENCES REGEXP RELEASE RENAME REPEAT REPLACE REQUIRE RESIGNAL RESTRICT RETURN REVOKE RIGHT RLIKE ROW ROWS
ROW_NUMBER SCHEMA SCHEMAS SECOND_MICROSECOND SELECT SENSITIVE SEPARATOR SET SHOW SIGNAL SMALLINT SPATIAL SPECIFIC SQL
SQLEXCEPTION SQLSTATE SQLWARNING SQL_BIG_RESULT SQL_CALC_FOUND_ROWS SQL_SMALL_RESULT SSL STARTING STORED
STRAIGHT_JOIN SYSTEM TABLE TERMINATED THEN TINYBLOB TINYINT TINYTEXT TO TRAILING TRIGGER TRUE UNDO UNION UNIQUE
UNLOCK UNSIGNED UPDATE USAGE USE USING UTC_DATE UTC_TIME UTC_TIMESTAMP VALUES VARBINARY VARCHAR VARCHARACTER VARYING
VIRTUAL WHEN WHERE WHILE WINDOW WITH WRITE XOR YEAR_MONTH ZEROFILL
`)

var keywords = db.Merge(reserved, db.CommonKeywords, db.WordSet(`
AFTER ALGORITHM AUTO_INCREMENT BEGIN BTREE CHARSET COLUMNS COMMENT COMMIT COMMITTED DEFINER DELIMITER DO ENGINE
ENGINES ENUM EVENT EVENTS EVERY FIELDS FLUSH FULL HANDLER HASH INVOKER ISOLATION LEVEL LOCAL LOGS MODE NAMES OPEN
PROCESSLIST QUERY REPEATABLE ROLLBACK SAVEPOINT SECURITY SERIALIZABLE SESSION SIGNED START STATUS TABLES TEMPORARY
TEXT TRANSACTION TRUNCATE UNCOMMITTED VARIABLES VIEW WARNINGS WORK SEQUENCE NOCACHE NOCYCLE INCREMENT
DATETIME TIMESTAMP DATE TIME JSON BIT BOOLEAN BOOL
`))

var functions = append(db.CommonFunctions, strings.Fields(`
concat_ws date_add date_sub date_format datediff curdate curtime now sysdate unix_timestamp from_unixtime
str_to_date ifnull if isnull group_concat json_extract json_object json_array json_unquote json_set
json_contains json_length json_keys json_arrayagg json_objectagg last_insert_id found_rows row_count uuid
md5 sha1 sha2 hex unhex char_length locate instr lpad rpad left right repeat reverse format conv
timestampdiff timestampadd week year month day hour minute second database user version
`)...)

var types = strings.Fields(`
tinyint smallint mediumint int integer bigint decimal numeric float double real bit boolean bool date datetime
timestamp time year char varchar binary varbinary tinyblob blob mediumblob longblob tinytext text mediumtext
longtext enum set json geometry point linestring polygon unsigned
`)

// lexOptions are MySQL's lexical rules (default sql_mode).
var lexOptions = sqlx.Options{
	Backticks: true, HashComments: true, BackslashEscape: true, DoubleQuoteStrings: true, AtParams: true,
	Delimiter: true, DashSpace: true,
	BlockStart: func(w []string) bool {
		if len(w) >= 2 && w[0] == "BEGIN" && w[1] == "NOT" { // MariaDB BEGIN NOT ATOMIC
			return true
		}
		if len(w) < 2 || (w[0] != "CREATE" && w[0] != "ALTER") {
			return false
		}
		for _, x := range w[1:] {
			switch x {
			case "PROCEDURE", "FUNCTION", "TRIGGER", "EVENT":
				return true
			case "TABLE", "VIEW", "INDEX", "DATABASE", "SCHEMA", "USER":
				return false
			}
		}
		return false
	},
}

// NewDialect builds the MySQL dialect.
func NewDialect(id string) *db.BaseDialect {
	return &db.BaseDialect{
		ID: id, QuoteL: "`", QuoteR: "`", Fold: db.FoldNone, Params: db.ParamQuestion,
		LexOpts: lexOptions, Reserved: reserved, KeywordSet: keywords, FuncList: functions, TypeList: types,
		StringBackslash: true,
	}
}

// KindOf maps MySQL type names (ColumnTypes and information_schema).
func KindOf(t string) db.Kind {
	t = strings.ToUpper(strings.TrimSpace(t))
	t = strings.TrimPrefix(t, "UNSIGNED ")
	if i := strings.IndexAny(t, "( "); i > 0 {
		t = t[:i]
	}
	switch t {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR", "BIT":
		// BIT(n) is shown and edited as an unsigned integer
		return db.KindInt
	case "FLOAT", "DOUBLE", "REAL":
		return db.KindFloat
	case "DECIMAL", "NUMERIC":
		return db.KindDecimal
	case "BOOL", "BOOLEAN":
		return db.KindBool
	case "DATE":
		return db.KindDate
	case "TIME":
		return db.KindTime
	case "DATETIME", "TIMESTAMP":
		return db.KindTimestamp
	case "CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "ENUM", "SET":
		return db.KindText
	case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY", "POINT",
		"LINESTRING", "POLYGON":
		return db.KindBytes
	case "JSON":
		return db.KindJSON
	}
	return db.KindOther
}
