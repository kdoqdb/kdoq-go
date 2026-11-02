package kdoq_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

func sets(n int32) []kdoq.Stage { return []kdoq.Stage{kdoq.Set(value.Doc{"n": n})} }

func TestBulkEncodesEachStatementsTargetAndMode(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	res, err := h.docs("c").Bulk(context.Background(), []kdoq.BulkStatement{
		{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey("a"), Stages: sets(1)},
		{Mode: kdoq.ModeUpdate, Target: kdoq.Where("n > 5").Multi(), Stages: sets(2)},
		{Mode: kdoq.ModeUpdate, Target: kdoq.ByKey("a"), Stages: []kdoq.Stage{kdoq.Unset("n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rec.last(t)
	if got.operation != 0x0407 || got.hasKey || len(got.statements) != 3 {
		t.Fatalf("call = %+v", got)
	}
	st := got.statements
	if string(st[0].Key) != "a" || st[0].Mode == nil || *st[0].Mode != 1 || st[0].HasFilter {
		t.Fatalf("statement 0 = %+v", st[0])
	}
	if st[1].HasKey || *st[1].Mode != 0 || st[1].Filter != "n > 5" || !st[1].Multi {
		t.Fatalf("statement 1 = %+v", st[1])
	}
	if string(st[2].Key) != "a" || st[2].Stages[0].Kind != fakeserver.StageRemove {
		t.Fatalf("statement 2 = %+v", st[2])
	}
	if st[1].Stages[0].Pairs[0].Operand.Literal != int32(2) || st[0].Stages[0].Pairs[0].Operand.Literal != int32(1) {
		t.Fatal("each statement keeps its own literals: the pool is per statement")
	}
	if res.Executed != 3 || len(res.Items) != 3 || res.Items[0].Index != 0 || res.Items[0].Status != 0 || !res.Items[0].Inserted || res.Items[0].Key != "a" {
		t.Fatalf("result = %+v", res)
	}
}

func TestBulkOrderedByDefaultAndUnorderedOnRequest(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	stmts := []kdoq.BulkStatement{{Target: kdoq.ByKey("a"), Stages: sets(1)}}
	for _, tc := range []struct {
		name    string
		opts    []kdoq.BulkOption
		ordered *uint64
		dur     *uint64
	}{
		{"default", nil, nil, nil},
		{"unordered", []kdoq.BulkOption{kdoq.BulkOptions{Ordered: false}}, ptr(uint64(0)), nil},
		{"ordered and relaxed", []kdoq.BulkOption{kdoq.BulkOptions{Ordered: true, Durability: kdoq.DurabilityRelaxed}}, ptr(uint64(1)), ptr(uint64(1))},
	} {
		if _, err := h.docs("c").Bulk(context.Background(), stmts, tc.opts...); err != nil {
			t.Fatal(err)
		}
		opts := rec.last(t).options
		for id, want := range map[int]*uint64{2: tc.ordered, 1: tc.dur} {
			got, ok := opts[id]
			if (want == nil) != !ok || (want != nil && got != *want) {
				t.Fatalf("%s: option %d = %v (present %v), want %v", tc.name, id, got, ok, want)
			}
		}
	}
}

func TestBulkReportsEachStatementsStatus(t *testing.T) {
	h, server := connectDocs(t)
	matched, modified := uint64(4), uint64(3)
	server.Intercept = func(c *fakeserver.Call) bool {
		c.Begin(nil)
		c.BulkItem(0, 0, fakeserver.MutationReply{Key: []byte("a"), Flags: 3})
		c.BulkItem(1, uint16(kdoq.CodeInvalidDocument), fakeserver.MutationReply{})
		c.BulkItem(2, 0, fakeserver.MutationReply{Flags: 3, Matched: &matched, Modified: &modified})
		c.BulkItem(3, uint16(kdoq.CodeExecutionMemoryLimit), fakeserver.MutationReply{})
		c.EndBulk(4)
		return true
	}
	stmts := make([]kdoq.BulkStatement, 4)
	for i := range stmts {
		stmts[i] = kdoq.BulkStatement{Target: kdoq.ByKey("a"), Stages: sets(1)}
	}
	res, err := h.docs("c").Bulk(context.Background(), stmts, kdoq.BulkOptions{Ordered: false})
	if err != nil {
		t.Fatalf("per-statement failures are not command failures: %v", err)
	}
	if res.Executed != 4 || len(res.Items) != 4 {
		t.Fatalf("result = %+v", res)
	}
	if it := res.Items[0]; it.Status != 0 || it.Key != "a" || it.Inserted || it.Matched != 0 {
		t.Fatalf("item 0 = %+v", it)
	}
	if res.Items[1].Status != kdoq.CodeInvalidDocument || res.Items[3].Status != kdoq.CodeExecutionMemoryLimit {
		t.Fatalf("statuses = %v, %v", res.Items[1].Status, res.Items[3].Status)
	}
	if it := res.Items[2]; it.Index != 2 || it.Matched != 4 || it.Modified != 3 || it.Status != 0 {
		t.Fatalf("item 2 = %+v", it)
	}
}

func TestOrderedBulkStopsAtTheFirstRejectedStatement(t *testing.T) {
	h, server := connectDocs(t)
	server.Intercept = func(c *fakeserver.Call) bool {
		c.Begin(nil)
		c.BulkItem(0, 0, fakeserver.MutationReply{Key: []byte("a"), Flags: 3})
		c.BulkItem(1, uint16(kdoq.CodeInvalidDocument), fakeserver.MutationReply{})
		c.EndBulk(2)
		return true
	}
	stmts := []kdoq.BulkStatement{
		{Target: kdoq.ByKey("a"), Stages: sets(1)}, {Target: kdoq.ByKey("b"), Stages: sets(1)}, {Target: kdoq.ByKey("c"), Stages: sets(1)},
	}
	res, err := h.docs("c").Bulk(context.Background(), stmts)
	if err != nil || res.Executed != 2 || len(res.Items) != 2 || res.Items[1].Status != kdoq.CodeInvalidDocument {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestACommandFailureAfterAcceptedStatementsIsTheBulkError(t *testing.T) {
	for name, finish := range map[string]func(*fakeserver.Call){
		"error frame": func(c *fakeserver.Call) { c.Fail(uint16(kdoq.CodeStorage), true) },
		"truncated":   func(c *fakeserver.Call) { c.Truncate() },
	} {
		t.Run(name, func(t *testing.T) {
			h, server := connectDocs(t)
			server.Intercept = func(c *fakeserver.Call) bool {
				c.Begin(nil)
				c.BulkItem(0, 0, fakeserver.MutationReply{Key: []byte("a"), Flags: 3})
				c.BulkItem(1, 0, fakeserver.MutationReply{Key: []byte("b"), Flags: 4})
				finish(c)
				return true
			}
			stmts := []kdoq.BulkStatement{
				{Target: kdoq.ByKey("a"), Stages: sets(1)}, {Target: kdoq.ByKey("b"), Stages: sets(1)}, {Target: kdoq.ByKey("c"), Stages: sets(1)},
			}
			_, err := h.docs("c").Bulk(context.Background(), stmts)
			var bulk *kdoq.BulkError
			if !errors.As(err, &bulk) {
				t.Fatalf("error = %v, want *kdoq.BulkError", err)
			}
			if len(bulk.Items) != 2 || bulk.Items[0].Key != "a" || !bulk.Items[1].Inserted || bulk.Err.Outcome != kdoq.OutcomeUnknown {
				t.Fatalf("bulk error = %+v / %+v", bulk, bulk.Err)
			}
			if name == "error frame" && !errors.Is(err, kdoq.ErrStorage) {
				t.Fatalf("errors.Is(ErrStorage) = false for %v", err)
			}
			var incomplete *kdoq.IncompleteError
			if name == "truncated" && !errors.As(err, &incomplete) {
				t.Fatalf("a truncated bulk must unwrap to *IncompleteError: %v", err)
			}
		})
	}
}

func TestABulkFailingBeforeAnyStatementIsAPlainError(t *testing.T) {
	h, server := connectDocs(t)
	server.Intercept = func(c *fakeserver.Call) bool { c.Fail(uint16(kdoq.CodeLimitExceeded), false); return true }
	_, err := h.docs("c").Bulk(context.Background(), []kdoq.BulkStatement{{Target: kdoq.ByKey("a"), Stages: sets(1)}})
	var bulk *kdoq.BulkError
	if errors.As(err, &bulk) || !errors.Is(err, kdoq.ErrLimitExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestBulkWithoutTheMutationCapabilityIsUnsupportedBeforeAStreamOpens(t *testing.T) {
	server := newServer(fakeserver.Config{Capabilities: 1 | 4 | 8})
	h := connect(t, server, "kdoq://h/shop")
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	_, err := h.docs("c").Bulk(context.Background(), []kdoq.BulkStatement{{Target: kdoq.ByKey("a"), Stages: sets(1)}})
	if !errors.Is(err, kdoq.ErrUnsupported) || calls != 0 {
		t.Fatalf("Bulk = %v after %d calls", err, calls)
	}
}

func TestInvalidBulksFailBeforeAnyNetworkTraffic(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	ctx := context.Background()
	if _, err := h.docs("c").Bulk(ctx, nil); err == nil {
		t.Fatal("an empty bulk must fail")
	}
	bad := []kdoq.BulkStatement{{Target: kdoq.ByKey("a"), Stages: sets(1)}, {Target: kdoq.Target{}, Stages: sets(1)}}
	if _, err := h.docs("c").Bulk(ctx, bad); err == nil {
		t.Fatal("a statement without a target must fail the whole bulk")
	}
	if rec.count() != 0 {
		t.Fatal("an invalid bulk reached the server")
	}
}

func TestStatementsShareOneDictionaryAcrossTheBulk(t *testing.T) {
	h, server := connectDocs(t)
	frames := make(chan int, 1)
	server.Intercept = func(c *fakeserver.Call) bool {
		n := 0
		for _, f := range c.Inputs {
			if f.Kind == 0x0300 {
				n++
			}
		}
		frames <- n
		return false
	}
	stmts := []kdoq.BulkStatement{
		{Target: kdoq.ByKey("a"), Stages: sets(1)},
		{Target: kdoq.ByKey("b"), Stages: sets(2)},
		{Target: kdoq.ByKey("c"), Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": value.Doc{"deep": int32(1)}})}},
	}
	if _, err := h.docs("c").Bulk(context.Background(), stmts); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, frames, "the dictionary count"); got != 2 {
		t.Fatalf("%d DICTIONARY frames, want 2: one for the first statement, one for the name the third adds", got)
	}
}
