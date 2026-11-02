package document

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"sync"

	"github.com/kdoqdb/kdoq-go/internal/source"
)

// Stream reads one document value by value, without building a tree. It
// applies the checks of Decode: whatever Decode rejects, a Stream read of the
// same bytes rejects too. Values that a caller does not read directly come
// from Value, which is the tree decoder.
type Stream struct {
	reader
	depth   int // containers entered
	frames  []frame
	ids     []uint32
	offsets []uint64
	buf     [8]frame
}

// frame is a container being read.
type frame struct {
	end         int // the limit of the enclosing body, restored on exit
	idBase      int // first entry of Stream.ids (objects)
	offBase     int // first entry of Stream.offsets
	count, next int
	valuesStart int
	indexed     bool // an array with an offset table
}

var _ source.Source = (*Stream)(nil)

var streams = sync.Pool{New: func() any { return new(Stream) }}

// NewStream returns a Stream over the document b, whose field IDs are named by
// names. Release it when done.
func NewStream(names Names, b []byte) *Stream {
	s := streams.Get().(*Stream)
	s.reader = reader{b: b, names: names}
	s.depth = 0
	s.frames = s.buf[:0]
	s.ids = s.ids[:0]
	s.offsets = s.offsets[:0]
	return s
}

// Release returns the Stream for reuse; it must not be used afterwards.
func (s *Stream) Release() {
	s.reader = reader{}
	streams.Put(s)
}

func (s *Stream) peek() (byte, error) {
	if s.pos >= s.limit() {
		return 0, malformed("truncated value")
	}
	return s.b[s.pos], nil
}

// Kind implements source.Source.
func (s *Stream) Kind() (source.Kind, error) {
	tag, err := s.peek()
	if err != nil {
		return 0, err
	}
	switch tag {
	case 0x0a:
		return source.Null, nil
	case 0x08:
		return source.Bool, nil
	case 0x10, 0x12, 0x80, 0x81:
		return source.Int, nil
	case 0x82, 0x83, 0x84, 0x85:
		return source.Uint, nil
	case 0x01:
		return source.Float, nil
	case 0x8b:
		return source.String, nil
	case 0x87, 0x88:
		return source.Object, nil
	case 0x89, 0x8a:
		return source.Array, nil
	}
	return source.Other, nil
}

// Value implements source.Source.
func (s *Stream) Value() (any, error) { return s.reader.value(s.depth) }

// Null implements source.Source.
func (s *Stream) Null() error { return s.expect(0x0a) }

// expect consumes the tag of the next value, which must be tag.
func (s *Stream) expect(tag byte) error {
	got, err := s.u8()
	if err != nil {
		return err
	}
	if got != tag {
		return malformed("unexpected value tag %#x", got)
	}
	return nil
}

// Bool implements source.Source.
func (s *Stream) Bool() (bool, error) {
	if err := s.expect(0x08); err != nil {
		return false, err
	}
	b, err := s.u8()
	if err != nil {
		return false, err
	}
	if b > 1 {
		return false, malformed("invalid Boolean")
	}
	return b == 1, nil
}

// Int implements source.Source.
func (s *Stream) Int() (int64, error) {
	tag, err := s.u8()
	if err != nil {
		return 0, err
	}
	switch tag {
	case 0x10:
		n, err := s.u32()
		return int64(int32(n)), err
	case 0x12:
		n, err := s.u64()
		return int64(n), err
	case 0x80:
		b, err := s.u8()
		return int64(int8(b)), err
	case 0x81:
		n, err := s.u16()
		return int64(int16(n)), err
	}
	return 0, malformed("unexpected value tag %#x", tag)
}

// Uint implements source.Source.
func (s *Stream) Uint() (uint64, error) {
	tag, err := s.u8()
	if err != nil {
		return 0, err
	}
	switch tag {
	case 0x82:
		b, err := s.u8()
		return uint64(b), err
	case 0x83:
		n, err := s.u16()
		return uint64(n), err
	case 0x84:
		n, err := s.u32()
		return uint64(n), err
	case 0x85:
		return s.u64()
	}
	return 0, malformed("unexpected value tag %#x", tag)
}

// Float implements source.Source.
func (s *Stream) Float() (float64, error) {
	if err := s.expect(0x01); err != nil {
		return 0, err
	}
	n, err := s.u64()
	return math.Float64frombits(n), err
}

// String implements source.Source.
func (s *Stream) String() (string, error) {
	if err := s.expect(0x8b); err != nil {
		return "", err
	}
	n, err := s.v32()
	if err != nil {
		return "", err
	}
	return s.text(int(n))
}

// enter consumes the tag and the length of a container body and narrows the
// reader to the body; the caller then reads the directory.
func (s *Stream) enter(plain, indexed byte) (tag byte, err error) {
	tag, err = s.u8()
	if err != nil {
		return 0, err
	}
	if tag != plain && tag != indexed {
		return 0, malformed("unexpected value tag %#x", tag)
	}
	if s.depth+1 > MaxDepth {
		return 0, malformed("container depth exceeds the limit")
	}
	n, err := s.v32()
	if err != nil {
		return 0, err
	}
	start := s.pos
	if _, err := s.take(int(n)); err != nil {
		return 0, err
	}
	s.frames = append(s.frames, frame{end: s.end, idBase: len(s.ids), offBase: len(s.offsets)})
	s.depth++
	s.pos, s.end = start, start+int(n)
	return tag, nil
}

// exit checks that the body was read to its end and leaves it.
func (s *Stream) exit(what string) error {
	if err := s.done(what); err != nil {
		return err
	}
	top := len(s.frames) - 1
	f := s.frames[top]
	s.end = f.end
	s.ids = s.ids[:f.idBase]
	s.offsets = s.offsets[:f.offBase]
	s.frames = s.frames[:top]
	s.depth--
	return nil
}

// EnterArray implements source.Source.
func (s *Stream) EnterArray() (int, error) {
	tag, err := s.enter(0x89, 0x8a)
	if err != nil {
		return 0, err
	}
	f := &s.frames[len(s.frames)-1]
	width := 0
	if tag == 0x8a {
		f.indexed = true
		w, err := s.u8()
		if err != nil {
			return 0, err
		}
		width = int(w)
		if !validWidth(width) {
			return 0, malformed("invalid array offset width")
		}
	}
	n, err := s.v32()
	if err != nil {
		return 0, err
	}
	if int(n) > s.limit()-s.pos {
		return 0, malformed("array count exceeds the body")
	}
	if f.indexed {
		for i := uint32(0); i < n; i++ {
			o, err := s.uint(width)
			if err != nil {
				return 0, err
			}
			s.offsets = append(s.offsets, o)
		}
	}
	f.count = int(n)
	f.valuesStart = s.pos
	return f.count, nil
}

// NextElem implements source.Source.
func (s *Stream) NextElem() error {
	f := &s.frames[len(s.frames)-1]
	if f.next >= f.count {
		return malformed("array has fewer elements than announced")
	}
	if f.indexed && s.offsets[f.offBase+f.next] != uint64(s.pos-f.valuesStart) {
		return malformed("array offset does not match the value position")
	}
	f.next++
	return nil
}

// ExitArray implements source.Source.
func (s *Stream) ExitArray() error { return s.exit("array") }

// EnterObject implements source.Source.
func (s *Stream) EnterObject() error {
	tag, err := s.enter(0x87, 0x88)
	if err != nil {
		return err
	}
	f := &s.frames[len(s.frames)-1]
	var offsetWidth int
	if tag == 0x88 {
		w, err := s.u8()
		if err != nil {
			return err
		}
		offsetWidth = int(w)
		m, err := s.take(8)
		if err != nil {
			return err
		}
		for mask := binary.LittleEndian.Uint64(m); mask != 0; mask &= mask - 1 {
			s.ids = append(s.ids, uint32(bits.TrailingZeros64(mask)))
		}
		f.count = len(s.ids) - f.idBase
		if !validWidth(offsetWidth) {
			return malformed("invalid directory width")
		}
	} else {
		iw, err := s.u8()
		if err != nil {
			return err
		}
		ow, err := s.u8()
		if err != nil {
			return err
		}
		n, err := s.v32()
		if err != nil {
			return err
		}
		idWidth := int(iw)
		offsetWidth, f.count = int(ow), int(n)
		if !validWidth(idWidth) || !validWidth(offsetWidth) {
			return malformed("invalid directory width")
		}
		if f.count > s.limit()-s.pos {
			return malformed("directory larger than the object")
		}
		for i := 0; i < f.count; i++ {
			id, err := s.uint(idWidth)
			if err != nil {
				return err
			}
			s.ids = append(s.ids, uint32(id))
		}
	}
	for i := 0; i < f.count; i++ {
		o, err := s.uint(offsetWidth)
		if err != nil {
			return err
		}
		s.offsets = append(s.offsets, o)
	}
	f.valuesStart = s.pos
	return nil
}

// NextField implements source.Source.
func (s *Stream) NextField() (string, bool, error) {
	f := &s.frames[len(s.frames)-1]
	if f.next == f.count {
		return "", false, nil
	}
	if s.offsets[f.offBase+f.next] != uint64(s.pos-f.valuesStart) {
		return "", false, malformed("object offset does not match the value position")
	}
	id := s.ids[f.idBase+f.next]
	name, ok := s.names[id]
	if !ok {
		return "", false, fmt.Errorf("%w: field id %d is not in the dictionary", ErrUnknownName, id)
	}
	f.next++
	return name, true, nil
}

// ExitObject implements source.Source.
func (s *Stream) ExitObject() error { return s.exit("object") }

// Finish implements source.Source.
func (s *Stream) Finish() error {
	if s.depth != 0 || s.pos != len(s.b) {
		return malformed("trailing bytes after the value")
	}
	return nil
}
