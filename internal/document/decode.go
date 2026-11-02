package document

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"time"
	"unicode/utf8"

	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

// Decode decodes one complete result value against a dictionary context.
func Decode(names Names, b []byte) (any, error) {
	r := &reader{b: b, names: names}
	v, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if r.pos != len(b) {
		return nil, malformed("trailing bytes after the value")
	}
	return v, nil
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", wire.ErrMalformed, fmt.Sprintf(format, args...))
}

type reader struct {
	b     []byte
	pos   int
	end   int // exclusive limit of the current body; 0 means len(b)
	names Names
}

func (r *reader) limit() int {
	if r.end != 0 {
		return r.end
	}
	return len(r.b)
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > r.limit() {
		return nil, malformed("truncated value")
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *reader) u8() (byte, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *reader) v32() (uint32, error) {
	n, size, err := wire.ReadU32V(r.b[r.pos:r.limit()])
	if err != nil {
		return 0, err
	}
	r.pos += size
	return n, nil
}

func (r *reader) text(n int) (string, error) {
	b, err := r.take(n)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", malformed("string is not valid UTF-8")
	}
	return string(b), nil
}

// body returns a reader limited to the next length-prefixed body.
func (r *reader) body() (*reader, error) {
	n, err := r.v32()
	if err != nil {
		return nil, err
	}
	start := r.pos
	if _, err := r.take(int(n)); err != nil {
		return nil, err
	}
	return &reader{b: r.b, pos: start, end: start + int(n), names: r.names}, nil
}

func (r *reader) done(what string) error {
	if r.pos != r.limit() {
		return malformed("%s body has trailing bytes", what)
	}
	return nil
}

func (r *reader) value(depth int) (any, error) {
	tag, err := r.u8()
	if err != nil {
		return nil, err
	}
	switch tag {
	case 0x01:
		n, err := r.u64()
		return math.Float64frombits(n), err
	case 0x05:
		return r.binary()
	case 0x06:
		return value.Undefined, nil
	case 0x07:
		b, err := r.take(12)
		if err != nil {
			return nil, err
		}
		var id value.ObjectID
		copy(id[:], b)
		return id, nil
	case 0x08:
		b, err := r.u8()
		if err != nil {
			return nil, err
		}
		if b > 1 {
			return nil, malformed("invalid Boolean")
		}
		return b == 1, nil
	case 0x09:
		b, err := r.take(8)
		if err != nil {
			return nil, err
		}
		return time.UnixMilli(int64(binary.LittleEndian.Uint64(b))).UTC(), nil
	case 0x0a:
		return nil, nil
	case 0x0d:
		b, err := r.take(4)
		if err != nil {
			return nil, err
		}
		n := int(int32(binary.LittleEndian.Uint32(b))) - 1
		src, err := r.text(n)
		if err != nil {
			return nil, err
		}
		if z, err := r.u8(); err != nil || z != 0 {
			return nil, malformed("Code is not zero terminated")
		}
		return value.Code{Source: src}, nil
	case 0x10:
		n, err := r.u32()
		return int32(n), err
	case 0x11:
		b, err := r.take(8)
		if err != nil {
			return nil, err
		}
		return value.Timestamp{Increment: binary.LittleEndian.Uint32(b), Seconds: binary.LittleEndian.Uint32(b[4:])}, nil
	case 0x12:
		n, err := r.u64()
		return int64(n), err
	case 0x13:
		b, err := r.take(16)
		if err != nil {
			return nil, err
		}
		var d value.Decimal128
		copy(d[:], b)
		return d, nil
	case 0x7f:
		return value.MaxKey{}, nil
	case 0x80:
		b, err := r.u8()
		return int32(int8(b)), err
	case 0x81:
		n, err := r.u16()
		return int32(int16(n)), err
	case 0x82:
		b, err := r.u8()
		return uint32(b), err
	case 0x83:
		n, err := r.u16()
		return uint32(n), err
	case 0x84:
		return r.u32()
	case 0x85:
		return r.u64()
	case 0x86:
		b, err := r.take(4)
		if err != nil {
			return nil, err
		}
		days := int64(int32(binary.LittleEndian.Uint32(b)))
		return value.CalendarDateOf(time.Unix(days*86400, 0).UTC()), nil
	case 0x87:
		return r.object(depth+1, false)
	case 0x88:
		return r.object(depth+1, true)
	case 0x89:
		return r.array(depth+1, false)
	case 0x8a:
		return r.array(depth+1, true)
	case 0x8b:
		n, err := r.v32()
		if err != nil {
			return nil, err
		}
		return r.text(int(n))
	case 0x8c:
		body, err := r.body()
		if err != nil {
			return nil, err
		}
		pn, err := body.v32()
		if err != nil {
			return nil, err
		}
		pattern, err := body.text(int(pn))
		if err != nil {
			return nil, err
		}
		on, err := body.v32()
		if err != nil {
			return nil, err
		}
		flags, err := body.text(int(on))
		if err != nil {
			return nil, err
		}
		return value.RegExp{Pattern: pattern, Flags: flags}, body.done("RegExp")
	case 0xff:
		return value.MinKey{}, nil
	}
	return nil, malformed("unsupported value tag %#x", tag)
}

func (r *reader) u16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (r *reader) u32() (uint32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *reader) u64() (uint64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

func (r *reader) binary() (any, error) {
	b, err := r.take(4)
	if err != nil {
		return nil, err
	}
	length := int(int32(binary.LittleEndian.Uint32(b)))
	subtype, err := r.u8()
	if err != nil {
		return nil, err
	}
	payload, err := r.take(length)
	if err != nil {
		return nil, err
	}
	if subtype == 2 {
		if len(payload) < 4 {
			return nil, malformed("truncated Binary subtype 2")
		}
		if int(int32(binary.LittleEndian.Uint32(payload))) != len(payload)-4 {
			return nil, malformed("inconsistent Binary subtype 2 framing")
		}
		payload = payload[4:]
	}
	data := make([]byte, len(payload))
	copy(data, payload)
	if subtype == 0 {
		return data, nil
	}
	return value.Binary{Subtype: subtype, Data: data}, nil
}

func (r *reader) object(depth int, bitmap bool) (any, error) {
	if depth > MaxDepth {
		return nil, malformed("container depth exceeds the limit")
	}
	body, err := r.body()
	if err != nil {
		return nil, err
	}
	var ids []uint32
	var offsetWidth, count int
	if bitmap {
		w, err := body.u8()
		if err != nil {
			return nil, err
		}
		offsetWidth = int(w)
		m, err := body.take(8)
		if err != nil {
			return nil, err
		}
		mask := binary.LittleEndian.Uint64(m)
		for mask != 0 {
			i := bits.TrailingZeros64(mask)
			ids = append(ids, uint32(i))
			mask &= mask - 1
		}
		count = len(ids)
	} else {
		iw, err := body.u8()
		if err != nil {
			return nil, err
		}
		ow, err := body.u8()
		if err != nil {
			return nil, err
		}
		n, err := body.v32()
		if err != nil {
			return nil, err
		}
		idWidth := int(iw)
		offsetWidth, count = int(ow), int(n)
		if !validWidth(idWidth) || !validWidth(offsetWidth) {
			return nil, malformed("invalid directory width")
		}
		if count > body.limit()-body.pos {
			return nil, malformed("directory larger than the object")
		}
		for i := 0; i < count; i++ {
			id, err := body.uint(idWidth)
			if err != nil {
				return nil, err
			}
			ids = append(ids, uint32(id))
		}
	}
	if bitmap && !validWidth(offsetWidth) {
		return nil, malformed("invalid directory width")
	}
	offsets := make([]uint64, count)
	for i := range offsets {
		if offsets[i], err = body.uint(offsetWidth); err != nil {
			return nil, err
		}
	}
	valuesStart := body.pos
	out := make(value.Doc, count)
	for i, id := range ids {
		if offsets[i] != uint64(body.pos-valuesStart) {
			return nil, malformed("object offset does not match the value position")
		}
		name, ok := r.names[id]
		if !ok {
			return nil, fmt.Errorf("%w: field id %d is not in the dictionary", ErrUnknownName, id)
		}
		v, err := body.value(depth)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, body.done("object")
}

// ErrUnknownName reports a field ID the dictionary does not define.
var ErrUnknownName = fmt.Errorf("%w: unknown dictionary entry", wire.ErrMalformed)

func validWidth(w int) bool { return w == 1 || w == 2 || w == 4 }

func (r *reader) uint(width int) (uint64, error) {
	b, err := r.take(width)
	if err != nil {
		return 0, err
	}
	switch width {
	case 1:
		return uint64(b[0]), nil
	case 2:
		return uint64(binary.LittleEndian.Uint16(b)), nil
	case 4:
		return uint64(binary.LittleEndian.Uint32(b)), nil
	}
	return 0, malformed("invalid width %d", width)
}

func (r *reader) array(depth int, indexed bool) (any, error) {
	if depth > MaxDepth {
		return nil, malformed("container depth exceeds the limit")
	}
	body, err := r.body()
	if err != nil {
		return nil, err
	}
	width := 0
	if indexed {
		w, err := body.u8()
		if err != nil {
			return nil, err
		}
		width = int(w)
		if !validWidth(width) {
			return nil, malformed("invalid array offset width")
		}
	}
	n, err := body.v32()
	if err != nil {
		return nil, err
	}
	if int(n) > body.limit()-body.pos {
		return nil, malformed("array count exceeds the body")
	}
	var offsets []uint64
	if indexed {
		for i := uint32(0); i < n; i++ {
			o, err := body.uint(width)
			if err != nil {
				return nil, err
			}
			offsets = append(offsets, o)
		}
	}
	valuesStart := body.pos
	out := make(value.Array, 0, n)
	for i := uint32(0); i < n; i++ {
		if indexed && offsets[i] != uint64(body.pos-valuesStart) {
			return nil, malformed("array offset does not match the value position")
		}
		v, err := body.value(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, body.done("array")
}
