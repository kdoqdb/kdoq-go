package kdoq_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/internal/transport"
)

func TestConnectParsesTheDSN(t *testing.T) {
	cases := []struct {
		name, dsn string
		opts      []kdoq.ConnectOption
		url       string
		database  string
	}{
		{"full", "kdoq://host.example:1234/shop", nil, "https://host.example:1234/native", "shop"},
		{"default port", "kdoq://host.example", nil, "https://host.example:27505/native", ""},
		{"options override", "kdoq://dsn-host:1/dsn-db", []kdoq.ConnectOption{kdoq.ConnectOptions{Host: "opt-host", Port: 2, Database: "opt-db"}}, "https://opt-host:2/native", "opt-db"},
		{"options only", "", []kdoq.ConnectOption{kdoq.ConnectOptions{Host: "h"}}, "https://h:27505/native", ""},
		{"ipv6", "kdoq://[::1]:9", nil, "https://[::1]:9/native", ""},
		{"later option wins", "kdoq://h", []kdoq.ConnectOption{kdoq.ConnectOptions{Port: 5}, kdoq.ConnectOptions{Port: 6}}, "https://h:6/native", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newServer(fakeserver.Config{})
			h := connect(t, server, c.dsn, c.opts...)
			dials := server.Dials()
			if len(dials) != 1 || dials[0].URL != c.url {
				t.Fatalf("dialed %v, want %s", dials, c.url)
			}
			if c.database != "" {
				db, err := h.client.DB("")
				if err != nil || db.Name() != c.database {
					t.Fatalf("DB(\"\") = %v, %v; want %s", db, err, c.database)
				}
			}
		})
	}
}

func TestConnectDecodesPercentEncodedCredentials(t *testing.T) {
	server := newServer(fakeserver.Config{Users: map[string]string{"a b": "p@ss:word"}})
	h := connect(t, server, "kdoq://a%20b:p%40ss%3Aword@h/db%20x")
	info, _, ok := h.client.Session()
	if !ok || info.Username != "a b" {
		t.Fatalf("session = %+v, %v", info, ok)
	}
	if db, _ := h.client.DB(""); db.Name() != "db x" {
		t.Fatalf("database = %q", db.Name())
	}
}

func TestConnectRejectsMistakesBeforeAnyNetwork(t *testing.T) {
	cases := []struct {
		name, dsn string
		opts      []kdoq.ConnectOption
	}{
		{"password without user (DSN)", "kdoq://:secret@h", nil},
		{"password without user (option)", "kdoq://h", []kdoq.ConnectOption{kdoq.ConnectOptions{Password: "x"}}},
		{"no host", "", nil},
		{"no host in DSN", "kdoq:///db", nil},
		{"wrong scheme", "https://h", nil},
		{"port zero is not a port", "kdoq://h:0", nil},
		{"port too large", "kdoq://h:70000", nil},
		{"query string", "kdoq://h?x=1", nil},
		{"bad certificate hash", "kdoq://h", []kdoq.ConnectOption{kdoq.ConnectOptions{CertificateHash: []byte{1, 2}}}},
		{"negative attempts", "kdoq://h", []kdoq.ConnectOption{kdoq.ConnectOptions{Retry: &kdoq.RetryPolicy{Attempts: -1}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newServer(fakeserver.Config{})
			opts := append([]kdoq.ConnectOption{kdoq.WithTransport(server)}, c.opts...)
			client, err := kdoq.Connect(context.Background(), c.dsn, opts...)
			var config *kdoq.ConfigError
			if !errors.As(err, &config) || client != nil {
				t.Fatalf("Connect = %v, %v; want a *ConfigError", client, err)
			}
			if n := len(server.Dials()); n != 0 {
				t.Fatalf("%d dials for a configuration error", n)
			}
		})
	}
}

func TestParseDSN(t *testing.T) {
	got, err := kdoq.ParseDSN("kdoq://me:pw@host:99/db")
	want := kdoq.DSN{Host: "host", Port: 99, Username: "me", Password: "pw", Database: "db"}
	if err != nil || got != want {
		t.Fatalf("ParseDSN = %+v, %v; want %+v", got, err, want)
	}
}

func TestDBWithoutNameNeedsAConnectTimeDatabase(t *testing.T) {
	h := connect(t, newServer(fakeserver.Config{}), "kdoq://h")
	var config *kdoq.ConfigError
	if _, err := h.client.DB(""); !errors.As(err, &config) {
		t.Fatalf("DB(\"\") error = %v, want a *ConfigError", err)
	}
	db, err := h.client.DB("explicit")
	if err != nil || db.Name() != "explicit" {
		t.Fatalf("DB(explicit) = %v, %v", db, err)
	}
}

func TestSCRAMAuthenticatesAUser(t *testing.T) {
	server := newServer(fakeserver.Config{Users: map[string]string{"ann": "secret"}})
	h := connect(t, server, "kdoq://ann:secret@h")
	info, level, ok := h.client.Session()
	if !ok || info.Username != "ann" || info.ID == 0 || level != kdoq.AccessAdmin {
		t.Fatalf("Session() = %+v, %v, %v", info, level, ok)
	}
	if got := (<-server.Connected).Username; got != "ann" {
		t.Fatalf("server saw user %q", got)
	}
}

func TestAnonymousAccessWithoutAUser(t *testing.T) {
	server := newServer(fakeserver.Config{})
	h := connect(t, server, "kdoq://h")
	info, _, ok := h.client.Session()
	if !ok || info.Username != "" {
		t.Fatalf("Session() = %+v, %v", info, ok)
	}
}

func TestAuthenticationFailures(t *testing.T) {
	cases := []struct {
		name   string
		cfg    fakeserver.Config
		dsn    string
		target error
	}{
		{"wrong password", fakeserver.Config{Users: map[string]string{"ann": "secret"}}, "kdoq://ann:wrong@h", kdoq.ErrAuthentication},
		{"unknown user", fakeserver.Config{Users: map[string]string{"ann": "secret"}}, "kdoq://bob:secret@h", kdoq.ErrAuthentication},
		{"forged server signature", fakeserver.Config{Users: map[string]string{"ann": "secret"}, ForgeSignature: true}, "kdoq://ann:secret@h", kdoq.ErrAuthentication},
		{"user against anonymous server", fakeserver.Config{}, "kdoq://ann:secret@h", kdoq.ErrIncompatible},
		{"anonymous against SCRAM server", fakeserver.Config{Users: map[string]string{"ann": "secret"}}, "kdoq://h", kdoq.ErrIncompatible},
		{"missing required capability", fakeserver.Config{Capabilities: 1 | 4}, "kdoq://h", kdoq.ErrIncompatible},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, err := kdoq.Connect(context.Background(), c.dsn, kdoq.WithTransport(newServer(c.cfg)))
			if client != nil || !errors.Is(err, c.target) {
				t.Fatalf("Connect = %v, %v; want %v", client, err, c.target)
			}
		})
	}
}

func TestDialFailureIsAConnectionError(t *testing.T) {
	server := newServer(fakeserver.Config{})
	server.DialHook = func(transport.DialConfig) error { return errors.New("refused") }
	_, err := kdoq.Connect(context.Background(), "kdoq://h", kdoq.WithTransport(server))
	var conn *kdoq.ConnectionError
	if !errors.As(err, &conn) {
		t.Fatalf("Connect error = %v, want a *ConnectionError", err)
	}
}

func TestConnectHonorsAnExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := kdoq.Connect(ctx, "kdoq://h", kdoq.WithTransport(newServer(fakeserver.Config{})))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect error = %v, want context.Canceled", err)
	}
}

func TestClientSendsNoOrigin(t *testing.T) {
	server := newServer(fakeserver.Config{})
	connect(t, server, "kdoq://h")
	if origin := server.Dials()[0].Header.Get("Origin"); origin != "" {
		t.Fatalf("Origin = %q, the client must send none", origin)
	}
}

func selfSigned(t *testing.T) *x509.Certificate {
	t.Helper()
	return newCertificate(t, "pinned.example")
}

func TestCertificateHashPinsTheLeafCertificate(t *testing.T) {
	cert := selfSigned(t)
	sum := sha256.Sum256(cert.Raw)
	server := newServer(fakeserver.Config{})
	connect(t, server, "kdoq://h", kdoq.ConnectOptions{CertificateHash: sum[:]})
	cfg := server.Dials()[0].TLS
	if !cfg.InsecureSkipVerify || cfg.VerifyConnection == nil {
		t.Fatalf("a pinned config must replace chain verification: %+v", cfg)
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Fatalf("the pinned certificate was rejected: %v", err)
	}
	other := newCertificate(t, "other.example")
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{other}}); err == nil {
		t.Fatal("a different certificate was accepted")
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("a missing certificate was accepted")
	}
}

func TestTLSConfigIsHonoredWithoutAHash(t *testing.T) {
	pool := x509.NewCertPool()
	mine := &tls.Config{ServerName: "kdoq.internal", RootCAs: pool}
	server := newServer(fakeserver.Config{})
	connect(t, server, "kdoq://h", kdoq.ConnectOptions{TLSConfig: mine})
	got := server.Dials()[0].TLS
	if got.ServerName != "kdoq.internal" || got.RootCAs != pool || got.InsecureSkipVerify || got.VerifyConnection != nil {
		t.Fatalf("the TLS config was not honored as given: %+v", got)
	}
	if got == mine {
		t.Fatal("the caller's config must be cloned, not shared")
	}
}
