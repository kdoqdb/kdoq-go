package kdoq

import (
	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/source"
	"github.com/kdoqdb/kdoq-go/value"
)

// readDocument reads the result document body, announced in context, into a
// T; key is the document key when keyed.
//
// The document is read straight into the T. When that fails, the document is
// read again through the tree decoder and value.Unmarshal, which are the
// reference and give the error: the path and the types involved, or the
// malformed response.
func readDocument[T any](op *session.Op, context uint64, body []byte, key string, keyed bool) (T, error) {
	var out T
	stream := document.NewStream(op.Names(context), body)
	err := source.Unmarshal(stream, key, keyed, &out)
	stream.Release()
	if err == nil {
		return out, nil
	}

	var zero T
	decoded, err := op.Decode(context, body)
	if err != nil {
		return zero, malformedResponse(err)
	}
	if keyed {
		err = value.UnmarshalKeyed(decoded, key, &zero)
	} else {
		err = value.Unmarshal(decoded, &zero)
	}
	return zero, err
}
