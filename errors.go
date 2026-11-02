package kdoq

import (
	"errors"
	"fmt"
)

// ErrorCode is a native protocol error code. Zero marks an error that did not
// come from the server.
type ErrorCode uint16

// The error codes of the native protocol (docs/reference/proto/errors.md).
const (
	CodeMalformed            ErrorCode = 1
	CodeIncompatible         ErrorCode = 2
	CodeAuthentication       ErrorCode = 3
	CodeNotReady             ErrorCode = 4
	CodeUnsupported          ErrorCode = 5
	CodeNotFound             ErrorCode = 6
	CodeOverloaded           ErrorCode = 7
	CodeRevoked              ErrorCode = 8
	CodeInvalidDocument      ErrorCode = 9
	CodeLimitExceeded        ErrorCode = 10
	CodeAlreadyExists        ErrorCode = 11
	CodeUnavailable          ErrorCode = 12
	CodeConflict             ErrorCode = 13
	CodeCancelled            ErrorCode = 14
	CodeStorage              ErrorCode = 15
	CodeWrongCase            ErrorCode = 16
	CodeUnloaded             ErrorCode = 17
	CodeCursorInUse          ErrorCode = 18
	CodeCursorExpired        ErrorCode = 19
	CodeAdmissionTimeout     ErrorCode = 20
	CodeWouldWait            ErrorCode = 21
	CodeMemoryWaitTimeout    ErrorCode = 22
	CodeExecutionMemoryLimit ErrorCode = 23
	CodeQueryQueueFull       ErrorCode = 24
	CodeSendStalled          ErrorCode = 25
	CodeForbidden            ErrorCode = 26
)

var codeNames = map[ErrorCode]string{
	CodeMalformed: "Malformed", CodeIncompatible: "Incompatible", CodeAuthentication: "Authentication",
	CodeNotReady: "NotReady", CodeUnsupported: "Unsupported", CodeNotFound: "NotFound",
	CodeOverloaded: "Overloaded", CodeRevoked: "Revoked", CodeInvalidDocument: "InvalidDocument",
	CodeLimitExceeded: "LimitExceeded", CodeAlreadyExists: "AlreadyExists", CodeUnavailable: "Unavailable",
	CodeConflict: "Conflict", CodeCancelled: "Cancelled", CodeStorage: "Storage", CodeWrongCase: "WrongCase",
	CodeUnloaded: "Unloaded", CodeCursorInUse: "CursorInUse", CodeCursorExpired: "CursorExpired",
	CodeAdmissionTimeout: "AdmissionTimeout", CodeWouldWait: "WouldWait",
	CodeMemoryWaitTimeout: "MemoryWaitTimeout", CodeExecutionMemoryLimit: "ExecutionMemoryLimit",
	CodeQueryQueueFull: "QueryQueueFull", CodeSendStalled: "SendStalled", CodeForbidden: "Forbidden",
}

// String returns the protocol name of the code.
func (c ErrorCode) String() string {
	if name, ok := codeNames[c]; ok {
		return name
	}
	return fmt.Sprintf("ErrorCode(%d)", uint16(c))
}

// Outcome tells whether a failed operation changed data.
type Outcome uint8

const (
	// OutcomeNotApplied means nothing was written.
	OutcomeNotApplied Outcome = iota
	// OutcomeUnknown means the work may have been accepted.
	OutcomeUnknown
)

// String returns "NotApplied" or "Unknown".
func (o Outcome) String() string {
	if o == OutcomeUnknown {
		return "Unknown"
	}
	return "NotApplied"
}

// Error is a failure of an operation. A server error carries its protocol
// code; a local error has Code zero. Errors match the Err sentinels with
// errors.Is by code. A timeout or cancellation of the context is an *Error
// that unwraps to the context's error, so errors.Is(err,
// context.DeadlineExceeded) holds.
type Error struct {
	Code      ErrorCode
	Outcome   Outcome
	RequestID uint64
	Message   string
	cause     error
}

// Error implements error.
func (e *Error) Error() string {
	text := "kdoq"
	if e.Code != 0 {
		text += " " + e.Code.String()
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	if e.RequestID != 0 {
		text += fmt.Sprintf(" (request %d)", e.RequestID)
	}
	if e.Outcome == OutcomeUnknown {
		text += " [outcome unknown]"
	}
	return text
}

// Is reports whether target is an *Error with the same nonzero code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code != 0 && t.Code == e.Code
}

// Unwrap returns the context error of a timeout or cancellation.
func (e *Error) Unwrap() error { return e.cause }

// One sentinel per error code, for errors.Is.
var (
	ErrMalformed            error = &Error{Code: CodeMalformed}
	ErrIncompatible         error = &Error{Code: CodeIncompatible}
	ErrAuthentication       error = &Error{Code: CodeAuthentication}
	ErrNotReady             error = &Error{Code: CodeNotReady}
	ErrUnsupported          error = &Error{Code: CodeUnsupported}
	ErrNotFound             error = &Error{Code: CodeNotFound}
	ErrOverloaded           error = &Error{Code: CodeOverloaded}
	ErrRevoked              error = &Error{Code: CodeRevoked}
	ErrInvalidDocument      error = &Error{Code: CodeInvalidDocument}
	ErrLimitExceeded        error = &Error{Code: CodeLimitExceeded}
	ErrAlreadyExists        error = &Error{Code: CodeAlreadyExists}
	ErrUnavailable          error = &Error{Code: CodeUnavailable}
	ErrConflict             error = &Error{Code: CodeConflict}
	ErrCancelled            error = &Error{Code: CodeCancelled}
	ErrStorage              error = &Error{Code: CodeStorage}
	ErrWrongCase            error = &Error{Code: CodeWrongCase}
	ErrUnloaded             error = &Error{Code: CodeUnloaded}
	ErrCursorInUse          error = &Error{Code: CodeCursorInUse}
	ErrCursorExpired        error = &Error{Code: CodeCursorExpired}
	ErrAdmissionTimeout     error = &Error{Code: CodeAdmissionTimeout}
	ErrWouldWait            error = &Error{Code: CodeWouldWait}
	ErrMemoryWaitTimeout    error = &Error{Code: CodeMemoryWaitTimeout}
	ErrExecutionMemoryLimit error = &Error{Code: CodeExecutionMemoryLimit}
	ErrQueryQueueFull       error = &Error{Code: CodeQueryQueueFull}
	ErrSendStalled          error = &Error{Code: CodeSendStalled}
	ErrForbidden            error = &Error{Code: CodeForbidden}
)

// ConfigError reports a bad DSN or option. It is returned before any network
// traffic.
type ConfigError struct{ Message string }

// Error implements error.
func (e *ConfigError) Error() string { return "kdoq: configuration: " + e.Message }

// ConnectionError reports that the server cannot be reached or that the
// connection was lost. A lost connection is never retried by the client.
type ConnectionError struct {
	Message   string
	Outcome   Outcome
	RequestID uint64
	Err       error
}

// Error implements error.
func (e *ConnectionError) Error() string {
	text := "kdoq: connection: " + e.Message
	if e.Err != nil {
		text += ": " + e.Err.Error()
	}
	if e.Outcome == OutcomeUnknown {
		text += " [outcome unknown]"
	}
	return text
}

// Unwrap returns the underlying cause.
func (e *ConnectionError) Unwrap() error { return e.Err }

// IncompleteError reports a response stream that ended or was reset without
// END or ERROR. Earlier complete items stay usable; the interrupted one does
// not.
type IncompleteError struct {
	Outcome   Outcome
	RequestID uint64
	// ResetCode is the application code of a reset stream; Reset tells whether
	// the peer reset it at all.
	Reset     bool
	ResetCode uint64
	Err       error
}

// Error implements error.
func (e *IncompleteError) Error() string {
	text := "kdoq: incomplete response"
	if e.Reset {
		text += fmt.Sprintf(": stream reset with code %d", e.ResetCode)
	}
	if e.Outcome == OutcomeUnknown {
		text += " [outcome unknown]"
	}
	return text
}

// Unwrap returns the underlying cause.
func (e *IncompleteError) Unwrap() error { return e.Err }

// isRetryable matches the refusals that are safe to repeat: overload and
// admission timeout, and only when the server says nothing was applied.
func isRetryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) || e.Outcome != OutcomeNotApplied {
		return false
	}
	return e.Code == CodeOverloaded || e.Code == CodeAdmissionTimeout
}

func isFatal(err error) bool {
	return errors.Is(err, ErrAuthentication) || errors.Is(err, ErrRevoked)
}
