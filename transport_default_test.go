package kdoq_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/quic-go/quic-go"
)

func TestConnectDefaultsToDirectNativeQUIC(t *testing.T) {
	certificate := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificate.Close()
	offered := make(chan []string, 1)
	tlsConfig := certificate.TLS.Clone()
	tlsConfig.NextProtos = []string{"kdoq-native"}
	tlsConfig.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		offered <- append([]string(nil), hello.SupportedProtos...)
		return nil, errors.New("stop after observing ALPN")
	}
	listener, err := quic.ListenAddr("127.0.0.1:0", tlsConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := kdoq.Connect(ctx, "kdoq://"+listener.Addr().String())
	if c != nil || err == nil {
		t.Fatalf("Connect = %v, %v", c, err)
	}
	select {
	case protocols := <-offered:
		if len(protocols) != 1 || protocols[0] != "kdoq-native" {
			t.Fatalf("default ALPN = %v", protocols)
		}
	case <-ctx.Done():
		t.Fatal("default transport did not attempt direct QUIC")
	}
}
