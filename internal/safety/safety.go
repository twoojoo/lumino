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
		abs, err := filepath.Abs(c.File)
		if err != nil {
			return err
		}
		if !strings.Contains(abs, string(filepath.Separator)+".dev"+string(filepath.Separator)) {
			return fmt.Errorf("dev build: database file %q refused (must live in a .dev/ directory)", c.File)
		}
		return nil
	}
	if c.SSH != nil && c.SSH.Host != "" {
		if err := devHost(c.SSH.Host, c.SSH.Port); err != nil {
			return fmt.Errorf("SSH: %w", err)
		}
		return nil // the target is resolved by the bastion (compose network)
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

// IsolateConfig points the settings directory inside .dev in dev builds.
func IsolateConfig() {
	if !Dev {
		return
	}
	if d := os.Getenv("LUMINO_CONFIG_DIR"); d != "" && strings.Contains(d, ".dev") {
		return
	}
	abs, _ := filepath.Abs(filepath.Join(".dev", "config"))
	_ = os.Setenv("LUMINO_CONFIG_DIR", abs)
}
