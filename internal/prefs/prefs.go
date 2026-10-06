// Package prefs persists user preferences in ~/.config/lumino/settings.json:
// theme, saved connections (without secrets: those live in the OS keyring),
// saved queries and the workspace (open tabs) restored at startup.
package prefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	"github.com/giovanni/lumino/internal/db"
)

// SavedQuery is a named query of a connection ("" = any connection).
type SavedQuery struct {
	Name   string `json:"name"`
	ConnID string `json:"connID,omitempty"`
	SQL    string `json:"sql"`
}

// TabState is an open tab restored at startup.
type TabState struct {
	Kind   string       `json:"kind"` // console | table
	ConnID string       `json:"connID"`
	Title  string       `json:"title,omitempty"`
	Text   string       `json:"text,omitempty"` // console SQL
	Schema string       `json:"schema,omitempty"`
	Ref    db.ObjectRef `json:"ref,omitempty"` // table tab
	Manual bool         `json:"manual,omitempty"`
}

// Prefs is the persisted state.
type Prefs struct {
	Theme        string       `json:"theme,omitempty"` // ayu-dark | ayu-light
	Connections  []db.Config  `json:"connections,omitempty"`
	SavedQueries []SavedQuery `json:"savedQueries,omitempty"`
	Tabs         []TabState   `json:"tabs,omitempty"`
	ActiveTab    int          `json:"activeTab,omitempty"`
	CheckUpdates *bool        `json:"checkUpdates,omitempty"` // nil = on
	FetchSize    int          `json:"fetchSize,omitempty"`    // rows per page (default 500)
	MaxRows      int          `json:"maxRows,omitempty"`      // rows kept in memory per result (default 1M)
	NotifyAfter  int          `json:"notifyAfter,omitempty"`  // seconds before a finished query notifies (default 10)
	Expanded     []string     `json:"expanded,omitempty"`     // tree nodes left open
	SidebarWidth int          `json:"sidebarWidth,omitempty"`
	// ValuePanelWidth is the width of the value panel drawer (px, 0 = default).
	ValuePanelWidth int            `json:"valuePanelWidth,omitempty"`
	FontSize        int            `json:"fontSize,omitempty"` // editor/grid font size (default 13)
	Extra           map[string]any `json:"extra,omitempty"`

	// LoadError reports a settings file that could not be parsed (moved
	// aside) or read (left in place, and Save refuses to overwrite it).
	LoadError error `json:"-"`

	mx   sync.Mutex
	path string
	// unreadable: the file exists but couldn't be read (EACCES, EIO…):
	// saving the defaults would replace it and lose the connections.
	unreadable bool
}

// ErrUnreadable is returned by Save when the settings file could not be
// read at startup: it is never overwritten with the defaults.
var ErrUnreadable = errors.New("the settings file could not be read at startup: not overwriting it (fix its permissions and restart)")

// Dir returns the config directory ($LUMINO_CONFIG_DIR overrides).
func Dir() string {
	if d := os.Getenv("LUMINO_CONFIG_DIR"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "lumino")
}

// Load reads the preferences (missing file → defaults).
func Load() *Prefs {
	p := Prefs{path: filepath.Join(Dir(), "settings.json")}
	b, err := os.ReadFile(p.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &p); err != nil {
			// keep the broken file (saved connections!) instead of
			// overwriting it with defaults on the next save
			p = Prefs{path: p.path, LoadError: fmt.Errorf("%s is invalid (%v); moved to %s", p.path, err, Quarantine(p.path, err))}
		}
	case !errors.Is(err, fs.ErrNotExist):
		p.unreadable = true
		p.LoadError = fmt.Errorf("cannot read %s (%v): changes won't be saved until it is readable", p.path, err)
	}
	if p.Theme != "ayu-light" && p.Theme != "ayu-mirage" {
		p.Theme = "ayu-dark"
	}
	return &p
}

// Save writes the preferences (0600).
func (p *Prefs) Save() error {
	p.mx.Lock()
	defer p.mx.Unlock()
	if p.unreadable {
		return ErrUnreadable
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p.path, b)
}

var prodRx = regexp.MustCompile(`(?i)(^|[^a-z])(prod|production|prd|live)([^a-z]|$)`)

// LooksProduction guesses production connections from their name.
func LooksProduction(name string) bool { return prodRx.MatchString(name) }

// Protected reports whether a connection is protected (explicit or guessed).
func Protected(c *db.Config) bool {
	if c.Protected != nil {
		return *c.Protected
	}
	return LooksProduction(c.Name)
}

// Conn returns the connection with id.
func (p *Prefs) Conn(id string) (*db.Config, bool) {
	for i := range p.Connections {
		if p.Connections[i].ID == id {
			return &p.Connections[i], true
		}
	}
	return nil, false
}

// PutConn adds or replaces a connection (by id).
func (p *Prefs) PutConn(c db.Config) {
	c.Password = ""
	if c.SSH != nil {
		s := *c.SSH
		s.Secret = ""
		c.SSH = &s
	}
	for i := range p.Connections {
		if p.Connections[i].ID == c.ID {
			p.Connections[i] = c
			return
		}
	}
	p.Connections = append(p.Connections, c)
}

// RemoveConn deletes a connection and its saved queries.
func (p *Prefs) RemoveConn(id string) {
	p.Connections = slices.DeleteFunc(p.Connections, func(c db.Config) bool { return c.ID == id })
	p.SavedQueries = slices.DeleteFunc(p.SavedQueries, func(q SavedQuery) bool { return q.ConnID == id })
	p.Tabs = slices.DeleteFunc(p.Tabs, func(t TabState) bool { return t.ConnID == id })
}

// MoveConn moves a connection before index to (reordering in the tree).
func (p *Prefs) MoveConn(id string, to int) {
	i := slices.IndexFunc(p.Connections, func(c db.Config) bool { return c.ID == id })
	if i < 0 {
		return
	}
	c := p.Connections[i]
	p.Connections = slices.Delete(p.Connections, i, i+1)
	to = max(0, min(to, len(p.Connections)))
	p.Connections = slices.Insert(p.Connections, to, c)
}

// UpdatesEnabled reports whether lumino looks for new releases (default on).
func (p *Prefs) UpdatesEnabled() bool { return p.CheckUpdates == nil || *p.CheckUpdates }

// SetUpdates turns release checks on or off.
func (p *Prefs) SetUpdates(on bool) { p.CheckUpdates = &on }

// Fetch returns the page size for result fetching.
func (p *Prefs) Fetch() int {
	if p.FetchSize <= 0 {
		return 500
	}
	return p.FetchSize
}

// RowLimit returns the maximum rows kept in memory per result.
func (p *Prefs) RowLimit() int {
	if p.MaxRows <= 0 {
		return 1_000_000
	}
	return p.MaxRows
}

// NotifySeconds returns the duration after which finished queries notify.
func (p *Prefs) NotifySeconds() int {
	if p.NotifyAfter <= 0 {
		return 10
	}
	return p.NotifyAfter
}

// SaveQuery adds or replaces a saved query (same name and connection).
func (p *Prefs) SaveQuery(q SavedQuery) {
	p.RemoveQuery(q)
	p.SavedQueries = append(p.SavedQueries, q)
}

// RemoveQuery deletes the saved query with q's name and connection.
func (p *Prefs) RemoveQuery(q SavedQuery) {
	p.SavedQueries = slices.DeleteFunc(p.SavedQueries, func(x SavedQuery) bool { return x.Name == q.Name && x.ConnID == q.ConnID })
}

// QueriesFor returns saved queries usable on a connection.
func (p *Prefs) QueriesFor(connID string) []SavedQuery {
	var out []SavedQuery
	for _, q := range p.SavedQueries {
		if q.ConnID == "" || q.ConnID == connID {
			out = append(out, q)
		}
	}
	return out
}
