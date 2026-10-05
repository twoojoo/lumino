package knownhosts

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func key(t *testing.T) ssh.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTrustOnFirstUse(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}
	k1, k2 := key(t), key(t)
	asked := 0
	cb := Callback(func(string, string) bool { asked++; return true })
	if err := cb("bastion:22", addr, k1); err != nil || asked != 1 {
		t.Fatalf("first use: %v asked=%d", err, asked)
	}
	if err := cb("bastion:22", addr, k1); err != nil || asked != 1 {
		t.Fatalf("known host asked again: %v asked=%d", err, asked)
	}
	var ch *ErrChanged
	if err := cb("bastion:22", addr, k2); !errors.As(err, &ch) {
		t.Fatalf("changed key not detected: %v", err)
	}
	deny := Callback(func(string, string) bool { return false })
	if err := deny("other:22", addr, k1); err == nil {
		t.Fatal("refused host accepted")
	}
}
