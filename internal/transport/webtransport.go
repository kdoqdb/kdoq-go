package transport

import (
	"context"
	"errors"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

// WebTransport dials real WebTransport sessions on quic-go.
type WebTransport struct{}

// Dial implements Dialer.
func (WebTransport) Dial(ctx context.Context, cfg DialConfig) (Session, error) {
	t := &webtransport.Dialer{
		TLSClientConfig: cfg.TLS,
		QUICConfig:      &quic.Config{EnableDatagrams: true},
	}
	rsp, sess, err := t.Dial(ctx, cfg.URL, cfg.Header)
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
}

func (s *wtSession) OpenStream(ctx context.Context) (Stream, error) {
	st, err := s.sess.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &wtStream{st: st}, nil
}

func (s *wtSession) Close(code uint32) error {
	err := s.sess.CloseWithError(webtransport.SessionErrorCode(code), "")
	_ = s.transport.Close()
	return err
}

func (s *wtSession) Done() <-chan struct{} { return s.sess.Context().Done() }

// Err reports the close the server sent: a WebTransport close capsule or, as
// for a revoked session, a QUIC connection close. The session context carries
// no cause, so the close error is read from the accept call, which returns it
// at once once the session is closed.
func (s *wtSession) Err() error {
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
		return &SessionError{Code: uint32(se.ErrorCode), Message: se.Message}
	}
	return remoteClose(err)
}

// remoteClose reports a QUIC connection close by the server, which is how a
// revoked session ends, and nil for any other error.
func remoteClose(err error) error {
	if ae, ok := errors.AsType[*quic.ApplicationError](err); ok && ae.Remote {
		return &SessionError{Code: uint32(ae.ErrorCode), Message: ae.ErrorMessage}
	}
	return nil
}

type wtStream struct{ st *webtransport.Stream }

func (s *wtStream) Read(p []byte) (int, error) {
	n, err := s.st.Read(p)
	return n, convert(err)
}

func (s *wtStream) Write(p []byte) (int, error) {
	n, err := s.st.Write(p)
	return n, convert(err)
}

func (s *wtStream) CloseWrite() error       { return s.st.Close() }
func (s *wtStream) CancelRead(code uint64)  { s.st.CancelRead(webtransport.StreamErrorCode(code)) }
func (s *wtStream) CancelWrite(code uint64) { s.st.CancelWrite(webtransport.StreamErrorCode(code)) }

func convert(err error) error {
	if se, ok := errors.AsType[*webtransport.StreamError](err); ok {
		return &StreamError{Code: uint64(se.ErrorCode)}
	}
	if closed := remoteClose(err); closed != nil {
		return closed
	}
	return err
}
