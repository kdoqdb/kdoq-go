package kdoq

import (
	"time"

	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Operation codes of the query operations.
const (
	opQueryCreate  = 0x0501
	opQueryCount   = 0x0502
	opCursorRead   = 0x0503
	opCursorClose  = 0x0504
	keyKindDoc     = 1
	keyKindGrouped = 2
)

func millis(d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64((d + time.Millisecond - 1) / time.Millisecond)
}

func optionalU64(b *wire.Builder, id int, v uint64) {
	if v != 0 {
		b.U64(id, v)
	}
}

func (o FindOptions) observeGroup() *wire.Builder {
	return new(wire.Builder).
		Bool(1, o.Observe.Progress || o.OnProgress != nil).
		Bool(2, o.Observe.Diagnostics || o.OnDiagnostic != nil).
		Bool(3, o.Observe.Statistics)
}

func (o FindOptions) budgetGroup() *wire.Builder {
	b := new(wire.Builder)
	optionalU64(b, 1, o.Budget.MemoryBytes)
	optionalU64(b, 2, millis(o.Budget.AdmissionWait))
	optionalU64(b, 3, millis(o.Budget.MemoryWait))
	optionalU64(b, 4, o.Budget.Steps)
	optionalU64(b, 5, o.Budget.ExpressionBytes)
	optionalU64(b, 6, millis(o.Budget.Active))
	return b
}

func pipelineGroup(stages []PipelineStage) *wire.Builder {
	b := new(wire.Builder)
	for _, s := range stages {
		switch s.kind {
		case 2:
			b.Group(2, new(wire.Builder).String(1, s.parts[0]).String(2, s.parts[1]).String(3, s.parts[2]))
		case 3:
			b.Group(3, new(wire.Builder))
		default:
			b.String(s.kind, s.expr)
		}
	}
	return b
}

// createSpec builds QUERY_CREATE. size is the page size to send.
func (c *Collection[T]) createSpec(filter string, pipeline []PipelineStage, o FindOptions, size uint32) session.Spec {
	var b wire.Builder
	c.namespace(&b)
	if filter != "" {
		b.String(3, filter)
	}
	if len(pipeline) > 0 {
		b.Group(4, pipelineGroup(pipeline))
	}
	page := new(wire.Builder).U32(1, size)
	optionalU64(page, 2, millis(o.IdleTTL))
	options := new(wire.Builder).Group(1, page)
	if o.Skip != 0 || o.Limit != 0 {
		window := new(wire.Builder)
		optionalU64(window, 1, o.Skip)
		optionalU64(window, 2, o.Limit)
		options.Group(2, window)
	}
	options.Group(3, o.observeGroup())
	if budget := o.budgetGroup(); budget.Len() > 0 {
		options.Group(4, budget)
	}
	b.Group(5, options)
	return session.Spec{Operation: opQueryCreate, Payload: b.Bytes()}
}

// readSpec builds CURSOR_READ for a page of size results.
func readSpec(id CursorID, size uint32, admissionWait time.Duration) session.Spec {
	b := new(wire.Builder).U64(1, uint64(id))
	options := new(wire.Builder).Group(1, new(wire.Builder).U32(1, size))
	if wait := millis(admissionWait); wait != 0 {
		options.Group(4, new(wire.Builder).U64(2, wait))
	}
	b.Group(2, options)
	return session.Spec{Operation: opCursorRead, Payload: b.Bytes()}
}

func closeSpec(id CursorID) session.Spec {
	return session.Spec{Operation: opCursorClose, Payload: new(wire.Builder).U64(1, uint64(id)).Bytes()}
}

func (o *observer) onProgress(f *wire.Fields) error {
	if o == nil || o.progress == nil {
		return nil
	}
	processed, err := f.RequireU64(1)
	if err != nil {
		return err
	}
	total, err := f.RequireU64(2)
	if err != nil {
		return err
	}
	o.progress(Progress{Processed: processed, Total: total})
	return nil
}

func (o *observer) onDiagnostic(f *wire.Fields) error {
	if o == nil || o.diagnostic == nil {
		return nil
	}
	var d Diagnostic
	var err error
	if d.Expression, err = f.RequireString(1); err != nil {
		return err
	}
	if stage, ok, err := f.U64(2); err != nil {
		return err
	} else if ok {
		d.Stage = &stage
	}
	span, ok, err := f.Group(3)
	if err != nil || !ok {
		return orMissing(err, 3)
	}
	if d.Span.Start, err = span.RequireU64(1); err != nil {
		return err
	}
	if d.Span.End, err = span.RequireU64(2); err != nil {
		return err
	}
	if d.Reason, err = f.RequireString(4); err != nil {
		return err
	}
	o.diagnostic(d)
	return nil
}

func orMissing(err error, id int) error {
	if err != nil {
		return err
	}
	_, e := (&wire.Fields{}).RequireBytes(id)
	return e
}

func parseStatistics(g *wire.Fields) (*Statistics, error) {
	var s Statistics
	group := func(id int) (*wire.Fields, error) {
		f, ok, err := g.Group(id)
		if err != nil || !ok {
			return nil, orMissing(err, id)
		}
		return f, nil
	}
	read := func(f *wire.Fields, dst ...*uint64) error {
		for i, d := range dst {
			v, err := f.RequireU64(i + 1)
			if err != nil {
				return err
			}
			*d = v
		}
		return nil
	}
	source, err := group(1)
	if err != nil {
		return nil, err
	}
	if err := read(source, &s.Source.Total, &s.Source.Processed, &s.Source.BloomCandidates); err != nil {
		return nil, err
	}
	docs, err := group(2)
	if err != nil {
		return nil, err
	}
	if err := read(docs, &s.Documents.Processed, &s.Documents.Selected, &s.Documents.Rejected, &s.Documents.FilterErrors); err != nil {
		return nil, err
	}
	budget, err := group(3)
	if err != nil {
		return nil, err
	}
	var active uint64
	if err := read(budget, &s.Budget.Steps, &s.Budget.ValueBytes, &active); err != nil {
		return nil, err
	}
	s.Budget.Active = time.Duration(active)
	memory, err := group(4)
	if err != nil {
		return nil, err
	}
	if err := read(memory, &s.Memory.Current, &s.Memory.Peak); err != nil {
		return nil, err
	}
	if r, ok, err := g.Group(5); err != nil {
		return nil, err
	} else if ok {
		s.Reduction = &struct{ AccumulatedDocuments, GroupsCreated uint64 }{}
		if err := read(r, &s.Reduction.AccumulatedDocuments, &s.Reduction.GroupsCreated); err != nil {
			return nil, err
		}
	}
	return &s, nil
}
