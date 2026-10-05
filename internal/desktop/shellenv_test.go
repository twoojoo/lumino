//go:build !windows

package desktop

import "testing"

func TestParseEnvBlock(t *testing.T) {
	out := []byte("motd noise\n" + envMarker + "PATH=/opt/bin:/usr/bin\x00FOO=a=b\x00PWD=/x\x00\x00" + envMarker + "trailing")
	env := parseEnvBlock(out)
	if env["PATH"] != "/opt/bin:/usr/bin" || env["FOO"] != "a=b" {
		t.Fatalf("got %v", env)
	}
	if _, ok := env["PWD"]; ok {
		t.Fatal("PWD must be skipped")
	}
	if parseEnvBlock([]byte("no markers")) != nil {
		t.Fatal("expected nil without markers")
	}
}
