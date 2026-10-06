package prefs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/giovanni/lumino/internal/ai"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/secrets"
)

// unreadable writes content to name in dir and removes read permission.
func unreadable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running with permissions that ignore file modes (root?)")
	}
	return path
}

// An unreadable settings file (EACCES, EIO…) must not be replaced with
// the defaults on the next save: the saved connections would be lost.
func TestLoadRegressUnreadableSettingsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUMINO_CONFIG_DIR", dir)
	const content = `{"connections":[{"id":"a","name":"keep me","driver":"sqlite","file":"/x.db"}]}`
	path := unreadable(t, dir, "settings.json", content)

	p := prefs.Load()
	if p.LoadError == nil {
		t.Fatal("no LoadError for an unreadable settings file")
	}
	if err := p.Save(); !errors.Is(err, prefs.ErrUnreadable) {
		t.Fatalf("Save = %v, want ErrUnreadable", err)
	}
	_ = os.Chmod(path, 0o600)
	b, err := os.ReadFile(path)
	if err != nil || string(b) != content {
		t.Fatalf("settings file changed: %q %v", b, err)
	}
}

func TestLoadRegressMissingSettingsStillSaves(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	p := prefs.Load()
	if p.LoadError != nil {
		t.Fatal(p.LoadError)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRegressUnreadableAIConfigNotOverwritten(t *testing.T) {
	secrets.UseMock()
	dir := t.TempDir()
	t.Setenv("LUMINO_CONFIG_DIR", dir)
	const content = `{"provider":"openai","disabledConnections":["x"]}`
	path := unreadable(t, dir, "ai.json", content)

	c := ai.LoadConfig()
	if c.LoadError == nil {
		t.Fatal("no LoadError for an unreadable ai.json")
	}
	if err := c.Save(); err == nil {
		t.Fatal("Save overwrote an unreadable ai.json")
	}
	_ = os.Chmod(path, 0o600)
	if b, _ := os.ReadFile(path); string(b) != content {
		t.Fatalf("ai.json changed: %q", b)
	}

	// missing file: defaults, saving works
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	c = ai.LoadConfig()
	if c.LoadError != nil {
		t.Fatal(c.LoadError)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}
