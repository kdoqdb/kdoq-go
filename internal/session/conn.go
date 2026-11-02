package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/kdoqdb/kdoq-go/internal/scram"
	"github.com/kdoqdb/kdoq-go/internal/transport"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Authentication methods and protocol constants.
const (
	MethodNone        = 0
	MethodSCRAMSHA256 = 1

	protocolVersion = 1
	minFrameCeiling = 1024
	encodingV4      = 4

	// CloseRevoked is the connection close code of a revoked session.
	CloseRevoked = 8
)

// Settings configure one connection.
type Settings struct {
	// SCRAM selects SCRAM-SHA-256 with Username and Password; otherwise the
	// explicit NONE method is offered.
	SCRAM              bool
	Username, Password string
	Required, Optional uint64
	FrameCeiling       uint64
	// Nonce fixes the SCRAM client nonce in tests.
	Nonce string
}

// Info describes an established session.
type Info struct {
	SessionID    uint64
	AccessLevel  uint8
	Capabilities uint64
}

// Conn is one established native connection.
type Conn struct {
	sess    transport.Session
	info    Info
	ceiling uint64

	writeMu sync.Mutex
	control transport.Stream

	nextRequest atomic.Uint64
	nextControl atomic.Uint64

	mu      sync.Mutex
	acks    map[uint64]chan bool
	failure error
	done    chan struct{}
	ended   bool
}

// Open sets up the control stream and authenticates. It does not return
// before READY has been received and, for SCRAM, the server signature has been
// verified.
func Open(ctx context.Context, sess transport.Session, s Settings) (*Conn, error) {
	c := &Conn{
		sess: sess,
		acks: map[uint64]chan bool{},
		done: make(chan struct{}),
	}
	stop := context.AfterFunc(ctx, func() { _ = sess.Close(0) })
	err := c.setup(ctx, s)
	if !stop() && err == nil {
		// The context ended while setup finished; the session is already closed.
		err = ctx.Err()
	}
	if err != nil {
		_ = sess.Close(0)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	go c.controlLoop()
	return c, nil
}

func (c *Conn) setup(ctx context.Context, s Settings) error {
	stream, err := c.sess.OpenStream(ctx)
	if err != nil {
		return &LostError{Cause: err}
	}
	c.control = stream
	method := uint16(MethodNone)
	var sc *scram.Client
	if s.SCRAM {
		method = MethodSCRAMSHA256
		if sc, err = scram.NewClient(s.Username, s.Password, s.Nonce); err != nil {
			return err
		}
	}
	var hello wire.Builder
	hello.U32(1, protocolVersion).U32(2, protocolVersion).U16List(3, encodingV4).
		U64(4, s.Required).U64(5, s.Optional).U16List(6, method).U64(7, s.FrameCeiling)
	if err := c.send(wire.Encode(wire.KindHello, hello.Bytes())); err != nil {
		return err
	}
	f, err := c.expect(wire.KindHello)
	if err != nil {
		return err
	}
	version, err := f.RequireU32(1)
	if err != nil {
		return err
	}
	encoding, err := f.RequireU16(2)
	if err != nil {
		return err
	}
	if version != protocolVersion || encoding != encodingV4 {
		return &IncompatibleError{Message: "the server selected an unsupported protocol version or encoding"}
	}
	caps, err := f.RequireU64(3)
	if err != nil {
		return err
	}
	selected, err := f.RequireU16(4)
	if err != nil {
		return err
	}
	if selected != method {
		return &IncompatibleError{Message: "the server selected an authentication method that was not offered"}
	}
	serverCeiling, err := f.RequireU64(5)
	if err != nil {
		return err
	}
	if caps&s.Required != s.Required || caps&^(s.Required|s.Optional) != 0 {
		return &IncompatibleError{Message: "the server selected capabilities that do not match the offer"}
	}
	if serverCeiling < minFrameCeiling {
		return &IncompatibleError{Message: "the server's frame ceiling is below the 1024-byte minimum"}
	}
	c.ceiling = min(s.FrameCeiling, serverCeiling)
	c.info.Capabilities = caps

	if sc != nil {
		if err := c.authenticate(sc); err != nil {
			return err
		}
	}
	ready, err := c.expect(wire.KindReady)
	if err != nil {
		return err
	}
	if c.info.SessionID, err = ready.RequireU64(1); err != nil {
		return err
	}
	if c.info.AccessLevel, err = ready.RequireU8(2); err != nil {
		return err
	}
	return nil
}

func (c *Conn) authenticate(sc *scram.Client) error {
	var first wire.Builder
	first.String(1, sc.First())
	if err := c.send(wire.Encode(wire.KindAuth, first.Bytes())); err != nil {
		return err
	}
	serverFirst, err := c.expectAuth()
	if err != nil {
		return err
	}
	final, err := sc.Final(serverFirst)
	if err != nil {
		return &AuthError{Cause: err}
	}
	var msg wire.Builder
	msg.String(1, final)
	if err := c.send(wire.Encode(wire.KindAuth, msg.Bytes())); err != nil {
		return err
	}
	serverFinal, err := c.expectAuth()
	if err != nil {
		return err
	}
	if err := sc.Verify(serverFinal); err != nil {
		return &AuthError{Cause: err}
	}
	return nil
}

func (c *Conn) expectAuth() (string, error) {
	f, err := c.expect(wire.KindAuth)
	if err != nil {
		return "", err
	}
	return f.RequireString(1)
}

func (c *Conn) expect(kind uint16) (*wire.Fields, error) {
	frame, err := wire.ReadFrame(c.control, wire.ControlFrameLimit+8)
	if err != nil {
		if errors.Is(err, wire.ErrMalformed) {
			return nil, err
		}
		return nil, &LostError{Cause: err}
	}
	if frame.Kind == wire.KindError {
		ef, err := wire.ParseError(frame.Payload)
		if err != nil {
			return nil, err
		}
		return nil, &ServerError{ef}
	}
	if frame.Kind != kind {
		return nil, errMalformed("unexpected frame kind %#x during setup", frame.Kind)
	}
	return wire.Parse(frame.Payload)
}

func errMalformed(format string, args ...any) error {
	return &protocolError{msg: fmt.Sprintf(format, args...)}
}

// protocolError reports a frame sequence the client cannot accept.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return e.msg }
func (e *protocolError) Unwrap() error { return wire.ErrMalformed }

func (c *Conn) send(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.control.Write(frame); err != nil {
		return &LostError{Cause: err}
	}
	return nil
}

func (c *Conn) controlLoop() {
	c.end(c.readControl())
}

// readControl handles control frames until the stream ends and returns the
// reason: nil for a clean end, the server's ERROR, or a protocol violation.
func (c *Conn) readControl() error {
	for {
		frame, err := wire.ReadFrame(c.control, wire.ControlFrameLimit+8)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch frame.Kind {
		case wire.KindControlAck:
			if err := c.acknowledge(frame.Payload); err != nil {
				return err
			}
		case wire.KindError:
			ef, err := wire.ParseError(frame.Payload)
			if err != nil {
				return err
			}
			return &ServerError{ef}
		default:
			return errMalformed("unexpected control frame kind %#x", frame.Kind)
		}
	}
}

func (c *Conn) acknowledge(payload []byte) error {
	f, err := wire.Parse(payload)
	if err != nil {
		return err
	}
	id, err := f.RequireU64(1)
	if err != nil {
		return err
	}
	c.mu.Lock()
	ack := c.acks[id]
	delete(c.acks, id)
	c.mu.Unlock()
	if ack != nil {
		found, _, _ := f.U8(3)
		ack <- found == 1
	}
	return nil
}

func (c *Conn) end(cause error) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	c.failure = cause
	for id, ack := range c.acks {
		close(ack)
		delete(c.acks, id)
	}
	c.mu.Unlock()
	close(c.done)
	_ = c.sess.Close(0)
}

// Info returns the session established by setup.
func (c *Conn) Info() Info { return c.info }

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Alive reports whether the connection is usable.
func (c *Conn) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Revoked reports whether the connection ended because its session was
// revoked: a Revoked ERROR on the control stream or close code 8, which the
// transport reports either as the session's close or as the error that ended
// a stream.
func (c *Conn) Revoked() bool {
	c.mu.Lock()
	failure := c.failure
	c.mu.Unlock()
	var se *ServerError
	if errors.As(failure, &se) && se.Code == CloseRevoked {
		return true
	}
	var closed *transport.SessionError
	if errors.As(failure, &closed) && closed.Code == CloseRevoked {
		return true
	}
	return errors.As(c.sess.Err(), &closed) && closed.Code == CloseRevoked
}

// Failure returns the error that ended the connection, if the server sent one.
func (c *Conn) Failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// Cancel asks the server to cancel the operation with the request ID. It does
// not wait for the acknowledgment.
func (c *Conn) Cancel(requestID uint64) {
	if !c.Alive() {
		return
	}
	var b wire.Builder
	b.U64(1, c.nextControl.Add(1)).U64(2, requestID)
	_ = c.send(wire.Encode(wire.KindCancel, b.Bytes()))
}

// Logout ends access: LOGOUT, then the acknowledgment (bounded by ctx), then
// the transport closes.
func (c *Conn) Logout(ctx context.Context) error {
	if !c.Alive() {
		return nil
	}
	id := c.nextControl.Add(1)
	ack := make(chan bool, 1)
	c.mu.Lock()
	c.acks[id] = ack
	c.mu.Unlock()
	var b wire.Builder
	b.U64(1, id)
	err := c.send(wire.Encode(wire.KindLogout, b.Bytes()))
	if err == nil {
		select {
		case <-ack:
		case <-c.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	c.Close()
	return err
}

// Close ends the connection without LOGOUT.
func (c *Conn) Close() { c.end(nil) }
