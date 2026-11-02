package transport

import (
	"context"
	"errors"
	"sync"

	"github.com/quic-go/quic-go"
)

type quicTransport struct{ settings }

func (q *quicTransport) Dial(ctx context.Context, address string) (Connection, error) {
	cfg, err := q.tlsFor(address, "kdoq-native")
	if err != nil {
		return nil, err
	}
	qc := q.quic.Clone()
	qc.MaxIncomingStreams = -1
	qc.MaxIncomingUniStreams = -1
	conn, err := quic.DialAddr(ctx, address, cfg, qc)
	if err != nil {
		return nil, quicError(err)
	}
	return &quicConnection{conn: conn}, nil
}

type quicConnection struct{ conn *quic.Conn }

func (q *quicConnection) OpenControl(ctx context.Context) (Stream, error) {
	return q.OpenOperation(ctx)
}
func (q *quicConnection) OpenOperation(ctx context.Context) (Stream, error) {
	st, err := q.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, quicError(err)
	}
	return &quicStream{st: st}, nil
}
func (q *quicConnection) Close() error          { return q.conn.CloseWithError(0, "") }
func (q *quicConnection) Done() <-chan struct{} { return q.conn.Context().Done() }
func (q *quicConnection) Err() error {
	err := context.Cause(q.conn.Context())
	var app *quic.ApplicationError
	if errors.As(err, &app) && !app.Remote && app.ErrorCode == 0 {
		return nil
	}
	return quicError(err)
}

type quicStream struct {
	st        *quic.Stream
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (s *quicStream) Read(p []byte) (int, error) { n, err := s.st.Read(p); return n, quicError(err) }
func (s *quicStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.st.Write(p)
	return n, quicError(err)
}
func (s *quicStream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return quicError(s.st.Close())
}
func (s *quicStream) Abort() error { s.st.CancelRead(14); s.st.CancelWrite(14); return nil }
func (s *quicStream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = responseEnd(s); s.st.CancelRead(0); s.st.CancelWrite(0) })
	return s.closeErr
}
func quicError(err error) error {
	var stream *quic.StreamError
	if errors.As(err, &stream) {
		return &StreamError{Domain: DomainNative, Code: uint64(stream.ErrorCode), Err: err}
	}
	var app *quic.ApplicationError
	if errors.As(err, &app) {
		return &SessionError{Domain: DomainNative, Code: uint64(app.ErrorCode), Message: app.ErrorMessage, Err: err}
	}
	var tr *quic.TransportError
	if errors.As(err, &tr) {
		return &SessionError{Domain: DomainQUIC, Code: uint64(tr.ErrorCode), Message: tr.ErrorMessage, Err: err}
	}
	return err
}
