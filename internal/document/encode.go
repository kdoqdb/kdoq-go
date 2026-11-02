package document

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

// InvalidError reports a value that cannot be written as a document.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// EncodeDocument encodes v, the result of value.Marshal, as one Object
// against dict.
func EncodeDocument(dict *Dictionary, v any) ([]byte, error) {
	switch v.(type) {
	case value.Doc, value.D:
	default:
		return nil, invalid("a document must be an object, not %T", v)
	}
	return encodeAt(dict, v, 0)
}

// Encode encodes any marshaled value (a scalar, array or object).
func Encode(dict *Dictionary, v any) ([]byte, error) { return encodeAt(dict, v, 0) }

func tagged(tag byte, parts ...[]byte) []byte {
	out := []byte{tag}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func widthOf(max uint64) int {
	switch {
	case max <= 0xff:
		return 1
	case max <= 0xffff:
		return 2
	default:
		return 4
	}
}

func putWidth(dst []byte, n uint64, width int) []byte {
	switch width {
	case 1:
		return append(dst, byte(n))
	case 2:
		return binary.LittleEndian.AppendUint16(dst, uint16(n))
	default:
		return binary.LittleEndian.AppendUint32(dst, uint32(n))
	}
}

type member struct {
	id    uint32
	value []byte
}

func encodeObject(dict *Dictionary, entries []value.E, depth int) ([]byte, error) {
	if depth+1 > MaxDepth {
		return nil, invalid("container depth exceeds %d levels", MaxDepth)
	}
	seen := make(map[string]struct{}, len(entries))
	members := make([]member, 0, len(entries))
	for _, e := range entries {
		if !utf8.ValidString(e.Key) {
			return nil, invalid("field name %q is not valid UTF-8", e.Key)
		}
		if _, dup := seen[e.Key]; dup {
			return nil, invalid("duplicate field %q", e.Key)
		}
		seen[e.Key] = struct{}{}
		id := dict.idOf(e.Key)
		encoded, err := encodeAt(dict, e.Value, depth+1)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", e.Key, err)
		}
		members = append(members, member{id: id, value: encoded})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].id < members[j].id })
	var values []byte
	for _, m := range members {
		values = append(values, m.value...)
	}
	var maxID uint64
	if len(members) > 0 {
		maxID = uint64(members[len(members)-1].id)
	}
	idWidth, offsetWidth := widthOf(maxID), widthOf(uint64(len(values)))
	body := []byte{byte(idWidth), byte(offsetWidth)}
	body = wire.AppendU32V(body, uint32(len(members)))
	for _, m := range members {
		body = putWidth(body, uint64(m.id), idWidth)
	}
	at := uint64(0)
	for _, m := range members {
		body = putWidth(body, at, offsetWidth)
		at += uint64(len(m.value))
	}
	body = append(body, values...)
	return tagged(0x87, wire.AppendU32V(nil, uint32(len(body))), body), nil
}

func sortedEntries(d value.Doc) []value.E {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]value.E, len(keys))
	for i, k := range keys {
		out[i] = value.E{Key: k, Value: d[k]}
	}
	return out
}

func encodeBinary(subtype byte, data []byte) []byte {
	if subtype == 2 {
		out := []byte{0x05}
		out = binary.LittleEndian.AppendUint32(out, uint32(len(data)+4))
		out = append(out, 2)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
		return append(out, data...)
	}
	out := []byte{0x05}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, subtype)
	return append(out, data...)
}

func encodeAt(dict *Dictionary, v any, depth int) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return []byte{0x0a}, nil
	case bool:
		if x {
			return []byte{0x08, 1}, nil
		}
		return []byte{0x08, 0}, nil
	case int32:
		return binary.LittleEndian.AppendUint32([]byte{0x10}, uint32(x)), nil
	case int64:
		return binary.LittleEndian.AppendUint64([]byte{0x12}, uint64(x)), nil
	case float64:
		return binary.LittleEndian.AppendUint64([]byte{0x01}, math.Float64bits(x)), nil
	case string:
		if !utf8.ValidString(x) {
			return nil, invalid("string is not valid UTF-8")
		}
		out := wire.AppendU32V([]byte{0x8b}, uint32(len(x)))
		return append(out, x...), nil
	case time.Time:
		return binary.LittleEndian.AppendUint64([]byte{0x09}, uint64(x.UnixMilli())), nil
	case []byte:
		return encodeBinary(0, x), nil
	case value.Binary:
		return encodeBinary(x.Subtype, x.Data), nil
	case value.Decimal128:
		return append([]byte{0x13}, x[:]...), nil
	case value.Timestamp:
		out := binary.LittleEndian.AppendUint32([]byte{0x11}, x.Increment)
		return binary.LittleEndian.AppendUint32(out, x.Seconds), nil
	case value.Code:
		out := binary.LittleEndian.AppendUint32([]byte{0x0d}, uint32(len(x.Source)+1))
		out = append(out, x.Source...)
		return append(out, 0), nil
	case value.ObjectID:
		return append([]byte{0x07}, x[:]...), nil
	case value.CalendarDate:
		days := time.Date(x.Year, time.Month(x.Month), int(x.Day), 0, 0, 0, 0, time.UTC).Unix() / 86400
		return binary.LittleEndian.AppendUint32([]byte{0x86}, uint32(int32(days))), nil
	case value.Array:
		return encodeArray(dict, x, depth)
	case value.Doc:
		return encodeObject(dict, sortedEntries(x), depth)
	case value.D:
		return encodeObject(dict, x, depth)
	}
	return nil, invalid("unsupported value of type %T", v)
}

func encodeArray(dict *Dictionary, items value.Array, depth int) ([]byte, error) {
	if depth+1 > MaxDepth {
		return nil, invalid("container depth exceeds %d levels", MaxDepth)
	}
	body := wire.AppendU32V(nil, uint32(len(items)))
	for i, item := range items {
		encoded, err := encodeAt(dict, item, depth+1)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		body = append(body, encoded...)
	}
	return tagged(0x89, wire.AppendU32V(nil, uint32(len(body))), body), nil
}
