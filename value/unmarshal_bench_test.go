package value_test

import (
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

type benchAddress struct {
	City   string
	Street string
	Zip    int32
}

type benchAccount struct {
	ID      string `kdoq:",key"`
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

// benchSource is shaped like the documents the wire decoder produces: every
// document is a value.Doc and numbers keep their wire types.
func benchSource() value.Doc {
	address := func(n int32) value.Doc {
		return value.Doc{"City": "Lisbon", "Street": "Rua Augusta", "Zip": n}
	}
	return value.Doc{
		"Name":    "Ann Example",
		"Age":     int32(41),
		"Balance": 1234.5,
		"Active":  true,
		"Tags":    value.Array{"a", "b", "c", "d"},
		"Scores":  value.Doc{"math": int64(90), "art": int64(70), "pe": int64(80)},
		"Created": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"Address": address(1000),
		"Prev":    value.Array{address(1100), address(1200)},
	}
}

func BenchmarkUnmarshalFlat(b *testing.B) {
	src := value.Doc{"City": "Lisbon", "Street": "Rua Augusta", "Zip": int32(1000)}
	b.ReportAllocs()
	for b.Loop() {
		var out benchAddress
		if err := value.Unmarshal(src, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalNested(b *testing.B) {
	src := benchSource()
	b.ReportAllocs()
	for b.Loop() {
		var out benchAccount
		if err := value.UnmarshalKeyed(src, "k1", &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalMap(b *testing.B) {
	src := benchSource()
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]any
		if err := value.Unmarshal(src, &out); err != nil {
			b.Fatal(err)
		}
	}
}
