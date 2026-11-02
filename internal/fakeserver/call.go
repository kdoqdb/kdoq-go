package fakeserver

import (
	"fmt"
	"sync"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/transport"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Call is one operation the server received.
type Call struct {
	Conn      *Conn
	Operation uint16
	RequestID uint64
	// Payload holds the fields of the COMMAND payload group.
	Payload *wire.Fields
	// Inputs holds the DICTIONARY and INPUT_VALUE frames in arrival order.
	Inputs []wire.Frame

	stream    transport.Stream
	cancelled chan struct{}
	once      sync.Once
	contexts  document.Contexts
}

func (c *Call) markCancelled() { c.once.Do(func() { close(c.cancelled) }) }

// Cancelled is closed when the client sent CANCEL for this call.
func (c *Call) Cancelled() <-chan struct{} { return c.cancelled }

// Fail answers with an ERROR frame.
func (c *Call) Fail(code uint16, unknown bool) {
	_, _ = c.stream.Write(wire.EncodeError(wire.ErrorFrame{RequestID: c.RequestID, Code: code, Unknown: unknown}))
	_ = c.stream.CloseWrite()
}

// Reset resets the response stream with an application code.
func (c *Call) Reset(code uint64) { transport.Reset(c.stream, code) }

// Truncate ends the response stream cleanly without END or ERROR.
func (c *Call) Truncate() { _ = c.stream.CloseWrite() }

// Begin, Item and End write response frames; Dictionary writes a DICTIONARY.
func (c *Call) Begin(b *wire.Builder) { c.frame(wire.KindBegin, b) }
func (c *Call) Item(b *wire.Builder)  { c.frame(wire.KindItem, b) }
func (c *Call) End(b *wire.Builder)   { c.frame(wire.KindEnd, b); _ = c.stream.CloseWrite() }

func (c *Call) frame(kind uint16, b *wire.Builder) {
	var payload []byte
	if b != nil {
		payload = b.Bytes()
	}
	_, _ = c.stream.Write(wire.Encode(kind, payload))
}

// Document returns the decoded body of the call's single INPUT_VALUE.
func (c *Call) Document() (any, []byte, error) {
	for _, f := range c.Inputs {
		if f.Kind != wire.KindInputValue {
			continue
		}
		fields, err := wire.Parse(f.Payload)
		if err != nil {
			return nil, nil, err
		}
		ctx, err := fields.RequireU64(1)
		if err != nil {
			return nil, nil, err
		}
		body, err := fields.RequireBytes(2)
		if err != nil {
			return nil, nil, err
		}
		names, ok := c.contexts[ctx]
		if !ok {
			return nil, nil, fmt.Errorf("context %d was not declared", ctx)
		}
		v, err := document.Decode(names, body)
		return v, body, err
	}
	return nil, nil, fmt.Errorf("no INPUT_VALUE")
}

// operation reads one operation stream and answers it.
func (c *Conn) operation(stream transport.Stream) {
	frame, err := wire.ReadFrame(stream, c.ceiling)
	if err != nil || frame.Kind != wire.KindCommand {
		transport.Reset(stream, 1)
		return
	}
	command, err := wire.Parse(frame.Payload)
	if err != nil {
		transport.Reset(stream, 1)
		return
	}
	op, _ := command.RequireU16(1)
	id, _ := command.RequireU64(2)
	call := &Call{
		Conn: c, Operation: op, RequestID: id, stream: stream,
		cancelled: make(chan struct{}), contexts: document.Contexts{},
	}
	if g, ok, _ := command.Group(3); ok {
		call.Payload = g
	} else {
		call.Payload, _ = wire.Parse(nil)
	}
	c.mu.Lock()
	c.calls[id] = call
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
	}()
	if id == 0 {
		call.Fail(1, false)
		return
	}
	for {
		f, err := wire.ReadFrame(stream, c.ceiling)
		if err != nil {
			return
		}
		if f.Kind == wire.KindInputEnd {
			break
		}
		if f.Kind == wire.KindDictionary {
			if err := call.contexts.Add(f.Payload); err != nil {
				call.Fail(1, false)
				return
			}
		}
		call.Inputs = append(call.Inputs, f)
	}
	if c.srv.Intercept != nil && c.srv.Intercept(call) {
		return
	}
	c.dispatch(call)
}

// requirement is the capability bit and access level an operation needs.
func requirement(op uint16) (bit uint64, level uint8) {
	switch {
	case op <= 0x0002, op >= 0x0101 && op <= 0x0103:
		return 1, 0
	case op == 0x0201, op == 0x0202, op == 0x0301, op == 0x0302, op == 0x0401:
		return 4, 1
	case op >= 0x0402 && op <= 0x0404:
		return 4, 2
	case op >= 0x0405 && op <= 0x0407:
		return 16, 2
	case op == 0x0504:
		return 8, 0
	case op >= 0x0501 && op <= 0x0503:
		return 8, 1
	}
	return 4, 3
}

func (c *Conn) dispatch(call *Call) {
	bit, level := requirement(call.Operation)
	op := call.Operation
	switch {
	case c.caps&bit == 0:
		call.Fail(2, false)
	case c.level < level:
		call.Fail(26, false)
	case op == 0x0001:
		c.srv.health(call)
	case op == 0x0002:
		c.srv.serverCapabilities(call)
	case op >= 0x0101 && op <= 0x0103:
		c.srv.sessions(call)
	case op >= 0x0201 && op <= 0x0205, op >= 0x0301 && op <= 0x0305:
		c.srv.namespaces(call)
	case op >= 0x0401 && op <= 0x0404:
		c.srv.documents(call)
	case op >= 0x0405 && op <= 0x0407:
		c.srv.mutations(call)
	case op >= 0x0501 && op <= 0x0504:
		c.srv.queries(call)
	case op == 0x0601, op == 0x0602:
		c.srv.persistence(call)
	default:
		call.Fail(5, false)
	}
}
