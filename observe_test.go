package kdoq_test

import (
	"context"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
)

func TestCallbacksRunInTheReadingGoroutineBeforeTheResultsOfTheirPage(t *testing.T) {
	h, server := connectDocs(t)
	seed(t, h.docs("c"), 2)
	stage := uint64(2)
	server.Query = func(req *fakeserver.QueryRequest) fakeserver.QueryResult {
		res := server.DefaultQuery(req)
		res.Diagnostics = []fakeserver.DiagnosticFrame{{Expression: "match", Stage: &stage, Start: 3, End: 9, Reason: "bad"}}
		return res
	}
	// The log is deliberately unsynchronized: the race detector fails the test
	// if a callback runs anywhere but in the goroutine that iterates.
	var log []string
	var diag kdoq.Diagnostic
	var progress kdoq.Progress
	opts := kdoq.FindOptions{
		OnProgress:   func(p kdoq.Progress) { progress = p; log = append(log, "progress") },
		OnDiagnostic: func(d kdoq.Diagnostic) { diag = d; log = append(log, "diagnostic") },
	}
	for _, err := range h.docs("c").Find(context.Background(), "", opts).All() {
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, "item")
	}
	if want := []string{"diagnostic", "progress", "item", "item"}; !equalStrings(log, want) {
		t.Fatalf("event order = %v, want %v", log, want)
	}
	if progress != (kdoq.Progress{Processed: 2, Total: 2}) {
		t.Fatalf("progress = %+v", progress)
	}
	if diag.Expression != "match" || diag.Stage == nil || *diag.Stage != 2 || diag.Span.Start != 3 || diag.Span.End != 9 || diag.Reason != "bad" {
		t.Fatalf("diagnostic = %+v", diag)
	}
}

func TestObservationIsOffUnlessRequested(t *testing.T) {
	h, server := connectDocs(t)
	if _, err := h.docs("c").Find(context.Background(), "").Collect(); err != nil {
		t.Fatal(err)
	}
	r := server.QueryRequests()[0]
	if !r.HasObserve || r.Progress || r.Diagnostics || r.Statistics {
		t.Fatalf("observe = %+v: nothing is reported unless asked for", r)
	}
	opts := kdoq.FindOptions{Observe: kdoq.Observe{Progress: true, Diagnostics: true, Statistics: true}}
	if _, err := h.docs("c").Find(context.Background(), "", opts).Collect(); err != nil {
		t.Fatal(err)
	}
	r = server.QueryRequests()[1]
	if !r.Progress || !r.Diagnostics || !r.Statistics {
		t.Fatalf("observe = %+v", r)
	}
}

func TestStatisticsAreAvailableAfterTheQueryEnds(t *testing.T) {
	h, _ := connectDocs(t)
	seed(t, h.docs("c"), 3)
	q := h.docs("c").Find(context.Background(), "", kdoq.FindOptions{PageSize: 2, Observe: kdoq.Observe{Statistics: true}})
	if _, err := q.Collect(); err != nil {
		t.Fatal(err)
	}
	s, ok := q.Statistics()
	if !ok {
		t.Fatal("no statistics")
	}
	if s.Source.Total != 11 || s.Source.Processed != 12 || s.Source.BloomCandidates != 13 ||
		s.Documents.Processed != 21 || s.Documents.Selected != 22 || s.Documents.Rejected != 23 || s.Documents.FilterErrors != 24 ||
		s.Budget.Steps != 31 || s.Budget.ValueBytes != 32 || s.Budget.Active != 33*time.Nanosecond ||
		s.Memory.Current != 41 || s.Memory.Peak != 42 || s.Reduction != nil {
		t.Fatalf("statistics = %+v", s)
	}
	plain := h.docs("c").Find(context.Background(), "")
	if _, err := plain.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.Statistics(); ok {
		t.Fatal("statistics must be absent when not requested")
	}
}

func TestReductionStatisticsCarryTheReductionGroup(t *testing.T) {
	h, _ := connectDocs(t)
	q := h.docs("c").Aggregate[int64](context.Background(), "", []kdoq.PipelineStage{kdoq.Reduce("k", "0", "a")},
		kdoq.FindOptions{Observe: kdoq.Observe{Statistics: true}})
	if _, err := q.Collect(); err != nil {
		t.Fatal(err)
	}
	s, ok := q.Statistics()
	if !ok || s.Reduction == nil || s.Reduction.AccumulatedDocuments != 51 || s.Reduction.GroupsCreated != 52 {
		t.Fatalf("statistics = %+v", s)
	}
}

func TestBudgetWindowAndTTLReachTheServer(t *testing.T) {
	h, server := connectDocs(t)
	opts := kdoq.FindOptions{
		IdleTTL: 90 * time.Second,
		Budget: kdoq.Budget{
			MemoryBytes: 1 << 20, AdmissionWait: 250 * time.Millisecond, MemoryWait: 2 * time.Second,
			Steps: 5000, ExpressionBytes: 4096, Active: 750 * time.Millisecond,
		},
	}
	if _, err := h.docs("c").Find(context.Background(), "", opts).Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.docs("c").Count(context.Background(), "", opts); err != nil {
		t.Fatal(err)
	}
	reqs := server.QueryRequests()
	want := map[int]uint64{1: 1 << 20, 2: 250, 3: 2000, 4: 5000, 5: 4096, 6: 750}
	for _, r := range reqs {
		if len(r.Budget) != len(want) {
			t.Fatalf("budget = %v", r.Budget)
		}
		for id, v := range want {
			if r.Budget[id] != v {
				t.Fatalf("op %#x budget[%d] = %d, want %d", r.Operation, id, r.Budget[id], v)
			}
		}
	}
	if reqs[0].IdleTTLms == nil || *reqs[0].IdleTTLms != 90000 {
		t.Fatalf("idle ttl = %v", reqs[0].IdleTTLms)
	}
}

func TestCursorReadsCarryTheAdmissionWait(t *testing.T) {
	h, server := connectDocs(t)
	seed(t, h.docs("c"), 2)
	ctx := context.Background()
	cur, err := h.docs("c").FindCursor(ctx, "", kdoq.FindOptions{PageSize: 1, Budget: kdoq.Budget{AdmissionWait: 40 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cur.Next(ctx); err != nil {
		t.Fatal(err)
	}
	reqs := server.QueryRequests()
	if reqs[1].Operation != 0x503 || reqs[1].Budget[2] != 40 {
		t.Fatalf("read request = %+v", reqs[1])
	}
}

func TestCursorNextCallbacksReceiveTheObservationsOfThePage(t *testing.T) {
	h, server := connectDocs(t)
	seed(t, h.docs("c"), 2)
	ctx := context.Background()
	cur, err := h.docs("c").FindCursor(ctx, "", kdoq.FindOptions{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation != 0x0503 {
			return false
		}
		c.SendProgress(5, 9)
		return false
	}
	// The intercept sends PROGRESS and then lets the default handler answer.
	var got []kdoq.Progress
	if _, err := cur.Next(ctx, kdoq.NextOptions{OnProgress: func(p kdoq.Progress) { got = append(got, p) }}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (kdoq.Progress{Processed: 5, Total: 9}) {
		t.Fatalf("progress = %+v", got)
	}
}
