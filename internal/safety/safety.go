// Package safety keeps development builds away from real databases.
//
// Release builds connect anywhere. Builds with the "dev" tag only accept the
// disposable databases of scripts/dbs (127.0.0.1, ports 55000-55999), SQLite
// files inside a .dev directory, and keep their settings under .dev/, so
// development and tests never touch the user's real connections, keyring
// or databases.
package safety

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/giovanni/lumino/internal/db"
)

// DevPorts is the port range of the development databases.
const DevPortMin, DevPortMax = 55000, 55999

func devHost(host string, port int) error {
	h := strings.TrimSpace(host)
	if ip := net.ParseIP(h); !(h == "localhost" || ip != nil && ip.IsLoopback()) {
		return fmt.Errorf("dev build: host %q refused (only 127.0.0.1 development databases)", host)
	}
	if port < DevPortMin || port > DevPortMax {
		return fmt.Errorf("dev build: port %d refused (development databases use %d-%d)", port, DevPortMin, DevPortMax)
	}
	return nil
}

// CheckConnection validates a connection before opening it.
func CheckConnection(c *db.Config) error {
	if !Dev {
		return nil
	}
	d, ok := db.Lookup(c.Driver)
	if !ok {
		return fmt.Errorf("unknown driver %q", c.Driver)
	}
	info := d.Info()
	if info.Has(db.FieldFile) {
		if c.File == ":memory:" {
			return nil
		}
		if strings.ContainsAny(c.File, "?#%") {
			return fmt.Errorf("dev build: database file %q refused (URI characters)", c.File)
		}
		abs, err := filepath.Abs(c.File)
		if err != nil {
			return err
		}
		// the real directory (symlinks resolved) must be inside a .dev one
		dir := filepath.Dir(abs)
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		if !inDevDir(dir) {
			return fmt.Errorf("dev build: database file %q refused (must live in a .dev/ directory)", c.File)
		}
		return nil
	}
	if c.SSH != nil && c.SSH.Host != "" {
		if err := devHost(c.SSH.Host, c.SSH.Port); err != nil {
			return fmt.Errorf("SSH: %w", err)
		}
		// the target is resolved by the bastion: only compose service names
		for _, h := range strings.Split(c.Host, ",") {
			h = strings.TrimSpace(h)
			if i := strings.LastIndexByte(h, ':'); i > 0 {
				h = h[:i]
			}
			if !composeName(h) {
				return fmt.Errorf("dev build: SSH target %q refused (only development compose services)", h)
			}
		}
		return nil
	}
	port := c.Port
	if port == 0 {
		port = info.DefaultPort
	}
	for _, h := range strings.Split(c.Host, ",") {
		if err := devHost(h, port); err != nil {
			return err
		}
	}
	return nil
}

// inDevDir reports whether a path has a ".dev" directory component.
func inDevDir(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".dev" {
			return true
		}
	}
	return false
}

// composeName reports whether h looks like a compose service name.
func composeName(h string) bool {
	if h == "" || h == "localhost" || net.ParseIP(h) != nil {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// IsolateConfig points the settings directory inside .dev in dev builds.
func IsolateConfig() {
	if !Dev {
		return
	}
	if d := os.Getenv("LUMINO_CONFIG_DIR"); d != "" && inDevDir(d) {
		return
	}
	abs, _ := filepath.Abs(filepath.Join(".dev", "config"))
	_ = os.Setenv("LUMINO_CONFIG_DIR", abs)
}
