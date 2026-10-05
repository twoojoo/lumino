package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/knownhosts"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/safety"
	"github.com/giovanni/lumino/internal/secrets"
)

type connStatus int

const (
	stIdle connStatus = iota
	stConnecting
	stReady
	stFailed
)

// metaKey identifies a schema (catalog may be empty).
type metaKey struct{ catalog, schema string }

func keyOf(r db.ObjectRef) metaKey { return metaKey{r.Catalog, r.Schema} }

// connState is the runtime state of a saved connection. All fields are
// owned by the UI goroutine.
type connState struct {
	a      *App
	id     string
	conn   db.Conn
	status connStatus
	err    error
	gen    int // bumped on every connect/disconnect: drops stale results

	catalogs   []string
	schemas    map[string][]db.Schema // by catalog
	objects    map[metaKey][]db.Object
	tables     map[db.ObjectRef]*db.TableInfo
	completion map[metaKey]map[string][]string
	loading    map[string]bool
	errs       map[string]error
	waiters    []connWaiter
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cfg returns the saved configuration (nil if the connection was deleted).
func (cs *connState) cfg() *db.Config {
	c, ok := cs.a.prefs.Conn(cs.id)
	if !ok {
		return nil
	}
	return c
}

func (cs *connState) name() string {
	if c := cs.cfg(); c != nil {
		return c.Name
	}
	return cs.id
}

func (cs *connState) info() db.Info {
	if c := cs.cfg(); c != nil {
		if d, ok := db.Lookup(c.Driver); ok {
			return d.Info()
		}
	}
	return db.Info{}
}

// caps returns the capabilities (of the server when connected).
func (cs *connState) caps() db.Capabilities {
	if cs.conn != nil {
		return cs.conn.Caps()
	}
	return cs.info().Caps
}

func (cs *connState) resetMeta() {
	cs.catalogs = nil
	cs.schemas = map[string][]db.Schema{}
	cs.objects = map[metaKey][]db.Object{}
	cs.tables = map[db.ObjectRef]*db.TableInfo{}
	cs.completion = map[metaKey]map[string][]string{}
	cs.loading = map[string]bool{}
	cs.errs = map[string]error{}
}

// conn returns the runtime state of a connection id.
func (a *App) connState(id string) *connState {
	cs, ok := a.conns[id]
	if !ok {
		cs = &connState{a: a, id: id}
		cs.resetMeta()
		a.conns[id] = cs
	}
	return cs
}

// protected reports whether a connection is production-like.
func (a *App) connProtected(id string) bool {
	c, ok := a.prefs.Conn(id)
	return ok && prefs.Protected(c)
}

func (a *App) connReadOnly(id string) bool {
	c, ok := a.prefs.Conn(id)
	return ok && c.ReadOnly
}

// connWaiter is a caller waiting for a connection attempt.
type connWaiter struct {
	ok   func(*connState)
	fail func(error)
}

// connect opens a connection (asking for missing secrets) and runs then
// once it is ready. Concurrent calls share the attempt.
func (a *App) connect(id string, then func(*connState)) { a.connectOr(id, then, nil) }

// connectOr is connect with a callback for a failed or cancelled attempt
// (callers holding a "busy" state reset it there).
func (a *App) connectOr(id string, then func(*connState), failed func(error)) {
	cs := a.connState(id)
	if cs.status == stReady {
		if then != nil {
			then(cs)
		}
		return
	}
	if then != nil || failed != nil {
		cs.waiters = append(cs.waiters, connWaiter{then, failed})
	}
	if cs.status == stConnecting {
		return
	}
	cfg := cs.cfg()
	if cfg == nil {
		cs.fail(errors.New("connection not found"))
		return
	}
	c := *cfg
	if err := safety.CheckConnection(&c); err != nil {
		cs.fail(err)
		return
	}
	info := cs.info()
	needPw := info.Has(db.FieldPassword)
	if needPw {
		if pw, ok := secrets.Get(id, secrets.Password); ok {
			c.Password = pw
			needPw = false
		} else if c.SavePassword {
			needPw = true // saved but missing (keyring cleared): ask again
		}
	}
	needSSH := false
	if c.SSH != nil && c.SSH.Host != "" && (c.SSH.Auth == "password" || c.SSH.Auth == "") {
		s := *c.SSH
		if v, ok := secrets.Get(id, secrets.SSH); ok {
			s.Secret = v
		} else {
			needSSH = true
		}
		c.SSH = &s
	}
	if (needPw && !passwordOptional(&c)) || needSSH {
		// connecting while the prompt is open: the tree doesn't ask again
		cs.status, cs.err = stConnecting, nil
		cs.gen++
		gen := cs.gen
		a.overlay = newSecretPrompt(a, &c, needPw, needSSH, func(pw, sshSecret string, save bool) {
			if cs.gen != gen {
				return
			}
			if needPw {
				c.Password = pw
			}
			if needSSH {
				c.SSH.Secret = sshSecret
			}
			// secrets are remembered only once they worked
			a.openConn(cs, c, func() {
				if needPw {
					_ = secrets.Set(id, secrets.Password, pw, save && c.SavePassword)
				}
				if needSSH {
					_ = secrets.Set(id, secrets.SSH, sshSecret, save && c.SavePassword)
				}
			})
		}, func() {
			if cs.gen == gen {
				cs.fail(errConnectCancelled)
			}
		})
		return
	}
	a.openConn(cs, c, nil)
}

var errConnectCancelled = errors.New("connection cancelled")

// passwordOptional: connections without a user (trust/peer auth, Trino,
// local ClickHouse) don't need a prompt.
func passwordOptional(c *db.Config) bool { return strings.TrimSpace(c.User) == "" }

func (a *App) openConn(cs *connState, c db.Config, onOK func()) {
	cs.status, cs.err = stConnecting, nil
	cs.gen++
	gen := cs.gen
	a.invalidate()
	hostKey := knownhosts.Callback(func(host, fp string) bool {
		answer := make(chan bool, 1)
		a.post(func() {
			cf := newConfirm(a, "Trust SSH host?", fmt.Sprintf("The SSH host %s is not known yet.\n\nKey fingerprint:\n%s\n\nTrust it and remember it?", host, fp), false, func(bool) { answer <- true })
			cf.onCancel = func() { answer <- false }
			a.overlay = cf
		})
		select {
		case ok := <-answer:
			return ok
		case <-time.After(2 * time.Minute):
			return false
		}
	})
	go func() {
		start := time.Now()
		conn, err := db.Open(context.Background(), &c, db.OpenOptions{HostKey: hostKey})
		a.post(func() {
			if cs.gen != gen { // disconnected or reconnected meanwhile
				if conn != nil {
					conn.Close()
				}
				return
			}
			if err != nil {
				cs.fail(err)
				return
			}
			cs.conn, cs.status = conn, stReady
			cs.resetMeta()
			if onOK != nil {
				onOK()
			}
			slog.Info("connected", "conn", c.Name, "version", conn.ServerVersion(), "took", time.Since(start))
			a.flash(fmt.Sprintf("Connected to %s (%s)", c.Name, conn.ServerVersion()), false)
			w := cs.waiters
			cs.waiters = nil
			for _, f := range w {
				if f.ok != nil {
					f.ok(cs)
				}
			}
			a.sidebar.expand(connKey(cs.id), true)
		})
	}()
}

func (cs *connState) fail(err error) {
	cs.status, cs.err = stFailed, err
	cs.dropWaiters(err)
	var ce *knownhosts.ErrChanged
	if errors.As(err, &ce) {
		cs.a.overlay = newMessage(cs.a, "SSH host key changed", ce.Error(), true)
	}
	if err != errConnectCancelled {
		cs.a.flash(fmt.Sprintf("%s: %v", cs.name(), err), true)
	}
}

// dropWaiters tells the waiting callers that the attempt is over.
func (cs *connState) dropWaiters(err error) {
	w := cs.waiters
	cs.waiters = nil
	for _, f := range w {
		if f.fail != nil {
			f.fail(err)
		}
	}
}

// disconnect closes the connection and the sessions of its tabs.
func (a *App) disconnect(id string) {
	cs := a.connState(id)
	for _, t := range a.tabs {
		if t.ConnID() == id {
			t.Disconnected()
		}
	}
	cs.gen++
	if cs.conn != nil {
		c := cs.conn
		go c.Close()
	}
	cs.conn, cs.status, cs.err = nil, stIdle, nil
	cs.dropWaiters(errors.New("disconnected"))
	cs.resetMeta()
}

// ---- metadata (lazy, cached, refreshable) ----

// load runs fetch in background once per key and stores its result with
// store, on the UI goroutine.
func (cs *connState) load(key string, fetch func(ctx context.Context, c db.Conn) (any, error), store func(any)) {
	if cs.status != stReady || cs.loading[key] {
		return
	}
	cs.loading[key] = true
	delete(cs.errs, key)
	gen, c := cs.gen, cs.conn
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), db.MetaTimeout)
		v, err := fetch(ctx, c)
		cancel()
		cs.a.post(func() {
			if cs.gen != gen {
				return
			}
			delete(cs.loading, key)
			if err != nil {
				cs.errs[key] = err
				return
			}
			store(v)
		})
	}()
}

func (cs *connState) loadCatalogs() {
	cs.load("catalogs", func(ctx context.Context, c db.Conn) (any, error) { return c.Catalog().Catalogs(ctx) },
		func(v any) { cs.catalogs = v.([]string) })
}

func (cs *connState) loadSchemas(cat string) {
	cs.load("schemas:"+cat, func(ctx context.Context, c db.Conn) (any, error) { return c.Catalog().Schemas(ctx, cat) },
		func(v any) { cs.schemas[cat] = v.([]db.Schema) })
}

func (cs *connState) loadObjects(k metaKey) {
	cs.load("objects:"+k.catalog+"\x00"+k.schema, func(ctx context.Context, c db.Conn) (any, error) {
		return c.Catalog().Objects(ctx, db.ObjectRef{Catalog: k.catalog, Schema: k.schema})
	}, func(v any) { cs.objects[k] = v.([]db.Object) })
}

func tableKey(r db.ObjectRef) string { return "table:" + r.Catalog + "\x00" + r.Schema + "\x00" + r.Name }

// loadTable describes a table; then runs with the result (also cached).
func (cs *connState) loadTable(ref db.ObjectRef, then func(*db.TableInfo, error)) {
	ref.Signature = ""
	if t, ok := cs.tables[ref]; ok && then != nil {
		then(t, nil)
		return
	}
	k := tableKey(ref)
	if then != nil {
		// callers waiting for the result: run fetch even if one is in flight
		gen, c := cs.gen, cs.conn
		if cs.status != stReady || c == nil {
			then(nil, errors.New("not connected"))
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), db.MetaTimeout)
			t, err := c.Catalog().Table(ctx, ref)
			cancel()
			cs.a.post(func() {
				if cs.gen != gen {
					then(nil, errors.New("connection was reset"))
					return
				}
				if err == nil {
					cs.tables[ref] = t
				}
				then(t, err)
			})
		}()
		return
	}
	cs.load(k, func(ctx context.Context, c db.Conn) (any, error) { return c.Catalog().Table(ctx, ref) },
		func(v any) { cs.tables[ref] = v.(*db.TableInfo) })
}

func (cs *connState) loadCompletion(k metaKey) {
	if _, ok := cs.completion[k]; ok {
		return
	}
	cs.load("completion:"+k.catalog+"."+k.schema, func(ctx context.Context, c db.Conn) (any, error) {
		return c.Catalog().Completion(ctx, db.ObjectRef{Catalog: k.catalog, Schema: k.schema})
	}, func(v any) { cs.completion[k] = v.(map[string][]string) })
}

// refreshSchema forgets the cached objects of a schema (after DDL).
func (cs *connState) refreshSchema(k metaKey) {
	delete(cs.objects, k)
	delete(cs.completion, k)
	for r := range cs.tables {
		if keyOf(r) == k {
			delete(cs.tables, r)
		}
	}
}

// refreshAll forgets every cached object.
func (cs *connState) refreshAll() {
	if cs.status == stReady {
		cs.resetMeta()
	}
}

// defaultSchema picks the schema a new console starts in.
func (cs *connState) defaultSchema(cat string) string {
	for _, s := range cs.schemas[cat] {
		if s.Current {
			return s.Name
		}
	}
	return ""
}

func secretsAvailable() bool { return secrets.Available() }

// releaseCursors closes the open cursors of a connection's tabs before a
// write when the database allows a single writer at a time (SQLite).
func (a *App) releaseCursors(connID string) {
	cs := a.connState(connID)
	if !cs.caps().SingleWriter {
		return
	}
	for _, t := range a.tabs {
		if t.ConnID() != connID {
			continue
		}
		switch x := t.(type) {
		case *ConsoleTab:
			for _, rs := range x.results {
				if rs.res != nil {
					rs.closeCursor()
					rs.partial = "cursor closed to let a write through (re-run to fetch more)"
					rs.more = false
				}
			}
		case *TableTab:
			if x.data != nil && x.data.rs.res != nil {
				x.data.rs.closeCursor()
				x.data.rs.partial = "cursor closed to let a write through (reload to fetch more)"
				x.data.rs.more = false
			}
		}
	}
}
