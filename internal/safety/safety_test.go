//go:build dev

package safety

import (
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	_ "github.com/twoojoo/lumino/internal/db/adapters/postgres"
	_ "github.com/twoojoo/lumino/internal/db/adapters/sqlite"
)

func TestDevGuard(t *testing.T) {
	ok := []db.Config{
		{Driver: "postgres", Host: "127.0.0.1", Port: 55432},
		{Driver: "postgres", Host: "postgres", Port: 5432, SSH: &db.SSHConfig{Host: "127.0.0.1", Port: 55222}},
		{Driver: "sqlite", File: "/x/.dev/a.sqlite"},
	}
	for _, c := range ok {
		if err := CheckConnection(&c); err != nil {
			t.Errorf("%+v refused: %v", c, err)
		}
	}
	bad := []db.Config{
		{Driver: "postgres", Host: "127.0.0.1", Port: 5432},
		{Driver: "postgres", Host: "db.example.com", Port: 55432},
		{Driver: "postgres", Host: "127.0.0.1"},
		{Driver: "postgres", Host: "x", SSH: &db.SSHConfig{Host: "bastion.example.com", Port: 22}},
		{Driver: "sqlite", File: "/home/u/real.db"},
		{Driver: "sqlite", File: "/home/u/real.db?/.dev/x"},
		{Driver: "sqlite", File: "/home/u/real.db#/.dev/x"},
		{Driver: "sqlite", File: "/home/u/.devtools/x.db"},
		{Driver: "postgres", Host: "prod.example.com", Port: 5432, SSH: &db.SSHConfig{Host: "127.0.0.1", Port: 55222}},
		{Driver: "postgres", Host: "10.0.0.5", Port: 5432, SSH: &db.SSHConfig{Host: "127.0.0.1", Port: 55222}},
	}
	for _, c := range bad {
		if err := CheckConnection(&c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}
