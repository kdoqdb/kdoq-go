package kdoq_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

// seed stores n documents d00, d01, ... with a field n.
func seed(t *testing.T, c *kdoq.Collection[value.Doc], n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := kdoq.Key(fmt.Sprintf("d%02d", i))
		if _, err := c.Put(context.Background(), key, value.Doc{"n": int32(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func keysOf[T any](items []kdoq.Item[T]) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.Key)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sizes(reqs []fakeserver.QueryRequest) []string {
	var out []string
	for _, r := range reqs {
		s := "-"
		if r.PageSize != nil {
			s = fmt.Sprint(*r.PageSize)
		}
		out = append(out, fmt.Sprintf("%#x:%s", r.Operation, s))
	}
	return out
}

func TestFindPagesTransparentlyWithTheConfiguredPageSize(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 5)
	var got []string
	for item, err := range docs.Find(context.Background(), "n >= 0", kdoq.FindOptions{PageSize: 2}).All() {
		if err != nil {
			t.Fatal(err)
		}
		if item.Group != nil || item.Value["n"] == nil {
			t.Fatalf("item = %+v: a plain query yields the collection type with a plain key", item)
		}
		got = append(got, string(item.Key))
	}
	if want := []string{"d00", "d01", "d02", "d03", "d04"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	reqs := server.QueryRequests()
	if want := []string{"0x501:2", "0x503:2", "0x503:2"}; !equalStrings(sizes(reqs), want) {
		t.Fatalf("requests = %v, want %v", sizes(reqs), want)
	}
	if reqs[0].Filter != "n >= 0" || !reqs[0].HasFilter || reqs[0].Database != "shop" || reqs[0].Collection != "c" {
		t.Fatalf("create request = %+v", reqs[0])
	}
	if server.OpenCursors() != 0 {
		t.Fatal("an exhausted query must leave no cursor open")
	}
}

func TestFindUsesTheDefaultPageSizeAndCapsTheMaximum(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	for _, tc := range []struct {
		opts kdoq.FindOptions
		want string
	}{{kdoq.FindOptions{}, "0x501:1000"}, {kdoq.FindOptions{PageSize: 50000}, "0x501:10000"}} {
		if _, err := docs.Find(context.Background(), "", tc.opts).Collect(); err != nil {
			t.Fatal(err)
		}
		reqs := server.QueryRequests()
		if got := sizes(reqs[len(reqs)-1:])[0]; got != tc.want {
			t.Fatalf("page size sent = %s, want %s", got, tc.want)
		}
	}
	reqs := server.QueryRequests()
	if reqs[0].HasFilter {
		t.Fatal("an empty filter must be sent as no filter")
	}
}

func TestFindHonorsSkipAndLimitAtCreationOnly(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 8)
	items, err := docs.Find(context.Background(), "", kdoq.FindOptions{PageSize: 2, Skip: 1, Limit: 4}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"d01", "d02", "d03", "d04"}; !equalStrings(keysOf(items), want) {
		t.Fatalf("keys = %v, want %v", keysOf(items), want)
	}
	reqs := server.QueryRequests()
	if reqs[0].Skip != 1 || reqs[0].Limit != 4 {
		t.Fatalf("create window = %d/%d", reqs[0].Skip, reqs[0].Limit)
	}
	for _, r := range reqs[1:] {
		if r.HasWindow {
			t.Fatalf("a continuation carries a window: %+v", r)
		}
	}
}

func TestLeavingTheLoopCancelsTheQueryAndClosesItsCursor(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 5)
	q := docs.Find(context.Background(), "", kdoq.FindOptions{PageSize: 2})
	seen := 0
	for _, err := range q.All() {
		if err != nil {
			t.Fatal(err)
		}
		seen++
		if _, open := q.CursorID(); !open {
			t.Fatal("the cursor must be open while pages remain")
		}
		break
	}
	if seen != 1 {
		t.Fatalf("saw %d items", seen)
	}
	if n := server.OpenCursors(); n != 0 {
		t.Fatalf("%d cursors left open after leaving the loop", n)
	}
	if _, open := q.CursorID(); open {
		t.Fatal("a closed query reports no cursor")
	}
}

func TestCancellingTheContextBetweenItemsClosesTheCursor(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last error
	n := 0
	for _, err := range docs.Find(ctx, "", kdoq.FindOptions{PageSize: 2}).All() {
		if err != nil {
			last = err
			break
		}
		n++
		cancel()
	}
	if !errors.Is(last, context.Canceled) || n != 1 {
		t.Fatalf("got %d items and error %v, want 1 and context.Canceled", n, last)
	}
	if got := server.OpenCursors(); got != 0 {
		t.Fatalf("%d cursors left open after cancellation", got)
	}
}

func TestCancellingTheContextDuringAPageCancelsTheServerOperation(t *testing.T) {
	h, server := connectDocs(t)
	started := make(chan *fakeserver.Call, 1)
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation != 0x0501 {
			return false
		}
		started <- c
		<-c.Cancelled()
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := h.docs("c").Find(ctx, "").Collect()
		result <- err
	}()
	recv(t, started, "the query to start")
	cancel()
	if err := recv(t, result, "the query to give up"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := recv(t, server.Conns()[0].Cancels, "a CANCEL"); got == 0 {
		t.Fatal("the cancellation must name the request")
	}
}

func TestCollectAndValuesIterate(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 3)
	items, err := docs.Find(context.Background(), "", kdoq.FindOptions{PageSize: 2}).Collect()
	if err != nil || len(items) != 3 {
		t.Fatalf("Collect = %d items, %v", len(items), err)
	}
	var ns []int32
	for v, err := range docs.Find(context.Background(), "").Values() {
		if err != nil {
			t.Fatal(err)
		}
		ns = append(ns, v["n"].(int32))
	}
	if len(ns) != 3 || ns[0] != 0 || ns[2] != 2 {
		t.Fatalf("values = %v", ns)
	}
}

func TestFindDecodesIntoTheTypedCollectionAndFillsTheKeyField(t *testing.T) {
	h, _ := connectDocs(t)
	c := users(t, h)
	ctx := context.Background()
	for _, u := range []user{{Name: "Ann", Age: 31}, {Name: "Bob", Age: 40}} {
		if _, err := c.Put(ctx, kdoq.Key("u-"+u.Name), u); err != nil {
			t.Fatal(err)
		}
	}
	items, err := c.Find(ctx, "").Collect()
	if err != nil || len(items) != 2 {
		t.Fatalf("Collect = %v, %v", items, err)
	}
	if want := (user{ID: "u-Ann", Name: "Ann", Age: 31}); items[0].Value != want || items[0].Key != "u-Ann" {
		t.Fatalf("first = %+v, want %+v", items[0], want)
	}
}

func TestAQueryIsIteratedOnce(t *testing.T) {
	h, _ := connectDocs(t)
	q := h.docs("c").Find(context.Background(), "")
	if _, err := q.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Collect(); err == nil {
		t.Fatal("iterating a query twice must fail")
	}
}

func TestCountUsesTheMetadataCountWithoutACursor(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 4)
	n, err := docs.Count(context.Background(), "n > 0")
	if err != nil || n != 4 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	reqs := server.QueryRequests()
	if len(reqs) != 1 || reqs[0].Operation != 0x0502 || reqs[0].Filter != "n > 0" || server.OpenCursors() != 0 {
		t.Fatalf("requests = %+v", reqs)
	}
	if _, err := docs.Count(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFailuresMapToTheDocumentedCodes(t *testing.T) {
	cases := map[uint16]error{
		10: kdoq.ErrLimitExceeded, 21: kdoq.ErrWouldWait, 22: kdoq.ErrMemoryWaitTimeout,
		23: kdoq.ErrExecutionMemoryLimit, 24: kdoq.ErrQueryQueueFull, 25: kdoq.ErrSendStalled, 9: kdoq.ErrInvalidDocument,
	}
	for code, sentinel := range cases {
		h, server := connectDocs(t)
		server.Intercept = func(c *fakeserver.Call) bool { c.Fail(code, false); return true }
		_, err := h.docs("c").Find(context.Background(), "x").Collect()
		if !errors.Is(err, sentinel) {
			t.Fatalf("code %d: Find error = %v", code, err)
		}
		if _, err := h.docs("c").Count(context.Background(), "x"); !errors.Is(err, sentinel) {
			t.Fatalf("code %d: Count error = %v", code, err)
		}
	}
}

func TestOverloadedQueryCreationIsRetriedBeforeAnyItem(t *testing.T) {
	h, server := connectDocs(t)
	seed(t, h.docs("c"), 2)
	refusals := 0
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation == 0x0501 && refusals < 2 {
			refusals++
			c.Fail(7, false)
			return true
		}
		return false
	}
	items, err := h.docs("c").Find(context.Background(), "").Collect()
	if err != nil || len(items) != 2 || refusals != 2 {
		t.Fatalf("Collect = %d items, %v after %d refusals", len(items), err, refusals)
	}
}
