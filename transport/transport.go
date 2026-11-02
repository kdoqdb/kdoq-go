// Package transport provides native protocol connections over H2, direct QUIC,
// and WebTransport. A Transport is reusable and safe for concurrent dialing;
// each Dial creates an independently owned physical connection and session.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Transport creates connections. address is a host:port, without a scheme or
// path. Dial's context bounds establishment only, not the returned connection.
// Implementations must honor cancellation and release resources on failure.
type Transport interface {
	// Dial establishes one independently owned connection.
	Dial(ctx context.Context, address string) (Connection, error)
}

// Connection multiplexes one native session on one physical connection.
// Methods must be safe for concurrent use. Operations must never be moved to
// another connection; a replacement requires a new native handshake.
type Connection interface {
	// OpenControl opens the session's only control stream, before operations.
	OpenControl(ctx context.Context) (Stream, error)
	// OpenOperation opens an independent operation stream. Waiting for capacity
	// must honor ctx and must not block progress on existing streams.
	OpenOperation(ctx context.Context) (Stream, error)
	// Close ends the connection and unblocks pending I/O. It is idempotent.
	Close() error
	// Done closes when the connection ends; Err then reports its cause.
	Done() <-chan struct{}
	// Err reports the cause after Done closes, or nil for a normal local close.
	Err() error
}

// Stream exchanges bytes, with concurrent reading and writing. Native frames
// have no relationship to transport packet or DATA boundaries. Methods that
// abort must be safe concurrently with I/O. Closing methods are idempotent;
// CloseWrite serializes with Write. Call Close after reading a native terminal.
type Stream interface {
	io.Reader
	io.Writer
	// CloseWrite ends input without stopping output (FIN or H2 END_STREAM).
	CloseWrite() error
	// Abort interrupts both directions and releases resources without draining.
	Abort() error
	// Close consumes response EOF and releases a completed exchange without
	// resetting a successful response before its delivery receipt. It may wait
	// for the peer; concurrent Abort or Connection.Close must unblock it.
	// The caller must bound this wait and must have stopped reading.
	Close() error
}

// Domain identifies the interpretation of a transport error code.
type Domain string

// ErrTrailingData means the peer sent bytes after the completed native exchange
// instead of transport EOF. It is a protocol violation, not a connection loss.
var ErrTrailingData = errors.New("unexpected bytes after native terminal frame")

const (
	// DomainNative identifies native application reset and close codes.
	DomainNative Domain = "native"
	// DomainH2 identifies HTTP/2 error codes, not native application codes.
	DomainH2 Domain = "h2"
	// DomainQUIC identifies QUIC transport errors, not application errors.
	DomainQUIC Domain = "quic"
)

// StreamError reports a stream reset. Code is meaningful only within Domain.
// Err preserves the underlying error for errors.Is and errors.As.
type StreamError struct {
	Domain Domain
	Code   uint64
	Err    error
}

// Error describes the reset and its code domain.
func (e *StreamError) Error() string {
	return fmt.Sprintf("%s stream reset with code %d", e.Domain, e.Code)
}

// Unwrap returns the original transport error.
func (e *StreamError) Unwrap() error { return e.Err }

// SessionError reports a connection close with a code in the named domain.
type SessionError struct {
	Domain  Domain
	Code    uint64
	Message string
	Err     error
}

// Error describes the connection close and its code domain.
func (e *SessionError) Error() string {
	return fmt.Sprintf("%s connection closed with code %d: %s", e.Domain, e.Code, e.Message)
}

// Unwrap returns the original transport error.
func (e *SessionError) Unwrap() error { return e.Err }

// responseEnd consumes only the transport tail, never an unbounded body. All
// native response bytes must already have been consumed by the protocol layer.
func responseEnd(r io.Reader) error {
	var tail [1]byte
	n, err := io.ReadFull(r, tail[:])
	if n != 0 {
		return ErrTrailingData
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
