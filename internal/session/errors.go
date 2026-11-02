// Package session runs one native connection: setup (HELLO, SCRAM, READY),
// the control stream and operation streams.
package session

import (
	"errors"
	"fmt"

	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// ServerError is an ERROR frame sent by the server.
type ServerError struct{ wire.ErrorFrame }

func (e *ServerError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("server error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("server error %d", e.Code)
}

// LostError reports that the connection ended or failed.
type LostError struct{ Cause error }

func (e *LostError) Error() string {
	if e.Cause == nil {
		return "the connection was lost"
	}
	return "the connection was lost: " + e.Cause.Error()
}

func (e *LostError) Unwrap() error { return e.Cause }

// IncompleteError reports a response stream that ended or was reset without
// END or ERROR.
type IncompleteError struct {
	// Reset is true when the peer reset the stream; ResetCode is its code.
	Reset     bool
	ResetCode uint64
	Cause     error
}

func (e *IncompleteError) Error() string {
	if e.Reset {
		return fmt.Sprintf("the stream was reset (code %d)", e.ResetCode)
	}
	return "the stream ended without END or ERROR"
}

func (e *IncompleteError) Unwrap() error { return e.Cause }

// AuthError reports a failed SCRAM exchange detected by the client, such as a
// server signature mismatch.
type AuthError struct{ Cause error }

func (e *AuthError) Error() string { return e.Cause.Error() }
func (e *AuthError) Unwrap() error { return e.Cause }

// IncompatibleError reports a HELLO answer the client cannot accept.
type IncompatibleError struct{ Message string }

func (e *IncompatibleError) Error() string { return e.Message }

// ErrCancelled is returned by Next after Cancel.
var ErrCancelled = errors.New("operation cancelled")
