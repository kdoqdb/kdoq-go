package kdoq_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

func TestManualCursorReadsPageByPageAndClosesIdempotently(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 5)
	ctx := context.Background()
	cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID() == 0 || server.OpenCursors() != 1 {
		t.Fatalf("cursor %d, %d open: creating a cursor reads nothing yet", cur.ID(), server.OpenCursors())
	}
	var pages [][]string
	for {
		page, err := cur.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, keysOf(page.Items))
		if page.Done {
			break
		}
	}
	want := [][]string{{"d00", "d01"}, {"d02", "d03"}, {"d04"}}
	if len(pages) != 3 || !equalStrings(pages[0], want[0]) || !equalStrings(pages[1], want[1]) || !equalStrings(pages[2], want[2]) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	if page, err := cur.Next(ctx); err != nil || !page.Done || len(page.Items) != 0 {
		t.Fatalf("Next after the end = %+v, %v", page, err)
	}
	if err := cur.Close(ctx); err != nil || cur.Close(ctx) != nil {
		t.Fatal("Close must be idempotent")
	}
	if server.OpenCursors() != 0 {
		t.Fatal("an exhausted cursor must be closed")
	}
}

func TestCursorNextSizeOverridesThePageSizeAndCloseReleasesTheCursor(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 6)
	ctx := context.Background()
	cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	page, err := cur.Next(ctx, kdoq.NextOptions{Size: 3})
	if err != nil || len(page.Items) != 3 || page.Done {
		t.Fatalf("Next(3) = %d items, done %v, %v", len(page.Items), page.Done, err)
	}
	if err := cur.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if server.OpenCursors() != 0 {
		t.Fatal("Close must close the server cursor")
	}
	if page, err := cur.Next(ctx); err != nil || !page.Done || len(page.Items) != 0 {
		t.Fatalf("Next after Close = %+v, %v", page, err)
	}
	if cur.Close(ctx) != nil {
		t.Fatal("a second Close must succeed")
	}
	reqs := server.QueryRequests()
	if got := sizes(reqs); got[0] != "0x501:0" || got[1] != "0x503:3" {
		t.Fatalf("requests = %v: the cursor is created empty and read by page", got)
	}
}

func twoClients(t *testing.T) (*harness, *harness, *fakeserver.Server) {
	t.Helper()
	server := newServer(fakeserver.Config{Users: map[string]string{"ann": "pw", "bob": "pw"}})
	return connect(t, server, "kdoq://ann:pw@h/shop"), connect(t, server, "kdoq://ann:pw@h/shop"), server
}

func TestAttachingACursorContinuesItFromAnotherConnectionOfTheSameIdentity(t *testing.T) {
	first, second, _ := twoClients(t)
	docs := first.docs("c")
	seed(t, docs, 4)
	ctx := context.Background()
	cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	page, err := cur.Next(ctx)
	if err != nil || !equalStrings(keysOf(page.Items), []string{"d00", "d01"}) {
		t.Fatalf("first page = %v, %v", keysOf(page.Items), err)
	}
	attached := second.client.AttachCursor[value.Doc](cur.ID())
	rest, err := attached.Next(ctx)
	if err != nil || !rest.Done || !equalStrings(keysOf(rest.Items), []string{"d02", "d03"}) {
		t.Fatalf("attached page = %v done %v, %v", keysOf(rest.Items), rest.Done, err)
	}
}

func TestAnotherIdentityCannotAttachACursor(t *testing.T) {
	first, _, server := twoClients(t)
	docs := first.docs("c")
	seed(t, docs, 2)
	cur, err := docs.FindCursor(context.Background(), "", kdoq.FindOptions{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	bob := connect(t, server, "kdoq://bob:pw@h/shop")
	_, err = bob.client.AttachCursor[value.Doc](cur.ID()).Next(context.Background())
	if !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Next on a foreign cursor = %v, want NotFound", err)
	}
}

func TestAttachingWithTheWrongResultTypeIsADecodingErrorFromNext(t *testing.T) {
	first, second, _ := twoClients(t)
	docs := first.docs("c")
	seed(t, docs, 2)
	ctx := context.Background()
	cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.client.AttachCursor[int64](cur.ID()).Next(ctx)
	var decoding *value.Error
	if !errors.As(err, &decoding) {
		t.Fatalf("Next = %v, want a decoding error (*value.Error)", err)
	}
}

func TestAnAggregateCursorDecodesIntoItsOwnType(t *testing.T) {
	h, server := connectDocs(t)
	server.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
		return fakeserver.QueryResult{Items: []fakeserver.Result{{Value: int64(7)}, {Value: int64(8)}}}
	}
	ctx := context.Background()
	cur, err := h.docs("c").AggregateCursor[int64](ctx, "", []kdoq.PipelineStage{kdoq.Count()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := cur.Next(ctx)
	if err != nil || len(page.Items) != 2 || page.Items[1].Value != 8 || !page.Done {
		t.Fatalf("Next = %+v, %v", page, err)
	}
}

func TestAnAutoPaginatedReadContinuesTheSameCursorAfterAReconnect(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 5)
	first := recv(t, server.Connected, "the first connection")
	var keys []string
	n := 0
	for item, err := range docs.Find(context.Background(), "", kdoq.FindOptions{PageSize: 2}).All() {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(item.Key))
		n++
		if n == 2 {
			first.Lose()
			h.waitState(kdoq.StateDisconnected)
		}
	}
	if want := []string{"d00", "d01", "d02", "d03", "d04"}; !equalStrings(keys, want) {
		t.Fatalf("keys = %v, want %v: the same cursor continues, nothing repeats", keys, want)
	}
	if got := len(server.Dials()); got != 2 {
		t.Fatalf("%d dials, want 2", got)
	}
	if got := sizes(server.QueryRequests()); len(got) != 3 || got[0] != "0x501:2" {
		t.Fatalf("requests = %v: no second QUERY_CREATE", got)
	}
}

func TestALostConnectionInsideAPageIsTheIncompleteStreamError(t *testing.T) {
	for name, cut := range map[string]func(*fakeserver.Call){
		"truncated": func(c *fakeserver.Call) { c.Truncate() },
		"reset":     func(c *fakeserver.Call) { c.Reset(25) },
	} {
		t.Run(name, func(t *testing.T) {
			h, server := connectDocs(t)
			seed(t, h.docs("c"), 4)
			server.Intercept = func(c *fakeserver.Call) bool {
				if c.Operation != 0x0503 {
					return false
				}
				c.Begin(nil)
				c.WriteResults(1, []fakeserver.Result{{Key: []byte("x"), Value: value.Doc{"n": int32(1)}}})
				cut(c)
				return true
			}
			var got []string
			var last error
			for item, err := range h.docs("c").Find(context.Background(), "", kdoq.FindOptions{PageSize: 2}).All() {
				if err != nil {
					last = err
					break
				}
				got = append(got, string(item.Key))
			}
			var incomplete *kdoq.IncompleteError
			if !errors.As(last, &incomplete) {
				t.Fatalf("error = %v, want *kdoq.IncompleteError", last)
			}
			if name == "reset" && (!incomplete.Reset || incomplete.ResetCode != 25) {
				t.Fatalf("incomplete = %+v", incomplete)
			}
			if !equalStrings(got, []string{"d00", "d01"}) {
				t.Fatalf("items = %v: only the first, complete page is delivered", got)
			}
			if n := server.OpenCursors(); n != 0 {
				t.Fatalf("%d cursors left open after the incomplete page", n)
			}
		})
	}
}

func TestACancelledCursorReadClosesTheCursorAndLaterReadsKeepFailing(t *testing.T) {
	h, server := connectDocs(t)
	seed(t, h.docs("c"), 4)
	cur, err := h.docs("c").FindCursor(context.Background(), "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation != 0x0503 {
			return false
		}
		started <- struct{}{}
		<-c.Cancelled()
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := cur.Next(ctx)
		result <- err
	}()
	recv(t, started, "the read to start")
	cancel()
	if err := recv(t, result, "the read to give up"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next = %v, want context.Canceled", err)
	}
	if server.OpenCursors() != 0 {
		t.Fatal("a cancelled read must close the cursor")
	}
	if page, err := cur.Next(context.Background()); !errors.Is(err, context.Canceled) || page.Done {
		t.Fatalf("a later Next = %+v, %v: it must not look like a clean end", page, err)
	}
}

func TestAnIncompleteCreationPageClosesTheCursorItNamed(t *testing.T) {
	h, server := connectDocs(t)
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation != 0x0501 {
			return false
		}
		c.Begin(new(wire.Builder).U64(1, 777).U64(2, 777))
		c.Truncate()
		return true
	}
	_, err := h.docs("c").Find(context.Background(), "").Collect()
	var incomplete *kdoq.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *kdoq.IncompleteError", err)
	}
	if got := server.ClosedCursors(); len(got) != 1 || got[0] != 777 {
		t.Fatalf("closed cursors = %v, want [777]", got)
	}
}

func TestARefusalAfterTheResponseBeganIsNotRetried(t *testing.T) {
	// A second QUERY_CREATE would open a second cursor and leak the first, and
	// a repeated CURSOR_READ would skip the items already consumed.
	t.Run("creation after BEGIN", func(t *testing.T) {
		h, server := connectDocs(t)
		creates := 0
		server.Intercept = func(c *fakeserver.Call) bool {
			if c.Operation != 0x0501 {
				return false
			}
			creates++
			c.Begin(new(wire.Builder).U64(1, 5).U64(2, 5))
			c.Fail(uint16(kdoq.CodeOverloaded), false)
			return true
		}
		_, err := h.docs("c").Find(context.Background(), "").Collect()
		if !errors.Is(err, kdoq.ErrOverloaded) || creates != 1 {
			t.Fatalf("error = %v after %d creations, want Overloaded after one", err, creates)
		}
		if got := server.ClosedCursors(); len(got) != 1 || got[0] != 5 {
			t.Fatalf("closed cursors = %v, want [5]", got)
		}
	})
	t.Run("page after an item", func(t *testing.T) {
		h, server := connectDocs(t)
		seed(t, h.docs("c"), 4)
		reads := 0
		server.Intercept = func(c *fakeserver.Call) bool {
			if c.Operation != 0x0503 {
				return false
			}
			reads++
			c.Begin(nil)
			c.WriteResults(1, []fakeserver.Result{{Key: []byte("x"), Value: value.Doc{}}})
			c.Fail(uint16(kdoq.CodeOverloaded), false)
			return true
		}
		_, err := h.docs("c").Find(context.Background(), "", kdoq.FindOptions{PageSize: 2}).Collect()
		if !errors.Is(err, kdoq.ErrOverloaded) || reads != 1 {
			t.Fatalf("error = %v after %d reads, want Overloaded after one", err, reads)
		}
	})
}
