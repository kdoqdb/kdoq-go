package wire_test

// Reference bytes for frames and the field grammar, as independent evidence
// that the Go implementation matches the server.
//
// Origin: the vectors are copied from the Rust protocol crate's wire tests,
// crates/kdoq-proto/tests/integration/wire.rs (DOCUMENT_GET, coalesced frames,
// invalid prefixes, field header and length rules, minimal U32V), and from the
// worked examples of docs/reference/proto/README.md and
// docs/reference/proto/values.md (DOCUMENT_UPSERT, DICTIONARY and
// INPUT_VALUE). Every expected byte was written by hand there; none is
// produced by this package.

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"

	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// DOCUMENT_GET for Test.Docs, request 42, key 00 ff 2f (41 bytes).
var documentGet = []byte{
	0x00, 0x01, 0x00, 0x00, 0x21, 0x00, 0x00, 0x00, // prefix: kind 0x0100, payload 33
	0x81, 0x02, 0x01, 0x04, // operation 0x0401
	0x82, 0x08, 0x2a, 0, 0, 0, 0, 0, 0, 0, // request_id 42
	0x83, 0x11, // payload group, 17 bytes
	0x81, 0x04, 0x54, 0x65, 0x73, 0x74, // database "Test"
	0x82, 0x04, 0x44, 0x6f, 0x63, 0x73, // collection "Docs"
	0x83, 0x03, 0x00, 0xff, 0x2f, // key
}

func TestWriterProducesTheReferenceCommand(t *testing.T) {
	var ns wire.Builder
	ns.String(1, "Test").String(2, "Docs").Binary(3, []byte{0x00, 0xff, 0x2f})
	var payload wire.Builder
	payload.U16(1, 0x0401).U64(2, 42).Group(3, &ns)
	if got := wire.Encode(wire.KindCommand, payload.Bytes()); !bytes.Equal(got, documentGet) {
		t.Fatalf("encoded %x\nwant    %x", got, documentGet)
	}
}

func TestReaderParsesTheReferenceCommandUnderFragmentation(t *testing.T) {
	for name, r := range map[string]io.Reader{
		"whole":      bytes.NewReader(documentGet),
		"one byte":   iotest.OneByteReader(bytes.NewReader(documentGet)),
		"half reads": iotest.HalfReader(bytes.NewReader(documentGet)),
	} {
		frame, err := wire.ReadFrame(r, 64<<20)
		if err != nil || frame.Kind != 0x0100 {
			t.Fatalf("%s: ReadFrame = %+v, %v", name, frame, err)
		}
		command, err := wire.Parse(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if op, _ := command.RequireU16(1); op != 0x0401 {
			t.Fatalf("%s: operation %#x", name, op)
		}
		if id, _ := command.RequireU64(2); id != 42 {
			t.Fatalf("%s: request %d", name, id)
		}
		group, ok, err := command.Group(3)
		if !ok || err != nil {
			t.Fatalf("%s: payload group: %v", name, err)
		}
		if db, _ := group.RequireString(1); db != "Test" {
			t.Fatalf("%s: database %q", name, db)
		}
		if coll, _ := group.RequireString(2); coll != "Docs" {
			t.Fatalf("%s: collection %q", name, coll)
		}
		if key, _ := group.RequireBytes(3); !bytes.Equal(key, []byte{0, 0xff, 0x2f}) {
			t.Fatalf("%s: key %x", name, key)
		}
	}
}

func TestInputEndAndWorkedUpsertExample(t *testing.T) {
	if got := wire.Encode(wire.KindInputEnd, nil); !bytes.Equal(got, []byte{0x02, 0x01, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("INPUT_END = %x", got)
	}
	// COMMAND (39 bytes) of the DOCUMENT_UPSERT example: key "k", operation 0x0406.
	var ns wire.Builder
	ns.String(1, "Test").String(2, "Docs").Binary(3, []byte("k"))
	var payload wire.Builder
	payload.U16(1, 0x0406).U64(2, 42).Group(3, &ns)
	want := []byte{
		0x00, 0x01, 0x00, 0x00, 0x1f, 0x00, 0x00, 0x00,
		0x81, 0x02, 0x06, 0x04,
		0x82, 0x08, 0x2a, 0, 0, 0, 0, 0, 0, 0,
		0x83, 0x0f,
		0x81, 0x04, 0x54, 0x65, 0x73, 0x74,
		0x82, 0x04, 0x44, 0x6f, 0x63, 0x73,
		0x83, 0x01, 0x6b,
	}
	if got := wire.Encode(wire.KindCommand, payload.Bytes()); !bytes.Equal(got, want) {
		t.Fatalf("UPSERT command %x\nwant %x", got, want)
	}
	// The ITEM of the reply: key "k", flags 4.
	item := []byte{0x01, 0x02, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00, 0x81, 0x01, 0x6b, 0x82, 0x01, 0x04}
	frame, err := wire.ReadFrame(bytes.NewReader(item), 1024)
	if err != nil || frame.Kind != wire.KindItem {
		t.Fatalf("ReadFrame = %+v, %v", frame, err)
	}
	fields, _ := wire.Parse(frame.Payload)
	if key, _ := fields.RequireBytes(1); string(key) != "k" {
		t.Fatalf("key %q", key)
	}
	if flags, _ := fields.RequireU8(2); flags != 4 {
		t.Fatalf("flags %d", flags)
	}
}

func TestWorkedDictionaryAndInputValueExample(t *testing.T) {
	var entry wire.Builder
	entry.U32(1, 0).String(2, "x")
	var dict wire.Builder
	dict.U64(1, 1).Group(2, &entry)
	wantDictionary := []byte{
		0x00, 0x03, 0x00, 0x00, 0x15, 0x00, 0x00, 0x00,
		0x81, 0x08, 0x01, 0, 0, 0, 0, 0, 0, 0,
		0x82, 0x09, 0x81, 0x04, 0, 0, 0, 0, 0x82, 0x01, 0x78,
	}
	if got := wire.Encode(wire.KindDictionary, dict.Bytes()); !bytes.Equal(got, wantDictionary) {
		t.Fatalf("DICTIONARY %x\nwant %x", got, wantDictionary)
	}
	var input wire.Builder
	input.U64(1, 1).Binary(2, []byte{0x87, 0x07, 0x01, 0x01, 0x01, 0x00, 0x00, 0x80, 0xf9})
	wantInput := []byte{
		0x01, 0x01, 0x00, 0x00, 0x15, 0x00, 0x00, 0x00,
		0x81, 0x08, 0x01, 0, 0, 0, 0, 0, 0, 0,
		0x82, 0x09, 0x87, 0x07, 0x01, 0x01, 0x01, 0x00, 0x00, 0x80, 0xf9,
	}
	if got := wire.Encode(wire.KindInputValue, input.Bytes()); !bytes.Equal(got, wantInput) {
		t.Fatalf("INPUT_VALUE %x\nwant %x", got, wantInput)
	}
}

func TestCoalescedFramesKeepTheirBoundariesAndInvalidPrefixesFail(t *testing.T) {
	// INPUT_END followed by BEGIN, both empty.
	r := bytes.NewReader([]byte{0x02, 0x01, 0, 0, 0, 0, 0, 0, 0x00, 0x02, 0, 0, 0, 0, 0, 0})
	for _, kind := range []uint16{0x0102, 0x0200} {
		frame, err := wire.ReadFrame(r, 1024)
		if err != nil || frame.Kind != kind || len(frame.Payload) != 0 {
			t.Fatalf("ReadFrame = %+v, %v; want kind %#x", frame, err, kind)
		}
	}
	if _, err := wire.ReadFrame(r, 1024); err != io.EOF {
		t.Fatalf("a clean end of stream must be io.EOF, got %v", err)
	}
	for name, prefix := range map[string][]byte{
		"kind zero":              {0, 0, 0, 0, 0, 0, 0, 0},
		"nonzero flags":          {1, 0, 1, 0, 0, 0, 0, 0},
		"nonzero high flags":     {1, 0, 0, 0x80, 0, 0, 0, 0},
		"length beyond ceilings": {1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff},
	} {
		if _, err := wire.ReadFrame(bytes.NewReader(prefix), 64<<20); !errors.Is(err, wire.ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := wire.ReadFrame(bytes.NewReader([]byte{1, 0, 0}), 1024); !errors.Is(err, wire.ErrIncomplete) {
		t.Errorf("truncated prefix: %v", err)
	}
	if _, err := wire.ReadFrame(bytes.NewReader([]byte{1, 0, 0, 0, 4, 0, 0, 0, 9}), 1024); !errors.Is(err, wire.ErrIncomplete) {
		t.Errorf("truncated payload: %v", err)
	}
}

func TestLengthIsCheckedAgainstTheCeilingBeforeAnyAllocation(t *testing.T) {
	// 8 + 1017 bytes exceeds the ceiling of 1024 by one; the payload is never read.
	over := &countingReader{data: []byte{1, 0, 0, 0, 0xf9, 0x03, 0, 0}}
	if _, err := wire.ReadFrame(over, 1024); !errors.Is(err, wire.ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
	if over.reads != 1 {
		t.Fatalf("the payload was read after an oversized prefix (%d reads)", over.reads)
	}
	exact := append([]byte{1, 0, 0, 0, 0xf8, 0x03, 0, 0}, make([]byte, 1016)...)
	frame, err := wire.ReadFrame(bytes.NewReader(exact), 1024)
	if err != nil || len(frame.Payload) != 1016 {
		t.Fatalf("a frame of exactly the ceiling: %+v, %v", len(frame.Payload), err)
	}
}

type countingReader struct {
	data  []byte
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, nil
}

func TestEveryFieldHeaderAndLengthRuleRejectsTheInvalidEncoding(t *testing.T) {
	required := []byte{0x81, 0x02, 0x07, 0x00}
	with := func(extra ...byte) []byte { return append(append([]byte(nil), required...), extra...) }
	for name, b := range map[string][]byte{
		"reserved bit set":           with(0xc2, 0x01, 'x'),
		"reserved bit set, R clear":  with(0x42, 0x01, 'x'),
		"id 64 as the reserved bit":  with(0x40, 0x00),
		"id zero, R=1":               with(0x80, 0x00),
		"id zero, R=0":               with(0x00, 0x00),
		"non-minimal length":         with(0x82, 0x81, 0x00, 'x'),
		"overlong length":            with(0x82, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00),
		"length above u32":           with(0x82, 0xff, 0xff, 0xff, 0xff, 0x1f),
		"truncated length":           with(0x82, 0x80),
		"truncated value":            with(0x82, 0x05, 'a', 'b'),
		"missing length":             with(0x82),
		"unknown field, long length": with(0x3f, 0x02, 0xaa),
	} {
		if _, err := wire.Parse(b); !errors.Is(err, wire.ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
	}
	for name, extra := range map[string][]byte{
		"wrong u16 width":  {0x82, 0x03, 7, 0, 0},
		"invalid UTF-8":    {0x83, 0x01, 0xff},
		"odd u16 list":     {0x84, 0x03, 1, 0, 2},
		"wrong u64 width":  {0x85, 0x01, 1},
		"wrong u8 width":   {0x86, 0x02, 1, 1},
		"wrong u32 width":  {0x87, 0x03, 1, 1, 1},
		"invalid UTF-8 in": {0x88, 0x02, 'a', 0xc3},
	} {
		f, err := wire.Parse(with(extra...))
		if err != nil {
			t.Fatalf("%s: the grammar level accepts it: %v", name, err)
		}
		var typeErr error
		switch extra[0] & 0x3f {
		case 2:
			_, _, typeErr = f.U16(2)
		case 3, 8:
			_, _, typeErr = f.String(int(extra[0] & 0x3f))
		case 4:
			_, typeErr = f.U16List(4)
		case 5:
			_, _, typeErr = f.U64(5)
		case 6:
			_, _, typeErr = f.U8(6)
		case 7:
			_, _, typeErr = f.U32(7)
		}
		if !errors.Is(typeErr, wire.ErrMalformed) {
			t.Errorf("%s: typed read error = %v, want ErrMalformed", name, typeErr)
		}
	}
}

func TestMissingRequiredFieldIsMalformed(t *testing.T) {
	f, err := wire.Parse([]byte{0x82, 0x01, 'x'})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.RequireU16(1); !errors.Is(err, wire.ErrMalformed) {
		t.Fatalf("error = %v", err)
	}
}

func TestMaximalLengthsUseTheMinimalU32VForm(t *testing.T) {
	for _, tc := range []struct {
		length int
		prefix []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{16_383, []byte{0xff, 0x7f}},
		{16_384, []byte{0x80, 0x80, 0x01}},
	} {
		var b wire.Builder
		b.Binary(1, bytes.Repeat([]byte{7}, tc.length))
		out := b.Bytes()
		if out[0] != 0x81 || !bytes.Equal(out[1:1+len(tc.prefix)], tc.prefix) {
			t.Fatalf("length %d encoded as %x", tc.length, out[:4])
		}
		f, err := wire.Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := f.RequireBytes(1); len(got) != tc.length {
			t.Fatalf("length %d parsed as %d", tc.length, len(got))
		}
	}
}

func TestSkippableFieldsAreNotRequired(t *testing.T) {
	var b wire.Builder
	b.U16(2, 7).SkippableString(4, "why")
	if b.Bytes()[len(b.Bytes())-5] != 0x04 {
		t.Fatalf("a skippable field must be sent with R=0: %x", b.Bytes())
	}
}
