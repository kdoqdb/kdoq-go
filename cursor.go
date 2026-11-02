package kdoq

import (
	"context"
	"errors"
	"time"
)

// Cursor reads the results of a query page by page. A Cursor belongs to one
// goroutine.
type Cursor[T any] struct {
	client        *Client
	id            CursorID
	size          uint32
	admissionWait time.Duration
	obs           *observer
	closed        bool
	// cancelled is the error of a read that ctx cancelled: the cursor was
	// closed, and later reads must not look like a clean end.
	cancelled error
}

// FindCursor creates the query and returns a cursor on it; no result is read
// yet. The cursor's page size is FindOptions.PageSize.
func (c *Collection[T]) FindCursor(ctx context.Context, filter string, opts ...FindOption) (*Cursor[T], error) {
	return openCursor[T, T](ctx, c, filter, nil, opts)
}

// AggregateCursor is FindCursor with a pipeline, with its own result type.
func (c *Collection[T]) AggregateCursor[R any](ctx context.Context, filter string, pipeline []PipelineStage, opts ...FindOption) (*Cursor[R], error) {
	return openCursor[T, R](ctx, c, filter, pipeline, opts)
}

func openCursor[T, R any](ctx context.Context, c *Collection[T], filter string, pipeline []PipelineStage, opts []FindOption) (*Cursor[R], error) {
	o := findOptions(opts)
	pg, err := c.client.fetch(ctx, c.createSpec(filter, pipeline, o, 0), &observer{progress: o.OnProgress, diagnostic: o.OnDiagnostic})
	if err != nil {
		if pg.cursor != 0 {
			_ = c.client.closeCursor(ctx, pg.cursor)
		}
		return nil, err
	}
	if pg.cursor == 0 {
		return nil, &Error{Code: CodeMalformed, Message: "the server returned no cursor"}
	}
	return &Cursor[R]{
		client: c.client, id: pg.cursor, size: o.pageSize(), admissionWait: o.Budget.AdmissionWait,
		obs: &observer{progress: o.OnProgress, diagnostic: o.OnDiagnostic},
	}, nil
}

// AttachCursor continues a server cursor by its identifier, for example one
// created on another connection of the same identity. R must be the type the
// cursor was created for; a mismatch is a decoding error from Next.
func (c *Client) AttachCursor[R any](id CursorID) *Cursor[R] {
	return &Cursor[R]{client: c, id: id, size: defaultPageSize, obs: &observer{}}
}

// ID returns the cursor identifier, to continue it from another connection.
func (c *Cursor[T]) ID() CursorID { return c.id }

// Next reads the next page. After the last page, and after Close, it returns
// an empty page that is Done; after a read cancelled by ctx the cursor is
// closed and Next keeps returning that error. If the page cannot be decoded as T, the page is
// consumed and the error is returned.
func (c *Cursor[T]) Next(ctx context.Context, opts ...NextOption) (Page[T], error) {
	if c.cancelled != nil {
		return Page[T]{}, c.cancelled
	}
	if c.closed {
		return Page[T]{Done: true}, nil
	}
	var o NextOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyNext(&o)
		}
	}
	size := c.size
	if o.Size > 0 {
		size = min(uint32(o.Size), maxPageSize)
	}
	obs := &observer{progress: c.obs.progress, diagnostic: c.obs.diagnostic}
	if o.OnProgress != nil {
		obs.progress = o.OnProgress
	}
	if o.OnDiagnostic != nil {
		obs.diagnostic = o.OnDiagnostic
	}
	pg, err := c.client.fetch(ctx, readSpec(c.id, size, c.admissionWait), obs)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			_ = c.Close(ctx)
			c.cancelled = err
		}
		return Page[T]{}, err
	}
	if pg.next == 0 {
		c.closed = true
	}
	items, err := decodePage[T](pg)
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: items, Done: pg.next == 0, Statistics: pg.stats}, nil
}

// Close closes the cursor on the server. It is idempotent.
func (c *Cursor[T]) Close(ctx context.Context) error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.client.closeCursor(ctx, c.id)
}
