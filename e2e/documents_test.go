//go:build e2e

package e2e_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/value"
)

type person struct {
	ID      kdoq.Key           `kdoq:",key"`
	Name    string             `kdoq:"name"`
	Age     int64              `json:"age"`
	Balance value.Decimal128   `kdoq:"balance"`
	Born    value.CalendarDate `kdoq:"born"`
	Seen    time.Time          `kdoq:"seen"`
	Tags    []string           `kdoq:"tags,omitempty"`
	Extra   map[string]any     `kdoq:"extra,omitempty"`
	Stamp   value.Timestamp    `kdoq:"stamp"`
	Raw     []byte             `kdoq:"raw"`
	Skipped string             `kdoq:"-"`
}

func TestDocumentsRoundTripTypedValues(t *testing.T) {
	srv := startServer(t, settings{})
	client := srv.connect(t, "", "")
	docs := collection(t, client, "shop", "people")
	ctx := opContext(t)
	db, _ := client.DB("shop")
	people := db.Collection[person]("people")

	balance, err := value.ParseDecimal128("12345678901234567890.12")
	if err != nil {
		t.Fatal(err)
	}
	want := person{
		Name:    "Ann",
		Age:     31,
		Balance: balance,
		Born:    value.CalendarDate{Year: 1990, Month: 2, Day: 3},
		Seen:    time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
		Tags:    []string{"a", "b"},
		Stamp:   value.Timestamp{Seconds: 7, Increment: 9},
		Raw:     []byte{0, 1, 2},
	}
	if _, err := people.Put(ctx, "ann", want); err != nil {
		t.Fatal(err)
	}
	got, err := people.Get(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	want.ID = "ann"
	if got.ID != want.ID || got.Name != want.Name || got.Age != want.Age || got.Balance != want.Balance ||
		got.Born != want.Born || !got.Seen.Equal(want.Seen) || got.Stamp != want.Stamp ||
		string(got.Raw) != string(want.Raw) || len(got.Tags) != 2 {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	generic, err := docs.Get(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if generic["name"] != "Ann" || generic["age"] != int64(31) || generic["balance"] != balance {
		t.Fatalf("generic Get = %v", generic)
	}
}

func TestMissingKeysAreNotFoundAndDeleteRemoves(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)

	if _, err := docs.Get(ctx, "missing"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get = %v, want NotFound", err)
	}
	if _, err := docs.Put(ctx, "k", value.Doc{"n": int32(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Get(ctx, "k"); !errors.Is(err, kdoq.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want NotFound", err)
	}
}

func TestInsertGeneratesAKeyAndBinaryKeysSurvive(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	ctx := opContext(t)

	generated, err := docs.Insert(ctx, value.Doc{"n": int32(1)})
	if err != nil || generated == "" {
		t.Fatalf("Insert = %q, %v", generated, err)
	}
	if _, err := docs.Insert(ctx, value.Doc{"n": int32(2)}, kdoq.WriteOptions{Key: generated}); !errors.Is(err, kdoq.ErrAlreadyExists) {
		t.Fatalf("Insert of an existing key = %v, want AlreadyExists", err)
	}

	binary := kdoq.Key("\x00\xff\x10")
	if _, err := docs.Put(ctx, binary, value.Doc{"n": int32(3)}, kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed}); err != nil {
		t.Fatal(err)
	}
	got, err := docs.Get(ctx, binary)
	if err != nil || got["n"] != int32(3) {
		t.Fatalf("Get of a binary key = %v, %v", got, err)
	}
	if _, ok := binary.Text(); ok || binary.String() != "00ff10" {
		t.Fatalf("binary key text = %q", binary.String())
	}
}

func TestAnUndefinedValueIsRejectedBeforeTheServer(t *testing.T) {
	srv := startServer(t, settings{})
	docs := collection(t, srv.connect(t, "", ""), "shop", "c")
	_, err := docs.Put(opContext(t), "k", value.Doc{"u": value.Undefined})
	if !errors.Is(err, kdoq.ErrInvalidDocument) {
		t.Fatalf("Put = %v, want InvalidDocument", err)
	}
}
