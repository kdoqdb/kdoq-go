package kdoq

import (
	"context"
	"errors"
	"strconv"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

// Operation codes of the online mutations.
const (
	opDocumentUpdate = 0x0405
	opDocumentUpsert = 0x0406
	opDocumentBulk   = 0x0407
)

func pathGroup(path []string) *wire.Builder {
	b := new(wire.Builder)
	for _, c := range path {
		b.String(1, c)
	}
	return b
}

// statementEncoder turns statements into the DICTIONARY and INPUT_VALUE frames
// of one operation stream. The dictionary is shared by its statements.
type statementEncoder struct {
	dict *document.Dictionary
}

func newStatementEncoder() *statementEncoder {
	return &statementEncoder{dict: document.NewDictionary()}
}

// encode returns the frames of one statement. For a bulk, mode and key go in
// the statement; a single operation sends the key in its payload.
func (e *statementEncoder) encode(t Target, stages []Stage, bulk bool, mode MutationMode) ([][]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		return nil, &ConfigError{Message: "a mutation needs at least one stage"}
	}
	var pool value.Doc
	operand := func(v any) (*wire.Builder, error) {
		if op, ok := v.(Operand); ok {
			return new(wire.Builder).String(2, op.text), nil
		}
		marshaled, err := value.Marshal(v)
		if err != nil {
			return nil, documentError(err)
		}
		if pool == nil {
			pool = value.Doc{}
		}
		pool[strconv.Itoa(len(pool))] = marshaled
		return new(wire.Builder).U32(1, uint32(len(pool)-1)), nil
	}
	encoded := new(wire.Builder)
	for _, s := range stages {
		if s.err != nil {
			return nil, s.err
		}
		switch s.kind {
		case stageSet:
			if len(s.assignments) == 0 {
				return nil, &Error{Code: CodeInvalidDocument, Message: "Set needs at least one field"}
			}
			body := new(wire.Builder)
			for _, a := range s.assignments {
				op, err := operand(a.value)
				if err != nil {
					return nil, err
				}
				body.Group(1, new(wire.Builder).Group(1, pathGroup(a.path)).Group(2, op))
			}
			encoded.Group(1, body)
		case stageUnset:
			if len(s.paths) == 0 {
				return nil, &Error{Code: CodeInvalidDocument, Message: "Unset needs at least one path"}
			}
			body := new(wire.Builder)
			for _, p := range s.paths {
				body.Group(1, pathGroup(p))
			}
			encoded.Group(2, body)
		case stageReplace:
			op, err := operand(s.operand)
			if err != nil {
				return nil, err
			}
			encoded.Group(3, new(wire.Builder).Group(1, op))
		default:
			return nil, &ConfigError{Message: "the zero Stage is not a stage"}
		}
	}
	var statement wire.Builder
	if pool != nil {
		object, err := document.EncodeDocument(e.dict, pool)
		if err != nil {
			return nil, documentError(err)
		}
		statement.Group(1, new(wire.Builder).U64(1, document.Context).Binary(2, object))
	}
	statement.Group(2, encoded)
	if bulk {
		if t.kind == targetKey {
			statement.Binary(3, []byte(t.key))
		}
		statement.U16(4, uint16(mode))
	}
	if t.kind == targetFilter {
		statement.String(5, t.filter)
		if t.multi {
			statement.U8(6, 1)
		}
	}
	return append(e.dict.Frames(), wire.Encode(wire.KindInputValue, statement.Bytes())), nil
}

// mutationOptions encodes the options group of an update, upsert or bulk, and
// is empty when every option has its default.
func mutationOptions(d Durability, ordered *bool) *wire.Builder {
	b := new(wire.Builder)
	switch d {
	case DurabilityDurable:
		b.U16(1, 0)
	case DurabilityRelaxed:
		b.U16(1, 1)
	}
	if ordered != nil {
		b.Bool(2, *ordered)
	}
	return b
}

// Update applies stages to the documents the target selects. An absent key is
// not an error: nothing is written.
func (c *Collection[T]) Update(ctx context.Context, t Target, stages []Stage, opts ...WriteOption) (MutationResult, error) {
	return c.mutate(ctx, opDocumentUpdate, t, stages, opts)
}

// Upsert is Update that inserts the result of applying stages to an empty
// document when nothing matches. A filter upsert inserts under a key the
// server generates.
func (c *Collection[T]) Upsert(ctx context.Context, t Target, stages []Stage, opts ...WriteOption) (MutationResult, error) {
	return c.mutate(ctx, opDocumentUpsert, t, stages, opts)
}

func (c *Collection[T]) mutate(ctx context.Context, op uint16, t Target, stages []Stage, opts []WriteOption) (MutationResult, error) {
	o := writeOptions(opts)
	frames, err := newStatementEncoder().encode(t, stages, false, 0)
	if err != nil {
		return MutationResult{}, err
	}
	var b wire.Builder
	c.namespace(&b)
	if t.kind == targetKey {
		b.Binary(3, []byte(t.key))
	}
	if options := mutationOptions(o.Durability, nil); options.Len() > 0 {
		b.Group(4, options)
	}
	resp, err := c.client.unary(ctx, session.Spec{Operation: op, Payload: b.Bytes(), Inputs: frames, Write: true}, CapMutations)
	if err != nil {
		return MutationResult{}, err
	}
	item, err := resp.single("the mutation")
	if err != nil {
		return MutationResult{}, &Error{Code: CodeMalformed, Message: "the mutation did not return exactly one item", Outcome: OutcomeUnknown}
	}
	return mutationResult(item, 1, 2, 3, 4)
}

// mutationResult reads key, flags, matched and modified from the given field
// ids; the ids differ between a single mutation and a bulk item.
func mutationResult(f *wire.Fields, keyID, flagsID, matchedID, modifiedID int) (MutationResult, error) {
	var r MutationResult
	if key, ok := f.Bytes(keyID); ok {
		r.Key = Key(key)
	}
	flags, err := f.RequireU8(flagsID)
	if err != nil {
		return r, malformedResponse(err)
	}
	r.Inserted = flags&4 != 0
	if r.Matched, _, err = f.U64(matchedID); err != nil {
		return r, malformedResponse(err)
	}
	if r.Modified, _, err = f.U64(modifiedID); err != nil {
		return r, malformedResponse(err)
	}
	return r, nil
}

// Bulk applies many statements with one outcome per statement. Ordered
// execution (the default) stops after the first rejected statement.
func (c *Collection[T]) Bulk(ctx context.Context, stmts []BulkStatement, opts ...BulkOption) (BulkResult, error) {
	o := bulkOptions(opts)
	if len(stmts) == 0 {
		return BulkResult{}, &ConfigError{Message: "a bulk needs at least one statement"}
	}
	enc := newStatementEncoder()
	var inputs [][]byte
	for _, s := range stmts {
		frames, err := enc.encode(s.Target, s.Stages, true, s.Mode)
		if err != nil {
			return BulkResult{}, err
		}
		inputs = append(inputs, frames...)
	}
	var b wire.Builder
	c.namespace(&b)
	var ordered *bool
	if o.orderedSet {
		ordered = &o.Ordered
	}
	if options := mutationOptions(o.Durability, ordered); options.Len() > 0 {
		b.Group(3, options)
	}
	resp, err := c.client.exchange(ctx, session.Spec{Operation: opDocumentBulk, Payload: b.Bytes(), Inputs: inputs, Write: true}, CapMutations, nil)
	if err != nil {
		return BulkResult{}, bulkFailure(resp, err)
	}
	items, err := bulkItems(resp)
	if err != nil {
		return BulkResult{}, err
	}
	executed, err := resp.end.RequireU32(1)
	if err != nil {
		return BulkResult{}, malformedResponse(err)
	}
	return BulkResult{Executed: int(executed), Items: items}, nil
}

func bulkItems(resp *response) ([]BulkItem, error) {
	items := make([]BulkItem, 0, len(resp.items))
	for _, f := range resp.items {
		index, err := f.RequireU32(1)
		if err != nil {
			return items, malformedResponse(err)
		}
		status, err := f.RequireU16(2)
		if err != nil {
			return items, malformedResponse(err)
		}
		result, err := mutationResult(f, 4, 3, 5, 6)
		if err != nil {
			return items, err
		}
		items = append(items, BulkItem{Index: int(index), Status: ErrorCode(status), MutationResult: result})
	}
	return items, nil
}

// bulkFailure wraps a failure that came after statements were accepted in a
// BulkError carrying their outcomes; the outcome of the rest is unknown.
func bulkFailure(resp *response, err error) error {
	if resp == nil || len(resp.items) == 0 {
		return err
	}
	items, _ := bulkItems(resp)
	var cause *Error
	if !errors.As(err, &cause) {
		cause = &Error{Message: err.Error(), cause: err}
	} else {
		copied := *cause
		cause = &copied
	}
	cause.Outcome = OutcomeUnknown
	return &BulkError{Err: cause, Items: items}
}
