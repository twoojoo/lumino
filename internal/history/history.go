// Package history records executed statements per connection in
// ~/.config/lumino/history/<connection>.jsonl (append-only, trimmed).
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/twoojoo/lumino/internal/prefs"
)

// Entry is one executed statement.
type Entry struct {
	At     time.Time `json:"at"`
	SQL    string    `json:"sql"`
	Millis int64     `json:"ms"`
	Rows   int64     `json:"rows"` // fetched or affected, -1 unknown
	Error  string    `json:"error,omitempty"`
	Schema string    `json:"schema,omitempty"`
}

// MaxEntries kept per connection (older ones are trimmed on load).
const MaxEntries = 5000

var mu sync.Mutex

func dir() string { return filepath.Join(prefs.Dir(), "history") }

func file(connID string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '.' {
			return '_'
		}
		return r
	}, connID)
	return filepath.Join(dir(), safe+".jsonl")
}

// Add appends an entry (empty statements are ignored).
// Size limits: huge statements (dump imports) are truncated in history.
const (
	maxSQL  = 256 << 10
	maxLine = 4 << 20 // JSON escaping can grow a truncated statement up to 6x
)

func Add(connID string, e Entry) error {
	if strings.TrimSpace(e.SQL) == "" || connID == "" {
		return nil
	}
	if len(e.SQL) > maxSQL {
		cut := maxSQL
		for cut > 0 && !utf8.RuneStart(e.SQL[cut]) {
			cut--
		}
		e.SQL = e.SQL[:cut] + "\n-- … (truncated in history)"
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file(connID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load returns entries newest first; the file is rewritten when it holds
// more than MaxEntries.
func Load(connID string) ([]Entry, error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := os.Open(file(connID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Entry
	// a reader instead of a Scanner: an oversized line (older versions
	// stored huge statements) is skipped instead of failing the whole load
	br := bufio.NewReaderSize(f, 64*1024)
	rewrite := false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var e Entry
			if len(line) <= maxLine && json.Unmarshal(line, &e) == nil {
				all = append(all, e)
			} else {
				rewrite = true
			}
		}
		if err != nil {
			break
		}
	}
	f.Close()
	if len(all) > MaxEntries || rewrite {
		all = all[max(0, len(all)-MaxEntries):]
		var b strings.Builder
		for _, e := range all {
			j, _ := json.Marshal(e)
			b.Write(j)
			b.WriteByte('\n')
		}
		_ = prefs.WriteFileAtomic(file(connID), []byte(b.String()))
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

// Clear deletes the history of a connection.
func Clear(connID string) error {
	mu.Lock()
	defer mu.Unlock()
	err := os.Remove(file(connID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
