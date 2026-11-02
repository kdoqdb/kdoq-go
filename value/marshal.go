package value

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

const maxDepth = 64

// Marshaler is implemented by types that choose their own document value. The
// result is converted again, so it may be any value Marshal accepts.
type Marshaler interface {
	MarshalKdoq() (any, error)
}

var (
	marshalerType = reflect.TypeFor[Marshaler]()
	timeType      = reflect.TypeFor[time.Time]()
)

// isValueStruct reports struct types that are values, not documents.
func isValueStruct(t reflect.Type) bool {
	switch t {
	case timeType, reflect.TypeFor[Binary](), reflect.TypeFor[Timestamp](), reflect.TypeFor[Code](),
		reflect.TypeFor[CalendarDate](), reflect.TypeFor[MinKey](), reflect.TypeFor[MaxKey](),
		reflect.TypeFor[RegExp](), reflect.TypeFor[E]():
		return true
	}
	return false
}

// Marshal converts a Go value into the document value the server stores:
// nil, bool, string, int32, int64, float64, time.Time, []byte, Binary,
// Decimal128, Timestamp, Code, ObjectID, CalendarDate, Doc, D and Array.
//
// int8, int16 and int32 become int32; int, int64 and every unsigned integer
// become int64, and an unsigned value above the int64 range is an error.
// Structs become D in field order, named by the `kdoq` tag, then the `json`
// tag, then the field name; the `kdoq:",key"` field is not part of the body
// (see KeyOf). Maps need string keys. Undefined, MinKey, MaxKey and RegExp
// cannot be written.
func Marshal(v any) (any, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, nil
	}
	return marshalValue(rv, "", 0)
}

// KeyOf returns the value of the `kdoq:",key"` field of the struct v (or of
// the struct it points to). ok is false when v has no such field or the field
// is empty.
func KeyOf(v any) (key string, ok bool, err error) {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return "", false, nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Struct || isValueStruct(rv.Type()) {
		return "", false, nil
	}
	info := infoOf(rv.Type())
	if info.err != nil {
		return "", false, &Error{Message: info.err.Error()}
	}
	if info.key == nil {
		return "", false, nil
	}
	f, present := fieldValue(rv, info.key.index, false)
	if !present || f.Len() == 0 {
		return "", false, nil
	}
	return f.String(), true, nil
}

func fail(path, format string, args ...any) error {
	return &Error{Path: path, Message: fmt.Sprintf(format, args...)}
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func marshalValue(v reflect.Value, path string, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fail(path, "nested too deep (more than %d levels)", maxDepth)
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, nil
	}
	if v.Type().Implements(marshalerType) {
		return callMarshaler(v.Interface().(Marshaler), path, depth)
	}
	if v.Kind() != reflect.Pointer && reflect.PointerTo(v.Type()).Implements(marshalerType) {
		addressable := v
		if !v.CanAddr() {
			addressable = reflect.New(v.Type()).Elem()
			addressable.Set(v)
		}
		return callMarshaler(addressable.Addr().Interface().(Marshaler), path, depth)
	}
	if v.Kind() == reflect.Pointer {
		return marshalValue(v.Elem(), path, depth+1)
	}
	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case time.Time, Binary, Decimal128, Timestamp, Code, ObjectID:
			return x, nil
		case CalendarDate:
			if !x.valid() {
				return nil, fail(path, "invalid calendar date %d-%d-%d", x.Year, x.Month, x.Day)
			}
			return x, nil
		case undefinedType:
			return nil, fail(path, "the undefined value cannot be written")
		case MinKey, MaxKey, RegExp:
			return nil, fail(path, "%T values cannot be written", x)
		case D:
			return marshalD(x, path, depth)
		}
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return int32(v.Int()), nil
	case reflect.Int, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if v.Uint() > math.MaxInt64 {
			return nil, fail(path, "unsigned value %d does not fit Int64", v.Uint())
		}
		return int64(v.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Slice, reflect.Array:
		return marshalList(v, path, depth)
	case reflect.Map:
		return marshalMap(v, path, depth)
	case reflect.Struct:
		return marshalStruct(v, path, depth)
	}
	return nil, fail(path, "unsupported type %s", v.Type())
}

func callMarshaler(m Marshaler, path string, depth int) (any, error) {
	out, err := m.MarshalKdoq()
	if err != nil {
		return nil, fail(path, "%v", err)
	}
	if out == nil {
		return nil, nil
	}
	return marshalValue(reflect.ValueOf(out), path, depth+1)
}

func marshalD(d D, path string, depth int) (any, error) {
	out := make(D, len(d))
	for i, e := range d {
		item, err := marshalNested(e.Value, joinPath(path, e.Key), depth)
		if err != nil {
			return nil, err
		}
		out[i] = E{Key: e.Key, Value: item}
	}
	return out, nil
}

func marshalNested(v any, path string, depth int) (any, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, nil
	}
	return marshalValue(rv, path, depth+1)
}

func marshalList(v reflect.Value, path string, depth int) (any, error) {
	if v.Type().Elem().Kind() == reflect.Uint8 {
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		out := make([]byte, v.Len())
		reflect.Copy(reflect.ValueOf(out), v)
		return out, nil
	}
	if v.Kind() == reflect.Slice && v.IsNil() {
		return nil, nil
	}
	out := make(Array, v.Len())
	for i := range out {
		item, err := marshalValue(v.Index(i), path+"["+strconv.Itoa(i)+"]", depth+1)
		if err != nil {
			return nil, err
		}
		out[i] = item
	}
	return out, nil
}

func marshalMap(v reflect.Value, path string, depth int) (any, error) {
	if v.Type().Key().Kind() != reflect.String {
		return nil, fail(path, "map keys must be strings, not %s", v.Type().Key())
	}
	if v.IsNil() {
		return nil, nil
	}
	out := make(Doc, v.Len())
	for it := v.MapRange(); it.Next(); {
		name := it.Key().String()
		item, err := marshalValue(it.Value(), joinPath(path, name), depth+1)
		if err != nil {
			return nil, err
		}
		out[name] = item
	}
	return out, nil
}

func marshalStruct(v reflect.Value, path string, depth int) (any, error) {
	info := infoOf(v.Type())
	if info.err != nil {
		return nil, fail(path, "%v", info.err)
	}
	out := make(D, 0, len(info.fields))
	for _, f := range info.fields {
		fv, present := fieldValue(v, f.index, false)
		if !present || (f.omitEmpty && isEmpty(fv)) || (f.omitZero && isZero(fv)) {
			continue
		}
		item, err := marshalValue(fv, joinPath(path, f.name), depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, E{Key: f.name, Value: item})
	}
	return out, nil
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

func isZero(v reflect.Value) bool {
	if z, ok := v.Interface().(interface{ IsZero() bool }); ok && v.Kind() != reflect.Pointer {
		return z.IsZero()
	}
	return v.IsZero()
}
