package kdoq_test

import (
	"context"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

func TestAggregateProjectionYieldsItsOwnTypeWithPlainKeys(t *testing.T) {
	h, server := connectDocs(t)
	server.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
		return fakeserver.QueryResult{Items: []fakeserver.Result{
			{Key: []byte("a"), Value: "Ann"}, {Key: []byte("b"), Value: "Bob"},
		}}
	}
	items, err := h.docs("c").Aggregate[string](context.Background(), "age > 1", []kdoq.PipelineStage{kdoq.Project("name")}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Value != "Ann" || items[0].Key != "a" || items[1].Value != "Bob" || items[1].Group != nil {
		t.Fatalf("items = %+v", items)
	}
	pipeline := server.QueryRequests()[0].Pipeline
	if len(pipeline) != 1 || pipeline[0].Kind != fakeserver.StageProject || pipeline[0].Expr != "name" {
		t.Fatalf("pipeline = %+v", pipeline)
	}
}

func TestAggregateReductionCarriesGroupingKeysAndKeepsStageOrder(t *testing.T) {
	h, server := connectDocs(t)
	server.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
		return fakeserver.QueryResult{Items: []fakeserver.Result{
			{HasGroup: true, Group: "red", Value: int64(3)},
			{HasGroup: true, Group: value.Doc{"c": "blue"}, Value: int64(5)},
			{HasGroup: true, Group: nil, Value: int64(1)},
		}}
	}
	stages := []kdoq.PipelineStage{
		kdoq.Match("n > 0"), kdoq.Reduce("color", "0", "$acc + 1"), kdoq.Project("$out"), kdoq.Count(),
	}
	items, err := h.docs("c").Aggregate[int64](context.Background(), "", stages).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Key != "" || items[0].Group == nil || items[0].Group.Value != "red" || items[0].Value != 3 {
		t.Fatalf("items = %+v", items)
	}
	if g, ok := items[1].Group.Value.(value.Doc); !ok || g["c"] != "blue" {
		t.Fatalf("a document grouping key = %#v", items[1].Group.Value)
	}
	if items[2].Group == nil || items[2].Group.Value != nil {
		t.Fatalf("a Null grouping key is a group whose value is nil, got %+v", items[2].Group)
	}
	got := server.QueryRequests()[0].Pipeline
	want := []fakeserver.Stage{
		{Kind: fakeserver.StageMatch, Expr: "n > 0"},
		{Kind: fakeserver.StageReduce, Reduce: [3]string{"color", "0", "$acc + 1"}},
		{Kind: fakeserver.StageProject, Expr: "$out"},
		{Kind: fakeserver.StageCount},
	}
	if len(got) != len(want) {
		t.Fatalf("pipeline = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stage %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAggregateCountYieldsOneItemWithoutAKey(t *testing.T) {
	h, server := connectDocs(t)
	server.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
		return fakeserver.QueryResult{Items: []fakeserver.Result{{Value: int64(42)}}}
	}
	items, err := h.docs("c").Aggregate[int64](context.Background(), "", []kdoq.PipelineStage{kdoq.Count()}).Collect()
	if err != nil || len(items) != 1 {
		t.Fatalf("Collect = %+v, %v", items, err)
	}
	if items[0].Value != 42 || items[0].Key != "" || items[0].Group != nil {
		t.Fatalf("item = %+v: a count has a value and no key", items[0])
	}
}

func TestAggregateIntoAStructFillsItsKeyField(t *testing.T) {
	h, server := connectDocs(t)
	server.Query = func(*fakeserver.QueryRequest) fakeserver.QueryResult {
		return fakeserver.QueryResult{Items: []fakeserver.Result{{Key: []byte("u1"), Value: value.Doc{"name": "Ann"}}}}
	}
	items, err := h.docs("c").Aggregate[user](context.Background(), "", []kdoq.PipelineStage{kdoq.Project("{name: name}")}).Collect()
	if err != nil || len(items) != 1 || items[0].Value != (user{ID: "u1", Name: "Ann"}) {
		t.Fatalf("Collect = %+v, %v", items, err)
	}
}
