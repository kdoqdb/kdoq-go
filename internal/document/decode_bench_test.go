package document

import (
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/internal/source"
	"github.com/kdoqdb/kdoq-go/value"
)

// benchBody encodes a document shaped like the ones the end-to-end benchmarks
// read and returns it with the dictionary names a server would announce.
func benchBody(b *testing.B) (Names, []byte) {
	b.Helper()
	address := func(n int32) value.Doc {
		return value.Doc{"City": "Lisbon", "Street": "Rua Augusta", "Zip": n}
	}
	doc := value.Doc{
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
	dict := NewDictionary()
	body, err := EncodeDocument(dict, doc)
	if err != nil {
		b.Fatal(err)
	}
	names := Names{}
	for name, id := range dict.ids {
		names[id] = name
	}
	return names, body
}

func BenchmarkDecode(b *testing.B) {
	names, body := benchBody(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(names, body); err != nil {
			b.Fatal(err)
		}
	}
}

type benchAddress struct {
	City   string
	Street string
	Zip    int32
}

type benchAccount struct {
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

// BenchmarkTreeUnmarshal reads a document into a struct through the tree.
func BenchmarkTreeUnmarshal(b *testing.B) {
	names, body := benchBody(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		tree, err := Decode(names, body)
		if err != nil {
			b.Fatal(err)
		}
		var out benchAccount
		if err := value.Unmarshal(tree, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStreamUnmarshal reads the same document into the same struct
// without a tree.
func BenchmarkStreamUnmarshal(b *testing.B) {
	names, body := benchBody(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		stream := NewStream(names, body)
		var out benchAccount
		err := source.Unmarshal(stream, "", false, &out)
		stream.Release()
		if err != nil {
			b.Fatal(err)
		}
	}
}
