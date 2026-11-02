package kdoq

import (
	"encoding/hex"
	"unicode/utf8"
)

// Key is a document key. It holds any bytes, so a string constant converts
// implicitly and a binary key is written with Key(string(b)).
type Key string

// Bytes returns a copy of the key's bytes.
func (k Key) Bytes() []byte { return []byte(k) }

// Text returns the key as text and true when it is valid UTF-8.
func (k Key) Text() (string, bool) {
	if utf8.ValidString(string(k)) {
		return string(k), true
	}
	return "", false
}

// String returns the key as text when it is valid UTF-8 and as hexadecimal
// digits otherwise.
func (k Key) String() string {
	if s, ok := k.Text(); ok {
		return s
	}
	return hex.EncodeToString([]byte(k))
}
