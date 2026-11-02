package kdoq

import (
	"context"

	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Find starts a query. An empty filter selects every document. The query runs
// when its results are iterated.
func (c *Collection[T]) Find(ctx context.Context, filter string, opts ...FindOption) *Query[T] {
	o := findOptions(opts)
	return newQuery[T](ctx, c.client, c.createSpec(filter, nil, o, o.pageSize()), o)
}

// Aggregate starts a query whose pipeline changes the shape of its results
// (projection, reduction, count). R is the type of the results.
func (c *Collection[T]) Aggregate[R any](ctx context.Context, filter string, pipeline []PipelineStage, opts ...FindOption) *Query[R] {
	o := findOptions(opts)
	return newQuery[R](ctx, c.client, c.createSpec(filter, pipeline, o, o.pageSize()), o)
}

// Count returns the number of documents matching the filter, from metadata.
// A count of an aggregation is Aggregate[int64] with the Count stage.
func (c *Collection[T]) Count(ctx context.Context, filter string, opts ...FindOption) (uint64, error) {
	o := findOptions(opts)
	var b wire.Builder
	c.namespace(&b)
	if filter != "" {
		b.String(3, filter)
	}
	options := new(wire.Builder).Group(3, o.observeGroup())
	if budget := o.budgetGroup(); budget.Len() > 0 {
		options.Group(4, budget)
	}
	b.Group(4, options)
	resp, err := c.client.exchange(ctx, session.Spec{Operation: opQueryCount, Payload: b.Bytes()}, CapQueries,
		&observer{progress: o.OnProgress, diagnostic: o.OnDiagnostic})
	if err != nil {
		return 0, err
	}
	item, err := resp.single("QUERY_COUNT")
	if err != nil {
		return 0, err
	}
	n, err := item.RequireU64(1)
	if err != nil {
		return 0, malformedResponse(err)
	}
	return n, nil
}
