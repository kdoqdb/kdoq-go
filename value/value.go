// Package value holds the value types of the kdoq server, their conversion
// from and to Go structs, and a readable text format. It has no network code
// and does not depend on the kdoq package.
package value

import (
	"encoding/hex"
	"fmt"
)

// Doc is an unordered document.
type Doc map[string]any

// D is an ordered document.
type D []E

// E is one field of a D.
type E struct {
	Key   string
	Value any
}

// Array is an array value.
type Array []any

// Binary is a binary value with a subtype. The generic subtype (0) is read as
// []byte.
type Binary struct {
	Subtype byte
	Data    []byte
}

// Timestamp is the server's internal timestamp value.
type Timestamp struct{ Seconds, Increment uint32 }

// Code is a code value.
type Code struct{ Source string }

// ObjectID is a 12-byte object identifier.
type ObjectID [12]byte

// String returns the identifier as 24 hexadecimal digits.
func (o ObjectID) String() string { return hex.EncodeToString(o[:]) }

// MinKey is the minimum key. It can be read but not written.
type MinKey struct{}

// MaxKey is the maximum key. It can be read but not written.
type MaxKey struct{}

// RegExp is a regular expression with its option letters. It can be read but
// not written.
type RegExp struct{ Pattern, Flags string }

type undefinedType struct{}

// Undefined is the undefined value. It appears in results only; writing it
// fails.
var Undefined = undefinedType{}

// Error reports a value that cannot be converted.
type Error struct {
	// Path locates the value inside the converted document, such as "a.b[2]".
	Path    string
	Message string
}

// Error implements error. It names the path of the value that could not be
// converted, such as "a.b[2]", followed by the reason.
func (e *Error) Error() string {
	if e.Path == "" {
		return "kdoq value: " + e.Message
	}
	return fmt.Sprintf("kdoq value: %s: %s", e.Path, e.Message)
}
