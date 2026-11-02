package kdoq_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go"
	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/value"
)

// The byte strings below are written by hand from the physical layouts in
// docs/architecture/document-v4.md and the worked example of
// docs/reference/proto/values.md ({x: Int8(-7)} is 87 07 01 01 01 00 00 80 f9).
// No expected byte is produced by the library under test.

func hx(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// v32 is an independent unsigned LEB128 writer.
func v32(n int) []byte {
	var out []byte
	for n >= 0x80 {
		out = append(out, byte(n&0x7f|0x80))
		n >>= 7
	}
	return append(out, byte(n))
}

type member struct {
	id    uint32
	name  string
	bytes []byte
}

// object builds a compact Object with four-byte ids and offsets, so that the
// reader is exercised with widths the writer does not choose itself.
func object(members ...member) []byte {
	var ids, offsets, values []byte
	for _, m := range members {
		ids = append(ids, byte(m.id), byte(m.id>>8), byte(m.id>>16), byte(m.id>>24))
		n := len(values)
		offsets = append(offsets, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
		values = append(values, m.bytes...)
	}
	body := append([]byte{4, 4}, v32(len(members))...)
	body = append(body, ids...)
	body = append(body, offsets...)
	body = append(body, values...)
	return append(append([]byte{0x87}, v32(len(body))...), body...)
}

func names(members ...member) document.Names {
	out := document.Names{}
	for _, m := range members {
		out[m.id] = m.name
	}
	return out
}

var (
	decimal  = value.Decimal128{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 0x30}
	objectID = value.ObjectID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
)

// readCases: reference bytes of one value and what a caller reads.
var readCases = []struct {
	name  string
	bytes []byte
	want  any
}{
	{"Double", hx("01000000000000f83f"), 1.5},
	{"Double NaN is kept", hx("01000000000000f87f"), math.NaN()},
	{"String", hx("8b026869"), "hi"},
	{"empty String", hx("8b00"), ""},
	{"Binary generic", hx("0503000000" + "00" + "010203"), []byte{1, 2, 3}},
	{"Binary subtype 2", hx("0506000000" + "02" + "02000000" + "0908"), value.Binary{Subtype: 2, Data: []byte{9, 8}}},
	{"Binary subtype 0x80", hx("0501000000" + "80" + "07"), value.Binary{Subtype: 0x80, Data: []byte{7}}},
	{"Binary empty", hx("0500000000" + "00"), []byte{}},
	{"ObjectId", hx("07" + "0102030405060708090a0b0c"), objectID},
	{"Boolean true", hx("0801"), true},
	{"Boolean false", hx("0800"), false},
	{"DateTime", hx("09e803000000000000"), time.UnixMilli(1000).UTC()},
	{"DateTime before 1970", hx("09" + "18fcffffffffffff"), time.UnixMilli(-1000).UTC()},
	{"Null", hx("0a"), nil},
	{"Code", hx("0d02000000" + "78" + "00"), value.Code{Source: "x"}},
	{"Int32", hx("10feffffff"), int32(-2)},
	{"Timestamp", hx("11" + "01000000" + "02000000"), value.Timestamp{Seconds: 2, Increment: 1}},
	{"Int64", hx("120500000000000000"), int64(5)},
	{"Int64 negative", hx("12ffffffffffffff7f"), int64(math.MaxInt64)},
	{"Decimal128", hx("13deadbeef0102030405060708090a0b30"), decimal},
	{"MaxKey", hx("7f"), value.MaxKey{}},
	{"MinKey", hx("ff"), value.MinKey{}},
	{"Int8", hx("80f9"), int32(-7)},
	{"Int16", hx("81d4fe"), int32(-300)},
	{"UInt8", hx("82c8"), uint32(200)},
	{"UInt16", hx("8360ea"), uint32(60000)},
	{"UInt32", hx("84ffffffff"), uint32(math.MaxUint32)},
	{"UInt64", hx("85ffffffffffffffff"), uint64(math.MaxUint64)},
	{"CalendarDate", hx("86384a0000"), value.CalendarDate{Year: 2022, Month: 1, Day: 8}},
	{"CalendarDate before 1970", hx("86" + "ffffffff"), value.CalendarDate{Year: 1969, Month: 12, Day: 31}},
	{"sequential Array", hx("890b02" + "1001000000" + "1002000000"), value.Array{int32(1), int32(2)}},
	{"empty Array", hx("890100"), value.Array{}},
	{"indexed Array", hx("8a0e" + "01" + "02" + "0005" + "1001000000" + "1002000000"), value.Array{int32(1), int32(2)}},
	{"RegExp", hx("8c04" + "0161" + "0169"), value.RegExp{Pattern: "a", Flags: "i"}},
	{"Undefined in a result", hx("06"), value.Undefined},
	{"bitmap Object", hx("880c" + "01" + "0100000000000000" + "00" + "80f9"), value.Doc{"a": int32(-7)}},
	{"compact Object", hx("8707" + "010101" + "0000" + "80f9"), value.Doc{"a": int32(-7)}},
	{"empty Object", hx("870301" + "0100"), value.Doc{}},
}

func TestReadsEveryValueTypeFromReferenceBytes(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	for _, c := range readCases {
		t.Run(c.name, func(t *testing.T) {
			// "a" is id 0 for the nested objects above; the value lives under id 1.
			fields := []member{{0, "a", nil}, {1, "v", c.bytes}}
			doc := object(fields[1:]...)
			server.Put("shop", "c", []byte(c.name), names(fields...), doc)
			got, err := docs.Get(context.Background(), kdoqKey(c.name))
			if err != nil {
				t.Fatal(err)
			}
			if !sameValue(got["v"], c.want) {
				t.Fatalf("read %#v, want %#v", got["v"], c.want)
			}
		})
	}
}

func sameValue(got, want any) bool {
	if f, ok := want.(float64); ok && math.IsNaN(f) {
		g, ok := got.(float64)
		return ok && math.IsNaN(g)
	}
	return reflect.DeepEqual(got, want)
}

// writeCases: what the client sends for a value, with the reference bytes.
var writeCases = []struct {
	name  string
	value any
	bytes []byte
}{
	{"Double", 1.5, hx("01000000000000f83f")},
	{"String", "hi", hx("8b026869")},
	{"empty String", "", hx("8b00")},
	{"multibyte String", "é", hx("8b02c3a9")},
	{"Binary generic", []byte{1, 2, 3}, hx("0503000000" + "00" + "010203")},
	{"Binary subtype 2", value.Binary{Subtype: 2, Data: []byte{9, 8}}, hx("0506000000" + "02" + "02000000" + "0908")},
	{"Binary subtype 0x80", value.Binary{Subtype: 0x80, Data: []byte{7}}, hx("0501000000" + "80" + "07")},
	{"ObjectId", objectID, hx("07" + "0102030405060708090a0b0c")},
	{"Boolean", true, hx("0801")},
	{"DateTime", time.UnixMilli(1000), hx("09e803000000000000")},
	{"Null", nil, hx("0a")},
	{"Code", value.Code{Source: "x"}, hx("0d02000000" + "78" + "00")},
	{"Int32 from int32", int32(-2), hx("10feffffff")},
	{"Int32 from int8", int8(-2), hx("10feffffff")},
	{"Int64 from int", 5, hx("120500000000000000")},
	{"Int64 from uint", uint(5), hx("120500000000000000")},
	{"Timestamp", value.Timestamp{Seconds: 2, Increment: 1}, hx("11" + "01000000" + "02000000")},
	{"Decimal128", decimal, hx("13deadbeef0102030405060708090a0b30")},
	{"CalendarDate", value.CalendarDate{Year: 2022, Month: 1, Day: 8}, hx("86384a0000")},
	{"Array", value.Array{int32(1), int32(2)}, hx("890b02" + "1001000000" + "1002000000")},
	{"empty Array", value.Array{}, hx("890100")},
}

func TestWritesEveryValueTypeAsReferenceBytes(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	for _, c := range writeCases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := docs.Put(context.Background(), kdoqKey(c.name), valueDoc("x", c.value)); err != nil {
				t.Fatal(err)
			}
			raw, ok := server.Doc("shop", "c", []byte(c.name))
			if !ok {
				t.Fatal("not stored")
			}
			want := append(hx("87"+hex.EncodeToString(v32(5+len(c.bytes)))+"0101010000"), c.bytes...)
			if !bytes.Equal(raw.Bytes, want) {
				t.Fatalf("sent %x\nwant %x", raw.Bytes, want)
			}
			if raw.Names[0] != "x" || len(raw.Names) != 1 {
				t.Fatalf("dictionary = %v", raw.Names)
			}
		})
	}
}

func TestWritesObjectsWithOneDictionaryAndSortedDirectory(t *testing.T) {
	h, server := connectDocs(t)
	db, _ := h.client.DB("")
	ordered := db.Collection[value.D]("c")
	// Names get ids in first-use order, top down; nested names continue the
	// same dictionary.
	doc := value.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: value.D{{Key: "c", Value: int32(2)}}}}
	if _, err := ordered.Put(context.Background(), "k", doc); err != nil {
		t.Fatal(err)
	}
	raw, _ := server.Doc("shop", "c", []byte("k"))
	inner := hx("870a" + "010101" + "02" + "00" + "1002000000")
	want := append(hx("8718"+"010102"+"0001"+"0005"+"1001000000"), inner...)
	if !bytes.Equal(raw.Bytes, want) {
		t.Fatalf("sent %x\nwant %x", raw.Bytes, want)
	}
	if !reflect.DeepEqual(raw.Names, document.Names{0: "a", 1: "b", 2: "c"}) {
		t.Fatalf("dictionary = %v", raw.Names)
	}
	got, err := ordered.Get(context.Background(), "k")
	if err != nil || len(got) != 2 {
		t.Fatalf("Get = %v, %v", got, err)
	}
}

func TestEmptyDocumentsDeclareTheirContext(t *testing.T) {
	h, server := connectDocs(t)
	docs := h.docs("c")
	if _, err := docs.Put(context.Background(), "k", value.Doc{}); err != nil {
		t.Fatalf("Put of an empty document: %v", err)
	}
	raw, _ := server.Doc("shop", "c", []byte("k"))
	if !bytes.Equal(raw.Bytes, hx("870301"+"0100")) {
		t.Fatalf("sent %x", raw.Bytes)
	}
}

func kdoqKey(name string) kdoq.Key { return kdoq.Key(name) }

func valueDoc(name string, v any) value.Doc { return value.Doc{name: v} }
