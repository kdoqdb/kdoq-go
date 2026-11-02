package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/quic-go/webtransport-go"
)

// webTransport creates WebTransport sessions on the pinned QUIC stack.
type webTransport struct{ settings }

// Dial implements Transport.
func (w *webTransport) Dial(ctx context.Context, address string) (Connection, error) {
	cfg, err := w.tlsFor(address, "h3")
	if err != nil {
		return nil, err
	}
	qc := w.quic.Clone()
	qc.EnableDatagrams = true
	t := &webtransport.Dialer{
		TLSClientConfig: cfg,
		QUICConfig:      qc,
	}
	rsp, sess, err := t.Dial(ctx, "https://"+address+"/native", nil)
	if err != nil {
		_ = t.Close()
		if rsp != nil {
			return nil, errors.New("server refused the WebTransport session: " + rsp.Status)
		}
		return nil, err
	}
	return &wtSession{sess: sess, transport: t}, nil
}

type wtSession struct {
	sess      *webtransport.Session
	transport *webtransport.Dialer
	closeOnce sync.Once
	closeErr  error
	local     atomic.Bool
}

func (s *wtSession) OpenOperation(ctx context.Context) (Stream, error) {
	st, err := s.sess.OpenStreamSync(ctx)
	if err != nil {
		return nil, convert(err)
	}
	return &wtStream{st: st}, nil
}

func (s *wtSession) OpenControl(ctx context.Context) (Stream, error) { return s.OpenOperation(ctx) }

func (s *wtSession) Close() error {
	s.closeOnce.Do(func() {
		s.local.Store(s.sess.Context().Err() == nil)
		// Each session owns its HTTP/3 transport. Close it first so an uncooperative
		// peer cannot block a WebTransport close capsule on CONNECT stream credit.
		s.closeErr = s.transport.Close()
		_ = s.sess.CloseWithError(0, "")
	})
	return s.closeErr
}

func (s *wtSession) Done() <-chan struct{} { return s.sess.Context().Done() }

// Err reports the close the server sent: a WebTransport close capsule or, as
// for a revoked session, a QUIC connection close. The session context carries
// no cause, so the close error is read from the accept call, which returns it
// at once once the session is closed.
func (s *wtSession) Err() error {
	if s.local.Load() {
		return nil
	}
	select {
	case <-s.sess.Context().Done():
	default:
		return nil
	}
	// The context is already cancelled, so the call cannot block: it either
	// returns the close error or, were the library to change, the context's.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.sess.AcceptStream(cancelled)
	if se, ok := errors.AsType[*webtransport.SessionError](err); ok && se.Remote {
		return &SessionError{Domain: DomainNative, Code: uint64(se.ErrorCode), Message: se.Message, Err: err}
	}
	return quicError(err)
}

type wtStream struct {
	st        *webtransport.Stream
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (s *wtStream) Read(p []byte) (int, error) {
	n, err := s.st.Read(p)
	return n, convert(err)
}

func (s *wtStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.st.Write(p)
	return n, convert(err)
}

func (s *wtStream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.st.Close()
}
func (s *wtStream) CancelRead(code uint64)  { s.st.CancelRead(webtransport.StreamErrorCode(code)) }
func (s *wtStream) CancelWrite(code uint64) { s.st.CancelWrite(webtransport.StreamErrorCode(code)) }

func convert(err error) error {
	if se, ok := errors.AsType[*webtransport.StreamError](err); ok {
		return &StreamError{Domain: DomainNative, Code: uint64(se.ErrorCode), Err: err}
	}
	return quicError(err)
}

func (s *wtStream) Abort() error { s.CancelRead(14); s.CancelWrite(14); return nil }
func (s *wtStream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = responseEnd(s); s.CancelRead(0); s.CancelWrite(0) })
	return s.closeErr
}
