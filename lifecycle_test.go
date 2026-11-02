package kdoq_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/internal/transport"
	"github.com/kdoqdb/kdoq-go/value"
)

func put(h *harness, key string) error {
	_, err := h.docs("c").Put(context.Background(), kdoq.Key(key), value.Doc{"k": key})
	return err
}

func TestALostConnectionIsNotRetriedButTheNextOperationReconnects(t *testing.T) {
	h, server := connectDocs(t)
	first := recv(t, server.Connected, "the first connection")
	h.waitState(kdoq.StateConnected)
	first.Lose()

	ev := h.waitState(kdoq.StateDisconnected)
	var lost *kdoq.ConnectionError
	if !errors.As(ev.err, &lost) {
		t.Fatalf("the callback error = %v, want a *ConnectionError", ev.err)
	}
	if _, _, ok := h.client.Session(); ok || h.client.State() != kdoq.StateDisconnected {
		t.Fatal("a lost connection must report no session and the Disconnected state")
	}
	if n := len(server.Dials()); n != 1 {
		t.Fatalf("%d dials: reconnecting must wait for the next operation", n)
	}

	if err := put(h, "after"); err != nil {
		t.Fatalf("the next operation must reconnect: %v", err)
	}
	if n := len(server.Dials()); n != 2 {
		t.Fatalf("%d dials, want 2", n)
	}
	h.waitState(kdoq.StateConnected)
	if _, _, ok := h.client.Session(); !ok || h.client.State() != kdoq.StateConnected {
		t.Fatal("the client must be connected again")
	}
}

func TestConcurrentOperationsShareOneReconnect(t *testing.T) {
	h, server := connectDocs(t)
	recv(t, server.Connected, "the first connection").Lose()
	h.waitState(kdoq.StateDisconnected)

	release := make(chan struct{})
	var dials atomic.Int32
	server.DialHook = func(transport.DialConfig) error {
		dials.Add(1)
		<-release
		return nil
	}
	const workers = 8
	var ready, done sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			if err := put(h, string(rune('a'+i))); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	ready.Wait()
	close(release)
	done.Wait()
	if got := dials.Load(); got != 1 {
		t.Fatalf("%d reconnect attempts for %d concurrent operations, want 1", got, workers)
	}
}

func TestAFailedReconnectFailsOnlyTheOperationsWaitingForIt(t *testing.T) {
	h, server := connectDocs(t)
	recv(t, server.Connected, "the first connection").Lose()
	h.waitState(kdoq.StateDisconnected)

	release := make(chan struct{})
	var dials atomic.Int32
	server.DialHook = func(transport.DialConfig) error {
		if dials.Add(1) == 1 {
			<-release
			return errors.New("the network is down")
		}
		return nil
	}
	const workers = 6
	var ready, done sync.WaitGroup
	var failures atomic.Int32
	ready.Add(workers)
	for i := range workers {
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			err := put(h, string(rune('a'+i)))
			var conn *kdoq.ConnectionError
			switch {
			case err == nil: // arrived after the failed attempt and reconnected itself
			case errors.As(err, &conn):
				failures.Add(1)
			default:
				t.Errorf("Put: %v", err)
			}
		}()
	}
	ready.Wait()
	close(release)
	done.Wait()
	if failures.Load() == 0 {
		t.Fatal("the operation that started the failed attempt must fail")
	}
	if h.client.State() == kdoq.StateClosed {
		t.Fatal("a failed connection attempt must not close the client")
	}
	if err := put(h, "later"); err != nil {
		t.Fatalf("the next attempt must succeed: %v", err)
	}
}

func TestNoReconnectKeepsALostConnectionLost(t *testing.T) {
	h, server := connectDocs(t, kdoq.ConnectOptions{NoReconnect: true})
	recv(t, server.Connected, "the connection").Lose()
	h.waitState(kdoq.StateDisconnected)
	var conn *kdoq.ConnectionError
	if err := put(h, "k"); !errors.As(err, &conn) {
		t.Fatalf("Put = %v, want a *ConnectionError", err)
	}
	if n := len(server.Dials()); n != 1 {
		t.Fatalf("%d dials with NoReconnect", n)
	}
}

func TestAuthenticationFailureOnReconnectClosesTheClient(t *testing.T) {
	server := newServer(fakeserver.Config{Users: map[string]string{"ann": "secret"}})
	h := connect(t, server, "kdoq://ann:secret@h/shop")
	recv(t, server.Connected, "the connection").Lose()
	h.waitState(kdoq.StateDisconnected)
	server.SetPassword("ann", "changed")

	if err := put(h, "k"); !errors.Is(err, kdoq.ErrAuthentication) {
		t.Fatalf("Put = %v, want ErrAuthentication", err)
	}
	closedWithin(t, h.client.Closed(), "the client to close")
	if !errors.Is(h.client.Err(), kdoq.ErrAuthentication) || h.client.State() != kdoq.StateClosed {
		t.Fatalf("Err() = %v, State() = %v", h.client.Err(), h.client.State())
	}
	if ev := h.waitState(kdoq.StateClosed); !errors.Is(ev.err, kdoq.ErrAuthentication) {
		t.Fatalf("the callback error = %v", ev.err)
	}
	dials := len(server.Dials())
	if err := put(h, "again"); !errors.Is(err, kdoq.ErrAuthentication) {
		t.Fatalf("a closed client keeps returning its failure: %v", err)
	}
	if len(server.Dials()) != dials {
		t.Fatal("a closed client must not dial again")
	}
}

func TestRevocationClosesTheClient(t *testing.T) {
	h, server := connectDocs(t)
	conn := recv(t, server.Connected, "the connection")
	conn.Revoke()
	closedWithin(t, h.client.Closed(), "the client to close")
	if !errors.Is(h.client.Err(), kdoq.ErrRevoked) {
		t.Fatalf("Err() = %v, want ErrRevoked", h.client.Err())
	}
	if ev := h.waitState(kdoq.StateClosed); !errors.Is(ev.err, kdoq.ErrRevoked) {
		t.Fatalf("the callback error = %v", ev.err)
	}
	if err := put(h, "k"); !errors.Is(err, kdoq.ErrRevoked) {
		t.Fatalf("Put after revocation = %v", err)
	}
	if n := len(server.Dials()); n != 1 {
		t.Fatalf("%d dials: a revoked client must not reconnect", n)
	}
}

func TestRevocationDuringAnOperationFailsItWithRevoked(t *testing.T) {
	h, server := connectDocs(t)
	server.Intercept = func(c *fakeserver.Call) bool {
		c.Conn.Revoke()
		return true
	}
	err := put(h, "k")
	if !errors.Is(err, kdoq.ErrRevoked) {
		t.Fatalf("Put = %v, want ErrRevoked", err)
	}
	closedWithin(t, h.client.Closed(), "the client to close")
}

func TestStateChangesReachTheCallbackInOrder(t *testing.T) {
	h, server := connectDocs(t)
	recv(t, server.Connected, "the connection").Lose()
	var seen []kdoq.State
	next := func() kdoq.State { return recv(t, h.states, "a state change").state }
	seen = append(seen, next(), next()) // Connected, then Disconnected
	if err := put(h, "k"); err != nil {
		t.Fatal(err)
	}
	seen = append(seen, next())
	if err := h.client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen = append(seen, next())
	want := []kdoq.State{kdoq.StateConnected, kdoq.StateDisconnected, kdoq.StateConnected, kdoq.StateClosed}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("states = %v, want %v", seen, want)
		}
	}
}

func TestCloseLogsOutAndIsIdempotent(t *testing.T) {
	h, server := connectDocs(t)
	conn := recv(t, server.Connected, "the connection")
	if err := h.client.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closedWithin(t, h.client.Closed(), "Closed()")
	if h.client.Err() != nil || h.client.State() != kdoq.StateClosed {
		t.Fatalf("Err() = %v, State() = %v after a normal Close", h.client.Err(), h.client.State())
	}
	closedWithin(t, conn.Done(), "the server to see the connection end")
	if err := h.client.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	var e *kdoq.ConnectionError
	if err := put(h, "k"); !errors.As(err, &e) {
		t.Fatalf("Put after Close = %v, want a *ConnectionError", err)
	}
	if n := len(server.Dials()); n != 1 {
		t.Fatalf("%d dials: a closed client must not reconnect", n)
	}
}

func TestCloseOfADisconnectedClientNeedsNoServer(t *testing.T) {
	h, server := connectDocs(t)
	recv(t, server.Connected, "the connection").Lose()
	h.waitState(kdoq.StateDisconnected)
	if err := h.client.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closedWithin(t, h.client.Closed(), "Closed()")
}

func TestASlowStateCallbackNeverBlocksTheClient(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 8)
	server := newServer(fakeserver.Config{})
	client, err := kdoq.Connect(context.Background(), "kdoq://h/shop",
		kdoq.WithTransport(server),
		kdoq.ConnectOptions{OnState: func(kdoq.State, error) { entered <- struct{}{}; <-block }},
	)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "the callback to start")
	db, _ := client.DB("shop")
	if _, err := db.Docs("c").Put(context.Background(), "k", value.Doc{"a": int32(1)}); err != nil {
		t.Fatalf("Put while the callback blocks: %v", err)
	}
	close(block)
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
