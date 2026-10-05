// Package secrets keeps connection passwords in the OS keyring (Secret
// Service on Linux, Keychain on macOS, Credential Manager on Windows).
// When no keyring is available, secrets live in memory for the session
// only: they are never written to disk in plain text.
package secrets

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/zalando/go-keyring"
)

const service = "lumino"

var (
	mu       sync.Mutex
	memory   = map[string]string{}
	disabled bool // keyring unavailable: memory only
	probed   bool
)

// Kinds of secrets of a connection.
const (
	Password = "password"
	SSH      = "ssh"
	APIKey   = "apikey"
)

func key(connID, kind string) string { return connID + ":" + kind }

// Available reports whether secrets persist across sessions (probes the
// keyring once).
func Available() bool {
	mu.Lock()
	defer mu.Unlock()
	probe()
	return !disabled
}

func probe() {
	if probed {
		return
	}
	probed = true
	_, err := keyring.Get(service, "probe")
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		slog.Warn("OS keyring unavailable: secrets kept for this session only", "err", err)
		disabled = true
	}
}

// Get returns a secret ("" and false when not stored).
func Get(connID, kind string) (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	k := key(connID, kind)
	if v, ok := memory[k]; ok {
		return v, true
	}
	probe()
	if disabled {
		return "", false
	}
	v, err := keyring.Get(service, k)
	if err != nil {
		return "", false
	}
	memory[k] = v
	return v, true
}

// Set stores a secret: in the keyring when persist is true and available,
// in memory (this session) otherwise.
func Set(connID, kind, value string, persist bool) error {
	mu.Lock()
	defer mu.Unlock()
	k := key(connID, kind)
	memory[k] = value
	probe()
	if !persist || disabled {
		if !disabled {
			_ = keyring.Delete(service, k) // "don't save" removes an old copy
		}
		return nil
	}
	if err := keyring.Set(service, k, value); err != nil {
		return err
	}
	return nil
}

// Delete forgets a secret everywhere.
func Delete(connID, kind string) {
	mu.Lock()
	defer mu.Unlock()
	k := key(connID, kind)
	delete(memory, k)
	probe()
	if !disabled {
		_ = keyring.Delete(service, k)
	}
}

// DeleteAll forgets every secret of a connection.
func DeleteAll(connID string) {
	for _, k := range []string{Password, SSH, APIKey} {
		Delete(connID, k)
	}
}

// UseMock switches to an in-process keyring (tests, dev builds).
func UseMock() {
	mu.Lock()
	defer mu.Unlock()
	keyring.MockInit()
	probed, disabled = true, false
	memory = map[string]string{}
}
