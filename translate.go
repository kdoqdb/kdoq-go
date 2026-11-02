package kdoq

import (
	"context"
	"errors"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

func outcomeOf(write bool) Outcome {
	if write {
		return OutcomeUnknown
	}
	return OutcomeNotApplied
}

// connectFailure wraps a failure to reach the server.
func connectFailure(message string, err error) error {
	return &ConnectionError{Message: message, Err: err}
}

// setupFailure translates an error of the connection setup.
func setupFailure(err error) error {
	var (
		server       *session.ServerError
		auth         *session.AuthError
		incompatible *session.IncompatibleError
	)
	switch {
	case errors.As(err, &server):
		return serverError(server, 0)
	case errors.As(err, &auth):
		return &Error{Code: CodeAuthentication, Message: auth.Error()}
	case errors.As(err, &incompatible):
		return &Error{Code: CodeIncompatible, Message: incompatible.Error()}
	case errors.Is(err, wire.ErrMalformed):
		return &Error{Code: CodeMalformed, Message: err.Error()}
	}
	return connectFailure("the connection failed during setup", err)
}

func serverError(e *session.ServerError, requestID uint64) *Error {
	out := &Error{Code: ErrorCode(e.Code), Message: e.Message, RequestID: e.RequestID}
	if out.RequestID == 0 {
		out.RequestID = requestID
	}
	if e.Unknown {
		out.Outcome = OutcomeUnknown
	}
	return out
}

// contextError turns the end of ctx into the public error: it unwraps to the
// context's error, and carries the outcome of the interrupted operation.
func contextError(ctx context.Context, write bool, requestID uint64) error {
	cause := context.Cause(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	} else if cause == nil || !errors.Is(cause, ctx.Err()) {
		cause = ctx.Err()
	}
	msg := "the operation was cancelled"
	if errors.Is(cause, context.DeadlineExceeded) {
		msg = "the operation timed out"
	}
	return &Error{Message: msg, Outcome: outcomeOf(write), RequestID: requestID, cause: cause}
}

// operationFailure translates an error of a started operation. write tells
// whether a lost response leaves the outcome unknown.
func (c *Client) operationFailure(err error, conn *session.Conn, requestID uint64, write bool) error {
	var (
		server     *session.ServerError
		lost       *session.LostError
		incomplete *session.IncompleteError
	)
	switch {
	case errors.As(err, &server):
		return serverError(server, requestID)
	case errors.As(err, &incomplete):
		if incomplete.Reset && incomplete.ResetCode == session.CloseRevoked {
			return &Error{Code: CodeRevoked, Message: "the session was revoked", RequestID: requestID}
		}
		return &IncompleteError{
			Outcome: outcomeOf(write), RequestID: requestID,
			Reset: incomplete.Reset, ResetCode: incomplete.ResetCode, Err: incomplete.Cause,
		}
	case errors.As(err, &lost):
		if conn != nil && conn.Revoked() {
			return &Error{Code: CodeRevoked, Message: "the session was revoked", RequestID: requestID}
		}
		return &ConnectionError{
			Message: "the connection was lost", Outcome: outcomeOf(write), RequestID: requestID, Err: lost.Cause,
		}
	case errors.Is(err, wire.ErrMalformed):
		return &Error{Code: CodeMalformed, Message: err.Error(), RequestID: requestID, Outcome: outcomeOf(write)}
	}
	return err
}

// documentError turns a failure to write a value as a document into an
// InvalidDocument error that applied nothing.
func documentError(err error) error {
	var (
		invalid *document.InvalidError
		bad     *value.Error
	)
	if errors.As(err, &invalid) || errors.As(err, &bad) {
		return &Error{Code: CodeInvalidDocument, Message: err.Error()}
	}
	return err
}
