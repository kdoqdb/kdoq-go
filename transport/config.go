package transport

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// TLSConfig configures server trust. Zero uses system roots and TLS 1.3.
// Config is cloned by the constructor and per connection. Callbacks must be
// concurrency safe. ServerName, if set, overrides the address hostname.
type TLSConfig struct {
	Config *tls.Config
	// CertificateHash pins the SHA-256 digest of the DER leaf certificate.
	// It replaces chain trust, but still verifies hostname and validity dates.
	// Config.VerifyConnection, if provided, runs as an additional check.
	CertificateHash []byte
}

// H2Config configures native HTTP/2 over TLS/TCP.
type H2Config struct{ TLS TLSConfig }

// QUICConfig configures direct native QUIC. Config is cloned; native ALPN and
// disabled incoming application streams are enforced by the transport.
type QUICConfig struct {
	TLS    TLSConfig
	Config *quic.Config
}

// WebTransportConfig configures native WebTransport over HTTP/3. Config is
// cloned; datagram negotiation required by WebTransport is always enabled.
type WebTransportConfig struct {
	TLS    TLSConfig
	Config *quic.Config
}

type settings struct {
	tls  *tls.Config
	quic *quic.Config
	pin  []byte
}

func newSettings(trust TLSConfig, qc *quic.Config) (settings, error) {
	if trust.CertificateHash != nil && len(trust.CertificateHash) != sha256.Size {
		return settings{}, errors.New("certificate hash must be a SHA-256 digest of 32 bytes")
	}
	cfg := &tls.Config{}
	if trust.Config != nil {
		cfg = trust.Config.Clone()
	}
	if cfg.MinVersion < tls.VersionTLS13 {
		cfg.MinVersion = tls.VersionTLS13
	}
	if cfg.MaxVersion != 0 && cfg.MaxVersion < cfg.MinVersion {
		return settings{}, errors.New("TLS maximum version is below TLS 1.3")
	}
	if trust.CertificateHash == nil && cfg.InsecureSkipVerify {
		return settings{}, errors.New("unverified TLS requires an explicit certificate hash")
	}
	q := &quic.Config{}
	if qc != nil {
		q = qc.Clone()
	}
	return settings{tls: cfg, quic: q, pin: append([]byte(nil), trust.CertificateHash...)}, nil
}
func (s settings) tlsFor(address, alpn string) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid transport address: %w", err)
	}
	cfg := s.tls.Clone()
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	if s.pin != nil {
		pin := append([]byte(nil), s.pin...)
		verify := cfg.VerifyConnection
		clock := time.Now
		if cfg.Time != nil {
			clock = cfg.Time
		}
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			sum := sha256.Sum256(leaf.Raw)
			if subtle.ConstantTimeCompare(pin, sum[:]) != 1 {
				return errors.New("server certificate does not match pinned hash")
			}
			if cfg.ServerName == "" {
				return errors.New("missing TLS server name")
			}
			if err := leaf.VerifyHostname(cfg.ServerName); err != nil {
				return err
			}
			now := clock()
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return errors.New("server certificate is outside its validity period")
			}
			if verify != nil {
				return verify(cs)
			}
			return nil
		}
	}
	cfg.NextProtos = []string{alpn}
	// Pin verification uses the configured name because IP literals are omitted
	// from SNI and ConnectionState.ServerName can therefore be empty.
	return cfg, nil
}

// NewH2 validates config and creates a reusable HTTP/2 transport without I/O.
func NewH2(config H2Config) (Transport, error) {
	s, err := newSettings(config.TLS, nil)
	if err != nil {
		return nil, err
	}
	return &h2Transport{settings: s}, nil
}

// NewQUIC validates config and creates a reusable direct QUIC transport without I/O.
func NewQUIC(config QUICConfig) (Transport, error) {
	s, err := newSettings(config.TLS, config.Config)
	if err != nil {
		return nil, err
	}
	return &quicTransport{settings: s}, nil
}

// NewWebTransport validates config and creates a reusable WebTransport transport without I/O.
func NewWebTransport(config WebTransportConfig) (Transport, error) {
	s, err := newSettings(config.TLS, config.Config)
	if err != nil {
		return nil, err
	}
	return &webTransport{settings: s}, nil
}
