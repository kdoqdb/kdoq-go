package transport_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/transport"
)

func h2Server(t *testing.T, handler http.HandlerFunc) (*httptest.Server, transport.TLSConfig) {
	t.Helper()
	return h2ServerWithStreamLimit(t, handler, 0)
}

func h2ServerWithStreamLimit(t *testing.T, handler http.HandlerFunc, limit int) (*httptest.Server, transport.TLSConfig) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.HTTP2 = &http.HTTP2Config{MaxConcurrentStreams: limit}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	hash := sha256.Sum256(srv.Certificate().Raw)
	return srv, transport.TLSConfig{CertificateHash: hash[:]}
}
func dialH2(t *testing.T, trust transport.TLSConfig, address string) transport.Connection {
	t.Helper()
	tr, err := transport.NewH2(transport.H2Config{TLS: trust})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := tr.Dial(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func TestH2ReadsEarlyResponseWhileUploadIsBlocked(t *testing.T) {
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.kdoq.native")
		_, _ = io.WriteString(w, "refused")
	})
	conn := dialH2(t, trust, srv.Listener.Addr().String())
	st, err := conn.OpenOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upload := make(chan error, 1)
	go func() { _, err := st.Write(make([]byte, 1<<20)); upload <- err }()
	got, err := io.ReadAll(st)
	if err != nil || string(got) != "refused" {
		t.Fatalf("response %q, %v", got, err)
	}
	_ = st.Close()
	select {
	case <-upload:
	case <-time.After(time.Second):
		t.Fatal("upload was not released")
	}
}
func TestH2UsesOnePhysicalConnectionAndHalfClosesInput(t *testing.T) {
	var mu sync.Mutex
	peers := map[string]bool{}
	routes := make(chan string, 2)
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers[r.RemoteAddr] = true
		mu.Unlock()
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/vnd.kdoq.native" {
			t.Error("invalid request metadata")
		}
		routes <- r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.kdoq.native")
		_, _ = w.Write(append([]byte("reply:"), body...))
	})
	conn := dialH2(t, trust, srv.Listener.Addr().String())
	for _, open := range []func(context.Context) (transport.Stream, error){conn.OpenControl, conn.OpenOperation} {
		st, err := open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.Write([]byte("input"))
		if err != nil {
			t.Fatal(err)
		}
		if err = st.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(st)
		if err != nil || string(got) != "reply:input" {
			t.Fatalf("response %q, %v", got, err)
		}
		_ = st.Close()
	}
	if a, b := <-routes, <-routes; a != "/native/control" || b != "/native/operation" {
		t.Fatalf("routes %q, %q", a, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(peers) != 1 {
		t.Fatalf("%d physical connections", len(peers))
	}
}
func TestPinnedTLSStillChecksNameDatesAndCustomVerifier(t *testing.T) {
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {})
	cases := []struct {
		name   string
		config transport.TLSConfig
		want   string
	}{
		{"wrong name", transport.TLSConfig{Config: &tls.Config{ServerName: "wrong.invalid"}, CertificateHash: trust.CertificateHash}, "certificate"},
		{"expired", transport.TLSConfig{Config: &tls.Config{Time: func() time.Time { return srv.Certificate().NotAfter.Add(time.Hour) }}, CertificateHash: trust.CertificateHash}, "validity"},
		{"additional verifier", transport.TLSConfig{Config: &tls.Config{VerifyConnection: func(tls.ConnectionState) error { return errors.New("custom rejection") }}, CertificateHash: trust.CertificateHash}, "custom rejection"},
		{"wrong hash", transport.TLSConfig{CertificateHash: make([]byte, 32)}, "pinned hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := transport.NewH2(transport.H2Config{TLS: tc.config})
			if err != nil {
				t.Fatal(err)
			}
			conn, err := tr.Dial(context.Background(), srv.Listener.Addr().String())
			if conn != nil {
				_ = conn.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Dial = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestH2ResetPreservesItsCodeDomain(t *testing.T) {
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.kdoq.native")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	c := dialH2(t, trust, srv.Listener.Addr().String())
	st, err := c.OpenOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = io.ReadAll(st)
	var reset *transport.StreamError
	if !errors.As(err, &reset) || reset.Domain != transport.DomainH2 || reset.Code != 2 {
		t.Fatalf("Read = %v; want H2 INTERNAL_ERROR", err)
	}
}

func TestH2OpeningHonorsCancellationBeforeResponseHeaders(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	c := dialH2(t, trust, srv.Listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := c.OpenOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Abort()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("opening stream did not reach the server")
	}
	cancel()
	result := make(chan error, 1)
	go func() { _, err := st.Read(make([]byte, 1)); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Read = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("opening stream ignored cancellation")
	}
}

func TestH2UsesCustomRootsAndSnapshotsTLSConfiguration(t *testing.T) {
	srv, _ := h2Server(t, func(http.ResponseWriter, *http.Request) {})
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	cfg := &tls.Config{RootCAs: roots}
	tr, err := transport.NewH2(transport.H2Config{TLS: transport.TLSConfig{Config: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerName = "mutated.invalid"
	c, err := tr.Dial(context.Background(), srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestH2CompletionRejectsTrailingData(t *testing.T) {
	srv, trust := h2Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.kdoq.native")
		_, _ = io.WriteString(w, "end!")
	})
	c := dialH2(t, trust, srv.Listener.Addr().String())
	st, err := c.OpenOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Abort()
	var terminal [3]byte
	if _, err = io.ReadFull(st, terminal[:]); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); !errors.Is(err, transport.ErrTrailingData) {
		t.Fatalf("Close = %v", err)
	}
}

func TestH2ConcurrentOperationsProgressWhileSettingsArrive(t *testing.T) {
	// Increasing the initial client limit of 100 while requests start exercises
	// synchronous state notifications that could deadlock the x/net adapter.
	srv, trust := h2ServerWithStreamLimit(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.kdoq.native")
		_, _ = io.WriteString(w, "ok")
	}, 250)
	conn := dialH2(t, trust, srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := func() error {
		st, err := conn.OpenOperation(ctx)
		if err != nil {
			return err
		}
		defer st.Abort()
		if err = st.CloseWrite(); err != nil {
			return err
		}
		body, err := io.ReadAll(st)
		if err == nil && string(body) != "ok" {
			err = errors.New("incorrect H2 response")
		}
		return err
	}
	const operations = 32
	result := make(chan error, operations)
	for range operations {
		go func() { result <- request() }()
	}
	for range operations {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent H2 operations stalled while settings arrived")
		}
	}
}
