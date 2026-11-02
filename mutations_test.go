package kdoq_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

// seen is what an update, upsert or bulk put on the wire.
type seen struct {
	operation  uint16
	statements []fakeserver.Statement
	key        []byte
	hasKey     bool
	options    map[int]uint64
}

type recorder struct {
	mu    sync.Mutex
	calls []seen
}

// record makes the server remember every mutation and answer it normally.
func record(server *fakeserver.Server) *recorder {
	r := &recorder{}
	server.Intercept = func(c *fakeserver.Call) bool {
		if c.Operation < 0x0405 || c.Operation > 0x0407 {
			return false
		}
		s := seen{operation: c.Operation, options: map[int]uint64{}}
		s.statements, _ = c.Statements()
		s.key, s.hasKey = c.Payload.Bytes(3)
		optionsID := 4
		if c.Operation == 0x0407 {
			optionsID, s.hasKey = 3, false
		}
		if g, ok, _ := c.Payload.Group(optionsID); ok {
			if v, ok, _ := g.U16(1); ok {
				s.options[1] = uint64(v)
			}
			if v, ok, _ := g.U8(2); ok {
				s.options[2] = uint64(v)
			}
		}
		r.mu.Lock()
		r.calls = append(r.calls, s)
		r.mu.Unlock()
		return false
	}
	return r
}

func (r *recorder) last(t *testing.T) seen {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("no mutation reached the server")
	}
	return r.calls[len(r.calls)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func literal(v any) fakeserver.Operand { return fakeserver.Operand{Literal: v} }
func expression(s string) fakeserver.Operand {
	return fakeserver.Operand{IsExpression: true, Expression: s}
}

func TestUpdateByKeyEncodesOrderedStages(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	docs := h.docs("c")
	if _, err := docs.Put(context.Background(), "k", value.Doc{"a": int32(0)}); err != nil {
		t.Fatal(err)
	}
	res, err := docs.Update(context.Background(), kdoq.ByKey("k"), []kdoq.Stage{
		kdoq.Set(value.D{{Key: "b", Value: kdoq.Expr("a + 1")}, {Key: "a", Value: int32(7)}, {Key: "c.d", Value: "x"}}),
		kdoq.Unset("e", "f.g"),
		kdoq.Replace(value.Doc{"z": true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "k" || res.Inserted {
		t.Fatalf("result = %+v", res)
	}
	got := rec.last(t)
	if got.operation != 0x0405 || string(got.key) != "k" || !got.hasKey || len(got.statements) != 1 {
		t.Fatalf("call = %+v", got)
	}
	st := got.statements[0]
	if st.HasKey || st.Mode != nil || st.HasFilter || st.Multi {
		t.Fatalf("a keyed update carries the key in the payload only: %+v", st)
	}
	if len(st.Stages) != 3 {
		t.Fatalf("stages = %+v", st.Stages)
	}
	pairs := st.Stages[0].Pairs
	want := []fakeserver.Pair{
		{Path: []string{"b"}, Operand: expression("a + 1")},
		{Path: []string{"a"}, Operand: literal(int32(7))},
		{Path: []string{"c", "d"}, Operand: literal("x")},
	}
	if len(pairs) != 3 {
		t.Fatalf("pairs = %+v", pairs)
	}
	for i, p := range want {
		if !equalPath(pairs[i].Path, p.Path) || pairs[i].Operand.IsExpression != p.Operand.IsExpression ||
			pairs[i].Operand.Expression != p.Operand.Expression || pairs[i].Operand.Literal != p.Operand.Literal {
			t.Fatalf("pair %d = %+v, want %+v: ordered documents keep their order and dotted keys are paths", i, pairs[i], p)
		}
	}
	remove := st.Stages[1]
	if remove.Kind != fakeserver.StageRemove || len(remove.Paths) != 2 || !equalPath(remove.Paths[0], []string{"e"}) || !equalPath(remove.Paths[1], []string{"f", "g"}) {
		t.Fatalf("unset stage = %+v", remove)
	}
	replace := st.Stages[2]
	if doc, ok := replace.Operand.Literal.(value.Doc); replace.Kind != fakeserver.StageReplace || !ok || doc["z"] != true {
		t.Fatalf("replace stage = %+v", replace)
	}
}

func equalPath(a, b []string) bool { return equalStrings(a, b) }

func TestSetOfAnUnorderedDocumentIsSortedByName(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	_, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), []kdoq.Stage{
		kdoq.Set(value.Doc{"z": int32(1), "a": int32(2), "m": int32(3)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range rec.last(t).statements[0].Stages[0].Pairs {
		names = append(names, p.Path[0])
	}
	if !equalStrings(names, []string{"a", "m", "z"}) {
		t.Fatalf("pair order = %v, want sorted", names)
	}
}

func TestSetPathAddressesAFieldWhoseNameContainsADot(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	_, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), []kdoq.Stage{
		kdoq.SetPath([]string{"a.b", "c"}, int64(5)),
		kdoq.SetPath([]string{"x"}, kdoq.Expr("y * 2")),
	})
	if err != nil {
		t.Fatal(err)
	}
	st := rec.last(t).statements[0]
	if len(st.Stages) != 2 || !equalPath(st.Stages[0].Pairs[0].Path, []string{"a.b", "c"}) || st.Stages[0].Pairs[0].Operand.Literal != int64(5) {
		t.Fatalf("stages = %+v: the dot of a path component is literal", st.Stages)
	}
	if op := st.Stages[1].Pairs[0].Operand; !op.IsExpression || op.Expression != "y * 2" {
		t.Fatalf("operand = %+v", op)
	}
}

func TestReplaceWithAnExpression(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	_, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), []kdoq.Stage{kdoq.Replace(kdoq.Expr("{n: 1}"))})
	if err != nil {
		t.Fatal(err)
	}
	if op := rec.last(t).statements[0].Stages[0].Operand; !op.IsExpression || op.Expression != "{n: 1}" {
		t.Fatalf("operand = %+v", op)
	}
}

func TestUpdateByFilterTargetsOneMatchOrEveryMatch(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	recording := server.Intercept
	matched, modified := uint64(3), uint64(2)
	server.Intercept = func(c *fakeserver.Call) bool {
		recording(c)
		c.ReplyMutation(fakeserver.MutationReply{Flags: 3, Matched: &matched, Modified: &modified})
		return true
	}
	stages := []kdoq.Stage{kdoq.Set(value.Doc{"seen": true})}
	one, err := h.docs("c").Update(context.Background(), kdoq.Where("n > 1"), stages)
	if err != nil {
		t.Fatal(err)
	}
	first := rec.last(t)
	if first.hasKey || !first.statements[0].HasFilter || first.statements[0].Filter != "n > 1" || first.statements[0].Multi {
		t.Fatalf("one-match call = %+v", first)
	}
	if one.Matched != 3 || one.Modified != 2 || one.Key != "" || one.Inserted {
		t.Fatalf("result = %+v", one)
	}
	if _, err := h.docs("c").Update(context.Background(), kdoq.Where("n > 1").Multi(), stages); err != nil {
		t.Fatal(err)
	}
	if !rec.last(t).statements[0].Multi {
		t.Fatal("Multi must select every match")
	}
}

func TestUpsertReportsWhetherItInserted(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	stages := []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}
	created, err := docs.Upsert(context.Background(), kdoq.ByKey("k"), stages)
	if err != nil || !created.Inserted || created.Key != "k" {
		t.Fatalf("first Upsert = %+v, %v", created, err)
	}
	again, err := docs.Upsert(context.Background(), kdoq.ByKey("k"), stages)
	if err != nil || again.Inserted || again.Key != "k" {
		t.Fatalf("second Upsert = %+v, %v", again, err)
	}
}

func TestUpdateOfAnAbsentKeyChangesNothing(t *testing.T) {
	h, server := connectDocs(t)
	res, err := h.docs("c").Update(context.Background(), kdoq.ByKey("ghost"), []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})})
	if err != nil || res.Inserted {
		t.Fatalf("Update = %+v, %v", res, err)
	}
	if _, ok := server.Doc("shop", "c", []byte("ghost")); ok {
		t.Fatal("an update must not create the document")
	}
}

func TestAnUpsertByFilterReturnsTheGeneratedKey(t *testing.T) {
	h, server := connectDocs(t)
	server.Intercept = func(c *fakeserver.Call) bool {
		c.ReplyMutation(fakeserver.MutationReply{Key: []byte("gen-1"), Flags: 4})
		return true
	}
	res, err := h.docs("c").Upsert(context.Background(), kdoq.Where("n = 1"), []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})})
	if err != nil || !res.Inserted || res.Key != "gen-1" || res.Matched != 0 {
		t.Fatalf("Upsert = %+v, %v", res, err)
	}
}

func TestMutationDurabilityReachesTheServer(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	stages := []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}
	if _, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), stages, kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed}); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t).options[1]; got != 1 {
		t.Fatalf("durability = %d, want 1 (relaxed)", got)
	}
	if _, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), stages); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.last(t).options[1]; ok {
		t.Fatal("the default durability is not sent")
	}
}

func TestInvalidMutationsFailBeforeAnyNetworkTraffic(t *testing.T) {
	h, server := connectDocs(t)
	rec := record(server)
	ctx := context.Background()
	docs := h.docs("c")
	set := []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}
	for name, call := range map[string]func() error{
		"multi on a key": func() error { _, err := docs.Update(ctx, kdoq.ByKey("k").Multi(), set); return err },
		"no target":      func() error { _, err := docs.Update(ctx, kdoq.Target{}, set); return err },
		"no stages":      func() error { _, err := docs.Update(ctx, kdoq.ByKey("k"), nil); return err },
		"empty path part": func() error {
			_, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.Set(value.Doc{"a..b": int32(1)})})
			return err
		},
		"empty SetPath": func() error {
			_, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.SetPath(nil, int32(1))})
			return err
		},
		"set of a non-object": func() error { _, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.Set(42)}); return err },
		"empty unset":         func() error { _, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.Unset()}); return err },
	} {
		var config *kdoq.ConfigError
		var e *kdoq.Error
		if err := call(); !errors.As(err, &config) && !errors.As(err, &e) {
			t.Fatalf("%s = %v, want a local error", name, err)
		}
	}
	_, err := docs.Update(ctx, kdoq.ByKey("k"), []kdoq.Stage{kdoq.Set(value.Doc{"u": value.Undefined})})
	if !errors.Is(err, kdoq.ErrInvalidDocument) {
		t.Fatalf("writing Undefined = %v, want InvalidDocument", err)
	}
	if rec.count() != 0 {
		t.Fatalf("%d invalid mutations reached the server", rec.count())
	}
}

func TestMutationsNeedTheNegotiatedCapabilityAndTheWriteLevel(t *testing.T) {
	server := newServer(fakeserver.Config{Capabilities: 1 | 4 | 8})
	h := connect(t, server, "kdoq://h/shop")
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	set := []kdoq.Stage{kdoq.Set(value.Doc{"n": int32(1)})}
	_, err := h.docs("c").Update(context.Background(), kdoq.ByKey("k"), set)
	if !errors.Is(err, kdoq.ErrUnsupported) || calls != 0 {
		t.Fatalf("Update = %v after %d calls; want Unsupported before a stream opens", err, calls)
	}
	reader, _ := connectLevel(t, 1)
	if _, err := reader.docs("c").Upsert(context.Background(), kdoq.ByKey("k"), set); !errors.Is(err, kdoq.ErrForbidden) {
		t.Fatalf("Upsert at Read level = %v, want Forbidden", err)
	}
}
