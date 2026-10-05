package prefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/giovanni/lumino/internal/db"
)

func TestSaveLoadNoSecrets(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	p := Load()
	if p.Theme != "ayu-dark" {
		t.Fatalf("default theme %q", p.Theme)
	}
	p.PutConn(db.Config{ID: "a", Name: "prod-db", Driver: "postgres", Password: "s3cret",
		SSH: &db.SSHConfig{Host: "bastion", User: "u", Auth: "password", Secret: "ssh-secret"}})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(Dir(), "settings.json"))
	for _, s := range []string{"s3cret", "ssh-secret"} {
		if contains(string(b), s) {
			t.Fatalf("secret %q written to settings.json", s)
		}
	}
	q := Load()
	c, ok := q.Conn("a")
	if !ok || c.SSH == nil || c.SSH.Host != "bastion" {
		t.Fatalf("connection not restored: %+v", c)
	}
	if !Protected(c) {
		t.Fatal("prod-db should be protected by name")
	}
	off := false
	c.Protected = &off
	if Protected(c) {
		t.Fatal("explicit policy ignored")
	}
}

func TestBrokenFileQuarantined(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	_ = os.WriteFile(filepath.Join(Dir(), "settings.json"), []byte("{broken"), 0o600)
	p := Load()
	if p.LoadError == nil {
		t.Fatal("expected a load error")
	}
	m, _ := filepath.Glob(filepath.Join(Dir(), "settings.json.broken-*"))
	if len(m) != 1 {
		t.Fatalf("broken file not kept: %v", m)
	}
}

func TestMoveAndRemove(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	p := Load()
	for _, id := range []string{"a", "b", "c"} {
		p.PutConn(db.Config{ID: id, Name: id})
	}
	p.SaveQuery(SavedQuery{Name: "q", ConnID: "b", SQL: "select 1"})
	p.MoveConn("c", 0)
	if p.Connections[0].ID != "c" {
		t.Fatalf("order %v", p.Connections)
	}
	p.RemoveConn("b")
	if len(p.Connections) != 2 || len(p.QueriesFor("b")) != 0 {
		t.Fatal("remove left data behind")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
