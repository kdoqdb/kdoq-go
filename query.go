package kdoq

import (
	"context"
	"iter"

	"github.com/kdoqdb/kdoq-go/internal/session"
)

// Query is a running read. Iterate it once with All, Values or Collect;
// leaving the loop early, or cancelling the context, cancels the query and
// closes its cursor. A Query belongs to one goroutine.
type Query[T any] struct {
	client *Client
	ctx    context.Context
	create session.Spec
	opts   FindOptions
	size   uint32
	obs    *observer

	started   bool
	cancelled bool
	cursor    CursorID
	stats     *Statistics
}

func newQuery[T any](ctx context.Context, c *Client, create session.Spec, opts FindOptions) *Query[T] {
	return &Query[T]{
		client: c, ctx: ctx, create: create, opts: opts, size: opts.pageSize(),
		obs: &observer{progress: opts.OnProgress, diagnostic: opts.OnDiagnostic},
	}
}

// All iterates the results page by page. The server cursor is closed when the
// loop ends, however it ends. A failure is yielded as the final element.
func (q *Query[T]) All() iter.Seq2[Item[T], error] {
	return func(yield func(Item[T], error) bool) {
		if q.started {
			yield(Item[T]{}, &Error{Message: "a query can be iterated only once"})
			return
		}
		q.started = true
		defer q.release()
		spec := q.create
		for {
			pg, err := q.client.fetch(q.ctx, spec, q.obs)
			if pg.cursor != 0 {
				q.cursor = pg.cursor
			}
			if err != nil {
				yield(Item[T]{}, err)
				return
			}
			q.cursor = pg.next
			if pg.stats != nil {
				q.stats = pg.stats
			}
			for _, f := range pg.resp.items {
				if q.cancelled {
					return
				}
				if q.ctx.Err() != nil {
					yield(Item[T]{}, contextError(q.ctx, false, 0))
					return
				}
				item, err := decodeItem[T](pg.resp.op, f)
				if !yield(item, err) || err != nil {
					return
				}
			}
			if q.cancelled || pg.next == 0 {
				return
			}
			spec = readSpec(pg.next, q.size, q.opts.Budget.AdmissionWait)
		}
	}
}

// release closes the cursor if one is still open.
func (q *Query[T]) release() { _ = q.Cancel() }

// Values iterates the values of the results and drops their keys.
func (q *Query[T]) Values() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for item, err := range q.All() {
			if !yield(item.Value, err) {
				return
			}
		}
	}
}

// Collect reads every result into a slice. It returns the results read so far
// together with the first failure.
func (q *Query[T]) Collect() ([]Item[T], error) {
	var out []Item[T]
	for item, err := range q.All() {
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}

// CursorID returns the cursor that stays open between pages, and false when
// there is none: before the first page, after the last one, or after the
// query was cancelled.
func (q *Query[T]) CursorID() (CursorID, bool) { return q.cursor, q.cursor != 0 }

// Statistics returns the statistics the server reported with the creation of
// the query, when Observe.Statistics was set.
func (q *Query[T]) Statistics() (*Statistics, bool) { return q.stats, q.stats != nil }

// Cancel stops the query and closes its cursor. Like every other method it
// belongs to the query's goroutine: call it from the loop body to end the loop
// at its next step. It is idempotent.
func (q *Query[T]) Cancel() error {
	q.cancelled = true
	id := q.cursor
	if id == 0 {
		return nil
	}
	q.cursor = 0
	return q.client.closeCursor(q.ctx, id)
}
