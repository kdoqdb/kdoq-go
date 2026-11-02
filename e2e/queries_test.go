//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

// seed stores n documents d00, d01, ... with a number n and a color.
func seed(t *testing.T, docs *kdoq.Collection[value.Doc], n int) {
	t.Helper()
	ctx := opContext(t)
	stmts := make([]kdoq.BulkStatement, n)
	for i := range stmts {
		color := "red"
		if i%2 == 1 {
			color = "blue"
		}
		stmts[i] = kdoq.BulkStatement{
			Mode:   kdoq.ModeUpsert,
			Target: kdoq.ByKey(kdoq.Key(fmt.Sprintf("d%02d", i))),
			Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(i), "color": color})},
		}
	}
	if _, err := docs.Bulk(ctx, stmts, kdoq.BulkOptions{Durability: kdoq.DurabilityRelaxed}); err != nil {
		t.Fatal(err)
	}
}

func keysOf[T any](items []kdoq.Item[T]) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.Key)
	}
	return out
}

func sameStrings(a, b []string) bool {
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

func sorted(keys []string) []string {
	out := append([]string(nil), keys...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestFindPagesTransparentlyAndFilters(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 7)

	items, err := docs.Find(opContext(t), "n >= 0", kdoq.FindOptions{PageSize: 2}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if got := sorted(keysOf(items)); len(got) != 7 || got[0] != "d00" || got[6] != "d06" {
		t.Fatalf("keys = %v", got)
	}
	for _, it := range items {
		if it.Group != nil || it.Value["color"] == nil {
			t.Fatalf("item = %+v", it)
		}
	}

	matched, err := docs.Find(opContext(t), `color == "blue"`).Collect()
	if err != nil || len(matched) != 3 {
		t.Fatalf("blue = %d, %v", len(matched), err)
	}
}

func TestSkipAndLimitWindowTheResult(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 7)
	var n int
	for _, err := range docs.Find(opContext(t), "", kdoq.FindOptions{Skip: 2, Limit: 3, PageSize: 2}).Values() {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("got %d documents, want 3", n)
	}
}

func TestLeavingTheLoopClosesTheServerCursor(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	docs := collection(t, client, "shop", "c")
	seed(t, docs, 9)

	q := docs.Find(opContext(t), "", kdoq.FindOptions{PageSize: 2})
	var id kdoq.CursorID
	for _, err := range q.All() {
		if err != nil {
			t.Fatal(err)
		}
		var ok bool
		if id, ok = q.CursorID(); !ok {
			t.Fatal("a query with more pages has no cursor")
		}
		break
	}
	// The cursor is closed: continuing it is NotFound.
	_, err := client.AttachCursor[value.Doc](id).Next(opContext(t))
	if !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Next on the closed cursor = %v, want NotFound", err)
	}
}

func TestACancelledContextStopsTheQuery(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 5)
	ctx, cancel := context.WithCancel(opContext(t))
	cancel()
	_, err := docs.Find(ctx, "").Collect()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect = %v, want context.Canceled", err)
	}
}

func TestCancellingTheContextBetweenPagesClosesTheCursor(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	docs := collection(t, client, "shop", "c")
	seed(t, docs, 9)

	ctx, cancel := context.WithCancel(opContext(t))
	defer cancel()
	q := docs.Find(ctx, "", kdoq.FindOptions{PageSize: 2})
	var id kdoq.CursorID
	var failure error
	for _, err := range q.All() {
		if err != nil {
			failure = err
			break
		}
		if id == 0 {
			id, _ = q.CursorID()
			cancel()
		}
	}
	if !errors.Is(failure, context.Canceled) || id == 0 {
		t.Fatalf("iteration ended with %v (cursor %d), want context.Canceled", failure, id)
	}
	_, err := client.AttachCursor[value.Doc](id).Next(opContext(t))
	if !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Next on the cancelled query's cursor = %v, want NotFound", err)
	}
}

func TestCountsAndReductions(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 6)
	ctx := opContext(t)

	if n, err := docs.Count(ctx, ""); err != nil || n != 6 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	if n, err := docs.Count(ctx, `color == "red"`); err != nil || n != 3 {
		t.Fatalf("Count(red) = %d, %v", n, err)
	}

	counted, err := docs.Aggregate[int64](ctx, `n >= 2`, []kdoq.PipelineStage{kdoq.Count()}).Collect()
	if err != nil || len(counted) != 1 || counted[0].Value != 4 {
		t.Fatalf("count stage = %+v, %v", counted, err)
	}

	groups, err := docs.Aggregate[int64](ctx, "", []kdoq.PipelineStage{kdoq.Reduce("color", "0", "$prev + 1")}).Collect()
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	for _, g := range groups {
		if g.Group == nil || g.Value != 3 {
			t.Fatalf("group = %+v", g)
		}
	}

	names, err := docs.Aggregate[string](ctx, "n == 1", []kdoq.PipelineStage{kdoq.Project("color")}).Collect()
	if err != nil || len(names) != 1 || names[0].Value != "blue" {
		t.Fatalf("projection = %+v, %v", names, err)
	}
}

func TestStatisticsAndManualCursor(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 5)
	ctx := opContext(t)

	q := docs.Find(ctx, "", kdoq.FindOptions{Observe: kdoq.Observe{Statistics: true}})
	if _, err := q.Collect(); err != nil {
		t.Fatal(err)
	}
	stats, ok := q.Statistics()
	if !ok || stats.Documents.Selected != 5 {
		t.Fatalf("Statistics = %+v, %v", stats, ok)
	}

	cur, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	var total int
	for {
		page, err := cur.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		total += len(page.Items)
		if page.Done {
			break
		}
	}
	if total != 5 {
		t.Fatalf("manual cursor read %d documents, want 5", total)
	}
	if err := cur.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAQuerySyntaxErrorIsReportedAsAnError(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	_, err := docs.Find(opContext(t), "n >>> (").Collect()
	var failure *kdoq.Error
	if !errors.As(err, &failure) {
		t.Fatalf("Collect = %v, want a *kdoq.Error", err)
	}
}
