package site

// Database is the hand-written part of a database page; ports, URL schemes
// and capabilities come from the adapter registered under Driver.
type Database struct {
	Slug   string // URL path segment
	Driver string // db.Info.ID of the adapter
	Name   string
	// Title and Description are the <title> and meta description.
	Title       string
	Description string
	Lib, LibURL string // Go client library
	Intro       []string
	Highlights  []string
	Notes       []string // limits and differences worth knowing
	// ExampleTitle/Example: a script showing what the console handles for
	// this dialect.
	ExampleTitle, Example string
}

var databases = []Database{
	{
		Slug: "postgresql", Driver: "postgres", Name: "PostgreSQL",
		Title:       "PostgreSQL GUI client and SQL editor — Lumino DB",
		Description: "Lumino is a free, native PostgreSQL GUI for Linux, Windows and macOS: SQL editor with completion, streaming data grid, inline editing, SSH tunnels and TLS.",
		Lib:         "jackc/pgx", LibURL: "https://github.com/jackc/pgx",
		Intro: []string{
			"Lumino connects to PostgreSQL with pgx, a pure-Go driver: nothing to install besides Lumino itself, no JDBC jars and no libpq.",
			"The navigator lists schemas, tables, views, materialized views, functions, procedures, sequences and triggers. Results stream from an open cursor, so a SELECT on a table with millions of rows shows the first page immediately and fetches more while you scroll.",
		},
		Highlights: []string{
			"Scripts with dollar-quoted function bodies ($$ … $$, $tag$ … $tag$) and nested /* */ comments are split correctly.",
			"Read-only connections open read-only sessions enforced by the server, not only by the client.",
			"Cancel a running statement with Esc (server-side cancel request).",
			"Auto-commit or manual transactions per console, with commit and rollback buttons.",
			"Table editing with a review of the generated INSERT/UPDATE/DELETE, saved in one transaction.",
			"TLS modes require, verify-ca and verify-full, client certificates, SSH tunnels.",
		},
		ExampleTitle: "Dollar quotes in a script",
		Example: `CREATE FUNCTION touch() RETURNS trigger AS $$
BEGIN
  NEW.updated_at := now(); -- the ";" inside $$ … $$ doesn't end the statement
  RETURN NEW;
END $$ LANGUAGE plpgsql;

SELECT id, email FROM customers WHERE updated_at > now() - interval '1 day';`,
	},
	{
		Slug: "cockroachdb", Driver: "cockroachdb", Name: "CockroachDB",
		Title:       "CockroachDB GUI client and SQL editor — Lumino DB",
		Description: "Browse and query CockroachDB with Lumino, a free native SQL IDE: schemas, streaming results, inline editing, read-only sessions, TLS client certificates.",
		Lib:         "jackc/pgx", LibURL: "https://github.com/jackc/pgx",
		Intro: []string{
			"CockroachDB speaks the PostgreSQL wire protocol; Lumino has a dedicated adapter on top of pgx that reads CockroachDB's own catalog, so databases, schemas, tables, views, materialized views and sequences show up as CockroachDB defines them.",
			"The default connection uses port 26257 and the defaultdb database, like the cockroach CLI.",
		},
		Highlights: []string{
			"Client certificate authentication over TLS (verify-full), as used by secure CockroachDB clusters.",
			"Read-only sessions enforced by the server for read-only connections.",
			"Streaming results: large scans show the first rows right away.",
			"Edit rows of tables with a primary key; every change is reviewed as SQL before saving.",
		},
		Notes: []string{
			"Functions, procedures and triggers are not listed in the navigator for CockroachDB.",
		},
		ExampleTitle: "Explore a cluster",
		Example: `SHOW DATABASES;
SELECT * FROM crdb_internal.ranges LIMIT 20;
EXPLAIN ANALYZE SELECT count(*) FROM orders WHERE created_at > now() - INTERVAL '7 days';`,
	},
	{
		Slug: "mysql", Driver: "mysql", Name: "MySQL",
		Title:       "MySQL GUI client and SQL editor — Lumino DB",
		Description: "Lumino is a free, native MySQL GUI for Linux, Windows and macOS: DELIMITER scripts, multiple result sets, inline table editing, SSH tunnels and TLS.",
		Lib:         "go-sql-driver/mysql", LibURL: "https://github.com/go-sql-driver/mysql",
		Intro: []string{
			"Lumino talks to MySQL with go-sql-driver/mysql, a pure-Go driver: no connector to download.",
			"Scripts written for the mysql command line client run as they are: DELIMITER changes, # comments, backtick identifiers and backslash escapes are understood by the script splitter and the highlighter.",
		},
		Highlights: []string{
			"DELIMITER // … // blocks for procedures and triggers.",
			"Multiple result sets from one statement (CALL of a procedure) are shown as separate result tabs.",
			"Read-only connections set SET SESSION TRANSACTION READ ONLY, so the server rejects writes.",
			"Navigator with databases, tables, views, functions, procedures and triggers.",
			"Inline editing of tables with a primary key, reviewed and saved in a transaction.",
			"SSH tunnels (password, key, agent) and TLS with client certificates.",
		},
		ExampleTitle: "DELIMITER in a script",
		Example: `DELIMITER //
CREATE PROCEDURE top_customers(IN n INT)
BEGIN
  SELECT id, name, balance FROM customers ORDER BY balance DESC LIMIT n;
END //
DELIMITER ;

CALL top_customers(10);`,
	},
	{
		Slug: "mariadb", Driver: "mariadb", Name: "MariaDB",
		Title:       "MariaDB GUI client and SQL editor — Lumino DB",
		Description: "Lumino is a free, native MariaDB client: SQL editor with completion, DELIMITER scripts, sequences, inline editing, SSH tunnels and TLS. Linux, Windows, macOS.",
		Lib:         "go-sql-driver/mysql", LibURL: "https://github.com/go-sql-driver/mysql",
		Intro: []string{
			"MariaDB has its own connection type in Lumino: it shares the MySQL protocol driver but knows the MariaDB-only objects, such as sequences.",
			"Everything that works for MySQL scripts works here too: DELIMITER, # comments, backticks and multiple result sets.",
		},
		Highlights: []string{
			"Sequences in the navigator next to tables, views, functions, procedures and triggers.",
			"Context-aware completion of tables, aliases and columns.",
			"Streaming results and client-side sort of the loaded rows.",
			"Inline editing with SQL review, export of results to CSV, JSON, SQL or Markdown.",
		},
		ExampleTitle: "Sequences",
		Example: `CREATE SEQUENCE invoice_no START WITH 1000 INCREMENT BY 1;
SELECT NEXTVAL(invoice_no);
INSERT INTO invoices (no, customer_id) VALUES (NEXTVAL(invoice_no), 42);`,
	},
	{
		Slug: "tidb", Driver: "tidb", Name: "TiDB",
		Title:       "TiDB GUI client and SQL editor — Lumino DB",
		Description: "Query and browse TiDB with Lumino, a free native SQL IDE: MySQL-compatible editor, sequences, streaming results and inline editing on port 4000.",
		Lib:         "go-sql-driver/mysql", LibURL: "https://github.com/go-sql-driver/mysql",
		Intro: []string{
			"TiDB is MySQL compatible but not identical. Lumino's TiDB connection type defaults to port 4000 and shows what TiDB actually has: databases, tables, views and sequences.",
		},
		Highlights: []string{
			"Same editor as MySQL: highlighting, completion, DELIMITER-aware scripts.",
			"Large result sets stream page by page.",
			"Row editing for tables with a primary key.",
		},
		Notes: []string{
			"TiDB has no stored functions, procedures or triggers, so the navigator doesn't show those folders.",
			"TiDB accepts read-only transactions without enforcing them: read-only connections are guarded by Lumino's own statement checks.",
		},
		ExampleTitle: "Inspect a query plan",
		Example: `EXPLAIN ANALYZE SELECT c.name, SUM(o.total)
FROM customers c JOIN orders o ON o.customer_id = c.id
GROUP BY c.name ORDER BY 2 DESC LIMIT 10;`,
	},
	{
		Slug: "sqlite", Driver: "sqlite", Name: "SQLite",
		Title:       "SQLite browser and editor — Lumino DB",
		Description: "Open, query and edit SQLite database files with Lumino, a free native SQLite browser for Linux, Windows and macOS. No sqlite3 library needed.",
		Lib:         "modernc.org/sqlite", LibURL: "https://gitlab.com/cznic/sqlite",
		Intro: []string{
			"Lumino embeds SQLite through modernc.org/sqlite, a translation of SQLite to Go: no sqlite3 shared library or CGo. Pick a file and you are connected.",
			"SQLite allows one writer at a time; Lumino closes its own open cursors before writing, so editing a table while a big result is open in another tab doesn't hit \"database is locked\".",
		},
		Highlights: []string{
			"Open any .sqlite / .db file; read-only connections turn on PRAGMA query_only.",
			"Tables, views and triggers in the navigator, structure with columns, indexes and foreign keys.",
			"Inline editing with insert, update and delete, saved in a single transaction.",
			"Results streamed from the cursor: browsing a table with millions of rows doesn't load it into memory.",
		},
		ExampleTitle: "A recursive CTE",
		Example: `WITH RECURSIVE days(d) AS (
  SELECT date('now', '-6 days') UNION ALL SELECT date(d, '+1 day') FROM days WHERE d < date('now')
)
SELECT d, (SELECT count(*) FROM orders WHERE date(created_at) = d) AS orders FROM days;`,
	},
	{
		Slug: "sql-server", Driver: "sqlserver", Name: "SQL Server",
		Title:       "SQL Server client for Linux and macOS — Lumino DB",
		Description: "Lumino is a free, native SQL Server and Azure SQL client for Linux, macOS and Windows: T-SQL scripts with GO, multiple result sets, inline editing.",
		Lib:         "microsoft/go-mssqldb", LibURL: "https://github.com/microsoft/go-mssqldb",
		Intro: []string{
			"Lumino connects to Microsoft SQL Server and Azure SQL with go-mssqldb, Microsoft's pure-Go driver. It runs the same way on Linux and macOS as on Windows: a native SQL Server GUI where SSMS isn't available.",
			"T-SQL scripts made for sqlcmd or SSMS work unchanged: GO batch separators, [bracketed] identifiers and @variables are handled by the splitter and the highlighter.",
		},
		Highlights: []string{
			"GO lines split a script into batches.",
			"Multiple result sets from one batch or procedure appear as separate result tabs.",
			"Navigator with schemas, tables, views, functions, procedures, sequences and triggers.",
			"Server-side cancel of running queries.",
			"Inline editing with SQL review, manual transactions per console.",
			"TLS encryption and SSH tunnels.",
		},
		ExampleTitle: "Batches with GO",
		Example: `CREATE OR ALTER PROCEDURE dbo.TopCustomers @n int AS
BEGIN
  SELECT TOP (@n) id, name, balance FROM dbo.customers ORDER BY balance DESC;
  SELECT COUNT(*) AS total FROM dbo.customers;
END
GO
EXEC dbo.TopCustomers @n = 5;
GO`,
	},
	{
		Slug: "oracle", Driver: "oracle", Name: "Oracle",
		Title:       "Oracle Database GUI client, no Instant Client — Lumino DB",
		Description: "Lumino is a free, native Oracle Database client: PL/SQL scripts, schemas, procedures, sequences, inline editing. Pure-Go driver, no Oracle Instant Client.",
		Lib:         "sijms/go-ora", LibURL: "https://github.com/sijms/go-ora",
		Intro: []string{
			"Lumino uses go-ora, a pure-Go implementation of the Oracle network protocol: no Oracle Instant Client, no tnsnames.ora, no JDBC driver. Host, port and service name are enough.",
			"PL/SQL blocks are split the way SQL*Plus does it: a line with a single / ends a block, so semicolons inside BEGIN … END don't break the script.",
		},
		Highlights: []string{
			"Schemas, tables, views, materialized views, functions, procedures, sequences and triggers in the navigator.",
			"Binds written as :name are highlighted as parameters.",
			"Server-side cancel of long statements.",
			"Manual transactions with commit and rollback; inline editing reviewed as SQL.",
			"TLS and SSH tunnels.",
		},
		ExampleTitle: "PL/SQL with /",
		Example: `CREATE OR REPLACE PROCEDURE raise_balance(p_id NUMBER, p_amount NUMBER) AS
BEGIN
  UPDATE customers SET balance = balance + p_amount WHERE id = p_id;
END;
/
SELECT owner, object_name, status FROM all_objects WHERE object_type = 'PROCEDURE' AND ROWNUM <= 20;`,
	},
	{
		Slug: "clickhouse", Driver: "clickhouse", Name: "ClickHouse",
		Title:       "ClickHouse GUI client and SQL editor — Lumino DB",
		Description: "Lumino is a free, native ClickHouse client: native protocol, streaming results for big analytical queries, cancel, materialized views, TLS.",
		Lib:         "ClickHouse/clickhouse-go", LibURL: "https://github.com/ClickHouse/clickhouse-go",
		Intro: []string{
			"Lumino connects to ClickHouse over the native protocol (port 9000) with the official Go client, and streams blocks of rows as they arrive: an analytical query over billions of rows shows results without buffering everything.",
			"ClickHouse-specific keywords (MergeTree engines, settings, functions) are highlighted and completed.",
		},
		Highlights: []string{
			"Databases, tables, views and materialized views in the navigator, with the DDL of each object.",
			"Cancel a running query from the console.",
			"Row editing works through mutations, and Lumino explains it before you save.",
			"TLS connections to ClickHouse Cloud and self-hosted clusters.",
		},
		Notes: []string{
			"ClickHouse has no transactions: there is no manual transaction mode.",
			"Updates and deletes are applied as ALTER TABLE … UPDATE/DELETE mutations, which rewrite data parts asynchronously; the UI warns before saving them.",
		},
		ExampleTitle: "An analytical query",
		Example: `SELECT toStartOfHour(ts) AS hour, uniqExact(user_id) AS users, quantile(0.95)(latency_ms) AS p95
FROM events
WHERE ts >= now() - INTERVAL 1 DAY
GROUP BY hour ORDER BY hour;`,
	},
	{
		Slug: "firebird", Driver: "firebird", Name: "Firebird",
		Title:       "Firebird database GUI client — Lumino DB",
		Description: "Lumino is a free, native Firebird GUI for Linux, Windows and macOS: SET TERM scripts, procedures, triggers, generators, transactions and inline editing.",
		Lib:         "nakagami/firebirdsql", LibURL: "https://github.com/nakagami/firebirdsql",
		Intro: []string{
			"Lumino connects to Firebird with firebirdsql, a pure-Go implementation of the wire protocol: no fbclient library. The database is a path or an alias on the server.",
			"Scripts from isql run as they are: SET TERM changes the terminator so procedure and trigger bodies can contain semicolons.",
		},
		Highlights: []string{
			"Tables, views, procedures, functions, triggers and generators (sequences) in the navigator.",
			"Transactions: auto-commit or manual mode with commit and rollback.",
			"Cancel long statements.",
			"Inline editing of tables with a primary key.",
		},
		Notes: []string{
			"Firebird has no schemas: objects are listed directly under the connection.",
		},
		ExampleTitle: "SET TERM in a script",
		Example: `SET TERM ^ ;
CREATE OR ALTER PROCEDURE customer_count RETURNS (n INTEGER) AS
BEGIN
  SELECT COUNT(*) FROM customers INTO :n;
  SUSPEND;
END^
SET TERM ; ^

SELECT n FROM customer_count;`,
	},
	{
		Slug: "mongodb", Driver: "mongodb", Name: "MongoDB",
		Title:       "MongoDB GUI client and shell editor — Lumino DB",
		Description: "Lumino is a free, native MongoDB GUI: mongosh console with completion, collections and views, inferred fields, indexes, type-safe document editing.",
		Lib:         "mongodb/mongo-go-driver", LibURL: "https://github.com/mongodb/mongo-go-driver",
		Intro: []string{
			"Lumino talks to MongoDB with the official Go driver. Databases show up like schemas, with their collections and views; the fields of a collection are inferred from a sample of its documents.",
			"The console runs mongosh statements — db.orders.find({…}).sort({…}), aggregate, insertOne, updateMany, createIndex, runCommand, use, show — with ObjectId(), ISODate(), NumberDecimal() and the other shell helpers. Results stream into the grid as you scroll.",
			"Paste a mongodb:// or mongodb+srv:// connection string to fill the connection form.",
		},
		Highlights: []string{
			"Collections, views, time series and capped collections in the navigator; inferred fields with their BSON types, indexes and validators.",
			"A query bar with filter, projection, sort and limit documents, completing field paths and operators; count the matches.",
			"Table, tree and JSON views of the documents: nested documents become address.city columns in the table, the tree and the text show every field.",
			"Document editing by _id: edit a cell, a field of the tree or the whole document as text — changes are applied by path with $set and $unset, reviewed before they run, and values keep their BSON type.",
			"Statements are classified before they run: deleteMany({}), drop() and $out need the typed confirmation on protected connections, read-only connections refuse writes.",
			"JavaScript in the console — variables, loops, functions, cursor.forEach() — with the result of a script streamed into the grid; every operation a script performs is checked while it runs, so read-only connections refuse its writes and protected ones confirm them.",
			"Transactions on replica sets and sharded clusters, SCRAM, X.509 and AWS IAM authentication, TLS and SSH tunnels.",
		},
		Notes: []string{
			"MongoDB 5.0 or newer. Transactions need a replica set or a sharded cluster.",
			"Read-only sessions are enforced by Lumino (every statement is parsed and classified), MongoDB has no server-side read-only session.",
			"Table columns come from the first 1000 documents of a result; the tree and JSON views show every field of every document.",
		},
		ExampleTitle: "MongoDB shell",
		Example: `db.orders.find({status: "shipped", total: {$gte: 100}}).sort({created: -1}).limit(50);
db.orders.aggregate([{$group: {_id: "$customer", spent: {$sum: "$total"}}}, {$sort: {spent: -1}}]);
db.customers.updateOne({_id: ObjectId("65f1c0a2e4b0a1b2c3d4e5f6")}, {$set: {email: "ada@example.com"}});`,
	},
	{
		Slug: "ferretdb", Driver: "mongodb", Name: "FerretDB",
		Title:       "FerretDB GUI client — Lumino DB",
		Description: "Browse, query and edit FerretDB with Lumino, a free native MongoDB-compatible client for Linux, Windows and macOS.",
		Lib:         "mongodb/mongo-go-driver", LibURL: "https://github.com/mongodb/mongo-go-driver",
		Intro: []string{
			"FerretDB speaks the MongoDB wire protocol on top of PostgreSQL. Lumino's MongoDB connection type is tested against it in the same conformance suite as MongoDB.",
			"Use the MongoDB connection type with the FerretDB host, port, user and password, or paste its mongodb:// URL.",
		},
		Highlights: []string{
			"Databases, collections and inferred fields in the navigator.",
			"mongosh statements in the console, with results streamed into the grid.",
			"Document editing by _id with a review of the generated statements.",
		},
		Notes: []string{
			"FerretDB has no transactions: edits are applied one document at a time.",
		},
		ExampleTitle: "MongoDB shell",
		Example: `db.events.find({type: "signup"}).sort({at: -1}).limit(20);
db.events.countDocuments({at: {$gte: ISODate("2026-01-01")}});`,
	},
	{
		Slug: "cassandra", Driver: "cassandra", Name: "Apache Cassandra",
		Title:       "Apache Cassandra GUI client and CQL editor — Lumino DB",
		Description: "Lumino is a free, native Cassandra GUI: CQL editor with completion, keyspaces, tables, materialized views, paging through large partitions, row editing.",
		Lib:         "apache/cassandra-gocql-driver", LibURL: "https://github.com/apache/cassandra-gocql-driver",
		Intro: []string{
			"Lumino uses the Apache Cassandra gocql driver with a native session, not a SQL shim: CQL goes to the cluster as written and results are paged with Cassandra's own paging state.",
			"Keyspaces show up like schemas, with tables, views, materialized views and user-defined functions.",
		},
		Highlights: []string{
			"CQL highlighting and completion, // and -- comments.",
			"Large partitions and full scans are paged as you scroll.",
			"Row editing by primary key (INSERT, UPDATE, DELETE), reviewed as CQL before it runs.",
			"TLS and SSH tunnels to reach clusters on private networks.",
		},
		Notes: []string{
			"Cassandra has no transactions: edits are applied statement by statement.",
		},
		ExampleTitle: "CQL",
		Example: `SELECT * FROM shop.orders_by_customer WHERE customer_id = 42 AND order_day >= '2026-01-01';
UPDATE shop.customers SET email = 'ada@example.com' WHERE id = 1;`,
	},
	{
		Slug: "scylladb", Driver: "cassandra", Name: "ScyllaDB",
		Title:       "ScyllaDB GUI client and CQL editor — Lumino DB",
		Description: "Browse, query and edit ScyllaDB with Lumino, a free native CQL client for Linux, Windows and macOS: keyspaces, materialized views, paging, TLS.",
		Lib:         "apache/cassandra-gocql-driver", LibURL: "https://github.com/apache/cassandra-gocql-driver",
		Intro: []string{
			"ScyllaDB is compatible with the Cassandra protocol, and Lumino's Cassandra / ScyllaDB connection type is tested against both in the same conformance suite.",
			"Paste a scylla:// URL to fill the connection form, or enter host, port 9042, keyspace and credentials.",
		},
		Highlights: []string{
			"Keyspaces, tables, views and materialized views in the navigator, with their CQL definition.",
			"Results paged from the cluster while you scroll.",
			"Editing by primary key with a review of the generated CQL.",
			"Read-only and production connection policies, useful on shared clusters.",
		},
		Notes: []string{
			"No transactions: changes run as individual CQL statements.",
		},
		ExampleTitle: "CQL",
		Example: `DESCRIBE KEYSPACES;
SELECT * FROM metrics.readings WHERE sensor_id = 7 AND day = '2026-10-01' LIMIT 500;`,
	},
	{
		Slug: "trino", Driver: "trino", Name: "Trino",
		Title:       "Trino (Presto) GUI client and SQL editor — Lumino DB",
		Description: "Lumino is a free, native Trino client: catalogs, schemas and tables in one tree, streaming results, cancel, materialized views. Works with Presto URLs.",
		Lib:         "trinodb/trino-go-client", LibURL: "https://github.com/trinodb/trino-go-client",
		Intro: []string{
			"Trino federates many data sources behind catalogs. Lumino shows the catalog level above schemas, so Hive, Iceberg, PostgreSQL and every other connector of the cluster are browsable in one tree.",
			"Queries run through the official Trino Go client; rows are shown as soon as the coordinator returns the first pages.",
		},
		Highlights: []string{
			"Catalogs, schemas, tables, views and materialized views.",
			"Cancel a running query: Lumino stops it on the coordinator.",
			"Completion of catalogs, schemas, tables and columns.",
			"TLS, SSH tunnels and trino:// or presto:// connection URLs.",
		},
		Notes: []string{
			"The data grid is read-only for Trino: run INSERT/UPDATE statements from the console where the connector supports them.",
		},
		ExampleTitle: "A federated query",
		Example: `SELECT o.order_id, c.name, o.total
FROM hive.sales.orders o
JOIN postgresql.public.customers c ON c.id = o.customer_id
WHERE o.order_date >= DATE '2026-01-01'
LIMIT 100;`,
	},
}
