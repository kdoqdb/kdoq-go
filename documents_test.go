package kdoq_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

type user struct {
	ID   kdoq.Key `kdoq:",key"`
	Name string   `kdoq:"name"`
	Age  int      `json:"age,omitempty"`
}

func connectDocs(t *testing.T, opts ...kdoq.ConnectOption) (*harness, *fakeserver.Server) {
	t.Helper()
	server := newServer(fakeserver.Config{})
	return connect(t, server, "kdoq://h/shop", opts...), server
}

func users(t *testing.T, h *harness) *kdoq.Collection[user] {
	t.Helper()
	db, err := h.client.DB("")
	if err != nil {
		t.Fatal(err)
	}
	return db.Collection[user]("users")
}

func TestPutThenGetTypedDocument(t *testing.T) {
	h, _ := connectDocs(t)
	c := users(t, h)
	ctx := context.Background()
	key, err := c.Put(ctx, "u1", user{Name: "Ann", Age: 31})
	if err != nil || key != "u1" {
		t.Fatalf("Put = %q, %v", key, err)
	}
	got, err := c.Get(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (user{ID: "u1", Name: "Ann", Age: 31}); got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
	if c.Database() != "shop" || c.Name() != "users" {
		t.Fatalf("namespace = %s.%s", c.Database(), c.Name())
	}
}

func TestPutReplacesAnExistingDocument(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	for _, n := range []int32{1, 2} {
		if _, err := docs.Put(ctx, "k", value.Doc{"n": n}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := docs.Get(ctx, "k")
	if err != nil || got["n"] != int32(2) {
		t.Fatalf("Get = %v, %v", got, err)
	}
}

func TestGenericDocumentsKeepNestedStructure(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	in := value.Doc{
		"name":  "Ann",
		"tags":  value.Array{"a", "b"},
		"inner": value.Doc{"deep": value.Doc{"n": int64(1) << 40}},
		"ratio": 0.25,
		"flag":  true,
		"none":  nil,
	}
	if _, err := docs.Put(ctx, "k", in); err != nil {
		t.Fatal(err)
	}
	got, err := docs.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if value.Format(got, 0) != value.Format(in, 0) {
		t.Fatalf("Get = %s, want %s", value.Format(got, 0), value.Format(in, 0))
	}
}

func TestGetMissingKeyIsNotFound(t *testing.T) {
	h, _ := connectDocs(t)
	_, err := h.docs("c").Get(context.Background(), "absent")
	if !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	var e *kdoq.Error
	if !errors.As(err, &e) || e.Code != kdoq.CodeNotFound || e.Outcome != kdoq.OutcomeNotApplied || e.RequestID == 0 {
		t.Fatalf("error = %+v", e)
	}
}

func TestDeleteRemovesTheDocument(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	docs.Put(ctx, "k", value.Doc{"a": int32(1)})
	if key, err := docs.Delete(ctx, "k"); err != nil || key != "k" {
		t.Fatalf("Delete = %q, %v", key, err)
	}
	if _, err := docs.Get(ctx, "k"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if _, err := docs.Delete(ctx, "k"); err != nil {
		t.Fatalf("deleting a missing key must succeed: %v", err)
	}
}

func TestInsertTakesItsKeyFromOptionThenFieldThenServer(t *testing.T) {
	h, server := connectDocs(t)
	c := users(t, h)
	ctx := context.Background()

	if key, err := c.Insert(ctx, user{ID: "from-field", Name: "a"}, kdoq.WriteOptions{Key: "from-option"}); err != nil || key != "from-option" {
		t.Fatalf("option must win: %q, %v", key, err)
	}
	if key, err := c.Insert(ctx, user{ID: "from-field", Name: "b"}); err != nil || key != "from-field" {
		t.Fatalf("field must be used: %q, %v", key, err)
	}
	key, err := c.Insert(ctx, user{Name: "c"})
	if err != nil || key == "" {
		t.Fatalf("server must generate a key: %q, %v", key, err)
	}
	got, err := c.Get(ctx, key)
	if err != nil || got.Name != "c" || got.ID != key {
		t.Fatalf("Get(%q) = %+v, %v", key, got, err)
	}
	if _, ok := server.Doc("shop", "users", []byte("from-field")); !ok {
		t.Fatal("the tagged key field must be stored as the key")
	}
}

func TestInsertOfALiveKeyIsAlreadyExists(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	if _, err := docs.Insert(ctx, value.Doc{"a": int32(1)}, kdoq.WriteOptions{Key: "k"}); err != nil {
		t.Fatal(err)
	}
	_, err := docs.Insert(ctx, value.Doc{"a": int32(2)}, kdoq.WriteOptions{Key: "k"})
	if !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatalf("second Insert = %v, want ErrAlreadyExists", err)
	}
}

func TestKeysCarryArbitraryBytes(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	keys := []kdoq.Key{"\x00\xff/", "ünïcode", "", "a\x00b"}
	for _, key := range keys {
		if _, err := docs.Put(ctx, key, value.Doc{"n": int32(len(key))}); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
		if _, ok := server.Doc("shop", "c", key.Bytes()); !ok {
			t.Fatalf("key %q did not arrive byte for byte", key)
		}
		if _, err := docs.Get(ctx, key); err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
	}
}

func TestDurabilityMapsToTheProtocolField(t *testing.T) {
	h, server := connectDocs(t)
	var mu sync.Mutex
	var seen []*wire.Fields
	server.Intercept = func(c *fakeserver.Call) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, c.Payload)
		return false
	}
	docs := h.docs("c")
	ctx := context.Background()
	doc := value.Doc{"a": int32(1)}
	docs.Put(ctx, "k", doc)
	docs.Put(ctx, "k", doc, kdoq.WriteOptions{Durability: kdoq.DurabilityDurable})
	docs.Put(ctx, "k", doc, kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed})
	docs.Delete(ctx, "k", kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed})
	docs.Insert(ctx, doc, kdoq.WriteOptions{Durability: kdoq.DurabilityDurable})

	want := []struct {
		present bool
		value   uint16
	}{{false, 0}, {true, 0}, {true, 1}, {true, 1}, {true, 0}}
	if len(seen) != len(want) {
		t.Fatalf("saw %d commands, want %d", len(seen), len(want))
	}
	for i, p := range seen {
		group, ok, err := p.Group(4)
		if err != nil || ok != want[i].present {
			t.Fatalf("command %d: options group present=%v, want %v (%v)", i, ok, want[i].present, err)
		}
		if ok {
			if got, _ := group.RequireU16(1); got != want[i].value {
				t.Fatalf("command %d: durability %d, want %d", i, got, want[i].value)
			}
		}
	}
}

func TestInvalidDocumentsAreRejectedBeforeTheNetwork(t *testing.T) {
	h, server := connectDocs(t)
	calls := 0
	server.Intercept = func(*fakeserver.Call) bool { calls++; return false }
	docs := h.docs("c")
	cases := map[string]value.Doc{
		"undefined":           {"a": value.Undefined},
		"nested undefined":    {"a": value.Doc{"b": value.Array{value.Undefined}}},
		"minkey":              {"a": value.MinKey{}},
		"maxkey":              {"a": value.MaxKey{}},
		"regexp":              {"a": value.RegExp{Pattern: "x"}},
		"unsigned overflow":   {"a": uint64(1) << 63},
		"invalid date":        {"a": value.CalendarDate{Year: 2020, Month: 2, Day: 31}},
		"nil document":        nil,
		"invalid utf-8":       {"a": "\xff"},
		"invalid field name":  {"\xff": int32(1)},
		"unsupported channel": {"a": make(chan int)},
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := docs.Put(context.Background(), "k", doc)
			var e *kdoq.Error
			if !errors.Is(err, kdoq.ErrInvalidDocument) || !errors.As(err, &e) || e.Outcome != kdoq.OutcomeNotApplied {
				t.Fatalf("Put error = %v, want ErrInvalidDocument/NotApplied", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("%d invalid documents reached the server", calls)
	}
}

func TestStructsMapThroughTagsAndKeyField(t *testing.T) {
	h, server := connectDocs(t)
	c := users(t, h)
	ctx := context.Background()
	if _, err := c.Insert(ctx, user{ID: "k", Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := server.Doc("shop", "users", []byte("k"))
	for _, name := range raw.Names {
		if name == "ID" || name == "age" {
			t.Fatalf("the key field and an omitted empty field must not be stored (names %v)", raw.Names)
		}
	}
	if len(raw.Names) != 1 || raw.Names[0] != "name" {
		t.Fatalf("names = %v, want only name", raw.Names)
	}
}

func TestUnknownFieldsInTheDocumentAreIgnored(t *testing.T) {
	h, _ := connectDocs(t)
	ctx := context.Background()
	h.docs("users").Put(ctx, "k", value.Doc{"name": "Ann", "extra": int32(1)})
	got, err := users(t, h).Get(ctx, "k")
	if err != nil || got.Name != "Ann" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestKeyFormatting(t *testing.T) {
	if s, ok := kdoq.Key("héllo").Text(); !ok || s != "héllo" {
		t.Fatalf("Text() = %q, %v", s, ok)
	}
	bin := kdoq.Key("\x00\xff")
	if _, ok := bin.Text(); ok {
		t.Fatal("binary key reported as text")
	}
	if got := bin.String(); got != "00ff" {
		t.Fatalf("String() = %q, want hex", got)
	}
	if got := kdoq.Key("ok").String(); got != "ok" {
		t.Fatalf("String() = %q", got)
	}
	if string(kdoq.Key("ab").Bytes()) != "ab" {
		t.Fatal("Bytes() differs")
	}
}

func TestClientAndHandlesAreSafeForConcurrentUse(t *testing.T) {
	h, _ := connectDocs(t)
	docs := h.docs("c")
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := kdoq.Key(string(rune('a' + i)))
			for n := range 8 {
				if _, err := docs.Put(ctx, key, value.Doc{"n": int32(n)}); err != nil {
					t.Error(err)
					return
				}
				got, err := docs.Get(ctx, key)
				if err != nil || got["n"] != int32(n) {
					t.Errorf("Get = %v, %v", got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
