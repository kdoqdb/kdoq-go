package kdoq_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/transport"
)

func TestPublicTransportOptionConnectsIndependentClients(t *testing.T) {
	tr := newServer(fakeserver.Config{})
	for range 2 {
		c, err := kdoq.Connect(context.Background(), "kdoq://h", kdoq.ConnectOptions{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.Conns()) != 2 {
		t.Fatal("each client must own a new connection")
	}
}

// domainTransport models a peer reset using a different transport code space.
// Only the public transport contract is involved in translating its errors.
type domainTransport struct{ transport.Transport }
type domainConnection struct{ transport.Connection }
type domainStream struct{ transport.Stream }

func (t domainTransport) Dial(ctx context.Context, addr string) (transport.Connection, error) {
	c, err := t.Transport.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	return domainConnection{c}, nil
}
func (c domainConnection) OpenOperation(ctx context.Context) (transport.Stream, error) {
	s, err := c.Connection.OpenOperation(ctx)
	if err != nil {
		return nil, err
	}
	return domainStream{s}, nil
}
func (s domainStream) Read(p []byte) (int, error) {
	n, err := s.Stream.Read(p)
	var reset *transport.StreamError
	if errors.As(err, &reset) {
		err = &transport.StreamError{Domain: transport.DomainH2, Code: reset.Code, Err: err}
	}
	return n, err
}
func TestH2ResetEightDoesNotRevokeTheNativeSession(t *testing.T) {
	server := newServer(fakeserver.Config{})
	h := connect(t, server, "kdoq://h", kdoq.ConnectOptions{Transport: domainTransport{server}})
	server.Intercept = func(c *fakeserver.Call) bool { c.Reset(8); return true }
	_, err := h.client.Server().Health(context.Background())
	var incomplete *kdoq.IncompleteError
	if !errors.As(err, &incomplete) || incomplete.ResetDomain != transport.DomainH2 || incomplete.ResetCode != 8 {
		t.Fatalf("Health = %v; want H2 reset 8", err)
	}
	if errors.Is(err, kdoq.ErrRevoked) || h.client.State() == kdoq.StateClosed {
		t.Fatal("H2 CANCEL must not revoke the session")
	}
	server.Intercept = nil
	if _, err = h.client.Server().Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(server.Dials()) != 1 {
		t.Fatal("a stream reset must not reconnect")
	}
}

type blockingReconnect struct {
	transport.Transport
	calls   atomic.Int32
	started chan struct{}
	stopped chan struct{}
}

func (b *blockingReconnect) Dial(ctx context.Context, addr string) (transport.Connection, error) {
	if b.calls.Add(1) == 1 {
		return b.Transport.Dial(ctx, addr)
	}
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return nil, ctx.Err()
}
func TestCloseCancelsAnInProgressReconnect(t *testing.T) {
	server := newServer(fakeserver.Config{})
	tr := &blockingReconnect{Transport: server, started: make(chan struct{}), stopped: make(chan struct{})}
	h := connect(t, server, "kdoq://h", kdoq.ConnectOptions{Transport: tr})
	recv(t, server.Connected, "first connection").Lose()
	h.waitState(kdoq.StateConnected)
	h.waitState(kdoq.StateDisconnected)
	result := make(chan error, 1)
	go func() { _, err := h.client.Server().Health(context.Background()); result <- err }()
	closedWithin(t, tr.started, "reconnect to begin")
	if err := h.client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closedWithin(t, tr.stopped, "reconnect cancellation")
	if err := recv(t, result, "operation to stop"); err == nil {
		t.Fatal("operation succeeded on a closed client")
	}
}

type completionTransport struct {
	transport.Transport
	entered chan struct{}
	failure error
}
type completionConnection struct {
	transport.Connection
	entered chan struct{}
	failure error
}
type completionStream struct {
	transport.Stream
	entered chan struct{}
	aborted chan struct{}
	failure error
	once    sync.Once
}

func (t completionTransport) Dial(ctx context.Context, address string) (transport.Connection, error) {
	c, err := t.Transport.Dial(ctx, address)
	if err != nil {
		return nil, err
	}
	return completionConnection{c, t.entered, t.failure}, nil
}
func (c completionConnection) OpenOperation(ctx context.Context) (transport.Stream, error) {
	s, err := c.Connection.OpenOperation(ctx)
	if err != nil {
		return nil, err
	}
	return &completionStream{Stream: s, entered: c.entered, aborted: make(chan struct{}), failure: c.failure}, nil
}
func (s *completionStream) Close() error {
	close(s.entered)
	if s.failure != nil {
		return s.failure
	}
	<-s.aborted
	return context.Canceled
}
func (s *completionStream) Abort() error {
	s.once.Do(func() { close(s.aborted) })
	return s.Stream.Abort()
}
func TestCancellationDuringResponseCompletionIsNotSuccess(t *testing.T) {
	server := newServer(fakeserver.Config{})
	entered := make(chan struct{})
	h := connect(t, server, "kdoq://h", kdoq.ConnectOptions{Transport: completionTransport{Transport: server, entered: entered}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := h.client.Server().Health(ctx); result <- err }()
	closedWithin(t, entered, "native END before transport EOF")
	cancel()
	if err := recv(t, result, "operation cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Health = %v; want cancellation", err)
	}
}

type blockedControlTransport struct {
	transport.Transport
	blocked atomic.Bool
	entered chan struct{}
	once    sync.Once
}
type blockedControlConnection struct {
	transport.Connection
	owner *blockedControlTransport
}
type blockedControlStream struct {
	transport.Stream
	conn  transport.Connection
	owner *blockedControlTransport
}

func (t *blockedControlTransport) Dial(ctx context.Context, address string) (transport.Connection, error) {
	c, err := t.Transport.Dial(ctx, address)
	if err != nil {
		return nil, err
	}
	return blockedControlConnection{c, t}, nil
}
func (c blockedControlConnection) OpenControl(ctx context.Context) (transport.Stream, error) {
	s, err := c.Connection.OpenControl(ctx)
	if err != nil {
		return nil, err
	}
	return blockedControlStream{s, c.Connection, c.owner}, nil
}
func (s blockedControlStream) Write(p []byte) (int, error) {
	if !s.owner.blocked.Load() {
		return s.Stream.Write(p)
	}
	s.owner.once.Do(func() { close(s.owner.entered) })
	<-s.conn.Done()
	return 0, net.ErrClosed
}
func TestCancellationDoesNotWaitForBlockedControlWrites(t *testing.T) {
	server := newServer(fakeserver.Config{})
	tr := &blockedControlTransport{Transport: server, entered: make(chan struct{})}
	h := connect(t, server, "kdoq://h", kdoq.ConnectOptions{Transport: tr})
	received := make(chan struct{})
	server.Intercept = func(*fakeserver.Call) bool { close(received); return true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := h.client.Server().Health(ctx); result <- err }()
	closedWithin(t, received, "operation to begin")
	tr.blocked.Store(true)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Health = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for blocked control output")
	}
	closedWithin(t, tr.entered, "queued CANCEL")
	closeCtx, stop := context.WithCancel(context.Background())
	stop()
	_ = h.client.Close(closeCtx)
}

func TestTrailingResponseDataIsAProtocolError(t *testing.T) {
	server := newServer(fakeserver.Config{})
	tr := completionTransport{Transport: server, entered: make(chan struct{}), failure: transport.ErrTrailingData}
	h := connect(t, server, "kdoq://h", kdoq.ConnectOptions{Transport: tr})
	if _, err := h.client.Server().Health(context.Background()); !errors.Is(err, kdoq.ErrMalformed) {
		t.Fatalf("Health = %v; want Malformed", err)
	}
}
