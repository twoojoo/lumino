// Package mongo is the MongoDB adapter (and compatible servers: FerretDB,
// Amazon DocumentDB, Azure Cosmos DB for MongoDB). Databases are the
// schema level, collections and views the objects; the console language
// is mongosh statements (see shell.go).
package mongo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/twoojoo/lumino/internal/db"
)

func init() { db.Register(Driver{}) }

// Driver opens MongoDB connections.
type Driver struct{}

// MinVersion is the oldest supported server.
const MinVersion = 5

// baseCaps: transactions are added for replica sets and sharded clusters.
// Read-only sessions are enforced by the adapter (SetReadOnly refuses
// writing statements), not by the server: ReadOnlyTx stays false.
var baseCaps = db.Capabilities{
	Schemas: true, Cancel: true, Views: true,
	Insert: true, Update: true, Delete: true, TLS: true,
}

func (Driver) Info() db.Info {
	return db.Info{
		ID: "mongodb", Name: "MongoDB", DefaultPort: 27017, DefaultDB: "test",
		DatabaseLabel: "Database",
		Fields:        []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps:          baseCaps,
		Schemes:       []string{"mongodb", "mongodb+srv"},
		DialectName:   "mongo",
		Folders:       []db.ObjectType{db.ObjCollection, db.ObjView},
	}
}

// Parameters handled by the adapter (the others are passed to the driver
// as connection string options: authSource, authMechanism, replicaSet,
// readPreference, appName, retryWrites, w, compressors…).
const (
	paramSRV = "srv" // true: mongodb+srv:// (DNS seed list)
)

// buildURI returns the connection string of cfg (without credentials).
func buildURI(cfg *db.Config, defPort int) (string, error) {
	srv := cfg.Param(paramSRV) == "true"
	var hosts []string
	for _, h := range strings.Split(cfg.Host, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if srv {
			if strings.Contains(h, ":") || net.ParseIP(strings.Trim(h, "[]")) != nil {
				return "", fmt.Errorf("mongodb+srv needs a DNS name without port (%q); an SSH tunnel can't use SRV", h)
			}
			hosts = append(hosts, h)
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			p := cfg.Port
			if p == 0 {
				p = defPort
			}
			h = net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(p))
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		hosts = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(defPort))}
	}
	if srv && len(hosts) != 1 {
		return "", errors.New("mongodb+srv takes exactly one host name")
	}
	scheme := "mongodb"
	if srv {
		scheme = "mongodb+srv"
	}
	q := url.Values{}
	keys := make([]string, 0, len(cfg.Params))
	for k := range cfg.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch strings.ToLower(k) {
		case paramSRV:
			continue
		case "tls", "ssl", "tlsinsecure", "tlscafile", "tlscertificatekeyfile", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames":
			continue // TLS comes from the connection's TLS settings
		}
		q.Set(k, cfg.Params[k])
	}
	// one node (no replica set name): talk to it directly, as the
	// development servers and SSH tunnels need (the replica set
	// advertises names the client can't reach)
	if !srv && len(hosts) == 1 && q.Get("replicaSet") == "" && q.Get("directConnection") == "" && q.Get("loadBalanced") == "" {
		q.Set("directConnection", "true")
	}
	u := scheme + "://" + strings.Join(hosts, ",") + "/"
	if d := strings.TrimSpace(cfg.Database); d != "" {
		u += url.PathEscape(d)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u, nil
}

func (Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	uri, err := buildURI(cfg, Driver{}.Info().DefaultPort)
	if err != nil {
		return nil, err
	}
	o := options.Client().ApplyURI(uri).SetAppName("Lumino").
		SetServerSelectionTimeout(15 * time.Second).SetConnectTimeout(15 * time.Second)
	if cfg.Param("appName") != "" {
		o.SetAppName(cfg.Param("appName"))
	}
	host := ""
	if hs := strings.Split(cfg.Host, ","); len(hs) == 1 {
		host = strings.Trim(strings.TrimSpace(hs[0]), "[]")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	tc, err := cfg.TLS.TLS(host) // several hosts: the driver checks each name
	if err != nil {
		return nil, err
	}
	if tc != nil {
		o.SetTLSConfig(tc)
	}
	mech := strings.ToUpper(cfg.Param("authMechanism"))
	if cfg.User != "" || mech != "" {
		cred := options.Credential{
			AuthMechanism: mech, AuthSource: cfg.Param("authSource"),
			Username: cfg.User, Password: cfg.Password, PasswordSet: cfg.Password != "",
		}
		if mech == "MONGODB-X509" && tc == nil {
			return nil, errors.New("X.509 authentication needs TLS with a client certificate")
		}
		o.SetAuth(cred)
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	cl, err := mongo.Connect(o)
	if err != nil {
		return nil, mapError(err)
	}
	c := &conn{cl: cl, cfg: *cfg, database: strings.TrimSpace(cfg.Database)}
	if c.database == "" {
		c.database = Driver{}.Info().DefaultDB
	}
	if err := c.probe(ctx); err != nil {
		cl.Disconnect(context.Background())
		return nil, err
	}
	c.lang = &language{}
	c.cat = &catalog{c: c}
	return c, nil
}

// conn is a client (a connection pool).
type conn struct {
	cl       *mongo.Client
	cfg      db.Config
	database string
	caps     db.Capabilities
	version  string
	ferret   bool
	lang     *language
	cat      *catalog
}

// probe reads the server version and topology.
func (c *conn) probe(ctx context.Context) error {
	admin := c.cl.Database("admin")
	var bi bson.D
	if err := admin.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err != nil {
		return mapError(err)
	}
	ver, _ := field(bi, "version")
	c.version, _ = ver.(string)
	if v, ok := field(bi, "ferretdb"); ok && v != nil {
		c.ferret = true
	}
	if fv, ok := field(bi, "ferretdbVersion"); ok && fv != nil {
		c.ferret = true
	}
	if major, _, _ := strings.Cut(c.version, "."); !c.ferret {
		if n, err := strconv.Atoi(major); err == nil && n < MinVersion {
			return fmt.Errorf("MongoDB %s is not supported (%d.0 or newer needed)", c.version, MinVersion)
		}
	}
	var hello bson.D
	if err := admin.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return mapError(err)
	}
	c.caps = baseCaps
	set, _ := field(hello, "setName")
	msg, _ := field(hello, "msg")
	if (set != nil || msg == "isdbgrid") && !c.ferret {
		c.caps.Transactions = true // replica sets and sharded clusters
	}
	return nil
}

func (c *conn) Info() db.Info         { return Driver{}.Info() }
func (c *conn) Caps() db.Capabilities { return c.caps }
func (c *conn) Language() db.Language { return c.lang }
func (c *conn) Catalog() db.Catalog   { return c.cat }

func (c *conn) ServerVersion() string {
	if c.ferret {
		return "FerretDB (MongoDB " + c.version + " compatible)"
	}
	return "MongoDB " + c.version
}

func (c *conn) Ping(ctx context.Context) error {
	return mapError(c.cl.Ping(ctx, readpref.Primary()))
}

func (c *conn) Close() error { return c.cl.Disconnect(context.Background()) }

func (c *conn) Session(ctx context.Context) (db.Session, error) {
	s := &session{c: c, database: c.database}
	for _, st := range split(c.cfg.InitSQL) {
		res, err := s.Execute(ctx, st.Text)
		if err != nil {
			return nil, fmt.Errorf("init script %q: %w", st.Text, err)
		}
		res.Close()
	}
	return s, nil
}

// mapError converts driver errors to *db.Error.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var de *db.Error
	if errors.As(err, &de) {
		return de // already mapped (scripts)
	}
	var pe *parseError
	if errors.As(err, &pe) {
		return &db.Error{Msg: pe.msg, PositionBytes: true, Position: pe.pos + 1, Err: err}
	}
	e := &db.Error{Msg: err.Error(), Err: err}
	var se mongo.ServerError
	if errors.As(err, &se) {
		var ce mongo.CommandError
		var we mongo.WriteException
		switch {
		case errors.As(err, &ce):
			e.Msg, e.Code = ce.Message, ce.Name
			if e.Code == "" {
				e.Code = strconv.Itoa(int(ce.Code))
			}
		case errors.As(err, &we) && len(we.WriteErrors) > 0:
			e.Msg, e.Code = we.WriteErrors[0].Message, strconv.Itoa(we.WriteErrors[0].Code)
		}
	}
	return e
}

// ParseURL reads a mongodb:// or mongodb+srv:// connection string.
func (Driver) ParseURL(s string) (db.Config, bool, error) {
	s = strings.TrimSpace(s)
	var cfg db.Config
	scheme, rest, ok := strings.Cut(s, "://")
	switch strings.ToLower(scheme) {
	case "mongodb":
	case "mongodb+srv":
		cfg.Params = map[string]string{paramSRV: "true"}
	default:
		return cfg, false, nil
	}
	if !ok {
		return cfg, true, errors.New("invalid connection string")
	}
	cfg.Driver = "mongodb"
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	path := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest, path = rest[:i], rest[i+1:]
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		user, pass, _ := strings.Cut(rest[:i], ":")
		var err error
		if cfg.User, err = url.PathUnescape(user); err != nil {
			return cfg, true, fmt.Errorf("user: %w", err)
		}
		if cfg.Password, err = url.PathUnescape(pass); err != nil {
			return cfg, true, fmt.Errorf("password: %w", err)
		}
		rest = rest[i+1:]
	}
	hosts := strings.Split(rest, ",")
	if len(hosts) == 1 {
		if h, p, err := net.SplitHostPort(hosts[0]); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil {
				return cfg, true, fmt.Errorf("invalid port %q", p)
			}
			cfg.Host, cfg.Port = h, n
		} else {
			cfg.Host = hosts[0]
		}
	} else {
		cfg.Host = rest
	}
	if cfg.Host == "" {
		return cfg, true, errors.New("the connection string names no host")
	}
	dbName, err := url.PathUnescape(path)
	if err != nil {
		return cfg, true, err
	}
	cfg.Database = dbName
	q, err := url.ParseQuery(query)
	if err != nil {
		return cfg, true, err
	}
	for k := range q {
		v := q.Get(k)
		switch strings.ToLower(k) {
		case "tls", "ssl":
			if v == "true" && cfg.TLS.Mode == "" {
				cfg.TLS.Mode = "verify-full"
			}
			continue
		case "tlsallowinvalidcertificates", "tlsinsecure":
			if v == "true" {
				cfg.TLS.Mode = "require"
			}
			continue
		case "tlscafile":
			cfg.TLS.CAFile = v
			continue
		case "tlscertificatekeyfile":
			cfg.TLS.CertFile, cfg.TLS.KeyFile = v, v
			continue
		case "tlsallowinvalidhostnames":
			if v == "true" && cfg.TLS.Mode != "require" {
				cfg.TLS.Mode = "verify-ca"
			}
			continue
		}
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params[k] = v
	}
	if cfg.Params[paramSRV] == "true" && cfg.TLS.Mode == "" && q.Get("tls") != "false" && q.Get("ssl") != "false" {
		cfg.TLS.Mode = "verify-full" // SRV implies TLS
	}
	return cfg, true, nil
}
