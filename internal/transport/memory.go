package transport

import (
	"context"
	"errors"
	public "github.com/kdoqdb/kdoq-go/transport"
	"io"
	"sync"
)

// ErrSessionClosed is returned by operations on a closed in-memory session.
var ErrSessionClosed = errors.New("session closed")

// Pipe returns a connected client and server end of an in-memory session. It
// stands in for the network in tests: streams buffer without limit, and the
// reset and close codes travel like the real transport's.
func Pipe() (*Memory, *Memory) {
	core := &memoryCore{done: make(chan struct{})}
	incoming := make(chan *memoryStream, 1024)
	client := &Memory{core: core, side: 0, incoming: nil, peerIncoming: incoming}
	server := &Memory{core: core, side: 1, incoming: incoming}
	return client, server
}

// Memory is one end of an in-memory session. The client end opens streams and
// the server end accepts them.
type Memory struct {
	core         *memoryCore
	side         int
	incoming     chan *memoryStream
	peerIncoming chan *memoryStream
}

type memoryCore struct {
	mu      sync.Mutex
	done    chan struct{}
	ended   bool
	errs    [2]error
	pipes   []*bytePipe
	closeBy int
}

// OpenStream implements Session.
func (m *Memory) OpenStream(ctx context.Context) (Stream, error) {
	if m.peerIncoming == nil {
		return nil, errors.New("the server end cannot open streams")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := m.core
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return nil, ErrSessionClosed
	}
	up, down := newBytePipe(), newBytePipe()
	c.pipes = append(c.pipes, up, down)
	c.mu.Unlock()
	m.peerIncoming <- &memoryStream{in: up, out: down}
	return &memoryStream{in: down, out: up}, nil
}

// Accept returns the next stream the client opened (server end only).
func (m *Memory) Accept(ctx context.Context) (Stream, error) {
	select {
	case s := <-m.incoming:
		return s, nil
	case <-m.core.done:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close implements Session. The peer sees a *SessionError with code.
func (m *Memory) CloseCode(code uint32) error {
	m.core.end(m.side, nil, &SessionError{Domain: public.DomainNative, Code: uint64(code)})
	return nil
}

// Abort ends the session as if the network failed: both ends see err.
func (m *Memory) Abort(err error) {
	m.core.end(-1, err, err)
}

// Done implements Session.
func (m *Memory) Done() <-chan struct{} { return m.core.done }

// Err implements Session.
func (m *Memory) Err() error {
	m.core.mu.Lock()
	defer m.core.mu.Unlock()
	return m.core.errs[m.side]
}

func (c *memoryCore) end(by int, selfErr, peerErr error) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	if by >= 0 {
		c.errs[by] = selfErr
		c.errs[1-by] = peerErr
	} else {
		c.errs[0], c.errs[1] = selfErr, peerErr
	}
	pipes := c.pipes
	c.mu.Unlock()
	for _, p := range pipes {
		p.abort(ErrSessionClosed)
	}
	close(c.done)
}

// bytePipe is one direction of a stream.
type bytePipe struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	fin     bool
	readErr error // reset or abort seen by the reader
	stopped error // the reader stopped; seen by the writer
}

func newBytePipe() *bytePipe {
	p := &bytePipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bytePipe) read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.fin && p.readErr == nil {
		p.cond.Wait()
	}
	if p.readErr != nil {
		return 0, p.readErr
	}
	if len(p.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *bytePipe) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped != nil {
		return 0, p.stopped
	}
	if p.fin || p.readErr != nil {
		return 0, io.ErrClosedPipe
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *bytePipe) finish() {
	p.mu.Lock()
	p.fin = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *bytePipe) reset(code uint64) {
	p.mu.Lock()
	if p.readErr == nil {
		p.readErr = &StreamError{Domain: public.DomainNative, Code: code}
		p.buf = nil
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *bytePipe) stop(code uint64) {
	p.mu.Lock()
	if p.stopped == nil {
		p.stopped = &StreamError{Domain: public.DomainNative, Code: code}
	}
	if p.readErr == nil {
		p.readErr = p.stopped
		p.buf = nil
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *bytePipe) abort(err error) {
	p.mu.Lock()
	if p.readErr == nil {
		p.readErr = err
		p.buf = nil
	}
	if p.stopped == nil {
		p.stopped = err
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

type memoryStream struct{ in, out *bytePipe }

func (s *memoryStream) Read(b []byte) (int, error)  { return s.in.read(b) }
func (s *memoryStream) Write(b []byte) (int, error) { return s.out.write(b) }
func (s *memoryStream) CloseWrite() error           { s.out.finish(); return nil }
func (s *memoryStream) CancelRead(code uint64)      { s.in.stop(code) }
func (s *memoryStream) CancelWrite(code uint64)     { s.out.reset(code) }

func (m *Memory) OpenControl(ctx context.Context) (Stream, error)   { return m.OpenStream(ctx) }
func (m *Memory) OpenOperation(ctx context.Context) (Stream, error) { return m.OpenStream(ctx) }
func (m *Memory) Close() error                                      { return m.CloseCode(0) }
func (s *memoryStream) Abort() error                                { s.CancelRead(14); s.CancelWrite(14); return nil }
func (s *memoryStream) Close() error                                { s.CancelRead(0); s.CancelWrite(0); return nil }
