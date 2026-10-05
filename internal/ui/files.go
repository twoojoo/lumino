package ui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// downloadsDir resolves where exported files go: $LUMINO_DOWNLOAD_DIR,
// XDG_DOWNLOAD_DIR (user-dirs.dirs), ~/Downloads, then home.
func downloadsDir() string {
	if d := os.Getenv("LUMINO_DOWNLOAD_DIR"); d != "" {
		_ = os.MkdirAll(d, 0o755)
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	home, _ := os.UserHomeDir()
	if f, err := os.Open(filepath.Join(home, ".config", "user-dirs.dirs")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if v, ok := strings.CutPrefix(l, "XDG_DOWNLOAD_DIR="); ok {
				v = strings.ReplaceAll(strings.Trim(v, `"`), "$HOME", home)
				if st, err := os.Stat(v); err == nil && st.IsDir() {
					return v
				}
			}
		}
	}
	if d := filepath.Join(home, "Downloads"); dirExists(d) {
		return d
	}
	return home
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// saveNew writes data to a new file in the downloads directory with mode
// 0600 (logs and secrets may hold credentials), never overwriting: name
// collisions get a numeric suffix. Returns the path written.
func saveNew(name string, data []byte) (string, error) {
	dir := downloadsDir()
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, name)
		if i > 0 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return "", err
		}
		return p, f.Close()
	}
	return "", fmt.Errorf("too many files named %s", name)
}
