//go:build e2e

package e2e_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

func TestUpdateAppliesOrderedStagesAndExpressions(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)
	if _, err := docs.Put(ctx, "k", value.Doc{"a": int32(1), "gone": true, "dot.name": int32(1)}); err != nil {
		t.Fatal(err)
	}

	res, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{
		kdoq.Set(value.D{{Key: "b", Value: kdoq.Expr("a + 1")}, {Key: "a", Value: int32(10)}}),
		kdoq.SetPath([]string{"dot.name"}, int32(5)),
		kdoq.Unset("gone"),
	})
	if err != nil || res.Key != "k" {
		t.Fatalf("Update = %+v, %v", res, err)
	}
	got, err := docs.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != int32(10) || fmt.Sprint(got["b"]) != "2" || got["dot.name"] != int32(5) || got["gone"] != nil {
		t.Fatalf("document = %v", got)
	}

	if _, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.Replace(value.Doc{"only": "this"})}); err != nil {
		t.Fatal(err)
	}
	got, _ = docs.Get(ctx, "k")
	if len(got) != 1 || got["only"] != "this" {
		t.Fatalf("after Replace = %v", got)
	}
}

func TestUpdateOfAMissingKeyMatchesNothingAndUpsertCreatesIt(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)
	stages := []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}

	if res, err := docs.Update(ctx, kdoq.ByKey("new"), stages); err != nil || res.Inserted {
		t.Fatalf("Update of an absent key = %+v, %v: it must match nothing", res, err)
	}
	if _, err := docs.Get(ctx, "new"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Update must not create the document: %v", err)
	}
	res, err := docs.Upsert(ctx, kdoq.ByKey("new"), stages)
	if err != nil || !res.Inserted {
		t.Fatalf("first Upsert = %+v, %v", res, err)
	}
	res, err = docs.Upsert(ctx, kdoq.ByKey("new"), stages)
	if err != nil || res.Inserted {
		t.Fatalf("second Upsert = %+v, %v", res, err)
	}
}

func TestFilterTargetsChangeOneOrEveryMatch(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	seed(t, docs, 6)
	ctx := opContext(t)

	one, err := docs.Update(ctx, kdoq.Where(`color == "red"`), []kdoq.Stage{kdoq.Set(value.Doc{"seen": true})})
	if err != nil || one.Modified != 1 {
		t.Fatalf("single = %+v, %v", one, err)
	}
	all, err := docs.Update(ctx, kdoq.Where(`color == "blue"`).Multi(), []kdoq.Stage{kdoq.Set(value.Doc{"seen": true})})
	if err != nil || all.Matched != 3 || all.Modified != 3 {
		t.Fatalf("multi = %+v, %v", all, err)
	}
	if n, err := docs.Count(ctx, "seen == true"); err != nil || n != 4 {
		t.Fatalf("Count = %d, %v", n, err)
	}
}

func TestBulkRunsStatementsAndReportsEachOne(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)

	res, err := docs.Bulk(ctx, []kdoq.BulkStatement{
		{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey("a"), Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}},
		{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey("b"), Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(2)})}},
		{Mode: kdoq.ModeUpdate, Target: kdoq.ByKey("a"), Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(3)})}},
	}, kdoq.BulkOptions{Ordered: true})
	if err != nil || res.Executed != 3 || len(res.Items) != 3 || !res.Items[0].Inserted || res.Items[2].Inserted {
		t.Fatalf("Bulk = %+v, %v", res, err)
	}
	if got, _ := docs.Get(ctx, "a"); got["n"] != int32(3) {
		t.Fatalf("a = %v", got)
	}
}

func TestUnorderedBulkReportsAFailedStatementAndGoesOn(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)

	res, err := docs.Bulk(ctx, []kdoq.BulkStatement{
		{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey("bad"), Stages: []kdoq.Stage{kdoq.Replace(kdoq.Expr("1"))}},
		{Mode: kdoq.ModeUpsert, Target: kdoq.ByKey("present"), Stages: []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(2)})}},
	}, kdoq.BulkOptions{Ordered: false})
	if err != nil {
		t.Fatalf("Bulk = %v", err)
	}
	if res.Items[0].Status != kdoq.CodeInvalidDocument || res.Items[1].Status != 0 {
		t.Fatalf("items = %+v", res.Items)
	}
	if _, err := docs.Get(ctx, "present"); err != nil {
		t.Fatalf("the statement after a failed one must run: %v", err)
	}
}

func TestAStatementThatCannotBeBuiltFailsBeforeTheServer(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	_, err := docs.Update(opContext(t), kdoq.ByKey("k"), nil)
	var config *kdoq.ConfigError
	if !errors.As(err, &config) {
		t.Fatalf("Update without stages = %v, want a *ConfigError", err)
	}
}
