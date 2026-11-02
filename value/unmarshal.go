package value

import (
	"reflect"
	"sort"
	"strconv"
	"sync"
)

// Unmarshaler is implemented by types that read themselves from a document
// value. v is the value as read: nil, bool, string, int32, int64, uint32,
// uint64, float64, time.Time, []byte, Binary, Doc, Array or one of the other
// value types.
type Unmarshaler interface {
	UnmarshalKdoq(v any) error
}

var unmarshalerType = reflect.TypeFor[Unmarshaler]()

// Unmarshal stores the document value src, as read from the server, in the
// value dst points to. Numbers widen to any integer or floating-point type
// that holds them exactly and fail otherwise; fields of src that dst does not
// have are ignored and fields missing from src are left unchanged. The
// `kdoq:",key"` field of a struct is not read (see UnmarshalKeyed).
func Unmarshal(src, dst any) error {
	rv := reflect.ValueOf(dst)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fail("", "the destination must be a non-nil pointer, not %T", dst)
	}
	return decodeInto(src, rv.Elem(), 0)
}

// UnmarshalKeyed is Unmarshal that also stores the document key in the
// `kdoq:",key"` field of the destination struct, if it has one.
func UnmarshalKeyed(src any, key string, dst any) error {
	if err := Unmarshal(src, dst); err != nil {
		return err
	}
	return storeKey(key, dst)
}

// storeKey stores key in the key field of the struct dst points to, if it has
// one.
func storeKey(key string, dst any) error {
	rv := reflect.ValueOf(dst).Elem()
	for rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct || isValueStruct(rv.Type()) {
		return nil
	}
	info := infoOf(rv.Type())
	if info.err != nil {
		return failHere("%v", info.err)
	}
	if info.key == nil {
		return nil
	}
	if f, ok := fieldValue(rv, info.key.index, true); ok {
		f.SetString(key)
	}
	return nil
}

func mismatch(src any, dv reflect.Value) error {
	return failHere("cannot read %T into %s", src, dv.Type())
}

func decodeInto(src any, dv reflect.Value, depth int) error {
	if depth > maxDepth {
		return failHere("nested too deep (more than %d levels)", maxDepth)
	}
	if dv.Kind() == reflect.Pointer {
		if src == nil {
			dv.SetZero()
			return nil
		}
		if dv.IsNil() {
			dv.Set(reflect.New(dv.Type().Elem()))
		}
		return decodeInto(src, dv.Elem(), depth+1)
	}
	traits := traitsOf(dv.Type())
	if traits.unmarshaler && dv.CanAddr() {
		if err := dv.Addr().Interface().(Unmarshaler).UnmarshalKdoq(src); err != nil {
			return failHere("%v", err)
		}
		return nil
	}
	if dv.Kind() == reflect.Interface {
		if dv.NumMethod() != 0 {
			return mismatch(src, dv)
		}
		if src == nil {
			dv.SetZero()
		} else {
			dv.Set(reflect.ValueOf(src))
		}
		return nil
	}
	if src == nil {
		dv.SetZero()
		return nil
	}
	if handled, err := decodeValueType(src, dv, traits.special); handled {
		return err
	}
	switch dv.Kind() {
	case reflect.Bool:
		if b, ok := src.(bool); ok {
			dv.SetBool(b)
			return nil
		}
	case reflect.String:
		if s, ok := src.(string); ok {
			dv.SetString(s)
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return decodeNumber(src, dv)
	case reflect.Slice, reflect.Array:
		return decodeList(src, dv, depth)
	case reflect.Map:
		return decodeMap(src, dv, depth)
	case reflect.Struct:
		return decodeStruct(src, dv, depth)
	}
	return mismatch(src, dv)
}

// decodeValueType reads the exact value types of this package.
func decodeValueType(src any, dv reflect.Value, special specialType) (bool, error) {
	switch special {
	case specialNone:
		return false, nil
	case specialExact:
		if reflect.TypeOf(src) != dv.Type() {
			return true, mismatch(src, dv)
		}
		dv.Set(reflect.ValueOf(src))
		return true, nil
	case specialBinary:
		switch x := src.(type) {
		case Binary:
			dv.Set(reflect.ValueOf(x))
		case []byte:
			dv.Set(reflect.ValueOf(Binary{Data: x}))
		default:
			return true, mismatch(src, dv)
		}
		return true, nil
	case specialDoc:
		switch x := src.(type) {
		case Doc:
			dv.Set(reflect.ValueOf(x))
		case D:
			dv.Set(reflect.ValueOf(docOf(x)))
		default:
			return true, mismatch(src, dv)
		}
		return true, nil
	case specialD:
		switch x := src.(type) {
		case D:
			dv.Set(reflect.ValueOf(x))
		case Doc:
			dv.Set(reflect.ValueOf(sortedD(x)))
		default:
			return true, mismatch(src, dv)
		}
		return true, nil
	case specialArray:
		switch x := src.(type) {
		case Array:
			dv.Set(reflect.ValueOf(x))
		default:
			return true, mismatch(src, dv)
		}
		return true, nil
	}
	return false, nil
}

// typeTraits are the facts about a destination type that do not depend on the
// value being read, worked out once per type.
type typeTraits struct {
	unmarshaler bool
	special     specialType
}

// specialType tells decodeValueType how to read one of the types of this
// package.
type specialType uint8

const (
	specialNone specialType = iota
	specialExact
	specialBinary
	specialDoc
	specialD
	specialArray
)

var traitsCache sync.Map // reflect.Type -> typeTraits

func traitsOf(t reflect.Type) typeTraits {
	if cached, ok := traitsCache.Load(t); ok {
		return cached.(typeTraits)
	}
	traits := typeTraits{unmarshaler: reflect.PointerTo(t).Implements(unmarshalerType)}
	switch t {
	case timeType, reflect.TypeFor[Decimal128](), reflect.TypeFor[Timestamp](), reflect.TypeFor[Code](),
		reflect.TypeFor[ObjectID](), reflect.TypeFor[CalendarDate](), reflect.TypeFor[MinKey](),
		reflect.TypeFor[MaxKey](), reflect.TypeFor[RegExp]():
		traits.special = specialExact
	case reflect.TypeFor[Binary]():
		traits.special = specialBinary
	case reflect.TypeFor[Doc]():
		traits.special = specialDoc
	case reflect.TypeFor[D]():
		traits.special = specialD
	case reflect.TypeFor[Array]():
		traits.special = specialArray
	}
	traitsCache.Store(t, traits)
	return traits
}

func docOf(d D) Doc {
	out := make(Doc, len(d))
	for _, e := range d {
		out[e.Key] = e.Value
	}
	return out
}

func sortedD(d Doc) D {
	out := make(D, 0, len(d))
	for k, v := range d {
		out = append(out, E{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func decodeList(src any, dv reflect.Value, depth int) error {
	if dv.Type().Elem().Kind() == reflect.Uint8 {
		var data []byte
		switch x := src.(type) {
		case []byte:
			data = x
		case Binary:
			data = x.Data
		default:
			return mismatch(src, dv)
		}
		if dv.Kind() == reflect.Slice {
			out := reflect.MakeSlice(dv.Type(), len(data), len(data))
			reflect.Copy(out, reflect.ValueOf(data))
			dv.Set(out)
			return nil
		}
		if len(data) > dv.Len() {
			return failHere("%d bytes do not fit %s", len(data), dv.Type())
		}
		dv.SetZero()
		reflect.Copy(dv, reflect.ValueOf(data))
		return nil
	}
	list, ok := src.(Array)
	if !ok {
		return mismatch(src, dv)
	}
	if dv.Kind() == reflect.Array {
		if len(list) > dv.Len() {
			return failHere("%d elements do not fit %s", len(list), dv.Type())
		}
		dv.SetZero()
	} else {
		dv.Set(reflect.MakeSlice(dv.Type(), len(list), len(list)))
	}
	for i, item := range list {
		if err := decodeInto(item, dv.Index(i), depth+1); err != nil {
			return indexed(err, i)
		}
	}
	return nil
}

func decodeMap(src any, dv reflect.Value, depth int) error {
	if dv.Type().Key().Kind() != reflect.String {
		return failHere("map keys must be strings, not %s", dv.Type().Key())
	}
	size := 0
	switch x := src.(type) {
	case Doc:
		size = len(x)
	case D:
		size = len(x)
	default:
		return mismatch(src, dv)
	}
	if dv.IsNil() {
		dv.Set(reflect.MakeMapWithSize(dv.Type(), size))
	}
	set := func(key string, v any) error {
		item := reflect.New(dv.Type().Elem()).Elem()
		if err := decodeInto(v, item, depth+1); err != nil {
			return named(err, key)
		}
		dv.SetMapIndex(reflect.ValueOf(key).Convert(dv.Type().Key()), item)
		return nil
	}
	return eachEntry(src, set)
}

// eachEntry calls fn for every field of the document src, a Doc or a D, in the
// order the value stores them; the order of a Doc is unspecified.
func eachEntry(src any, fn func(key string, v any) error) error {
	switch x := src.(type) {
	case Doc:
		for k, v := range x {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	case D:
		for _, e := range x {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeStruct(src any, dv reflect.Value, depth int) error {
	var lookup func(name string) (any, bool)
	switch x := src.(type) {
	case Doc:
		lookup = func(name string) (any, bool) {
			v, ok := x[name]
			return v, ok
		}
	case D:
		byName := make(Doc, len(x))
		for _, e := range x {
			byName[e.Key] = e.Value
		}
		lookup = func(name string) (any, bool) {
			v, ok := byName[name]
			return v, ok
		}
	default:
		return mismatch(src, dv)
	}
	info := infoOf(dv.Type())
	if info.err != nil {
		return failHere("%v", info.err)
	}
	for _, f := range info.fields {
		item, present := lookup(f.name)
		if !present {
			continue
		}
		fv, ok := fieldValue(dv, f.index, true)
		if !ok {
			return fail(f.name, "cannot allocate the embedded struct holding the field")
		}
		if err := decodeInto(item, fv, depth+1); err != nil {
			return named(err, f.name)
		}
	}
	return nil
}

// failHere reports a problem with the value being read; the callers up the
// recursion prepend the field names and indexes that lead to it (named and
// indexed), so a successful read never builds a path.
func failHere(format string, args ...any) error {
	return fail("", format, args...)
}

func named(err error, name string) error {
	if e, ok := err.(*Error); ok {
		e.Path = name + pathSuffix(e.Path)
	}
	return err
}

func indexed(err error, i int) error {
	if e, ok := err.(*Error); ok {
		e.Path = "[" + strconv.Itoa(i) + "]" + pathSuffix(e.Path)
	}
	return err
}

// pathSuffix is the part of a path that follows its first segment.
func pathSuffix(rest string) string {
	if rest == "" || rest[0] == '[' {
		return rest
	}
	return "." + rest
}
