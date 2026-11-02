package kdoq_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

// TestOneClientServesManyGoroutines mixes every operation family on one client
// and handle from several goroutines; its value is under the race detector.
// Each Query and Cursor belongs to the goroutine that made it.
func TestOneClientServesManyGoroutines(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	seed(t, docs, 6)
	ctx := context.Background()

	const workers = 12
	errs := make(chan error, workers*8)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail := func(what string, err error) {
				if err != nil {
					errs <- fmt.Errorf("worker %d: %s: %w", w, what, err)
				}
			}
			items, err := docs.Find(ctx, "", kdoq.FindOptions{PageSize: 2}).Collect()
			fail("Find", err)
			if err == nil && len(items) < 6 {
				fail("Find", fmt.Errorf("%d items", len(items)))
			}
			n, err := docs.Count(ctx, "")
			fail("Count", err)
			if err == nil && n < 6 {
				fail("Count", fmt.Errorf("count %d", n))
			}
			cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 3})
			fail("FindCursor", err)
			if err == nil {
				_, err = cur.Next(ctx)
				fail("Next", err)
				fail("Close", cur.Close(ctx))
			}
			key := kdoq.Key(fmt.Sprintf("w%02d", w))
			_, err = docs.Upsert(ctx, kdoq.ByKey(key), []kdoq.Stage{kdoq.Set(value.Doc{"w": int32(w)})})
			fail("Upsert", err)
			_, err = docs.Bulk(ctx, []kdoq.BulkStatement{
				{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey(key + "b"), Stages: sets(1)},
				{Mode: kdoq.ModeUpdate, Target: kdoq.ByKey(key), Stages: sets(2)},
			}, kdoq.BulkOptions{Ordered: true})
			fail("Bulk", err)
			_, err = h.client.Databases().List(ctx)
			fail("Databases.List", err)
			_, err = h.client.Sessions().List(ctx)
			fail("Sessions.List", err)
			fail("WALSync", docs.WALSync(ctx))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := server.OpenCursors(); got != 0 {
		t.Fatalf("%d cursors left open", got)
	}
}
