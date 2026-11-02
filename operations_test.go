package kdoq_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

var sentinels = map[uint16]error{
	1: kdoq.ErrMalformed, 2: kdoq.ErrIncompatible, 3: kdoq.ErrAuthentication, 4: kdoq.ErrNotReady,
	5: kdoq.ErrUnsupported, 6: kdoq.ErrNotFound, 7: kdoq.ErrOverloaded, 8: kdoq.ErrRevoked,
	9: kdoq.ErrInvalidDocument, 10: kdoq.ErrLimitExceeded, 11: kdoq.ErrAlreadyExists, 12: kdoq.ErrUnavailable,
	13: kdoq.ErrConflict, 14: kdoq.ErrCancelled, 15: kdoq.ErrStorage, 16: kdoq.ErrWrongCase, 17: kdoq.ErrUnloaded,
	18: kdoq.ErrCursorInUse, 19: kdoq.ErrCursorExpired, 20: kdoq.ErrAdmissionTimeout, 21: kdoq.ErrWouldWait,
	22: kdoq.ErrMemoryWaitTimeout, 23: kdoq.ErrExecutionMemoryLimit, 24: kdoq.ErrQueryQueueFull,
	25: kdoq.ErrSendStalled, 26: kdoq.ErrForbidden,
}

func TestEveryServerErrorCodeMatchesItsSentinelOnly(t *testing.T) {
	// Each code gets its own client: Authentication and Revoked close it.
	for want := uint16(1); want <= 26; want++ {
		h, server := connectDocs(t, kdoq.ConnectOptions{Retry: &kdoq.RetryPolicy{Attempts: 1}})
		server.Intercept = func(c *fakeserver.Call) bool {
			c.Fail(want, false)
			return true
		}
		_, err := h.docs("c").Get(context.Background(), "k")
		for code, sentinel := range sentinels {
			if got := errors.Is(err, sentinel); got != (code == want) {
				t.Fatalf("code %d: errors.Is(%v) = %v", want, sentinel, got)
			}
		}
		var e *kdoq.Error
		if !errors.As(err, &e) || e.Code != kdoq.ErrorCode(want) {
			t.Fatalf("code %d: error = %+v", want, e)
		}
		if e.Code.String() == "" || e.Error() == "" {
			t.Fatalf("code %d has no text", want)
		}
	}
}

func TestErrorsCarryCodeOutcomeAndRequestID(t *testing.T) {
	h, server := connectDocs(t)
	var requestID atomic.Uint64
	server.Intercept = func(c *fakeserver.Call) bool {
		requestID.Store(c.RequestID)
		c.Fail(15, true)
		return true
	}
	_, err := h.docs("c").Put(context.Background(), "k", value.Doc{"a": int32(1)})
	var e *kdoq.Error
	if !errors.As(err, &e) || !errors.Is(err, kdoq.ErrStorage) {
		t.Fatalf("error = %v", err)
	}
	if e.Code != kdoq.CodeStorage || e.Outcome != kdoq.OutcomeUnknown || e.RequestID != requestID.Load() || e.RequestID == 0 {
		t.Fatalf("error = %+v, request %d", e, requestID.Load())
	}
}

func TestContextDeadlineIsTheStandardDeadlineError(t *testing.T) {
	h, server := connectDocs(t)
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err := h.docs("c").Get(expired, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get error = %v, want context.DeadlineExceeded", err)
	}
	_, err = h.docs("c").Put(expired, "k", value.Doc{"a": int32(1)})
	var e *kdoq.Error
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &e) || e.Outcome != kdoq.OutcomeNotApplied {
		t.Fatalf("Put error = %v; nothing was sent, so the outcome is NotApplied", err)
	}
	if calls != 0 {
		t.Fatalf("%d operations reached the server with an expired context", calls)
	}
}

func TestOperationTimeoutAppliesOnlyWithoutADeadline(t *testing.T) {
	h, _ := connectDocs(t, kdoq.ConnectOptions{OperationTimeout: time.Nanosecond})
	docs := h.docs("c")
	if _, err := docs.Get(context.Background(), "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a context without a deadline must get the operation timeout: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), eventually)
	defer cancel()
	if _, err := docs.Put(ctx, "k", value.Doc{"a": int32(1)}); err != nil {
		t.Fatalf("the context deadline must override the operation timeout: %v", err)
	}
}

func TestCancellingTheContextCancelsTheServerOperation(t *testing.T) {
	for _, write := range []bool{false, true} {
		name := map[bool]string{false: "read", true: "write"}[write]
		t.Run(name, func(t *testing.T) {
			h, server := connectDocs(t)
			started := make(chan *fakeserver.Call, 1)
			server.Intercept = func(c *fakeserver.Call) bool {
				started <- c
				<-c.Cancelled()
				return true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				var err error
				if write {
					_, err = h.docs("c").Put(ctx, "k", value.Doc{"a": int32(1)})
				} else {
					_, err = h.docs("c").Get(ctx, "k")
				}
				result <- err
			}()
			call := recv(t, started, "the operation to reach the server")
			cancel()
			err := recv(t, result, "the cancelled operation to return")
			var e *kdoq.Error
			if !errors.Is(err, context.Canceled) || !errors.As(err, &e) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			wantOutcome := map[bool]kdoq.Outcome{false: kdoq.OutcomeNotApplied, true: kdoq.OutcomeUnknown}[write]
			if e.Outcome != wantOutcome || e.RequestID != call.RequestID {
				t.Fatalf("error = %+v, want outcome %v and request %d", e, wantOutcome, call.RequestID)
			}
			if got := recv(t, call.Conn.Cancels, "CANCEL on the control stream"); got != call.RequestID {
				t.Fatalf("CANCEL named request %d, want %d", got, call.RequestID)
			}
		})
	}
}

func TestAWriteLostInFlightHasAnUnknownOutcome(t *testing.T) {
	cases := []struct {
		name     string
		lose     func(*fakeserver.Call)
		incident any
	}{
		{"connection lost", func(c *fakeserver.Call) { c.Conn.Lose() }, new(*kdoq.ConnectionError)},
		{"stream ended", func(c *fakeserver.Call) { c.Truncate() }, new(*kdoq.IncompleteError)},
		{"stream reset", func(c *fakeserver.Call) { c.Reset(25) }, new(*kdoq.IncompleteError)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, server := connectDocs(t)
			calls := 0
			server.Intercept = func(call *fakeserver.Call) bool { calls++; c.lose(call); return true }
			ctx := context.Background()
			_, werr := h.docs("c").Put(ctx, "k", value.Doc{"a": int32(1)})
			_, rerr := h.docs("c").Get(ctx, "k")
			check := func(err error) (kdoq.Outcome, uint64) {
				t.Helper()
				switch target := c.incident.(type) {
				case **kdoq.ConnectionError:
					var e *kdoq.ConnectionError
					if !errors.As(err, &e) {
						t.Fatalf("error = %v, want a *ConnectionError", err)
					}
					return e.Outcome, e.RequestID
				case **kdoq.IncompleteError:
					var e *kdoq.IncompleteError
					if !errors.As(err, &e) {
						t.Fatalf("error = %v, want an *IncompleteError", err)
					}
					if c.name == "stream reset" && (!e.Reset || e.ResetCode != 25) {
						t.Fatalf("reset = %v/%d, want code 25", e.Reset, e.ResetCode)
					}
					return e.Outcome, e.RequestID
				default:
					t.Fatalf("unexpected target %T", target)
				}
				return 0, 0
			}
			if outcome, id := check(werr); outcome != kdoq.OutcomeUnknown || id == 0 {
				t.Fatalf("write: outcome %v, request %d", outcome, id)
			}
			if outcome, _ := check(rerr); outcome != kdoq.OutcomeNotApplied {
				t.Fatalf("read: outcome %v, want NotApplied", outcome)
			}
			if calls != 2 {
				t.Fatalf("%d calls: a lost response must never be retried", calls)
			}
		})
	}
}

func TestOverloadAndAdmissionTimeoutAreRetried(t *testing.T) {
	for _, code := range []uint16{7, 20} {
		h, server := connectDocs(t)
		var calls atomic.Int32
		server.Intercept = func(c *fakeserver.Call) bool {
			if calls.Add(1) <= 2 {
				c.Fail(code, false)
				return true
			}
			return false
		}
		if _, err := h.docs("c").Put(context.Background(), "k", value.Doc{"a": int32(1)}); err != nil {
			t.Fatalf("code %d: Put = %v", code, err)
		}
		if calls.Load() != 3 {
			t.Fatalf("code %d: %d attempts, want 3", code, calls.Load())
		}
	}
}

func TestRetriesStopAtTheConfiguredAttempts(t *testing.T) {
	h, server := connectDocs(t, kdoq.ConnectOptions{Retry: &kdoq.RetryPolicy{
		Attempts: 4, Backoff: kdoq.Backoff{Initial: time.Nanosecond, NoJitter: true},
	}})
	var calls atomic.Int32
	server.Intercept = func(c *fakeserver.Call) bool { calls.Add(1); c.Fail(7, false); return true }
	_, err := h.docs("c").Get(context.Background(), "k")
	if !errors.Is(err, kdoq.ErrOverloaded) || calls.Load() != 4 {
		t.Fatalf("error = %v after %d calls, want ErrOverloaded after 4", err, calls.Load())
	}
}

func TestRetriesCanBeDisabled(t *testing.T) {
	h, server := connectDocs(t, kdoq.ConnectOptions{Retry: &kdoq.RetryPolicy{Attempts: 1}})
	var calls atomic.Int32
	server.Intercept = func(c *fakeserver.Call) bool { calls.Add(1); c.Fail(7, false); return true }
	if _, err := h.docs("c").Get(context.Background(), "k"); !errors.Is(err, kdoq.ErrOverloaded) || calls.Load() != 1 {
		t.Fatalf("error = %v after %d calls, want one", err, calls.Load())
	}
}

func TestOtherErrorsAreNotRetried(t *testing.T) {
	for _, code := range []uint16{1, 6, 9, 10, 11, 12, 13, 15, 21, 22, 23, 24, 25, 26} {
		h, server := connectDocs(t)
		var calls atomic.Int32
		server.Intercept = func(c *fakeserver.Call) bool { calls.Add(1); c.Fail(code, false); return true }
		if _, err := h.docs("c").Get(context.Background(), "k"); err == nil || calls.Load() != 1 {
			t.Fatalf("code %d: %d calls, want exactly one", code, calls.Load())
		}
	}
}

func TestRetriesStayWithinTheDeadline(t *testing.T) {
	// The backoff is an hour: only the context can end the wait.
	h, server := connectDocs(t, kdoq.ConnectOptions{Retry: &kdoq.RetryPolicy{
		Attempts: 100, Backoff: kdoq.Backoff{Initial: time.Hour, Max: time.Hour, NoJitter: true},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan struct{}, 1)
	var calls atomic.Int32
	server.Intercept = func(c *fakeserver.Call) bool {
		calls.Add(1)
		c.Fail(7, false)
		failed <- struct{}{}
		return true
	}
	result := make(chan error, 1)
	go func() {
		_, err := h.docs("c").Get(ctx, "k")
		result <- err
	}()
	recv(t, failed, "the first refusal")
	cancel()
	err := recv(t, result, "the operation to give up")
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("error = %v after %d calls, want context.Canceled after one", err, calls.Load())
	}
}

func TestAccessLevelFailuresAreForbidden(t *testing.T) {
	level := uint8(1)
	server := newServer(fakeserver.Config{Level: &level})
	h := connect(t, server, "kdoq://h/shop")
	_, _, ok := h.client.Session()
	if _, level, _ := h.client.Session(); !ok || level != kdoq.AccessRead {
		t.Fatalf("level = %v", level)
	}
	ctx := context.Background()
	if _, err := h.docs("c").Put(ctx, "k", value.Doc{"a": int32(1)}); !errors.Is(err, kdoq.ErrForbidden) {
		t.Fatalf("Put at Read level = %v, want ErrForbidden", err)
	}
	if _, err := h.docs("c").Get(ctx, "k"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get at Read level = %v, want to reach the document", err)
	}
	if h.client.State() != kdoq.StateConnected {
		t.Fatal("a forbidden operation must not disturb the session")
	}
}

func TestAnOperationTheServerDidNotNegotiateFailsBeforeAStreamOpens(t *testing.T) {
	server := newServer(fakeserver.Config{Capabilities: 1 | 8 | 16})
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	h := connect(t, server, "kdoq://h/shop", kdoq.ConnectOptions{
		Capabilities: kdoq.Capabilities{Required: kdoq.CapQueries, Optional: kdoq.CapDocuments},
	})
	if h.client.Capabilities().Has(kdoq.CapDocuments) {
		t.Fatal("documents were not offered by the server")
	}
	_, err := h.docs("c").Get(context.Background(), "k")
	if !errors.Is(err, kdoq.ErrUnsupported) || calls != 0 {
		t.Fatalf("Get = %v after %d calls; want ErrUnsupported without a stream", err, calls)
	}
}

func TestNegotiatedCapabilitiesAreReported(t *testing.T) {
	h, _ := connectDocs(t)
	want := kdoq.CapAdministration | kdoq.CapDocuments | kdoq.CapQueries | kdoq.CapMutations
	if got := h.client.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %d, want %d", got, want)
	}
}

func TestConcurrentOperationsSurviveMixedFailures(t *testing.T) {
	h, server := connectDocs(t)
	var refused sync.Map
	server.Intercept = func(c *fakeserver.Call) bool {
		key, _ := c.Payload.Bytes(3)
		if _, again := refused.LoadOrStore(string(key), true); !again {
			c.Fail(7, false) // every key is refused once, then served
			return true
		}
		return false
	}
	docs := h.docs("c")
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := kdoq.Key(string(rune('a' + i)))
			if _, err := docs.Put(context.Background(), key, value.Doc{"i": int32(i)}); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestAnOverloadRefusalWithAnUnknownOutcomeIsNotRetried(t *testing.T) {
	h, server := connectDocs(t)
	var calls atomic.Int32
	server.Intercept = func(c *fakeserver.Call) bool { calls.Add(1); c.Fail(7, true); return true }
	_, err := h.docs("c").Put(context.Background(), "k", value.Doc{"a": int32(1)})
	if !errors.Is(err, kdoq.ErrOverloaded) || calls.Load() != 1 {
		t.Fatalf("error = %v after %d calls, want ErrOverloaded after one", err, calls.Load())
	}
}
