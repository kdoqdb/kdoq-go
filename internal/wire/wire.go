// Package wire encodes and decodes the frames and the field grammar of the
// native protocol (docs/reference/proto/README.md). It works on bytes only.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Message kinds.
const (
	KindHello      = 0x0001
	KindAuth       = 0x0002
	KindReady      = 0x0003
	KindCancel     = 0x0004
	KindControlAck = 0x0005
	KindLogout     = 0x0006
	KindCommand    = 0x0100
	KindInputValue = 0x0101
	KindInputEnd   = 0x0102
	KindBegin      = 0x0200
	KindItem       = 0x0201
	KindDiagnostic = 0x0202
	KindProgress   = 0x0203
	KindEnd        = 0x0204
	KindError      = 0x0205
	KindDictionary = 0x0300
)

// ControlFrameLimit is the size limit of bootstrap and control frames.
const ControlFrameLimit = 8192

// ErrMalformed is wrapped by every grammar violation.
var ErrMalformed = errors.New("malformed frame")

// ErrIncomplete is returned when a stream ends inside a frame.
var ErrIncomplete = errors.New("stream ended inside a frame")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// Frame is one decoded frame.
type Frame struct {
	Kind    uint16
	Payload []byte
}

// Encode returns the complete frame: the 8-byte prefix and the payload.
func Encode(kind uint16, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint16(out[0:], kind)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

// EncodeFields returns the frame of kind whose payload is the given fields.
func EncodeFields(kind uint16, b *Builder) []byte {
	return Encode(kind, b.Bytes())
}

// ReadFrame reads the next frame. It returns io.EOF at a clean end of stream
// and ErrIncomplete when the stream ends inside a frame. A frame larger than
// ceiling complete bytes is rejected before its payload is read.
func ReadFrame(r io.Reader, ceiling uint64) (Frame, error) {
	var prefix [8]byte
	n, err := io.ReadFull(r, prefix[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return Frame{}, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return Frame{}, ErrIncomplete
		}
		return Frame{}, err
	}
	kind := binary.LittleEndian.Uint16(prefix[0:])
	flags := binary.LittleEndian.Uint16(prefix[2:])
	length := binary.LittleEndian.Uint32(prefix[4:])
	if kind == 0 {
		return Frame{}, malformed("message kind zero")
	}
	if flags != 0 {
		return Frame{}, malformed("nonzero flags %#x", flags)
	}
	if 8+uint64(length) > ceiling {
		return Frame{}, malformed("frame of %d bytes exceeds the ceiling of %d", 8+uint64(length), ceiling)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return Frame{}, ErrIncomplete
		}
		return Frame{}, err
	}
	return Frame{Kind: kind, Payload: payload}, nil
}

// AppendU32V appends an unsigned LEB128 in minimal form.
func AppendU32V(dst []byte, n uint32) []byte {
	for n >= 0x80 {
		dst = append(dst, byte(n)|0x80)
		n >>= 7
	}
	return append(dst, byte(n))
}

// ReadU32V reads a minimal U32V at the start of b and returns its value and
// length in bytes.
func ReadU32V(b []byte) (uint32, int, error) {
	var value uint64
	for i := 0; i < 5; i++ {
		if i >= len(b) {
			return 0, 0, malformed("truncated U32V")
		}
		c := b[i]
		if i == 4 && c > 0x0f {
			return 0, 0, malformed("U32V overflow")
		}
		value |= uint64(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			if i > 0 && c == 0 {
				return 0, 0, malformed("non-minimal U32V")
			}
			return uint32(value), i + 1, nil
		}
	}
	return 0, 0, malformed("overlong U32V")
}

// Builder appends fields to a payload. Every field is sent with R=1.
type Builder struct{ buf []byte }

// Bytes returns the encoded fields.
func (b *Builder) Bytes() []byte { return b.buf }

// Len returns the encoded size.
func (b *Builder) Len() int { return len(b.buf) }

func (b *Builder) raw(id int, value []byte) *Builder {
	b.buf = append(b.buf, 0x80|byte(id))
	b.buf = AppendU32V(b.buf, uint32(len(value)))
	b.buf = append(b.buf, value...)
	return b
}

// U8 appends a u8 field.
func (b *Builder) U8(id int, v uint8) *Builder { return b.raw(id, []byte{v}) }

// Bool appends a boolean field.
func (b *Builder) Bool(id int, v bool) *Builder {
	if v {
		return b.U8(id, 1)
	}
	return b.U8(id, 0)
}

// U16 appends a u16 field.
func (b *Builder) U16(id int, v uint16) *Builder {
	return b.raw(id, binary.LittleEndian.AppendUint16(nil, v))
}

// U32 appends a u32 field.
func (b *Builder) U32(id int, v uint32) *Builder {
	return b.raw(id, binary.LittleEndian.AppendUint32(nil, v))
}

// U64 appends a u64 field.
func (b *Builder) U64(id int, v uint64) *Builder {
	return b.raw(id, binary.LittleEndian.AppendUint64(nil, v))
}

// SkippableString appends a String field with R=0, which a receiver that does
// not know the field may skip.
func (b *Builder) SkippableString(id int, v string) *Builder {
	b.buf = append(b.buf, byte(id))
	b.buf = AppendU32V(b.buf, uint32(len(v)))
	b.buf = append(b.buf, v...)
	return b
}

// String appends a String field.
func (b *Builder) String(id int, v string) *Builder { return b.raw(id, []byte(v)) }

// Binary appends a Binary field.
func (b *Builder) Binary(id int, v []byte) *Builder { return b.raw(id, v) }

// U16List appends a list of u16.
func (b *Builder) U16List(id int, v ...uint16) *Builder {
	var buf []byte
	for _, n := range v {
		buf = binary.LittleEndian.AppendUint16(buf, n)
	}
	return b.raw(id, buf)
}

// Raw appends a field whose value is already encoded, such as a payload group
// built elsewhere.
func (b *Builder) Raw(id int, value []byte) *Builder { return b.raw(id, value) }

// Group appends a group field whose body is the fields of g.
func (b *Builder) Group(id int, g *Builder) *Builder { return b.raw(id, g.buf) }

// Fields is a parsed payload or group. Each id maps to its values in order.
type Fields struct {
	byID map[int][][]byte
}

// Parse splits a payload into fields. It rejects reserved header bits, field
// id zero, non-minimal lengths and truncated values.
func Parse(payload []byte) (*Fields, error) {
	f := &Fields{byID: map[int][][]byte{}}
	for pos := 0; pos < len(payload); {
		header := payload[pos]
		pos++
		id := int(header & 0x3f)
		if header&0x40 != 0 || id == 0 {
			return nil, malformed("invalid field header %#x", header)
		}
		length, n, err := ReadU32V(payload[pos:])
		if err != nil {
			return nil, err
		}
		pos += n
		if uint64(pos)+uint64(length) > uint64(len(payload)) {
			return nil, malformed("truncated field %d", id)
		}
		f.byID[id] = append(f.byID[id], payload[pos:pos+int(length)])
		pos += int(length)
	}
	return f, nil
}

// Has reports whether field id is present.
func (f *Fields) Has(id int) bool { return len(f.byID[id]) > 0 }

// Bytes returns the first value of field id.
func (f *Fields) Bytes(id int) ([]byte, bool) {
	v := f.byID[id]
	if len(v) == 0 {
		return nil, false
	}
	return v[0], true
}

// All returns every value of field id in order.
func (f *Fields) All(id int) [][]byte { return f.byID[id] }

func (f *Fields) fixed(id, size int) ([]byte, bool, error) {
	v, ok := f.Bytes(id)
	if !ok {
		return nil, false, nil
	}
	if len(v) != size {
		return nil, false, malformed("field %d has length %d, expected %d", id, len(v), size)
	}
	return v, true, nil
}

// U8 reads an optional u8.
func (f *Fields) U8(id int) (uint8, bool, error) {
	v, ok, err := f.fixed(id, 1)
	if !ok {
		return 0, false, err
	}
	return v[0], true, nil
}

// U16 reads an optional u16.
func (f *Fields) U16(id int) (uint16, bool, error) {
	v, ok, err := f.fixed(id, 2)
	if !ok {
		return 0, false, err
	}
	return binary.LittleEndian.Uint16(v), true, nil
}

// U32 reads an optional u32.
func (f *Fields) U32(id int) (uint32, bool, error) {
	v, ok, err := f.fixed(id, 4)
	if !ok {
		return 0, false, err
	}
	return binary.LittleEndian.Uint32(v), true, nil
}

// U64 reads an optional u64.
func (f *Fields) U64(id int) (uint64, bool, error) {
	v, ok, err := f.fixed(id, 8)
	if !ok {
		return 0, false, err
	}
	return binary.LittleEndian.Uint64(v), true, nil
}

// String reads an optional String and checks it is valid UTF-8.
func (f *Fields) String(id int) (string, bool, error) {
	v, ok := f.Bytes(id)
	if !ok {
		return "", false, nil
	}
	if !utf8.Valid(v) {
		return "", false, malformed("field %d is not valid UTF-8", id)
	}
	return string(v), true, nil
}

// Group parses an optional group.
func (f *Fields) Group(id int) (*Fields, bool, error) {
	v, ok := f.Bytes(id)
	if !ok {
		return nil, false, nil
	}
	g, err := Parse(v)
	return g, err == nil, err
}

// Groups parses every occurrence of a repeated group.
func (f *Fields) Groups(id int) ([]*Fields, error) {
	var out []*Fields
	for _, v := range f.byID[id] {
		g, err := Parse(v)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// U16List reads an optional list of u16.
func (f *Fields) U16List(id int) ([]uint16, error) {
	v, ok := f.Bytes(id)
	if !ok {
		return nil, nil
	}
	if len(v)%2 != 0 {
		return nil, malformed("field %d is not a list of u16", id)
	}
	out := make([]uint16, 0, len(v)/2)
	for i := 0; i < len(v); i += 2 {
		out = append(out, binary.LittleEndian.Uint16(v[i:]))
	}
	return out, nil
}

// RequireU8, RequireU16, RequireU32, RequireU64 and RequireString read a
// required field.

func (f *Fields) RequireU8(id int) (uint8, error) {
	v, ok, err := f.U8(id)
	return v, requireErr(id, ok, err)
}

func (f *Fields) RequireU16(id int) (uint16, error) {
	v, ok, err := f.U16(id)
	return v, requireErr(id, ok, err)
}

func (f *Fields) RequireU32(id int) (uint32, error) {
	v, ok, err := f.U32(id)
	return v, requireErr(id, ok, err)
}

func (f *Fields) RequireU64(id int) (uint64, error) {
	v, ok, err := f.U64(id)
	return v, requireErr(id, ok, err)
}

func (f *Fields) RequireString(id int) (string, error) {
	v, ok, err := f.String(id)
	return v, requireErr(id, ok, err)
}

func (f *Fields) RequireBytes(id int) ([]byte, error) {
	v, ok := f.Bytes(id)
	return v, requireErr(id, ok, nil)
}

func requireErr(id int, ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return malformed("missing required field %d", id)
	}
	return nil
}
