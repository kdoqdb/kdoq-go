// Package source is the seam between the document decoder and the value
// unmarshaler. The decoder implements Source over the bytes of one document,
// the value package consumes it into a Go value, and neither imports the
// other's internals: reading a document into a typed value builds no
// intermediate tree.
package source

// Kind is what the next value of a Source is, as far as the unmarshaler needs
// to tell without reading it.
type Kind uint8

const (
	// Other is any value read through Source.Value: dates, identifiers,
	// binary data, decimals and the other value types of the value package.
	Other Kind = iota
	Null
	Bool
	// Int is a signed integer of any wire width.
	Int
	// Uint is an unsigned integer of any wire width.
	Uint
	Float
	String
	Array
	Object
)

// Source reads the values of one document in order. Each value is consumed
// exactly once, by the method that matches its Kind or by Value, which accepts
// every kind. After an error the Source is unusable.
type Source interface {
	// Kind reports the kind of the next value without consuming it.
	Kind() (Kind, error)
	// Value consumes the next value of any kind and returns it as the tree
	// decoder would: nil, bool, string, int32, int64, uint32, uint64,
	// float64, time.Time, []byte, Binary, Doc, Array or another value type.
	Value() (any, error)

	Null() error
	Bool() (bool, error)
	Int() (int64, error)
	Uint() (uint64, error)
	Float() (float64, error)
	String() (string, error)

	// EnterArray consumes the header of the next value, an Array, and returns
	// its length n. NextElem is then called before each of the n elements,
	// which are consumed in turn, and ExitArray closes the array.
	EnterArray() (int, error)
	NextElem() error
	ExitArray() error

	// EnterObject consumes the header of the next value, an Object. NextField
	// then names each field and leaves its value next to be consumed; it
	// reports false after the last field, and ExitObject closes the object.
	EnterObject() error
	NextField() (name string, ok bool, err error)
	ExitObject() error

	// Finish checks that the document has no bytes left.
	Finish() error
}

// Unmarshal reads the whole document in src into dst, a non-nil pointer, and,
// when keyed, stores key in the key field of dst if it has one. The value
// package sets it when it is initialized; code that imports the value package
// may call it.
//
// A failed Unmarshal leaves dst in an unspecified state and its error does not
// describe the failure precisely: callers that need the error read the
// document again through the tree decoder.
var Unmarshal func(src Source, key string, keyed bool, dst any) error
