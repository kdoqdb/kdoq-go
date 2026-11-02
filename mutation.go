package kdoq

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kdoqdb/kdoq-go/value"
)

type targetKind uint8

const (
	targetNone targetKind = iota
	targetKey
	targetFilter
)

// Target selects the documents a mutation applies to: one key, or the first
// or every match of a filter. The zero Target selects nothing and is refused.
type Target struct {
	kind   targetKind
	key    Key
	filter string
	multi  bool
}

// ByKey targets the document stored under key.
func ByKey(k Key) Target { return Target{kind: targetKey, key: k} }

// Where targets the first document that matches the filter expression.
func Where(filter string) Target { return Target{kind: targetFilter, filter: filter} }

// Multi widens a Where target to every match. It does not apply to ByKey.
func (t Target) Multi() Target {
	t.multi = true
	return t
}

func (t Target) validate() error {
	switch {
	case t.kind == targetNone:
		return &ConfigError{Message: "a mutation needs a target: use ByKey or Where"}
	case t.kind == targetKey && t.multi:
		return &ConfigError{Message: "Multi applies to a Where target, not to ByKey"}
	case t.kind == targetFilter && t.filter == "":
		return &ConfigError{Message: "a Where target needs a filter expression"}
	}
	return nil
}

// Operand is an expression of the query language used as a value, evaluated
// by the server against the document. Build one with Expr.
type Operand struct{ text string }

// Expr returns an expression of the query language as a value, for Set,
// SetPath and Replace.
func Expr(text string) Operand { return Operand{text: text} }

type stageKind uint8

const (
	stageSet stageKind = iota + 1
	stageUnset
	stageReplace
)

type assignment struct {
	path  []string
	value any
}

// Stage is one step of a mutation: a set of assignments, a removal of fields
// or a replacement of the whole document. Stages run in order and each sees
// the result of the one before. Build one with Set, SetPath, Unset or Replace.
type Stage struct {
	kind        stageKind
	assignments []assignment
	paths       [][]string
	operand     any
	err         error
}

// Set assigns values to fields. fields is a value.Doc, whose entries apply in
// name order, or a value.D, whose entries apply in the order given; a struct is
// applied in field order. A name such as "a.b" is the path a, then b. A value
// is a literal or an Expr.
func Set(fields any) Stage {
	entries, err := entriesOf(fields)
	if err != nil {
		return Stage{kind: stageSet, err: err}
	}
	s := Stage{kind: stageSet}
	for _, e := range entries {
		path, err := splitPath(e.Key)
		if err != nil {
			return Stage{kind: stageSet, err: err}
		}
		s.assignments = append(s.assignments, assignment{path: path, value: e.Value})
	}
	return s
}

// SetPath assigns v to the field at path, whose components are literal field
// names: unlike Set, a dot in a component is part of the name.
func SetPath(path []string, v any) Stage {
	if err := checkPath(path); err != nil {
		return Stage{kind: stageSet, err: err}
	}
	return Stage{kind: stageSet, assignments: []assignment{{path: append([]string(nil), path...), value: v}}}
}

// Unset removes fields. A path such as "a.b" is the path a, then b. Removing
// an absent field succeeds.
func Unset(paths ...string) Stage {
	s := Stage{kind: stageUnset}
	for _, p := range paths {
		path, err := splitPath(p)
		if err != nil {
			return Stage{kind: stageUnset, err: err}
		}
		s.paths = append(s.paths, path)
	}
	return s
}

// Replace replaces the whole document with doc, a document or an Expr that
// yields one.
func Replace(doc any) Stage { return Stage{kind: stageReplace, operand: doc} }

func entriesOf(fields any) ([]value.E, error) {
	switch f := fields.(type) {
	case value.D:
		return f, nil
	case value.Doc:
		out := make([]value.E, 0, len(f))
		for k, v := range f {
			out = append(out, value.E{Key: k, Value: v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out, nil
	}
	marshaled, err := value.Marshal(fields)
	if err != nil {
		return nil, documentError(err)
	}
	if d, ok := marshaled.(value.D); ok {
		return d, nil
	}
	if d, ok := marshaled.(value.Doc); ok {
		return entriesOf(d)
	}
	return nil, &Error{Code: CodeInvalidDocument, Message: fmt.Sprintf("Set needs a document, not %T", fields)}
}

func splitPath(name string) ([]string, error) {
	path := strings.Split(name, ".")
	return path, checkPath(path)
}

func checkPath(path []string) error {
	if len(path) == 0 {
		return &Error{Code: CodeInvalidDocument, Message: "a field path needs at least one component"}
	}
	for _, c := range path {
		if c == "" {
			return &Error{Code: CodeInvalidDocument, Message: fmt.Sprintf("invalid field path %q: empty component", strings.Join(path, "."))}
		}
	}
	return nil
}

// MutationResult is the outcome of Update or Upsert, or of one bulk statement.
type MutationResult struct {
	// Key is the key of the document: the targeted key, or the key an upsert
	// inserted. It is empty for a filter statement that inserted nothing.
	Key Key
	// Matched and Modified count the documents of a filter statement; they
	// are zero for a key statement.
	Matched, Modified uint64
	// Inserted reports that an upsert created the document.
	Inserted bool
}

// MutationMode selects what a bulk statement does when nothing matches.
type MutationMode uint8

const (
	// ModeUpdate changes matching documents only.
	ModeUpdate MutationMode = iota
	// ModeUpsert inserts a document when nothing matches.
	ModeUpsert
)

// BulkStatement is one statement of Bulk.
type BulkStatement struct {
	Mode   MutationMode
	Target Target
	Stages []Stage
}

// BulkOption configures Bulk. Only the library implements it; the BulkOptions
// struct is an option itself.
type BulkOption interface{ applyBulk(*BulkOptions) }

// BulkOptions configure a bulk. Passing a BulkOptions always sets Ordered, so
// to set only Durability keep the default order with Ordered: true. Without
// any BulkOptions the bulk is ordered.
type BulkOptions struct {
	// Ordered stops at the first rejected statement; false continues with the
	// independent statements.
	Ordered    bool
	Durability Durability

	orderedSet bool
}

func (o BulkOptions) applyBulk(t *BulkOptions) {
	t.Ordered, t.orderedSet = o.Ordered, true
	if o.Durability != DurabilityDefault {
		t.Durability = o.Durability
	}
}

func bulkOptions(opts []BulkOption) BulkOptions {
	var o BulkOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyBulk(&o)
		}
	}
	return o
}

// BulkResult is the outcome of a bulk: one item per executed statement.
type BulkResult struct {
	Executed int
	Items    []BulkItem
}

// BulkItem is the outcome of one statement of a bulk.
type BulkItem struct {
	Index int
	// Status is zero for an accepted statement, otherwise the error code that
	// rejected it.
	Status ErrorCode
	MutationResult
}

// BulkError reports a failure of the whole command after some statements were
// already accepted. Items are the outcomes received; those statements stay
// applied.
type BulkError struct {
	Err   *Error
	Items []BulkItem
}

// Error implements error.
func (e *BulkError) Error() string {
	return fmt.Sprintf("kdoq: bulk failed after %d statements: %v", len(e.Items), e.Err)
}

// Unwrap returns the failure, so errors.Is and errors.As see through it.
func (e *BulkError) Unwrap() error { return e.Err }
