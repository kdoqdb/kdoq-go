package kdoq

import "time"

const (
	defaultPageSize = 1000
	maxPageSize     = 10000
)

// CursorID identifies a server cursor.
type CursorID uint64

// FindOption configures Find, FindCursor, Aggregate, AggregateCursor and
// Count. Only the library implements it; the FindOptions struct is an option
// itself.
type FindOption interface{ applyFind(*FindOptions) }

// FindOptions configure a query. Zero values mean the defaults; options given
// later override non-zero fields of earlier ones.
type FindOptions struct {
	// PageSize is the number of results per page: 1000 by default, at most
	// 10000. Larger values are capped.
	PageSize int
	// IdleTTL is the time the server keeps the cursor between pages.
	IdleTTL time.Duration
	// Skip and Limit window the results of the whole query. They are sent
	// with the creation of the query only. A zero Limit is unlimited.
	Skip, Limit uint64
	// Budget caps the resources the query may use, within server policy.
	Budget Budget
	// Observe selects what the server reports besides results.
	Observe Observe
	// OnProgress and OnDiagnostic receive the observations, synchronously, in
	// the goroutine that reads the results. Setting one turns the matching
	// observation on.
	OnProgress   func(Progress)
	OnDiagnostic func(Diagnostic)
}

// Budget caps the resources of one query. Zero fields leave the server
// policy in force.
type Budget struct {
	MemoryBytes, Steps, ExpressionBytes uint64
	AdmissionWait, MemoryWait, Active   time.Duration
}

// Observe selects optional server reports. Progress is off unless OnProgress
// is set; statistics are read with Query.Statistics or Page.Statistics.
type Observe struct {
	Progress, Diagnostics, Statistics bool
}

func (o FindOptions) applyFind(t *FindOptions) {
	if o.PageSize != 0 {
		t.PageSize = o.PageSize
	}
	if o.IdleTTL != 0 {
		t.IdleTTL = o.IdleTTL
	}
	if o.Skip != 0 {
		t.Skip = o.Skip
	}
	if o.Limit != 0 {
		t.Limit = o.Limit
	}
	b := &t.Budget
	setU64(&b.MemoryBytes, o.Budget.MemoryBytes)
	setU64(&b.Steps, o.Budget.Steps)
	setU64(&b.ExpressionBytes, o.Budget.ExpressionBytes)
	setDuration(&b.AdmissionWait, o.Budget.AdmissionWait)
	setDuration(&b.MemoryWait, o.Budget.MemoryWait)
	setDuration(&b.Active, o.Budget.Active)
	t.Observe.Progress = t.Observe.Progress || o.Observe.Progress
	t.Observe.Diagnostics = t.Observe.Diagnostics || o.Observe.Diagnostics
	t.Observe.Statistics = t.Observe.Statistics || o.Observe.Statistics
	if o.OnProgress != nil {
		t.OnProgress = o.OnProgress
	}
	if o.OnDiagnostic != nil {
		t.OnDiagnostic = o.OnDiagnostic
	}
}

func setU64(dst *uint64, v uint64) {
	if v != 0 {
		*dst = v
	}
}

func setDuration(dst *time.Duration, v time.Duration) {
	if v != 0 {
		*dst = v
	}
}

func findOptions(opts []FindOption) FindOptions {
	var o FindOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyFind(&o)
		}
	}
	return o
}

// pageSize returns the page size to send.
func (o FindOptions) pageSize() uint32 {
	switch {
	case o.PageSize <= 0:
		return defaultPageSize
	case o.PageSize > maxPageSize:
		return maxPageSize
	}
	return uint32(o.PageSize)
}

// NextOption configures Cursor.Next. Only the library implements it; the
// NextOptions struct is an option itself.
type NextOption interface{ applyNext(*NextOptions) }

// NextOptions configure one page of a cursor. Zero values mean the cursor's
// own settings.
type NextOptions struct {
	// Size is the number of results of this page, capped at 10000.
	Size int
	// OnProgress and OnDiagnostic receive the observations of this page.
	OnProgress   func(Progress)
	OnDiagnostic func(Diagnostic)
}

func (o NextOptions) applyNext(t *NextOptions) {
	if o.Size != 0 {
		t.Size = o.Size
	}
	if o.OnProgress != nil {
		t.OnProgress = o.OnProgress
	}
	if o.OnDiagnostic != nil {
		t.OnDiagnostic = o.OnDiagnostic
	}
}

// Progress reports how far the server has read its source.
type Progress struct {
	Processed, Total uint64
}

// Diagnostic reports a problem the server met while evaluating an expression.
type Diagnostic struct {
	// Expression is the role of the expression: query, match, projection or a
	// reduction role.
	Expression string
	// Stage is the pipeline stage index, nil when the diagnostic has none.
	Stage *uint64
	// Span is the part of the expression text the diagnostic concerns.
	Span   struct{ Start, End uint64 }
	Reason string
}

// Statistics describes the work of a query, reported at the end of its
// creation when Observe.Statistics is set.
type Statistics struct {
	Source struct {
		Total, Processed, BloomCandidates uint64
	}
	Documents struct {
		Processed, Selected, Rejected, FilterErrors uint64
	}
	Budget struct {
		Steps, ValueBytes uint64
		Active            time.Duration
	}
	Memory struct {
		Current, Peak uint64
	}
	// Reduction is set for grouped reductions only.
	Reduction *struct{ AccumulatedDocuments, GroupsCreated uint64 }
}

// PipelineStage is one stage of an aggregation. Build one with Project, Match,
// Reduce or Count.
type PipelineStage struct {
	kind  int
	expr  string
	parts [3]string
}

// Project replaces each item's value with the value of a projection expression.
func Project(expr string) PipelineStage { return PipelineStage{kind: 1, expr: expr} }

// Match keeps the items for which the expression is true.
func Match(expr string) PipelineStage { return PipelineStage{kind: 4, expr: expr} }

// Reduce groups the items by the key expression and folds each group from the
// initial expression with the accumulate expression. Its items carry a
// grouping key in Item.Group.
func Reduce(key, initial, accumulate string) PipelineStage {
	return PipelineStage{kind: 2, parts: [3]string{key, initial, accumulate}}
}

// Count replaces the items with one item that counts them. It has no key.
func Count() PipelineStage { return PipelineStage{kind: 3} }
