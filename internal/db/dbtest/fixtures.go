package dbtest

import (
	"embed"
)

//go:embed fixtures/*.sql
var fixtures embed.FS

// Fixture returns the fixture script of a dialect.
func Fixture(name string) string {
	b, err := fixtures.ReadFile("fixtures/" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}
