// Command lumino is a fast, native database IDE.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/twoojoo/lumino/internal/desktop"
	"github.com/twoojoo/lumino/internal/safety"
	"github.com/twoojoo/lumino/internal/secrets"
	"github.com/twoojoo/lumino/internal/ui"
	"github.com/twoojoo/lumino/internal/update"

	// database adapters
	_ "github.com/twoojoo/lumino/internal/db/adapters/cassandra"
	_ "github.com/twoojoo/lumino/internal/db/adapters/clickhouse"
	_ "github.com/twoojoo/lumino/internal/db/adapters/cockroach"
	_ "github.com/twoojoo/lumino/internal/db/adapters/firebird"
	_ "github.com/twoojoo/lumino/internal/db/adapters/mssql"
	_ "github.com/twoojoo/lumino/internal/db/adapters/mysql"
	_ "github.com/twoojoo/lumino/internal/db/adapters/oracle"
	_ "github.com/twoojoo/lumino/internal/db/adapters/postgres"
	_ "github.com/twoojoo/lumino/internal/db/adapters/sqlite"
	_ "github.com/twoojoo/lumino/internal/db/adapters/trino"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	attachConsole()
	var (
		conn     = flag.String("connection", "", "saved connection to open (name or id)")
		logLevel = flag.String("log-level", "warn", "debug|info|warn|error")
		showVer  = flag.Bool("version", false, "print the version and exit")
	)
	// old macOS launchers pass a process serial number (-psn_0_12345)
	os.Args = slices.DeleteFunc(os.Args, func(a string) bool { return strings.HasPrefix(a, "-psn_") })
	flag.Parse()
	if *showVer {
		fmt.Println("lumino", version)
		return
	}
	ui.Version = version
	update.Cleanup() // leftovers of a previous self-update (Windows)

	// desktop launchers start apps with a minimal environment; release
	// builds load the login shell's (SSH agent socket, PATH).
	if !safety.Dev {
		desktop.ImportShellEnv()
	}
	// dev builds keep settings under .dev and never touch the real keyring
	safety.IsolateConfig()
	if safety.Dev {
		secrets.UseMock()
	}
	if err := setupLog(*logLevel); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if safety.Dev {
		slog.Info("dev build: development databases only", "config", os.Getenv("LUMINO_CONFIG_DIR"))
	}

	// match the desktop cursor (Gio's Wayland backend defaults to size 32)
	desktop.ApplyCursorEnv()

	if err := ui.Run(ui.Options{Connection: *conn}); err != nil {
		fmt.Fprintln(os.Stderr, "lumino:", err)
		os.Exit(1)
	}
}

// setupLog writes logs to the user cache dir, never to the terminal.
func setupLog(level string) error {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelWarn
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "lumino")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lumino.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: lv})))
	return nil
}
