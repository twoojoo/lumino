// Command signsums manages the ed25519 key that signs release checksums
// (internal/update verifies SHA256SUMS.txt.sig before installing anything).
//
//	go run ./scripts/signsums keygen <private-key-file>   prints the public key
//	UPDATE_SIGNING_KEY=… go run ./scripts/signsums sign SHA256SUMS.txt
//	go run ./scripts/signsums verify <public-key> SHA256SUMS.txt
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "signsums: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		die("usage: keygen <file> | sign <file> | verify <pubkey> <file>")
	}
	switch os.Args[1] {
	case "keygen":
		if _, err := os.Stat(os.Args[2]); err == nil {
			die("%s exists: refusing to overwrite a signing key", os.Args[2])
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			die("%v", err)
		}
		seed := base64.StdEncoding.EncodeToString(priv.Seed())
		if err := os.WriteFile(os.Args[2], []byte(seed+"\n"), 0o600); err != nil {
			die("%v", err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
	case "sign":
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			die("UPDATE_SIGNING_KEY must hold the base64 key seed")
		}
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
		if err := os.WriteFile(os.Args[2]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
			die("%v", err)
		}
	case "verify":
		if len(os.Args) < 4 {
			die("usage: verify <pubkey> <file>")
		}
		pub, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil || len(pub) != ed25519.PublicKeySize {
			die("bad public key")
		}
		data, err := os.ReadFile(os.Args[3])
		if err != nil {
			die("%v", err)
		}
		raw, err := os.ReadFile(os.Args[3] + ".sig")
		if err != nil {
			die("%v", err)
		}
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || !ed25519.Verify(pub, data, sig) {
			die("signature does NOT verify")
		}
		fmt.Println("signature ok")
	default:
		die("unknown command %q", os.Args[1])
	}
}
