package document

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/internal/source"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

type streamAddress struct {
	City string
	Zip  int32
}

type streamBase struct {
	Origin string
}

type streamDoc struct {
	ID      string `kdoq:",key"`
	Name    string
	Age     int32
	Wide    uint8
	Ratio   float32
	Big     uint64
	Active  bool
	Nick    *string
	Missing *string
	Tags    []string
	Fixed   [2]int16
	Scores  map[string]int64
	Created time.Time
	Raw     []byte
	Any     any
	Home    streamAddress
	Work    *streamAddress
	Prev    []streamAddress
	Matrix  [][]int32
	Empty   []int
	Doc     value.Doc
	streamBase
}

// streamSample is a document that fits streamDoc. The client encoder writes no
// unsigned integers, so those appear only when unsigned is set.
func streamSample(unsigned bool) value.Doc {
	doc := value.Doc{
		"Name":    "Ann",
		"Age":     int32(41),
		"Wide":    int32(200),
		"Ratio":   1.5,
		"Active":  true,
		"Nick":    nil,
		"Tags":    value.Array{"a", "b"},
		"Fixed":   value.Array{int32(-1), int64(2)},
		"Scores":  value.Doc{"math": int64(90), "art": int32(70)},
		"Created": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"Raw":     []byte{1, 2, 3},
		"Any":     value.Doc{"deep": value.Array{int32(1), "x"}},
		"Home":    value.Doc{"City": "Lisbon", "Zip": int32(1000)},
		"Work":    value.Doc{"City": "Porto", "Zip": int32(4000), "Extra": value.Array{true}},
		"Prev":    value.Array{value.Doc{"City": "Faro", "Zip": int32(8000)}, value.Doc{"City": "Braga"}},
		"Matrix":  value.Array{value.Array{int32(1), int32(2)}, value.Array{}, value.Array{int32(3)}},
		"Empty":   value.Array{},
		"Doc":     value.Doc{"k": "v"},
		"Origin":  "embedded",
		"Unknown": value.Doc{"a": value.Array{value.Doc{"b": int32(1)}}, "c": "d"},
	}
	if unsigned {
		doc["Big"] = uint64(1 << 63)
		doc["Wide"] = uint32(200)
		doc["Prev"] = value.Array{value.Doc{"City": "Faro", "Zip": uint32(8000)}, value.Doc{"City": "Braga", "Zip": uint32(70000)}}
		doc["Fixed"] = value.Array{int32(-1), uint32(2)}
	}
	return doc
}

// A style is a way of writing a document: the client's, or the one a server
// uses, with bitmap objects, indexed arrays and compact integers.
type style struct {
	name   string
	sample value.Doc
	encode func(*testing.T, value.Doc) (Names, []byte)
}

func styles() []style {
	return []style{
		{"client", streamSample(false), func(t *testing.T, doc value.Doc) (Names, []byte) { return encodeSample(t, doc) }},
		{"server", streamSample(true), func(t *testing.T, doc value.Doc) (Names, []byte) { return serverEncode(t, doc) }},
	}
}

func serverEncode(t testing.TB, doc value.Doc) (Names, []byte) {
	t.Helper()
	dict := NewDictionary()
	body, err := serverValue(dict, doc)
	if err != nil {
		t.Fatal(err)
	}
	names := Names{}
	for name, id := range dict.ids {
		names[id] = name
	}
	return names, body
}

func serverValue(dict *Dictionary, v any) ([]byte, error) {
	switch x := v.(type) {
	case int32:
		switch {
		case x == int32(int8(x)):
			return []byte{0x80, byte(int8(x))}, nil
		case x == int32(int16(x)):
			return binary.LittleEndian.AppendUint16([]byte{0x81}, uint16(int16(x))), nil
		}
	case uint32:
		switch {
		case x <= 0xff:
			return []byte{0x82, byte(x)}, nil
		case x <= 0xffff:
			return binary.LittleEndian.AppendUint16([]byte{0x83}, uint16(x)), nil
		}
		return binary.LittleEndian.AppendUint32([]byte{0x84}, x), nil
	case uint64:
		return binary.LittleEndian.AppendUint64([]byte{0x85}, x), nil
	case value.Array:
		var offsets, values []byte
		for _, item := range x {
			offsets = binary.LittleEndian.AppendUint16(offsets, uint16(len(values)))
			encoded, err := serverValue(dict, item)
			if err != nil {
				return nil, err
			}
			values = append(values, encoded...)
		}
		body := append(append([]byte{2}, wire.AppendU32V(nil, uint32(len(x)))...), offsets...)
		body = append(body, values...)
		return append(append([]byte{0x8a}, wire.AppendU32V(nil, uint32(len(body)))...), body...), nil
	case value.Doc:
		type member struct {
			id    uint32
			value []byte
		}
		var members []member
		for _, e := range sortedEntries(x) {
			encoded, err := serverValue(dict, e.Value)
			if err != nil {
				return nil, err
			}
			members = append(members, member{dict.idOf(e.Key), encoded})
		}
		sort.Slice(members, func(i, j int) bool { return members[i].id < members[j].id })
		var mask uint64
		var offsets, values []byte
		for _, m := range members {
			if m.id >= 64 {
				return nil, fmt.Errorf("sample object has too many field names for a bitmap")
			}
			mask |= 1 << m.id
			offsets = binary.LittleEndian.AppendUint16(offsets, uint16(len(values)))
			values = append(values, m.value...)
		}
		body := binary.LittleEndian.AppendUint64([]byte{2}, mask)
		body = append(append(body, offsets...), values...)
		return append(append([]byte{0x88}, wire.AppendU32V(nil, uint32(len(body)))...), body...), nil
	}
	return Encode(dict, v)
}

func encodeSample(t testing.TB, doc value.Doc) (Names, []byte) {
	t.Helper()
	dict := NewDictionary()
	body, err := EncodeDocument(dict, doc)
	if err != nil {
		t.Fatal(err)
	}
	names := Names{}
	for name, id := range dict.ids {
		names[id] = name
	}
	return names, body
}

func viaTree(names Names, b []byte, dst any) error {
	v, err := Decode(names, b)
	if err != nil {
		return err
	}
	return value.UnmarshalKeyed(v, "k1", dst)
}

func viaStream(names Names, b []byte, dst any) error {
	s := NewStream(names, b)
	defer s.Release()
	return source.Unmarshal(s, "k1", true, dst)
}

func TestStreamMatchesTree(t *testing.T) {
	for _, st := range styles() {
		names, body := st.encode(t, st.sample)
		for name, newDst := range map[string]func() any{
			"struct": func() any { return new(streamDoc) },
			"map":    func() any { return new(map[string]any) },
			"doc":    func() any { return new(value.Doc) },
			"any":    func() any { return new(any) },
			"empty":  func() any { return new(struct{}) },
		} {
			t.Run(st.name+"/"+name, func(t *testing.T) {
				want, got := newDst(), newDst()
				if err := viaTree(names, body, want); err != nil {
					t.Fatalf("tree: %v", err)
				}
				if err := viaStream(names, body, got); err != nil {
					t.Fatalf("stream: %v", err)
				}
				if !reflect.DeepEqual(want, got) {
					t.Errorf("stream = %+v\nwant     %+v", got, want)
				}
			})
		}
	}
}

// A document that does not fit the destination fails through the stream too,
// so that callers can read it again through the tree for the precise error.
func TestStreamRejectsMismatch(t *testing.T) {
	for name, doc := range map[string]value.Doc{
		"string for number": {"Age": "x"},
		"number overflow":   {"Wide": int32(300)},
		"fraction":          {"Age": 1.5},
		"object for list":   {"Tags": value.Doc{"a": "b"}},
		"element type":      {"Tags": value.Array{int32(1)}},
		"array too long":    {"Fixed": value.Array{int32(1), int32(2), int32(3)}},
		"nested mismatch":   {"Home": value.Doc{"Zip": "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			names, body := encodeSample(t, doc)
			if err := viaTree(names, body, new(streamDoc)); err == nil {
				t.Fatal("the tree accepted the document")
			}
			if err := viaStream(names, body, new(streamDoc)); err == nil {
				t.Error("the stream accepted the document")
			}
		})
	}
}

// The stream never accepts bytes the tree decoder rejects, and reads what
// both accept alike, whatever the damage.
func TestStreamIsAsStrictAsTree(t *testing.T) {
	for _, st := range styles() {
		names, body := st.encode(t, st.sample)
		t.Run(st.name, func(t *testing.T) { checkStrict(t, names, body) })
	}
}

func checkStrict(t *testing.T, names Names, body []byte) {
	check := func(t *testing.T, damaged []byte) {
		t.Helper()
		_, treeErr := Decode(names, damaged)
		for _, newDst := range []func() any{
			func() any { return new(streamDoc) },
			func() any { return new(struct{}) },
		} {
			want, got := newDst(), newDst()
			wantErr, gotErr := viaTree(names, damaged, want), viaStream(names, damaged, got)
			if treeErr != nil && gotErr == nil {
				t.Fatalf("the stream accepted bytes the tree rejects (%v): % x", treeErr, damaged)
			}
			if wantErr == nil && gotErr == nil && !reflect.DeepEqual(want, got) {
				t.Fatalf("different results for % x:\nstream %+v\ntree   %+v", damaged, got, want)
			}
		}
	}
	for n := 0; n < len(body); n++ {
		check(t, body[:n])
	}
	for i := range body {
		for _, xor := range []byte{0x01, 0x80, 0xff} {
			damaged := append([]byte(nil), body...)
			damaged[i] ^= xor
			check(t, damaged)
		}
	}
	check(t, append(append([]byte(nil), body...), 0))
}

func TestStreamReuseKeepsNothing(t *testing.T) {
	names, body := encodeSample(t, streamSample(false))
	first := new(streamDoc)
	if err := viaStream(names, body, first); err != nil {
		t.Fatal(err)
	}
	// A failed read leaves frames and directories behind on the pooled
	// Stream; the next read must start clean.
	_ = viaStream(names, body[:len(body)/2], new(streamDoc))
	second := new(streamDoc)
	if err := viaStream(names, body, second); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("a reused Stream read %+v, then %+v", first, second)
	}
}
