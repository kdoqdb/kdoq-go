package kdoq

import (
	"context"
	"errors"
	"io"

	"github.com/kdoqdb/kdoq-go/internal/retry"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// response is the complete answer to a unary operation.
type response struct {
	op    *session.Op
	begin *wire.Fields
	items []*wire.Fields
	end   *wire.Fields
}

// withDeadline applies the operation timeout when ctx has no deadline.
func (c *Client) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.cfg.operationTimeout)
}

// observer receives the PROGRESS and DIAGNOSTIC frames of an operation, in the
// goroutine that reads the response.
type observer struct {
	progress   func(Progress)
	diagnostic func(Diagnostic)
}

// unary runs an operation that returns all its items: it retries the
// overload and admission-timeout errors within the deadline, reconnects
// lazily and never retries after a lost connection.
func (c *Client) unary(ctx context.Context, spec session.Spec, need Capability) (*response, error) {
	resp, err := c.exchange(ctx, spec, need, nil)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// exchange is unary with observation. When it fails after the response began,
// it also returns what was received so far. Once BEGIN or an item has arrived
// the refusal is never retried: a repeated QUERY_CREATE would open a second
// cursor and a repeated CURSOR_READ would skip the items already consumed.
func (c *Client) exchange(ctx context.Context, spec session.Spec, need Capability, obs *observer) (*response, error) {
	ctx, cancel := c.withDeadline(ctx)
	defer cancel()
	var resp *response
	retryable := func(err error) bool {
		return isRetryable(err) && (resp == nil || (resp.begin == nil && len(resp.items) == 0))
	}
	err := retry.Do(ctx, c.cfg.retry, retryable, func() error {
		var err error
		resp, err = c.attempt(ctx, spec, need, obs)
		return err
	})
	if err != nil {
		if ctx.Err() != nil && isRetryable(err) {
			var last *Error
			errors.As(err, &last)
			err = contextError(ctx, false, last.RequestID) // the last attempt was refused
		}
		if isFatal(err) {
			c.fail(err)
		}
		return resp, err
	}
	return resp, nil
}

func (c *Client) attempt(ctx context.Context, spec session.Spec, need Capability, obs *observer) (*response, error) {
	conn, err := c.ensure(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx, false, 0)
		}
		return nil, err
	}
	if need != 0 && !Capability(conn.Info().Capabilities).Has(need) {
		return nil, &Error{Code: CodeUnsupported, Message: "the server did not negotiate the capability this operation needs"}
	}
	op, err := conn.Start(ctx, spec)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx, false, 0)
		}
		return nil, c.operationFailure(err, conn, 0, false)
	}
	stop := context.AfterFunc(ctx, op.Cancel)
	defer stop()
	defer op.Close()
	resp := &response{op: op}
	for {
		ev, err := op.Next()
		if err != nil {
			if ctx.Err() != nil {
				return resp, contextError(ctx, spec.Write, op.RequestID())
			}
			if errors.Is(err, io.EOF) {
				err = &session.IncompleteError{}
			}
			return resp, c.operationFailure(err, conn, op.RequestID(), spec.Write)
		}
		switch ev.Kind {
		case wire.KindBegin:
			resp.begin = ev.Fields
		case wire.KindItem:
			resp.items = append(resp.items, ev.Fields)
		case wire.KindProgress:
			if err := obs.onProgress(ev.Fields); err != nil {
				return resp, c.operationFailure(err, conn, op.RequestID(), spec.Write)
			}
		case wire.KindDiagnostic:
			if err := obs.onDiagnostic(ev.Fields); err != nil {
				return resp, c.operationFailure(err, conn, op.RequestID(), spec.Write)
			}
		case wire.KindEnd:
			resp.end = ev.Fields
			return resp, nil
		}
	}
}
