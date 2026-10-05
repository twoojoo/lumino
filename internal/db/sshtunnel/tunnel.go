// Package sshtunnel forwards a local TCP port to a database host through an
// SSH bastion.
package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Config of a tunnel.
type Config struct {
	Host    string
	Port    int // default 22
	User    string
	Auth    string // password | key | agent
	KeyFile string
	Secret  string // password or key passphrase
	// HostKey verifies the server key (known_hosts / trust on first use).
	HostKey ssh.HostKeyCallback
}

// Tunnel is a running local forward.
type Tunnel struct {
	client *ssh.Client
	ln     net.Listener
	target string
	wg     sync.WaitGroup
	once   sync.Once
	done   chan struct{}
}

// ErrPassphrase is returned when an encrypted key needs a passphrase.
var ErrPassphrase = errors.New("the SSH key is encrypted: enter its passphrase")

func authMethods(c Config) ([]ssh.AuthMethod, func(), error) {
	switch c.Auth {
	case "password", "":
		return []ssh.AuthMethod{ssh.Password(c.Secret),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = c.Secret
				}
				return ans, nil
			})}, func() {}, nil
	case "key":
		path := c.KeyFile
		if len(path) > 1 && path[:2] == "~/" {
			home, _ := os.UserHomeDir()
			path = home + path[1:]
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read SSH key: %w", err)
		}
		var signer ssh.Signer
		if c.Secret != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(c.Secret))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
			var pe *ssh.PassphraseMissingError
			if errors.As(err, &pe) {
				return nil, nil, ErrPassphrase
			}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("parse SSH key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, func() {}, nil
	case "agent":
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, nil, errors.New("SSH agent not available ($SSH_AUTH_SOCK is not set)")
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, nil, fmt.Errorf("SSH agent: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}, func() { conn.Close() }, nil
	}
	return nil, nil, fmt.Errorf("unknown SSH auth %q", c.Auth)
}

// Open connects to the bastion and listens on 127.0.0.1:<random> forwarding
// to target (host:port as seen from the bastion).
func Open(ctx context.Context, c Config, target string) (*Tunnel, error) {
	port := c.Port
	if port == 0 {
		port = 22
	}
	auth, cleanup, err := authMethods(c)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	hk := c.HostKey
	if hk == nil {
		return nil, errors.New("no SSH host key verification configured")
	}
	cc := &ssh.ClientConfig{User: c.User, Auth: auth, HostKeyCallback: hk, Timeout: 15 * time.Second}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("SSH connect %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, addr, cc)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", addr, err)
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)
	// check the target is reachable now: a clear error beats a driver timeout
	probe, err := client.Dial("tcp", target)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("SSH tunnel to %s: %w", target, err)
	}
	probe.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		client.Close()
		return nil, err
	}
	t := &Tunnel{client: client, ln: ln, target: target, done: make(chan struct{})}
	t.wg.Add(2)
	go t.accept()
	go t.keepAlive()
	return t, nil
}

// LocalAddr is the 127.0.0.1:port to connect the driver to.
func (t *Tunnel) LocalAddr() (string, int) {
	a := t.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (t *Tunnel) accept() {
	defer t.wg.Done()
	for {
		local, err := t.ln.Accept()
		if err != nil {
			return
		}
		go t.forward(local)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	remote, err := t.client.Dial("tcp", t.target)
	if err != nil {
		slog.Warn("ssh tunnel dial", "target", t.target, "err", err)
		local.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(remote, local)
	go cp(local, remote)
	wg.Wait()
	local.Close()
	remote.Close()
}

func (t *Tunnel) keepAlive() {
	defer t.wg.Done()
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			if _, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				slog.Warn("ssh keepalive failed", "err", err)
			}
		}
	}
}

// Close stops the forward and the SSH connection.
func (t *Tunnel) Close() {
	t.once.Do(func() {
		close(t.done)
		t.ln.Close()
		t.client.Close()
		t.wg.Wait()
	})
}
