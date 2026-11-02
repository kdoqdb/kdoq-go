package fakeserver

import (
	"fmt"
	"strconv"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/wire"
	"github.com/kdoqdb/kdoq-go/value"
)

// Stage kinds of a mutation statement, as the protocol numbers them.
const (
	StageAssign  = 1
	StageRemove  = 2
	StageReplace = 3
)

// Operand is a stage operand: an expression, or a literal read from the
// statement's literal pool.
type Operand struct {
	IsExpression bool
	Expression   string
	Literal      any
}

// Pair assigns an operand to a path.
type Pair struct {
	Path    []string
	Operand Operand
}

// MutationStage is one stage of a received statement.
type MutationStage struct {
	Kind    int
	Pairs   []Pair     // Assign
	Paths   [][]string // Remove
	Operand Operand    // Replace
}

// Statement is one parsed statement INPUT_VALUE.
type Statement struct {
	Stages    []MutationStage
	Key       []byte
	HasKey    bool
	Mode      *uint16
	Filter    string
	HasFilter bool
	Multi     bool
}

// MutationReply is the ITEM of an update or upsert, or the outcome of one
// bulk statement.
type MutationReply struct {
	Key      []byte
	Flags    uint8
	Matched  *uint64
	Modified *uint64
}

// Statements parses every statement of the call, in order.
func (c *Call) Statements() ([]Statement, error) {
	var out []Statement
	for _, f := range c.Inputs {
		if f.Kind != wire.KindInputValue {
			continue
		}
		st, err := c.parseStatement(f.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func (c *Call) parseStatement(payload []byte) (Statement, error) {
	var st Statement
	f, err := wire.Parse(payload)
	if err != nil {
		return st, err
	}
	var pool map[string]any
	if g, ok, err := f.Group(1); err != nil {
		return st, err
	} else if ok {
		ctx, err := g.RequireU64(1)
		if err != nil {
			return st, err
		}
		body, err := g.RequireBytes(2)
		if err != nil {
			return st, err
		}
		names, ok := c.contexts[ctx]
		if !ok {
			return st, fmt.Errorf("literal pool context %d was not declared", ctx)
		}
		v, err := document.Decode(names, body)
		if err != nil {
			return st, err
		}
		doc, ok := v.(value.Doc)
		if !ok {
			return st, fmt.Errorf("the literal pool is %T, not an object", v)
		}
		pool = doc
	}
	stages, ok := f.Bytes(2)
	if !ok {
		return st, fmt.Errorf("a statement needs stages")
	}
	if st.Stages, err = parseStages(stages, pool); err != nil {
		return st, err
	}
	st.Key, st.HasKey = f.Bytes(3)
	if mode, ok, _ := f.U16(4); ok {
		st.Mode = &mode
	}
	st.Filter, st.HasFilter, _ = f.String(5)
	multi, _, _ := f.U8(6)
	st.Multi = multi == 1
	return st, nil
}

func parseStages(raw []byte, pool map[string]any) ([]MutationStage, error) {
	var out []MutationStage
	err := eachField(raw, func(id int, body []byte) error {
		stage := MutationStage{Kind: id}
		var err error
		switch id {
		case StageAssign:
			stage.Pairs, err = parsePairs(body, pool)
		case StageRemove:
			err = eachField(body, func(_ int, path []byte) error {
				components, err := parsePath(path)
				stage.Paths = append(stage.Paths, components)
				return err
			})
		case StageReplace:
			var f *wire.Fields
			if f, err = wire.Parse(body); err == nil {
				var operand []byte
				if operand, err = f.RequireBytes(1); err == nil {
					stage.Operand, err = parseOperand(operand, pool)
				}
			}
		}
		out = append(out, stage)
		return err
	})
	return out, err
}

func parsePairs(body []byte, pool map[string]any) ([]Pair, error) {
	var pairs []Pair
	err := eachField(body, func(_ int, raw []byte) error {
		f, err := wire.Parse(raw)
		if err != nil {
			return err
		}
		path, err := f.RequireBytes(1)
		if err != nil {
			return err
		}
		operand, err := f.RequireBytes(2)
		if err != nil {
			return err
		}
		var p Pair
		if p.Path, err = parsePath(path); err != nil {
			return err
		}
		if p.Operand, err = parseOperand(operand, pool); err != nil {
			return err
		}
		pairs = append(pairs, p)
		return nil
	})
	return pairs, err
}

func parsePath(raw []byte) ([]string, error) {
	f, err := wire.Parse(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range f.All(1) {
		out = append(out, string(c))
	}
	return out, nil
}

func parseOperand(raw []byte, pool map[string]any) (Operand, error) {
	f, err := wire.Parse(raw)
	if err != nil {
		return Operand{}, err
	}
	if text, ok, _ := f.String(2); ok {
		return Operand{IsExpression: true, Expression: text}, nil
	}
	index, err := f.RequireU32(1)
	if err != nil {
		return Operand{}, err
	}
	v, ok := pool[strconv.Itoa(int(index))]
	if !ok {
		return Operand{}, fmt.Errorf("literal %d is not in the pool", index)
	}
	return Operand{Literal: v}, nil
}

// eachField visits the fields of an ordered group in order.
func eachField(raw []byte, visit func(id int, body []byte) error) error {
	for pos := 0; pos < len(raw); {
		id := int(raw[pos] & 0x3f)
		length, n, err := wire.ReadU32V(raw[pos+1:])
		if err != nil {
			return err
		}
		start := pos + 1 + n
		if start+int(length) > len(raw) {
			return fmt.Errorf("truncated field")
		}
		if err := visit(id, raw[start:start+int(length)]); err != nil {
			return err
		}
		pos = start + int(length)
	}
	return nil
}

// ReplyMutation writes BEGIN, the single mutation ITEM and END.
func (c *Call) ReplyMutation(r MutationReply) {
	c.Begin(nil)
	b := new(wire.Builder)
	if r.Key != nil {
		b.Binary(1, r.Key)
	}
	b.U8(2, r.Flags)
	if r.Matched != nil {
		b.U64(3, *r.Matched)
	}
	if r.Modified != nil {
		b.U64(4, *r.Modified)
	}
	c.Item(b)
	c.End(nil)
}

// BulkItem writes the ITEM of one bulk statement.
func (c *Call) BulkItem(index uint32, status uint16, r MutationReply) {
	b := new(wire.Builder).U32(1, index).U16(2, status).U8(3, r.Flags)
	if r.Key != nil {
		b.Binary(4, r.Key)
	}
	if r.Matched != nil {
		b.U64(5, *r.Matched)
	}
	if r.Modified != nil {
		b.U64(6, *r.Modified)
	}
	c.Item(b)
}

// EndBulk writes END with the number of statements executed.
func (c *Call) EndBulk(executed uint32) { c.End(new(wire.Builder).U32(1, executed)) }

// mutations answers update, upsert and bulk the way a server would without
// filters: a key statement matches when the document exists, and an upsert
// of a missing key inserts an empty document.
func (s *Server) mutations(call *Call) {
	statements, err := call.Statements()
	if err != nil || len(statements) == 0 {
		call.Fail(1, false)
		return
	}
	db, err1 := call.Payload.RequireString(1)
	coll, err2 := call.Payload.RequireString(2)
	if err1 != nil || err2 != nil {
		call.Fail(1, false)
		return
	}
	if call.Operation == 0x0407 {
		call.Begin(nil)
		for i, st := range statements {
			reply := s.apply(db, coll, st, st.Mode != nil && *st.Mode == 1)
			call.BulkItem(uint32(i), 0, reply)
		}
		call.EndBulk(uint32(len(statements)))
		return
	}
	key, _ := call.Payload.Bytes(3)
	st := statements[0]
	if !st.HasFilter {
		st.Key, st.HasKey = key, true
	}
	call.ReplyMutation(s.apply(db, coll, st, call.Operation == 0x0406))
}

func (s *Server) apply(db, coll string, st Statement, upsert bool) MutationReply {
	if st.HasFilter {
		return MutationReply{}
	}
	if _, exists := s.Doc(db, coll, st.Key); exists {
		return MutationReply{Key: st.Key, Flags: 3}
	}
	if !upsert {
		return MutationReply{Key: st.Key}
	}
	empty, _ := document.EncodeDocument(document.NewDictionary(), value.Doc{})
	s.Put(db, coll, st.Key, document.Names{}, empty)
	return MutationReply{Key: st.Key, Flags: 4}
}
