package kdoq_test

import (
	"context"

	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
)

func TestConnectParsesTheDSN(t *testing.T) {
	cases := []struct {
		name, dsn string
		opts      []kdoq.ConnectOption
		url       string
		database  string
	}{
		{"full", "kdoq://host.example:1234/shop", nil, "host.example:1234", "shop"},
		{"default port", "kdoq://host.example", nil, "host.example:27505", ""},
		{"options override", "kdoq://dsn-host:1/dsn-db", []kdoq.ConnectOption{kdoq.ConnectOptions{Host: "opt-host", Port: 2, Database: "opt-db"}}, "opt-host:2", "opt-db"},
		{"options only", "", []kdoq.ConnectOption{kdoq.ConnectOptions{Host: "h"}}, "h:27505", ""},
		{"ipv6", "kdoq://[::1]:9", nil, "[::1]:9", ""},
		{"later option wins", "kdoq://h", []kdoq.ConnectOption{kdoq.ConnectOptions{Port: 5}, kdoq.ConnectOptions{Port: 6}}, "h:6", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newServer(fakeserver.Config{})
			h := connect(t, server, c.dsn, c.opts...)
			dials := server.Dials()
			if len(dials) != 1 || dials[0] != c.url {
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
	server.DialHook = func(string) error { return errors.New("refused") }
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
