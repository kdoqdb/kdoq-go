package session

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/transport"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Spec describes one operation: its code, the payload fields of its COMMAND
// and the complete DICTIONARY and INPUT_VALUE frames sent before INPUT_END.
type Spec struct {
	Operation uint16
	Payload   []byte
	Inputs    [][]byte
	// Write marks an operation that may have been accepted when its response
	// is lost.
	Write bool
}

// Event is one response frame.
type Event struct {
	Kind   uint16
	Fields *wire.Fields
}

// Op is the response side of one operation stream. It belongs to one
// goroutine, except Cancel, which may be called from any.
type Op struct {
	conn      *Conn
	stream    transport.Stream
	requestID uint64
	write     bool

	contexts  document.Contexts
	finished  bool
	cancelled atomic.Bool
}

// Start opens a stream and sends the operation. The upload runs in the
// background; the response is read with Next.
func (c *Conn) Start(ctx context.Context, spec Spec) (*Op, error) {
	if !c.Alive() {
		return nil, &LostError{}
	}
	stream, err := c.sess.OpenStream(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &LostError{Cause: err}
	}
	op := &Op{
		conn:      c,
		stream:    stream,
		requestID: c.nextRequest.Add(1),
		write:     spec.Write,
		contexts:  document.Contexts{},
	}
	var cmd wire.Builder
	cmd.U16(1, spec.Operation).U64(2, op.requestID)
	if spec.Payload != nil {
		cmd.Raw(3, spec.Payload)
	}
	go func() {
		frames := append([][]byte{wire.Encode(wire.KindCommand, cmd.Bytes())}, spec.Inputs...)
		frames = append(frames, wire.Encode(wire.KindInputEnd, nil))
		for _, f := range frames {
			if _, err := stream.Write(f); err != nil {
				return // the response side reports the failure
			}
		}
		_ = stream.CloseWrite()
	}()
	return op, nil
}

// RequestID returns the operation's request ID.
func (o *Op) RequestID() uint64 { return o.requestID }

// Write reports whether the operation is a write.
func (o *Op) Write() bool { return o.write }

// Names returns the output dictionary of a context.
func (o *Op) Names(context uint64) document.Names { return o.contexts[context] }

// Decode decodes a result value announced in context.
func (o *Op) Decode(context uint64, b []byte) (any, error) {
	return document.Decode(o.contexts[context], b)
}

// Next returns the next response event. After END the operation is finished
// and Next returns io.EOF. A server ERROR is a *ServerError; a response that
// ends or is reset without END is an *IncompleteError; a lost connection is a
// *LostError.
func (o *Op) Next() (Event, error) {
	for {
		if o.cancelled.Load() {
			return Event{}, ErrCancelled
		}
		if o.finished {
			return Event{}, io.EOF
		}
		frame, err := wire.ReadFrame(o.stream, o.conn.ceiling)
		if o.cancelled.Load() {
			return Event{}, ErrCancelled
		}
		if err != nil {
			o.finish()
			return Event{}, o.lost(err)
		}
		switch frame.Kind {
		case wire.KindDictionary:
			if err := o.contexts.Add(frame.Payload); err != nil {
				o.abort()
				return Event{}, err
			}
			continue
		case wire.KindError:
			o.abort()
			ef, err := wire.ParseError(frame.Payload)
			if err != nil {
				return Event{}, err
			}
			return Event{}, &ServerError{ef}
		case wire.KindBegin, wire.KindItem, wire.KindDiagnostic, wire.KindProgress, wire.KindEnd:
			fields, err := wire.Parse(frame.Payload)
			if err != nil {
				o.abort()
				return Event{}, err
			}
			if frame.Kind == wire.KindEnd {
				o.finish()
			}
			return Event{Kind: frame.Kind, Fields: fields}, nil
		}
		o.abort()
		return Event{}, errMalformed("unexpected frame kind %#x in a response", frame.Kind)
	}
}

// lost classifies a failed read of the response.
func (o *Op) lost(err error) error {
	var reset *transport.StreamError
	switch {
	case !o.conn.Alive():
		return &LostError{Cause: err}
	case errors.As(err, &reset):
		return &IncompleteError{Reset: true, ResetCode: reset.Code, Cause: err}
	case errors.Is(err, wire.ErrIncomplete), errors.Is(err, io.EOF):
		return &IncompleteError{Cause: err}
	case errors.Is(err, wire.ErrMalformed):
		return err
	}
	return &LostError{Cause: err}
}

func (o *Op) finish() { o.finished = true }

// abort stops both directions after an error frame or a protocol violation.
func (o *Op) abort() {
	o.finished = true
	o.stream.CancelWrite(0)
	o.stream.CancelRead(0)
}

// Cancel stops the operation: CANCEL on the control stream and a closed read
// side. A concurrent or later Next returns ErrCancelled.
func (o *Op) Cancel() {
	if !o.cancelled.CompareAndSwap(false, true) {
		return
	}
	o.conn.Cancel(o.requestID)
	o.stream.CancelRead(14)
	o.stream.CancelWrite(14)
}

// Close releases the stream of an operation that was not read to its end.
func (o *Op) Close() {
	if !o.finished && !o.cancelled.Load() {
		o.abort()
	}
}
