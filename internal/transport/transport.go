// Package transport is the seam between the protocol layers and the network.
// The real implementation is a WebTransport session; tests substitute an
// in-memory pair (see Pipe).
package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
)

// Stream is one bidirectional stream.
type Stream interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	// CloseWrite ends the send side with FIN.
	CloseWrite() error
	// CancelRead stops the receive side (STOP_SENDING).
	CancelRead(code uint64)
	// CancelWrite resets the send side.
	CancelWrite(code uint64)
}

// StreamError is returned by Read or Write when the peer reset the stream.
type StreamError struct{ Code uint64 }

func (e *StreamError) Error() string { return fmt.Sprintf("stream reset with code %d", e.Code) }

// SessionError describes how a session ended when the peer closed it.
type SessionError struct {
	Code    uint32
	Message string
}

func (e *SessionError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("session closed with code %d", e.Code)
	}
	return fmt.Sprintf("session closed with code %d: %s", e.Code, e.Message)
}

// Session is one established transport session.
type Session interface {
	// OpenStream opens a client-created bidirectional stream.
	OpenStream(ctx context.Context) (Stream, error)
	// Close ends the session with an application code.
	Close(code uint32) error
	// Done is closed when the session has ended, whatever the cause.
	Done() <-chan struct{}
	// Err explains the end of the session once Done is closed: a
	// *SessionError when the peer closed it, nil for a local close.
	Err() error
}

// DialConfig is what a dialer needs to reach the server.
type DialConfig struct {
	// URL is the WebTransport URL, https://host:port/native.
	URL string
	// TLS verifies the server certificate. The certificate-hash pin is already
	// folded into it.
	TLS *tls.Config
	// Header holds the extra request headers. The client sets none, in
	// particular no Origin.
	Header http.Header
}

// Dialer opens a session.
type Dialer interface {
	Dial(ctx context.Context, cfg DialConfig) (Session, error)
}

// DialerFunc adapts a function to a Dialer.
type DialerFunc func(ctx context.Context, cfg DialConfig) (Session, error)

// Dial implements Dialer.
func (f DialerFunc) Dial(ctx context.Context, cfg DialConfig) (Session, error) {
	return f(ctx, cfg)
}
