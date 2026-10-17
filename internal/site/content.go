package site

// Text shared by the HTML pages and llms.txt / llms-full.txt.

const (
	name    = "Lumino DB"
	tagline = "A fast, native, open-source database IDE"
	summary = "Lumino is a free, open-source (GPL-3.0) database IDE for Linux, Windows and macOS, written in Go " +
		"with a GPU-rendered native UI: no Electron, no JVM. It connects to PostgreSQL, MySQL, MariaDB, SQLite, " +
		"SQL Server, Oracle, ClickHouse, Cassandra, Trino and more with built-in pure-Go drivers."
)

type feature struct{ Title, Text string }

var features = []feature{
	{"Native and light", "One small binary with a GPU-rendered UI. No Electron, no Java runtime, no drivers to download: every database driver is built in."},
	{"SQL console", "Syntax highlighting per dialect, context-aware completion of tables, aliases and columns, find/replace, run the statement at the caret or the whole script, cancel, error positions in the editor, history and saved queries."},
	{"Scripts as they are", "Dollar quotes, DELIMITER, GO, PL/SQL \"/\" and SET TERM are understood, so scripts written for psql, mysql, sqlcmd, SQL*Plus or isql run unchanged."},
	{"Streaming results", "The grid reads rows from an open cursor while you scroll: millions of rows without loading them into memory. Multiple result sets, cell selection, copy as TSV, CSV, JSON, SQL or Markdown, export to file."},
	{"Table editing", "Server-side WHERE and ORDER BY, inline insert/update/delete, a review of the generated SQL, transactional save. Structure (columns, indexes, keys, constraints, triggers) and DDL."},
	{"Safe on production", "Read-only connections block writes; protected connections confirm every write, require typing to confirm destructive statements and start consoles in manual transaction mode."},
	{"Secure connections", "Passwords in the OS keyring, SSH tunnels (password, key, agent) with known-hosts verification, TLS with CA verification and client certificates, connection URLs."},
	{"AI assistant", "Optional: Anthropic or any OpenAI-compatible provider, Ollama included. It reads the schema, samples rows with sensitive columns redacted, runs read-only queries and proposes SQL you insert yourself."},
}

type faq struct{ Q, A string }

var homeFAQ = []faq{
	{"Is there a native DBeaver alternative without Java or Electron?",
		"Yes. Lumino is a single native binary with a GPU-rendered UI, and it streams large result sets from an open cursor instead of loading them into memory."},
	{"Which databases does Lumino support?",
		"PostgreSQL, CockroachDB, MySQL, MariaDB, TiDB, SQLite, SQL Server, Oracle, ClickHouse, Firebird, Cassandra / ScyllaDB and Trino. The drivers are built in: there are no external drivers or client libraries to install."},
	{"Is Lumino free?",
		"Yes. Lumino is free software under the GNU GPL 3.0 or later, with no paid edition."},
	{"Which operating systems does it run on?",
		"Linux (AppImage, .deb, .rpm, .tar.gz), Windows 10 and later (x64 and ARM64) and macOS 12 and later (Apple silicon and Intel)."},
	{"Can I use it safely on production databases?",
		"Connections can be marked read-only (writes are blocked) or protected: every write asks for confirmation, destructive statements must be confirmed by typing, and consoles start in manual transaction mode."},
	{"Does the AI assistant send my data anywhere?",
		"Only if you configure it, and only to the provider you choose (a local Ollama model works too). Its database tools are read-only and columns with sensitive names are redacted from samples."},
	{"How are updates delivered?",
		"Lumino updates itself and installs only releases whose checksums are signed with the Lumino release key. .deb and .rpm installs are updated by the package manager."},
}

// shot is a screenshot shipped in static/img (made by
// TestUISiteScreenshots, see the Makefile target site-shots).
type shot struct{ File, Alt string }

var shots = []shot{
	{"console.png", "Lumino SQL console with a query and its results in the data grid"},
	{"completion.png", "Context-aware SQL completion of the columns of a table alias"},
	{"table.png", "Table view with inline edits waiting to be saved"},
	{"review.png", "Review of the generated SQL before saving table edits"},
	{"structure.png", "Table structure: columns, indexes and keys"},
	{"light.png", "Lumino with the Ayu light theme"},
}

// Lumino vs DBeaver. Keep it factual: what each has, no benchmarks.
type compareRow struct{ Topic, Lumino, Other string }

var dbeaverRows = []compareRow{
	{"Runtime", "Native Go binary, GPU-rendered UI", "Java (Eclipse RCP), bundled JRE"},
	{"License", "GPL-3.0-or-later, single free edition", "Community Edition under Apache-2.0; commercial PRO editions"},
	{"Database drivers", "Built in, pure Go: nothing to download, works offline", "JDBC drivers, downloaded on first connection"},
	{"Supported databases", "12 (relational, analytical, Cassandra/ScyllaDB, Trino)", "Many more: anything with a JDBC driver"},
	{"Large results", "Streamed from an open cursor while scrolling", "Fetched in pages (\"fetch next page\")"},
	{"Scripts", "Dollar quotes, DELIMITER, GO, PL/SQL /, SET TERM", "Script execution with configurable delimiters"},
	{"Production safety", "Read-only and protected connections, typed confirmation of destructive statements", "Connection types (e.g. production) with confirmation and manual commit"},
	{"SSH and TLS", "SSH tunnels with known-hosts check, TLS with client certificates", "SSH tunnels, TLS, proxies"},
	{"AI assistant", "Optional, any OpenAI-compatible provider or Anthropic, read-only tools", "Available, depending on edition and provider"},
	{"ER diagrams, data import and migration", "Not available", "Available"},
	{"Updates", "Built-in, signed (ed25519) self-update", "Built-in update check"},
}

var dbeaverFAQ = []faq{
	{"Is Lumino a drop-in replacement for DBeaver?",
		"For everyday work on the supported databases (browse, query, edit rows, read DDL) yes. If you need ER diagrams, data migration between databases or a database Lumino doesn't support, DBeaver is still the better tool."},
	{"Why choose a native database client over DBeaver?",
		"Lumino starts as a single native process without a Java runtime and needs no JDBC driver downloads, which also makes it usable on machines without internet access to driver repositories."},
	{"Does Lumino support Cassandra like DBeaver?",
		"Yes, Cassandra and ScyllaDB are supported in the free edition with a native CQL session. In DBeaver, NoSQL databases such as Cassandra are part of the commercial editions."},
	{"Can I import my DBeaver connections?",
		"Not yet. Lumino accepts connection URLs (postgres://, mysql://, sqlserver://…), so a connection can be created by pasting its URL."},
}
