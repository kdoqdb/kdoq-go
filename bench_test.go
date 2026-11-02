package kdoq_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/fakeserver"
	"github.com/kdoqdb/kdoq-go/value"
)

type benchAddress struct {
	City   string
	Street string
	Zip    int32
}

type benchAccount struct {
	ID      kdoq.Key `kdoq:",key"`
	Name    string
	Age     int32
	Balance float64
	Active  bool
	Tags    []string
	Scores  map[string]int64
	Created time.Time
	Address benchAddress
	Prev    []benchAddress
}

const benchPage = 1000

func benchAccountAt(i int) benchAccount {
	return benchAccount{
		Name: "Ann Example", Age: int32(i), Balance: 1234.5, Active: true,
		Tags:    []string{"a", "b", "c", "d"},
		Scores:  map[string]int64{"math": 90, "art": 70, "pe": 80},
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Address: benchAddress{"Lisbon", "Rua Augusta", 1000},
		Prev:    []benchAddress{{"Lisbon", "Rua Augusta", 1100}, {"Lisbon", "Rua Augusta", 1200}},
	}
}

// benchClient connects to a fake server holding benchPage accounts, so a
// benchmark measures the client: decoding, the cursor and the call machinery.
func benchClient(b *testing.B) *kdoq.Client {
	b.Helper()
	ctx := context.Background()
	client, err := kdoq.Connect(ctx, "kdoq://h/shop",
		kdoq.WithTransport(fakeserver.New(fakeserver.Config{})),
		kdoq.ConnectOptions{Retry: minimalRetry})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = client.Close(ctx) })
	db, err := client.DB("")
	if err != nil {
		b.Fatal(err)
	}
	accounts := db.Collection[benchAccount]("accounts")
	for i := 0; i < benchPage; i++ {
		if _, err := accounts.Put(ctx, kdoq.Key(fmt.Sprintf("k%04d", i)), benchAccountAt(i)); err != nil {
			b.Fatal(err)
		}
	}
	return client
}

func benchGet[T any](b *testing.B) {
	client := benchClient(b)
	db, _ := client.DB("")
	docs := db.Collection[T]("accounts")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := docs.Get(ctx, "k0001"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetTyped(b *testing.B)    { benchGet[benchAccount](b) }
func BenchmarkGetDoc(b *testing.B)      { benchGet[value.Doc](b) }
func BenchmarkGetPlainMap(b *testing.B) { benchGet[map[string]any](b) }

func benchPageOf[T any](b *testing.B) {
	client := benchClient(b)
	db, _ := client.DB("")
	docs := db.Collection[T]("accounts")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		cursor, err := docs.FindCursor(ctx, "", kdoq.FindOptions{PageSize: benchPage})
		if err != nil {
			b.Fatal(err)
		}
		page, err := cursor.Next(ctx)
		if err != nil || len(page.Items) != benchPage {
			b.Fatalf("page of %d items, err %v", len(page.Items), err)
		}
		_ = cursor.Close(ctx)
	}
	b.ReportMetric(float64(b.Elapsed())/float64(b.N*benchPage), "ns/doc")
}

func BenchmarkPageTyped(b *testing.B) { benchPageOf[benchAccount](b) }
func BenchmarkPageDoc(b *testing.B)   { benchPageOf[value.Doc](b) }
