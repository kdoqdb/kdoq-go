package value

import (
	"errors"
	"reflect"

	"github.com/kdoqdb/kdoq-go/internal/source"
)

func init() { source.Unmarshal = unmarshalSource }

// errStream is the error of a read through a source.Source. It carries no
// detail: the caller reads the document again through the tree decoder and
// Unmarshal, whose errors name the path and the types involved.
var errStream = errors.New("value: the document does not fit the destination")

// unmarshalSource is Unmarshal, and UnmarshalKeyed when keyed, reading the
// document from src instead of from a tree. It stores exactly what they
// store; it is only faster.
func unmarshalSource(src source.Source, key string, keyed bool, dst any) error {
	rv := reflect.ValueOf(dst)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errStream
	}
	if err := decodeStream(src, rv.Elem(), 0); err != nil {
		return err
	}
	if err := src.Finish(); err != nil {
		return err
	}
	if keyed {
		return storeKey(key, dst)
	}
	return nil
}

// decodeTree reads the next value of s as a tree and decodes that, for the
// destinations and values decodeStream does not read directly.
func decodeTree(s source.Source, dv reflect.Value, depth int) error {
	v, err := s.Value()
	if err != nil {
		return err
	}
	return decodeInto(v, dv, depth)
}

// decodeStream is decodeInto reading from a source.Source. It follows the
// same cases in the same order.
func decodeStream(s source.Source, dv reflect.Value, depth int) error {
	if depth > maxDepth {
		return errStream
	}
	kind, err := s.Kind()
	if err != nil {
		return err
	}
	if dv.Kind() == reflect.Pointer {
		if kind == source.Null {
			dv.SetZero()
			return s.Null()
		}
		if dv.IsNil() {
			dv.Set(reflect.New(dv.Type().Elem()))
		}
		return decodeStream(s, dv.Elem(), depth+1)
	}
	traits := traitsOf(dv.Type())
	if (traits.unmarshaler && dv.CanAddr()) || dv.Kind() == reflect.Interface {
		return decodeTree(s, dv, depth)
	}
	if kind == source.Null {
		dv.SetZero()
		return s.Null()
	}
	if traits.special != specialNone {
		return decodeTree(s, dv, depth)
	}
	switch dv.Kind() {
	case reflect.Bool:
		if kind == source.Bool {
			b, err := s.Bool()
			dv.SetBool(b)
			return err
		}
	case reflect.String:
		if kind == source.String {
			str, err := s.String()
			dv.SetString(str)
			return err
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return streamNumber(s, kind, dv)
	case reflect.Slice, reflect.Array:
		if kind == source.Array && dv.Type().Elem().Kind() != reflect.Uint8 {
			return streamList(s, dv, depth)
		}
	case reflect.Map:
		if kind == source.Object && dv.Type().Key().Kind() == reflect.String {
			return streamMap(s, dv, depth)
		}
	case reflect.Struct:
		if kind == source.Object {
			return streamStruct(s, dv, depth)
		}
	}
	return decodeTree(s, dv, depth)
}

func streamNumber(s source.Source, kind source.Kind, dv reflect.Value) error {
	var (
		signed   int64
		unsigned uint64
		float    float64
		class    reflect.Kind
		err      error
	)
	switch kind {
	case source.Int:
		signed, err = s.Int()
		class = reflect.Int64
	case source.Uint:
		unsigned, err = s.Uint()
		class = reflect.Uint64
	case source.Float:
		float, err = s.Float()
		class = reflect.Float64
	default:
		return errStream
	}
	if err != nil {
		return err
	}
	if !storeNumber(class, signed, unsigned, float, dv) {
		return errStream
	}
	return nil
}

func streamList(s source.Source, dv reflect.Value, depth int) error {
	n, err := s.EnterArray()
	if err != nil {
		return err
	}
	if dv.Kind() == reflect.Array {
		if n > dv.Len() {
			return errStream
		}
		dv.SetZero()
	} else {
		dv.Set(reflect.MakeSlice(dv.Type(), n, n))
	}
	for i := 0; i < n; i++ {
		if err := s.NextElem(); err != nil {
			return err
		}
		if err := decodeStream(s, dv.Index(i), depth+1); err != nil {
			return err
		}
	}
	return s.ExitArray()
}

func streamMap(s source.Source, dv reflect.Value, depth int) error {
	if err := s.EnterObject(); err != nil {
		return err
	}
	if dv.IsNil() {
		dv.Set(reflect.MakeMap(dv.Type()))
	}
	for {
		name, ok, err := s.NextField()
		if err != nil {
			return err
		}
		if !ok {
			return s.ExitObject()
		}
		item := reflect.New(dv.Type().Elem()).Elem()
		if err := decodeStream(s, item, depth+1); err != nil {
			return err
		}
		dv.SetMapIndex(reflect.ValueOf(name).Convert(dv.Type().Key()), item)
	}
}

func streamStruct(s source.Source, dv reflect.Value, depth int) error {
	info := infoOf(dv.Type())
	if info.err != nil {
		return errStream
	}
	if err := s.EnterObject(); err != nil {
		return err
	}
	for {
		name, ok, err := s.NextField()
		if err != nil {
			return err
		}
		if !ok {
			return s.ExitObject()
		}
		i, known := info.byName[name]
		if !known {
			// Read the value like any other, so that an ignored field is
			// checked as strictly as a used one.
			if _, err := s.Value(); err != nil {
				return err
			}
			continue
		}
		fv, ok := fieldValue(dv, info.fields[i].index, true)
		if !ok {
			return errStream
		}
		if err := decodeStream(s, fv, depth+1); err != nil {
			return err
		}
	}
}
