package kdoq_test

import (
	"context"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

// minimalRetry keeps retries instant: the backoff is injected, never slept.
var minimalRetry = &kdoq.RetryPolicy{Attempts: 3, Backoff: kdoq.Backoff{Initial: time.Nanosecond, Max: time.Nanosecond, NoJitter: true}}

// eventually bounds how long a test waits for an event the code under test
// produces by itself; it is a failure bound, not an expected wait.
const eventually = 10 * time.Second

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(eventually):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func closedWithin(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(eventually):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// harness is a client connected to a fake server.
type harness struct {
	t      *testing.T
	server *fakeserver.Server
	client *kdoq.Client
	states chan stateChange
}

type stateChange struct {
	state kdoq.State
	err   error
}

func newServer(cfg fakeserver.Config) *fakeserver.Server { return fakeserver.New(cfg) }

// connect connects a client with fast retries to the server.
func connect(t *testing.T, server *fakeserver.Server, dsn string, opts ...kdoq.ConnectOption) *harness {
	t.Helper()
	h := &harness{t: t, server: server, states: make(chan stateChange, 64)}
	all := append([]kdoq.ConnectOption{
		kdoq.WithTransport(server),
		kdoq.ConnectOptions{
			Retry: minimalRetry,
			OnState: func(s kdoq.State, err error) {
				h.states <- stateChange{s, err}
			},
		},
	}, opts...)
	client, err := kdoq.Connect(context.Background(), dsn, all...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	h.client = client
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), eventually)
		defer cancel()
		_ = client.Close(ctx)
	})
	return h
}

func (h *harness) waitState(want kdoq.State) stateChange {
	h.t.Helper()
	for {
		ev := recv(h.t, h.states, "state "+want.String())
		if ev.state == want {
			return ev
		}
	}
}

func (h *harness) docs(name string) *kdoq.Collection[value.Doc] {
	db, err := h.client.DB("")
	if err != nil {
		h.t.Fatal(err)
	}
	return db.Docs(name)
}
