package dbtest

import (
	"embed"
)

//go:embed fixtures/*.sql fixtures/*.js
var fixtures embed.FS

// Fixture returns the fixture script of a dialect.
func Fixture(name string) string {
	b, err := fixtures.ReadFile("fixtures/" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Script returns a fixture file by name with its extension (mongo.js).
func Script(file string) string {
	b, err := fixtures.ReadFile("fixtures/" + file)
	if err != nil {
		panic(err)
	}
	return string(b)
}
