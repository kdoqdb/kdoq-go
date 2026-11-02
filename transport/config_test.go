package transport_test

import (
	"github.com/kdoqdb/kdoq-go/transport"
	"testing"
)

func TestConstructorsRejectInvalidCertificateHash(t *testing.T) {
	tls := transport.TLSConfig{CertificateHash: []byte{1, 2}}
	constructors := []struct {
		name   string
		create func() (transport.Transport, error)
	}{
		{"h2", func() (transport.Transport, error) { return transport.NewH2(transport.H2Config{TLS: tls}) }},
		{"quic", func() (transport.Transport, error) { return transport.NewQUIC(transport.QUICConfig{TLS: tls}) }},
		{"webtransport", func() (transport.Transport, error) {
			return transport.NewWebTransport(transport.WebTransportConfig{TLS: tls})
		}},
	}
	for _, c := range constructors {
		t.Run(c.name, func(t *testing.T) {
			tr, err := c.create()
			if err == nil || tr != nil {
				t.Fatalf("constructor = %v, %v; want invalid configuration", tr, err)
			}
		})
	}
}
