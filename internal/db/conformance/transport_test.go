//go:build integration

package conformance

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
	"github.com/twoojoo/lumino/internal/db/sshtunnel"
)

func devPath(p ...string) string {
	d, _ := filepath.Abs(filepath.Join(append([]string{"../../../.dev"}, p...)...))
	return d
}

func ping(t *testing.T, cfg db.Config, o db.OpenOptions) db.Conn {
	t.Helper()
	conn, err := db.Open(context.Background(), &cfg, o)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return conn
}

func needs(t *testing.T, addr string) {
	if !dbtest.Reachable(addr) {
		t.Skipf("%s not reachable (scripts/dbs/dbs.sh up)", addr)
	}
}

func TestTLSPostgres(t *testing.T) {
	needs(t, "127.0.0.1:55432")
	base := db.Config{Driver: "postgres", Host: "127.0.0.1", Port: 55432, Database: "lumino", User: "lumino", Password: "lumino"}
	ca := devPath("certs", "ca.crt")
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			cfg := base
			cfg.TLS = db.TLSConfig{Mode: mode, CAFile: ca}
			conn := ping(t, cfg, db.OpenOptions{})
			s, err := conn.Session(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, rows, err := dbtest.Query(context.Background(), s, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()")
			if err != nil || len(rows) != 1 || rows[0][0] != true {
				t.Fatalf("connection not encrypted: %v %v", rows, err)
			}
		})
	}
	t.Run("verify-full wrong name", func(t *testing.T) {
		cfg := base
		cfg.TLS = db.TLSConfig{Mode: "verify-full", CAFile: ca, ServerName: "evil.example.com"}
		if _, err := db.Open(context.Background(), &cfg, db.OpenOptions{}); err == nil {
			t.Fatal("certificate for another name accepted")
		}
	})
	t.Run("verify-ca unknown CA", func(t *testing.T) {
		cfg := base
		cfg.TLS = db.TLSConfig{Mode: "verify-ca"} // system roots: the test CA is unknown
		if _, err := db.Open(context.Background(), &cfg, db.OpenOptions{}); err == nil {
			t.Fatal("certificate from an unknown CA accepted")
		}
	})
}

func TestTLSMySQL(t *testing.T) {
	needs(t, "127.0.0.1:55306")
	base := db.Config{Driver: "mysql", Host: "127.0.0.1", Port: 55306, User: "root", Password: "lumino"}
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			cfg := base
			cfg.TLS = db.TLSConfig{Mode: mode, CAFile: devPath("certs", "ca.crt")}
			conn := ping(t, cfg, db.OpenOptions{})
			s, err := conn.Session(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, rows, err := dbtest.Query(context.Background(), s, "SHOW SESSION STATUS LIKE 'Ssl_cipher'")
			if err != nil || len(rows) != 1 || db.FormatValue(rows[0][1], db.KindText) == "" {
				t.Fatalf("connection not encrypted: %v %v", rows, err)
			}
		})
	}
}

func TestSSHTunnel(t *testing.T) {
	needs(t, "127.0.0.1:55222")
	needs(t, "127.0.0.1:55432")
	var seen ssh.PublicKey
	opts := db.OpenOptions{HostKey: func(hostname string, remote net.Addr, key ssh.PublicKey) error { seen = key; return nil }}
	// the database host is the compose service name, resolved by the bastion
	base := db.Config{Driver: "postgres", Host: "postgres", Port: 5432, Database: "lumino", User: "lumino", Password: "lumino"}
	cases := map[string]db.SSHConfig{
		"password":      {Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "password", Secret: "lumino"},
		"key":           {Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "key", KeyFile: devPath("ssh", "id_ed25519")},
		"encrypted key": {Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "key", KeyFile: devPath("ssh", "id_ed25519_enc"), Secret: "secret"},
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			sshc := sc
			cfg.SSH = &sshc
			conn := ping(t, cfg, opts)
			if seen == nil {
				t.Fatal("host key callback not called")
			}
			if !strings.HasPrefix(conn.ServerVersion(), "PostgreSQL") {
				t.Fatalf("version %q", conn.ServerVersion())
			}
		})
	}
	t.Run("wrong password", func(t *testing.T) {
		cfg := base
		cfg.SSH = &db.SSHConfig{Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "password", Secret: "nope"}
		if _, err := db.Open(context.Background(), &cfg, opts); err == nil {
			t.Fatal("wrong SSH password accepted")
		}
	})
	t.Run("encrypted key without passphrase", func(t *testing.T) {
		cfg := base
		cfg.SSH = &db.SSHConfig{Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "key", KeyFile: devPath("ssh", "id_ed25519_enc")}
		_, err := db.Open(context.Background(), &cfg, opts)
		if !errors.Is(err, sshtunnel.ErrPassphrase) {
			t.Fatalf("got %v, want ErrPassphrase", err)
		}
	})
	t.Run("host key rejected", func(t *testing.T) {
		cfg := base
		cfg.SSH = &db.SSHConfig{Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "password", Secret: "lumino"}
		reject := db.OpenOptions{HostKey: func(string, net.Addr, ssh.PublicKey) error { return errors.New("unknown host key") }}
		if _, err := db.Open(context.Background(), &cfg, reject); err == nil {
			t.Fatal("rejected host key still connected")
		}
	})
}

func TestPostgresNotices(t *testing.T) {
	needs(t, "127.0.0.1:55432")
	conn := ping(t, db.Config{Driver: "postgres", Host: "127.0.0.1", Port: 55432, Database: "lumino", User: "lumino", Password: "lumino"}, db.OpenOptions{})
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Execute(context.Background(), "DO $$ BEGIN RAISE NOTICE 'hello %', 42; END $$")
	if err != nil {
		t.Fatal(err)
	}
	msgs := res.Messages()
	res.Close()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "hello 42") {
		t.Fatalf("notices %q", msgs)
	}
	// drained: the next statement doesn't repeat them
	res, err = s.Execute(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if m := res.Messages(); len(m) != 0 {
		t.Fatalf("notices not drained: %q", m)
	}
	res.Close()
}

// TestSSHTunnelMongo reaches the replica set member through the bastion:
// the tunnel goes to one node, which the client must use directly.
func TestSSHTunnelMongo(t *testing.T) {
	needs(t, "127.0.0.1:55222")
	needs(t, "127.0.0.1:55017")
	cfg := db.Config{Driver: "mongodb", Host: "mongo", Port: 27017, Database: "lumino_test",
		SSH: &db.SSHConfig{Host: "127.0.0.1", Port: 55222, User: "lumino", Auth: "password", Secret: "lumino"}}
	opts := db.OpenOptions{HostKey: func(string, net.Addr, ssh.PublicKey) error { return nil }}
	conn := ping(t, cfg, opts)
	if !strings.HasPrefix(conn.ServerVersion(), "MongoDB") || !conn.Caps().Transactions {
		t.Fatalf("version %q caps %+v", conn.ServerVersion(), conn.Caps())
	}
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Execute(context.Background(), "db.runCommand({ping: 1})")
	if err != nil {
		t.Fatal(err)
	}
	res.Close()
}

// TestMongoAuthTLS: SCRAM and X.509 authentication over TLS on the
// keyFile replica set (mongo-secure), and the failures that must happen.
func TestMongoAuthTLS(t *testing.T) {
	needs(t, "127.0.0.1:55019")
	ca := devPath("certs", "ca.crt")
	base := db.Config{Driver: "mongodb", Host: "localhost", Port: 55019, Database: "lumino_test",
		TLS: db.TLSConfig{Mode: "verify-full", CAFile: ca}}
	open := func(cfg db.Config) (db.Conn, error) {
		return db.Open(context.Background(), &cfg, db.OpenOptions{})
	}
	t.Run("SCRAM", func(t *testing.T) {
		cfg := base
		cfg.User, cfg.Password = "lumino", "lumino"
		conn := ping(t, cfg, db.OpenOptions{})
		if !conn.Caps().Transactions {
			t.Fatal("replica set without transactions")
		}
		cfg.Params = map[string]string{"authMechanism": "SCRAM-SHA-256"}
		ping(t, cfg, db.OpenOptions{})
	})
	t.Run("X.509", func(t *testing.T) {
		cfg := base
		cfg.Params = map[string]string{"authMechanism": "MONGODB-X509"}
		cfg.TLS.CertFile, cfg.TLS.KeyFile = devPath("certs", "client.crt"), devPath("certs", "client.key")
		conn := ping(t, cfg, db.OpenOptions{})
		s, err := conn.Session(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		res, err := s.Execute(context.Background(), `db.x509_t.insertOne({ok: true})`)
		if err != nil {
			t.Fatalf("write as the X.509 user: %v", err)
		}
		res.Close()
	})
	t.Run("IP verified", func(t *testing.T) {
		cfg := base
		cfg.Host, cfg.User, cfg.Password = "127.0.0.1", "lumino", "lumino"
		ping(t, cfg, db.OpenOptions{})
	})
	bad := map[string]func(*db.Config){
		"wrong password": func(c *db.Config) { c.User, c.Password = "lumino", "nope" },
		"no credentials": func(c *db.Config) {},
		"no TLS": func(c *db.Config) {
			c.User, c.Password, c.TLS = "lumino", "lumino", db.TLSConfig{}
		},
		"unknown CA": func(c *db.Config) {
			c.User, c.Password, c.TLS.CAFile = "lumino", "lumino", ""
		},
		"X.509 without a certificate": func(c *db.Config) {
			c.Params = map[string]string{"authMechanism": "MONGODB-X509"}
		},
	}
	for name, mod := range bad {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mod(&cfg)
			conn, err := open(cfg)
			if err == nil {
				// connecting is lazy for some failures: a command must fail
				s, serr := conn.Session(context.Background())
				if serr == nil {
					res, eerr := s.Execute(context.Background(), "db.x.find({})")
					if eerr == nil {
						res.Close()
					}
					s.Close()
					serr = eerr
				}
				conn.Close()
				err = serr
			}
			if err == nil {
				t.Fatal("connection accepted")
			}
		})
	}
}
