package db

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/giovanni/lumino/internal/db/sshtunnel"
)

// OpenOptions are environment hooks for Open.
type OpenOptions struct {
	// HostKey verifies SSH host keys (required for SSH connections).
	HostKey ssh.HostKeyCallback
}

// tunnelConn closes the tunnel with the connection.
type tunnelConn struct {
	Conn
	t *sshtunnel.Tunnel
}

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.t.Close()
	return err
}

// Open connects cfg through its driver, opening the SSH tunnel first when
// configured. cfg is not modified.
func Open(ctx context.Context, cfg *Config, o OpenOptions) (Conn, error) {
	d, ok := Lookup(cfg.Driver)
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", cfg.Driver)
	}
	ctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	c := *cfg
	if c.Params != nil {
		p := make(map[string]string, len(c.Params))
		for k, v := range c.Params {
			p[k] = v
		}
		c.Params = p
	}
	if c.SSH == nil || strings.TrimSpace(c.SSH.Host) == "" {
		conn, err := d.Open(ctx, &c)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	info := d.Info()
	if !info.Has(FieldHost) {
		return nil, fmt.Errorf("%s connections can't use an SSH tunnel", info.Name)
	}
	port := c.Port
	if port == 0 {
		port = info.DefaultPort
	}
	host := strings.TrimSpace(c.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if i := strings.IndexByte(host, ','); i >= 0 {
		host = strings.TrimSpace(host[:i]) // clusters: tunnel to the first node
	}
	if h, p, err := net.SplitHostPort(host); err == nil { // node with its own port
		if n, err := strconv.Atoi(p); err == nil {
			host, port = h, n
		}
	}
	host = strings.Trim(host, "[]")
	target := net.JoinHostPort(host, strconv.Itoa(port))
	t, err := sshtunnel.Open(ctx, sshtunnel.Config{
		Host: c.SSH.Host, Port: c.SSH.Port, User: c.SSH.User, Auth: c.SSH.Auth,
		KeyFile: c.SSH.KeyFile, Secret: c.SSH.Secret, HostKey: o.HostKey,
	}, target)
	if err != nil {
		return nil, err
	}
	lh, lp := t.LocalAddr()
	if c.TLS.ServerName == "" && c.TLS.Mode == "verify-full" {
		c.TLS.ServerName = host // verify the real name, not 127.0.0.1
	}
	c.Host, c.Port = lh, lp
	// a fresh timeout: the tunnel may have waited for the user to trust the
	// host key
	octx, ocancel := context.WithTimeout(context.Background(), ConnectTimeout)
	defer ocancel()
	conn, err := d.Open(octx, &c)
	if err != nil {
		t.Close()
		return nil, err
	}
	return &tunnelConn{Conn: conn, t: t}, nil
}
