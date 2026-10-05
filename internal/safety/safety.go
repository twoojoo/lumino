// Package safety resolves which kubeconfig kubeon may use.
//
// Release builds behave like kubectl. Builds with the "dev" tag are locked to
// the disposable kind cluster in .dev/kubeconfig (context kind-kubeon-*), so
// development never touches real clusters configured on the machine.
package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DevContextPrefix is the only context prefix allowed in dev builds.
const DevContextPrefix = "kind-kubeon"

// ResolveKubeconfig returns the kubeconfig path to use.
func ResolveKubeconfig(flag string) (string, error) {
	p := flag
	if p == "" {
		p = defaultKubeconfig()
	}
	if p == "" {
		return "", fmt.Errorf("no kubeconfig found")
	}
	// release builds accept a $KUBECONFIG path list (merged like kubectl)
	var out []string
	for _, f := range filepath.SplitList(p) {
		if f == "" {
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			return "", err
		}
		if err := checkPath(abs); err != nil {
			return "", err
		}
		out = append(out, abs)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no kubeconfig found")
	}
	if Dev && len(out) > 1 {
		return "", fmt.Errorf("dev build: a single kubeconfig is required")
	}
	return strings.Join(out, string(filepath.ListSeparator)), nil
}

// CheckContext validates a context name before connecting.
func CheckContext(name string) error {
	if Dev && !strings.HasPrefix(name, DevContextPrefix) {
		return fmt.Errorf("dev build: context %q refused (only %s* allowed)", name, DevContextPrefix)
	}
	return nil
}

// FilterContexts drops contexts not allowed in this build.
func FilterContexts(names []string) []string {
	if !Dev {
		return names
	}
	out := names[:0:0]
	for _, n := range names {
		if CheckContext(n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func defaultKubeconfig() string {
	if Dev {
		return devKubeconfig()
	}
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

func devKubeconfig() string {
	if p := os.Getenv("KUBEON_DEV_KUBECONFIG"); p != "" {
		return p
	}
	return filepath.Join(".dev", "kubeconfig")
}

func checkPath(abs string) error {
	if !Dev {
		return nil
	}
	if filepath.Base(filepath.Dir(abs)) != ".dev" {
		return fmt.Errorf("dev build: kubeconfig %q refused (must live in a .dev/ directory)", abs)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(abs, filepath.Join(home, ".kube")) {
			return fmt.Errorf("dev build: %q refused", abs)
		}
	}
	return nil
}

// IsolateCaches points client-go/k9s caches inside .dev in dev builds so
// development never mixes with the user's ~/.kube/cache.
func IsolateCaches(kubeconfig string) {
	if !Dev {
		return
	}
	_ = os.Setenv("KUBECACHEDIR", filepath.Join(filepath.Dir(kubeconfig), "cache"))
	_ = os.Unsetenv("KUBECONFIG")
}
