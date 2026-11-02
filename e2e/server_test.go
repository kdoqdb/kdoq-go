//go:build e2e

// Package e2e_test runs the library against a real kdoq process. The server
// is started per test with a generated self-signed certificate and its own
// configuration in a temporary directory; the port and the certificate hash
// are passed to the client explicitly.
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/transport"
	"github.com/kdoqdb/kdoq-go/value"
)

// eventually bounds a wait for something the server produces by itself; it is a
// failure bound, not an expected wait.
const eventually = 30 * time.Second

var serverBinary string
var binding string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	binding = os.Getenv("KDOQ_TRANSPORT")
	if binding == "" {
		binding = "quic"
	}
	if binding != "quic" && binding != "h2" && binding != "webtransport" {
		fmt.Fprintln(os.Stderr, "invalid KDOQ_TRANSPORT:", binding)
		return 1
	}
	bin, err := locateServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: no server binary:", err)
		return 1
	}
	serverBinary = bin
	return m.Run()
}

// locateServer returns KDOQ_BIN, or builds the server of the enclosing
// repository and returns the executable cargo reports.
func locateServer() (string, error) {
	if bin := os.Getenv("KDOQ_BIN"); bin != "" {
		return filepath.Abs(bin)
	}
	cmd := exec.Command("cargo", "build", "-p", "kdoq", "--bin", "kdoq", "--message-format=json")
	cmd.Dir = ".."
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cargo build: %w (set KDOQ_BIN to a built kdoq)", err)
	}
	var exe string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		var msg struct {
			Reason     string `json:"reason"`
			Executable string `json:"executable"`
			Target     struct {
				Name string `json:"name"`
			} `json:"target"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Reason == "compiler-artifact" &&
			msg.Target.Name == "kdoq" && msg.Executable != "" {
			exe = msg.Executable
		}
	}
	if exe == "" {
		return "", errors.New("cargo built no kdoq executable")
	}
	return exe, nil
}

// user is a configured identity of the server.
type user struct {
	Name, Password, Level string
}

// settings selects what a test server offers.
type settings struct {
	Users []user // empty: authentication is disabled
}

// server is one real kdoq process over a data directory that outlives restarts.
type server struct {
	t        *testing.T
	dir      string
	settings settings
	hash     []byte
	port     int

	mu      sync.Mutex
	log     bytes.Buffer
	cmd     *exec.Cmd
	exited  chan struct{}
	stopped bool
}

// startServer starts a server in a fresh temporary directory and stops it when
// the test ends, showing the server log if the test failed.
func startServer(t *testing.T, s settings) *server {
	t.Helper()
	srv := &server{t: t, dir: t.TempDir(), settings: s}
	srv.writeCertificate()
	srv.start(0)
	t.Cleanup(func() {
		srv.stop()
		if t.Failed() {
			t.Logf("server log:\n%s", srv.logText())
		}
	})
	return srv
}

func (s *server) writeCertificate() {
	s.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		s.t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		s.t.Fatal(err)
	}
	s.write("cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	s.write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	sum := sha256.Sum256(der)
	s.hash = sum[:]
}

func (s *server) write(name string, data []byte) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), data, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// configuration renders the YAML file; JSON is valid YAML.
func (s *server) configuration(port int) []byte {
	addresses := map[string]string{"h2": "127.0.0.1:0", "quic": "127.0.0.1:0", "webtransport": "127.0.0.1:0"}
	addresses[binding] = fmt.Sprintf("127.0.0.1:%d", port)
	cfg := map[string]any{
		"data":    map[string]any{"path": "db"},
		"logging": map[string]any{"format": "json"},
		"tls":     map[string]any{"certificate": "cert.pem", "private_key": "key.pem", "allow_self_signed": true},
		"native":  map[string]any{"enabled": true, "listen": map[string]any{"h2": addresses["h2"], "quic": addresses["quic"]}},
		"http":    map[string]any{"listen": map[string]any{"http": "127.0.0.1:0", "quic": addresses["webtransport"]}},
	}
	if len(s.settings.Users) > 0 {
		var users []map[string]any
		for _, u := range s.settings.Users {
			entry := map[string]any{"username": u.Name, "password": u.Password}
			if u.Level != "" {
				entry["level"] = u.Level
			}
			users = append(users, entry)
		}
		cfg["auth"] = map[string]any{"enabled": true, "users": users}
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

var nativeAddress = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

// start launches the process and waits for its ready event. A non-zero port is
// requested explicitly, so a restarted server keeps the address clients know.
func (s *server) start(port int) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Join(s.dir, "db"), 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.write("config.yml", s.configuration(port))
	cmd := exec.Command(serverBinary, "--config", filepath.Join(s.dir, "config.yml"))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	exited := make(chan struct{})
	ready := make(chan int, 1)
	s.mu.Lock()
	s.cmd, s.exited, s.stopped = cmd, exited, false
	s.mu.Unlock()

	go func() {
		defer close(exited)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(nil, 16<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			s.mu.Lock()
			s.log.Write(line)
			s.log.WriteByte('\n')
			s.mu.Unlock()
			var event struct {
				Event   string `json:"event"`
				Address string `json:"native_address"`
				H2      string `json:"h2_address"`
				QUIC    string `json:"quic_address"`
			}
			if json.Unmarshal(line, &event) != nil || event.Event != "server_ready" {
				continue
			}
			address := event.Address
			if binding == "h2" {
				address = event.H2
			}
			if binding == "quic" {
				address = event.QUIC
			}
			if m := nativeAddress.FindStringSubmatch(address); m != nil {
				p, _ := strconv.Atoi(m[1])
				select {
				case ready <- p:
				default:
				}
			}
		}
		_ = cmd.Wait()
	}()

	select {
	case p := <-ready:
		s.port = p
	case <-exited:
		s.t.Fatalf("the server exited before it was ready:\n%s", s.logText())
	case <-time.After(eventually):
		s.stop()
		s.t.Fatalf("the server was not ready:\n%s", s.logText())
	}
}

// stop ends the process gracefully and waits for it; it is idempotent.
func (s *server) stop() {
	s.mu.Lock()
	cmd, exited, already := s.cmd, s.exited, s.stopped
	s.stopped = true
	s.mu.Unlock()
	if cmd == nil || already {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(eventually):
		_ = cmd.Process.Kill()
		<-exited
	}
}

// restart stops the server and starts it again over the same data and port.
func (s *server) restart() {
	s.t.Helper()
	port := s.port
	s.stop()
	s.start(port)
}

func (s *server) logText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.String()
}

// connect opens a client to the server with the explicit port and the
// certificate hash, and closes it when the test ends.
func (s *server) connect(t *testing.T, name, password string, opts ...kdoq.ConnectOption) *kdoq.Client {
	t.Helper()
	client, err := s.dial(context.Background(), name, password, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), eventually)
		defer cancel()
		_ = client.Close(ctx)
	})
	return client
}

func (s *server) dial(ctx context.Context, name, password string, opts ...kdoq.ConnectOption) (*kdoq.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, eventually)
	defer cancel()
	tr, err := s.transport()
	if err != nil {
		return nil, err
	}
	all := append([]kdoq.ConnectOption{kdoq.ConnectOptions{
		Host:      "127.0.0.1",
		Port:      s.port,
		Username:  name,
		Password:  password,
		Transport: tr,
	}}, opts...)
	return kdoq.Connect(ctx, "kdoq://127.0.0.1", all...)
}

// transport returns the same reusable public transport used by client scenarios.
func (s *server) transport() (transport.Transport, error) {
	tls := transport.TLSConfig{CertificateHash: s.hash}
	var tr transport.Transport
	var err error
	switch binding {
	case "h2":
		tr, err = transport.NewH2(transport.H2Config{TLS: tls})
	case "quic":
		tr, err = transport.NewQUIC(transport.QUICConfig{TLS: tls})
	case "webtransport":
		tr, err = transport.NewWebTransport(transport.WebTransportConfig{TLS: tls})
	}
	return tr, err
}

// collection creates the database and collection and returns a generic
// document handle.
func collection(t *testing.T, client *kdoq.Client, db, name string) *kdoq.Collection[value.Doc] {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventually)
	defer cancel()
	if err := client.Databases().Create(ctx, db); err != nil && !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatal(err)
	}
	handle, err := client.DB(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Collections().Create(ctx, name); err != nil && !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatal(err)
	}
	return handle.Docs(name)
}

func opContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventually)
	t.Cleanup(cancel)
	return ctx
}

func closedWithin(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(eventually):
		t.Fatal("timed out waiting for the client to close")
	}
}
