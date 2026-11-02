package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
)

type h2Transport struct{ settings }

func (h *h2Transport) Dial(ctx context.Context, address string) (Connection, error) {
	cfg, err := h.tlsFor(address, "h2")
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	stopDial := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopDial()
	secured := tls.Client(raw, cfg)
	if err = secured.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if secured.ConnectionState().NegotiatedProtocol != "h2" {
		_ = raw.Close()
		return nil, errors.New("server did not negotiate h2")
	}
	c := &h2Connection{address: address, done: make(chan struct{})}
	observed := &h2Socket{Conn: secured, owner: c}
	// A dedicated ClientConn cannot pool, redial or attach operations to another
	// native session. RoundTrip also supports concurrent request/response bodies.
	driver, err := http2.ConfigureTransports(&http.Transport{})
	if err == nil {
		driver.DisableCompression = true
		driver.MaxHeaderListSize = 8192
		driver.StrictMaxConcurrentStreams = true
		c.client, err = driver.NewClientConn(observed)
	}
	if err != nil {
		_ = secured.Close()
		return nil, h2Error(err)
	}
	if !stopDial() {
		_ = c.Close()
		return nil, ctx.Err()
	}
	return c, nil
}

type h2Connection struct {
	address string
	client  *http2.ClientConn
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	err     error
	control bool
}

func (c *h2Connection) end(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = h2Error(err); c.mu.Unlock(); close(c.done) })
}
func (c *h2Connection) Done() <-chan struct{} { return c.done }
func (c *h2Connection) Err() error            { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *h2Connection) Close() error          { c.end(nil); return c.client.Close() }
func (c *h2Connection) OpenControl(ctx context.Context) (Stream, error) {
	c.mu.Lock()
	if c.control {
		c.mu.Unlock()
		return nil, errors.New("control stream already opened")
	}
	c.control = true
	c.mu.Unlock()
	return c.open(ctx, "/native/control")
}
func (c *h2Connection) OpenOperation(ctx context.Context) (Stream, error) {
	return c.open(ctx, "/native/operation")
}
func (c *h2Connection) open(ctx context.Context, path string) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-c.done:
		return nil, net.ErrClosed
	default:
	}
	reader, writer := io.Pipe()
	lifetime, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(lifetime, http.MethodPost, "https://"+c.address+path, reader)
	if err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/vnd.kdoq.native")
	s := &h2Stream{reader: reader, writer: writer, cancel: cancel, ready: make(chan struct{})}
	stopOpening := context.AfterFunc(ctx, func() { _ = s.Abort() })
	go func() {
		defer stopOpening()
		rsp, err := c.client.RoundTrip(req)
		if err == nil && rsp.StatusCode != http.StatusOK {
			_ = rsp.Body.Close()
			err = fmt.Errorf("native H2 request refused: HTTP %d", rsp.StatusCode)
		}
		if err == nil && rsp.Header.Get("Content-Type") != "application/vnd.kdoq.native" {
			_ = rsp.Body.Close()
			err = errors.New("invalid native H2 response content type")
		}
		s.mu.Lock()
		if err != nil {
			s.err = h2Error(err)
			_ = reader.CloseWithError(s.err)
		} else {
			s.body = rsp.Body
			if s.closed {
				_ = rsp.Body.Close()
			}
		}
		s.mu.Unlock()
		close(s.ready)
	}()
	return s, nil
}

type h2Socket struct {
	net.Conn
	owner *h2Connection
}

func (s *h2Socket) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	if err != nil {
		s.owner.end(err)
	}
	return n, err
}
func (s *h2Socket) Write(p []byte) (int, error) {
	n, err := s.Conn.Write(p)
	if err != nil {
		s.owner.end(err)
	}
	return n, err
}

type h2Stream struct {
	reader    *io.PipeReader
	writer    *io.PipeWriter
	cancel    context.CancelFunc
	ready     chan struct{}
	mu        sync.Mutex
	body      io.ReadCloser
	err       error
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (s *h2Stream) Read(p []byte) (int, error) {
	<-s.ready
	s.mu.Lock()
	body, err := s.body, s.err
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	n, err := body.Read(p)
	return n, h2Error(err)
}
func (s *h2Stream) Write(p []byte) (int, error) { n, err := s.writer.Write(p); return n, h2Error(err) }
func (s *h2Stream) CloseWrite() error           { return s.writer.Close() }
func (s *h2Stream) Abort() error                { return s.release() }
func (s *h2Stream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = responseEnd(s); _ = s.release() })
	return s.closeErr
}
func (s *h2Stream) release() error {
	s.cancel()
	_ = s.writer.CloseWithError(context.Canceled)
	_ = s.reader.CloseWithError(context.Canceled)
	s.mu.Lock()
	s.closed = true
	body := s.body
	s.mu.Unlock()
	if body != nil {
		return body.Close()
	}
	return nil
}
func h2Error(err error) error {
	var stream http2.StreamError
	if errors.As(err, &stream) {
		return &StreamError{Domain: DomainH2, Code: uint64(stream.Code), Err: err}
	}
	var conn http2.ConnectionError
	if errors.As(err, &conn) {
		return &SessionError{Domain: DomainH2, Code: uint64(conn), Err: err}
	}
	var goaway http2.GoAwayError
	if errors.As(err, &goaway) {
		return &SessionError{Domain: DomainH2, Code: uint64(goaway.ErrCode), Message: goaway.DebugData, Err: err}
	}
	return err
}
