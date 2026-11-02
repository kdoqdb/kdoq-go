package kdoq

import "github.com/kdoqdb/kdoq-go/internal/transport"

// WithTransport replaces the network with a dialer, for tests.
func WithTransport(d transport.Dialer) ConnectOption { return ConnectOptions{dialer: d} }

// WithNonce fixes the SCRAM client nonce, for tests.
func WithNonce(nonce string) ConnectOption { return ConnectOptions{nonce: nonce} }
