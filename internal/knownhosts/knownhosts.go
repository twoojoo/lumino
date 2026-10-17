// Package knownhosts verifies SSH host keys against lumino's own
// known_hosts file (~/.config/lumino/known_hosts), trusting new hosts only
// after the user confirms their fingerprint.
package knownhosts

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/twoojoo/lumino/internal/prefs"
)

var mu sync.Mutex

// File is the known_hosts path.
func File() string { return filepath.Join(prefs.Dir(), "known_hosts") }

// ErrChanged reports a host key that differs from the trusted one.
type ErrChanged struct {
	Host        string
	Fingerprint string
}

func (e *ErrChanged) Error() string {
	return fmt.Sprintf("the SSH host key of %s CHANGED (now %s): this may be a man-in-the-middle attack. "+
		"If the server was reinstalled, remove its line from %s", e.Host, e.Fingerprint, File())
}

// Callback returns a HostKeyCallback; unknown hosts are passed to confirm
// (which may block waiting for the user) and saved when accepted.
func Callback(confirm func(host, fingerprint string) bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		path := File()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			mu.Unlock()
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.WriteFile(path, nil, 0o600)
		}
		cb, err := knownhosts.New(path)
		mu.Unlock()
		if err != nil {
			return err
		}
		err = cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		fp := ssh.FingerprintSHA256(key)
		if len(ke.Want) > 0 {
			return &ErrChanged{Host: hostname, Fingerprint: fp}
		}
		if confirm == nil || !confirm(hostname, fp) {
			return fmt.Errorf("SSH host %s not trusted (%s)", hostname, fp)
		}
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n")
		return err
	}
}
