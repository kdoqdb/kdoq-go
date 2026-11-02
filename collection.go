package kdoq

import (
	"context"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

// Operation codes of the keyed document operations.
const (
	opDocumentGet    = 0x0401
	opDocumentPut    = 0x0402
	opDocumentInsert = 0x0403
	opDocumentDelete = 0x0404
)

// Collection is a handle on a collection whose documents are read and written
// as T. Creating one touches no network and it is safe for concurrent use.
type Collection[T any] struct {
	client         *Client
	database, name string
}

// Database returns the name of the database.
func (c *Collection[T]) Database() string { return c.database }

// Name returns the collection name.
func (c *Collection[T]) Name() string { return c.name }

func (c *Collection[T]) namespace(b *wire.Builder) {
	b.String(1, c.database).String(2, c.name)
}

func (c *Collection[T]) keyPayload(key Key, opts WriteOptions, withKey bool, write bool) []byte {
	var b wire.Builder
	c.namespace(&b)
	if withKey {
		b.Binary(3, []byte(key))
	}
	if write {
		switch opts.Durability {
		case DurabilityDurable:
			b.Group(4, new(wire.Builder).U16(1, 0))
		case DurabilityRelaxed:
			b.Group(4, new(wire.Builder).U16(1, 1))
		}
	}
	return b.Bytes()
}

// Get reads the document stored under key. A missing key is ErrNotFound.
func (c *Collection[T]) Get(ctx context.Context, key Key) (T, error) {
	var zero T
	resp, err := c.client.unary(ctx, session.Spec{
		Operation: opDocumentGet,
		Payload:   c.keyPayload(key, WriteOptions{}, true, false),
	}, CapDocuments)
	if err != nil {
		return zero, err
	}
	if len(resp.items) != 1 {
		return zero, &Error{Code: CodeMalformed, Message: "DOCUMENT_GET did not return exactly one item"}
	}
	item := resp.items[0]
	itemKey, err := item.RequireBytes(1)
	if err != nil {
		return zero, malformedResponse(err)
	}
	contextID, err := item.RequireU64(2)
	if err != nil {
		return zero, malformedResponse(err)
	}
	body, err := item.RequireBytes(3)
	if err != nil {
		return zero, malformedResponse(err)
	}
	return readDocument[T](resp.op, contextID, body, string(itemKey), true)
}

func malformedResponse(err error) error {
	return &Error{Code: CodeMalformed, Message: "invalid response: " + err.Error()}
}

// Put stores doc under key, replacing the document there or creating it. It
// returns the key.
func (c *Collection[T]) Put(ctx context.Context, key Key, doc T, opts ...WriteOption) (Key, error) {
	o := writeOptions(opts)
	inputs, _, err := c.encode(doc)
	if err != nil {
		return "", err
	}
	return c.write(ctx, session.Spec{
		Operation: opDocumentPut,
		Payload:   c.keyPayload(key, o, true, true),
		Inputs:    inputs,
		Write:     true,
	})
}

// Insert stores doc under a new key and fails with ErrAlreadyExists if the key
// is live. The key is WriteOptions.Key, else the `kdoq:",key"` field of doc,
// else the server generates one. It returns the key.
func (c *Collection[T]) Insert(ctx context.Context, doc T, opts ...WriteOption) (Key, error) {
	o := writeOptions(opts)
	inputs, tagged, err := c.encode(doc)
	if err != nil {
		return "", err
	}
	key, withKey := o.Key, o.Key != ""
	if !withKey && tagged != "" {
		key, withKey = Key(tagged), true
	}
	return c.write(ctx, session.Spec{
		Operation: opDocumentInsert,
		Payload:   c.keyPayload(key, o, withKey, true),
		Inputs:    inputs,
		Write:     true,
	})
}

// Delete removes the document under key. Deleting a missing key succeeds. It
// returns the key.
func (c *Collection[T]) Delete(ctx context.Context, key Key, opts ...WriteOption) (Key, error) {
	return c.write(ctx, session.Spec{
		Operation: opDocumentDelete,
		Payload:   c.keyPayload(key, writeOptions(opts), true, true),
		Write:     true,
	})
}

// encode marshals doc and returns the DICTIONARY and INPUT_VALUE frames and
// the value of the key field, if the document has one.
func (c *Collection[T]) encode(doc T) ([][]byte, string, error) {
	marshaled, err := value.Marshal(doc)
	if err != nil {
		return nil, "", documentError(err)
	}
	tagged, _, err := value.KeyOf(doc)
	if err != nil {
		return nil, "", documentError(err)
	}
	dict := document.NewDictionary()
	body, err := document.EncodeDocument(dict, marshaled)
	if err != nil {
		return nil, "", documentError(err)
	}
	var input wire.Builder
	input.U64(1, document.Context).Binary(2, body)
	return append(dict.Frames(), wire.Encode(wire.KindInputValue, input.Bytes())), tagged, nil
}

func (c *Collection[T]) write(ctx context.Context, spec session.Spec) (Key, error) {
	resp, err := c.client.unary(ctx, spec, CapDocuments)
	if err != nil {
		return "", err
	}
	if len(resp.items) != 1 {
		return "", &Error{Code: CodeMalformed, Message: "the response did not carry exactly one key", Outcome: OutcomeUnknown}
	}
	key, err := resp.items[0].RequireBytes(1)
	if err != nil {
		return "", malformedResponse(err)
	}
	return Key(key), nil
}
