// Command kubeon is a native Kubernetes IDE (Lens-like) powered by the k9s engine.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/giovanni/lumino/internal/desktop"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/safety"
	"github.com/giovanni/lumino/internal/ui"
	"github.com/giovanni/lumino/internal/update"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	attachConsole()
	var (
		kubeconfig = flag.String("kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
		kctx       = flag.String("context", "", "kube context to open (default: current-context)")
		ns         = flag.String("namespace", "", "initial namespace (default: all)")
		resource   = flag.String("resource", "", "initial resource gvr or alias, e.g. v1/pods")
		logLevel   = flag.String("log-level", "warn", "debug|info|warn|error")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	// old macOS launchers pass a process serial number (-psn_0_12345)
	os.Args = slices.DeleteFunc(os.Args, func(a string) bool { return strings.HasPrefix(a, "-psn_") })
	flag.Parse()
	if *showVer {
		fmt.Println("kubeon", version)
		return
	}
	ui.Version = version
	update.Cleanup() // leftovers of a previous self-update (Windows)

	// desktop launchers start apps with a minimal environment; release
	// builds load the login shell's (PATH for exec auth plugins, KUBECONFIG).
	// Dev builds never do: they must not see the user's kube settings.
	if !safety.Dev {
		desktop.ImportShellEnv()
	}

	prefs.MigrateLegacyDirs()
	if err := setupLog(*logLevel); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	kc, err := safety.ResolveKubeconfig(*kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kubeon:", err)
		os.Exit(2)
	}
	if *kctx != "" {
		if err := safety.CheckContext(*kctx); err != nil {
			fmt.Fprintln(os.Stderr, "kubeon:", err)
			os.Exit(2)
		}
	}
	safety.IsolateCaches(kc)
	if safety.Dev {
		slog.Info("dev build: locked to", "kubeconfig", kc)
	}

	// match the desktop cursor (Gio's Wayland backend defaults to size 32)
	desktop.ApplyCursorEnv()

	if err := ui.Run(ui.Options{Kubeconfig: kc, Context: *kctx, Namespace: *ns, Resource: *resource}); err != nil {
		fmt.Fprintln(os.Stderr, "kubeon:", err)
		os.Exit(1)
	}
}

// setupLog writes logs to the user cache dir, never to the terminal UI.
func setupLog(level string) error {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelWarn
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "kubeon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "kubeon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: lv})))
	return nil
}
