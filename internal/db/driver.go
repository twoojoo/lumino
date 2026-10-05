package db

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capabilities tell the UI what a connection supports.
type Capabilities struct {
	Catalogs     bool // a catalog level above schemas (Trino)
	Schemas      bool // false: objects live directly under the connection
	Transactions bool
	ReadOnlyTx   bool // the server can enforce read-only sessions
	Cancel       bool // a running statement can be cancelled
	ForeignKeys  bool
	Sequences    bool
	Functions    bool
	Procedures   bool
	Triggers     bool
	Views        bool
	MatViews     bool
	Insert       bool // row editing in the grid
	Update       bool
	Delete       bool
	// EditNeedsWarning: updates/deletes are asynchronous mutations or
	// otherwise unusual (ClickHouse ALTER … UPDATE); the UI warns first.
	EditNeedsWarning string
	TLS              bool
	MultiResultSets  bool
	// SingleWriter: an open cursor in one session blocks writes from the
	// others (SQLite): the UI closes cursors before writing.
	SingleWriter bool
}

// Field is a connection form field shown for a driver.
type Field string

const (
	FieldHost     Field = "host"
	FieldPort     Field = "port"
	FieldDatabase Field = "database"
	FieldUser     Field = "user"
	FieldPassword Field = "password"
	FieldFile     Field = "file"
)

// Info describes a driver.
type Info struct {
	ID          string // stable id stored in connection configs
	Name        string // display name
	DefaultPort int
	DefaultUser string
	DefaultDB   string
	// DatabaseLabel names the database field ("Database", "Keyspace", "Service name"…).
	DatabaseLabel string
	Fields        []Field
	Caps          Capabilities
	// Schemes are dburl schemes mapped to this driver when pasting a URL.
	Schemes []string
	// DialectName selects highlighting/keywords (postgres, mysql, …).
	DialectName string
}

// Has reports whether the connection form shows f.
func (i Info) Has(f Field) bool {
	for _, x := range i.Fields {
		if x == f {
			return true
		}
	}
	return false
}

// Driver opens connections to one kind of database.
type Driver interface {
	Info() Info
	// Open connects with cfg. The address in cfg is already rewritten to a
	// local tunnel when SSH is configured.
	Open(ctx context.Context, cfg *Config) (Conn, error)
}

var (
	regMx   sync.RWMutex
	drivers = map[string]Driver{}
)

// Register makes a driver available. Adapters call it from init.
func Register(d Driver) {
	regMx.Lock()
	defer regMx.Unlock()
	id := d.Info().ID
	if _, dup := drivers[id]; dup {
		panic("db: driver registered twice: " + id)
	}
	drivers[id] = d
}

// Lookup returns the driver with id.
func Lookup(id string) (Driver, bool) {
	regMx.RLock()
	defer regMx.RUnlock()
	d, ok := drivers[id]
	return d, ok
}

// Drivers lists registered drivers by name.
func Drivers() []Driver {
	regMx.RLock()
	defer regMx.RUnlock()
	out := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// Conn is an open connection (a pool, for most databases).
type Conn interface {
	Info() Info
	// Caps may differ from Info().Caps depending on the server version.
	Caps() Capabilities
	Dialect() Dialect
	ServerVersion() string
	Ping(ctx context.Context) error
	Catalog() Catalog
	// Session reserves a dedicated server session (console tab, table
	// editor): session state and transactions are kept across statements.
	Session(ctx context.Context) (Session, error)
	Close() error
}

// Session is a dedicated server session. Its methods are not safe for
// concurrent use except Cancel and TxActive.
type Session interface {
	// Execute runs one statement. The returned Result streams rows and must
	// be closed before the next Execute (Execute closes it otherwise).
	Execute(ctx context.Context, stmt string, args ...any) (Result, error)
	// Cancel aborts the running statement, if any.
	Cancel()
	Begin(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	TxActive() bool
	// SetReadOnly asks the server to refuse writes for this session (no-op
	// returning ErrNotSupported where unavailable).
	SetReadOnly(ctx context.Context, on bool) error
	CurrentSchema(ctx context.Context) (string, error)
	SetCurrentSchema(ctx context.Context, schema string) error
	Close() error
}

// Result is a cursor over the result sets of a statement.
type Result interface {
	// Columns of the current result set; nil for statements without rows.
	Columns() []Column
	// Fetch returns up to n rows of the current result set; more is false
	// once it is exhausted.
	Fetch(ctx context.Context, n int) (rows []Row, more bool, err error)
	// RowsAffected of a statement without rows (-1 = unknown).
	RowsAffected() int64
	// NextResultSet advances to the next result set (multi-statement
	// batches, procedures). It returns false when there is none.
	NextResultSet(ctx context.Context) (bool, error)
	// Messages are server notices (RAISE NOTICE, PRINT, warnings).
	Messages() []string
	Close() error
}

// Catalog reads database metadata. Every call is independent and safe for
// concurrent use.
type Catalog interface {
	// Catalogs lists catalogs (only when Caps.Catalogs).
	Catalogs(ctx context.Context) ([]string, error)
	// Schemas lists schemas of a catalog ("" = the connection's).
	Schemas(ctx context.Context, catalog string) ([]Schema, error)
	// Objects lists the objects of the given types in a schema.
	Objects(ctx context.Context, schema ObjectRef, types ...ObjectType) ([]Object, error)
	// Table describes a table or view.
	Table(ctx context.Context, ref ObjectRef) (*TableInfo, error)
	// DDL returns the CREATE statement(s) of an object.
	DDL(ctx context.Context, ref ObjectRef) (string, error)
	// Completion returns tables and their columns of a schema in one round
	// trip, for the editor's autocomplete.
	Completion(ctx context.Context, schema ObjectRef) (map[string][]string, error)
}

// TLSConfig configures transport encryption.
type TLSConfig struct {
	// Mode: "" (driver default), disable, require (no verification),
	// verify-ca, verify-full.
	Mode     string `json:"mode,omitempty"`
	CAFile   string `json:"caFile,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	// ServerName overrides the name verified in verify-full mode.
	ServerName string `json:"serverName,omitempty"`
}

// Enabled reports whether TLS is on.
func (t TLSConfig) Enabled() bool { return t.Mode != "" && t.Mode != "disable" }

// SSHConfig configures an SSH tunnel to the database host.
type SSHConfig struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user"`
	// Auth: password | key | agent
	Auth    string `json:"auth"`
	KeyFile string `json:"keyFile,omitempty"`
	// Password / key passphrase, filled from the keyring at connect.
	Secret string `json:"-"`
}

// Config is a saved connection. Secrets are never serialized: they live in
// the OS keyring and are set in Password / SSH.Secret before Open.
type Config struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Host     string            `json:"host,omitempty"`
	Port     int               `json:"port,omitempty"`
	Database string            `json:"database,omitempty"`
	User     string            `json:"user,omitempty"`
	File     string            `json:"file,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	TLS      TLSConfig         `json:"tls,omitempty"`
	SSH      *SSHConfig        `json:"ssh,omitempty"`
	// SavePassword: the password is stored in the keyring; otherwise it
	// is asked at every connect.
	SavePassword bool   `json:"savePassword,omitempty"`
	Color        string `json:"color,omitempty"` // #rrggbb tab/tree accent
	Group        string `json:"group,omitempty"`
	InitSQL      string `json:"initSQL,omitempty"` // run on every new session
	// Protected (production): red frame, manual transactions, typed
	// confirmations. nil = guessed from the name.
	Protected *bool `json:"protected,omitempty"`
	ReadOnly  bool  `json:"readOnly,omitempty"`

	Password string `json:"-"`
}

// Addr returns host:port with the driver default port.
func (c *Config) Addr(defPort int) string {
	p := c.Port
	if p == 0 {
		p = defPort
	}
	return net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(p))
}

// Param returns a driver parameter.
func (c *Config) Param(k string) string { return c.Params[k] }

// Timeouts used by the API.
const (
	ConnectTimeout = 20 * time.Second
	MetaTimeout    = 60 * time.Second
)
