package kdoq

import (
	"context"

	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Item is one result of a query.
type Item[T any] struct {
	// Key is the document key of a plain result. A grouped or counted result
	// has none.
	Key Key
	// Group is the grouping key of a reduction result, nil otherwise.
	Group *GroupKey
	Value T
}

// GroupKey is the grouping key of a reduction result.
type GroupKey struct{ Value any }

// Page is one page of a cursor.
type Page[T any] struct {
	Items []Item[T]
	// Done is true when the cursor is exhausted and closed.
	Done bool
	// Statistics is set when the server reported statistics with this page.
	Statistics *Statistics
}

// fetched is the raw answer to QUERY_CREATE or CURSOR_READ.
type fetched struct {
	resp *response
	// cursor is the cursor BEGIN named, zero when the response did not get
	// that far; next is the cursor still open after the page, zero when the
	// query is exhausted.
	cursor, next CursorID
	stats        *Statistics
}

// fetch reads one page. After a failure it still returns what is known about
// the cursor, so that the caller can close it.
func (c *Client) fetch(ctx context.Context, spec session.Spec, obs *observer) (*fetched, error) {
	resp, err := c.exchange(ctx, spec, CapQueries, obs)
	out := &fetched{resp: resp}
	if resp != nil && resp.begin != nil {
		if id, ok, _ := resp.begin.U64(1); ok {
			out.cursor = CursorID(id)
		}
	}
	if err != nil {
		return out, err
	}
	if resp.begin == nil {
		return out, &Error{Code: CodeMalformed, Message: "the response did not begin"}
	}
	next, err := resp.end.RequireU64(1)
	if err != nil {
		return out, malformedResponse(err)
	}
	out.next = CursorID(next)
	if g, ok, err := resp.end.Group(2); err != nil {
		return out, malformedResponse(err)
	} else if ok {
		if out.stats, err = parseStatistics(g); err != nil {
			return out, malformedResponse(err)
		}
	}
	return out, nil
}

// closeCursor asks the server to close a cursor. It outlives the cancellation
// of ctx, because it is what cleans up after one.
func (c *Client) closeCursor(ctx context.Context, id CursorID) error {
	_, err := c.unary(context.WithoutCancel(ctx), closeSpec(id), CapQueries)
	return err
}

// decodeItem decodes one ITEM of a query response.
func decodeItem[T any](op *session.Op, f *wire.Fields) (Item[T], error) {
	var item Item[T]
	context, err := f.RequireU64(2)
	if err != nil {
		return item, malformedResponse(err)
	}
	body, err := f.RequireBytes(3)
	if err != nil {
		return item, malformedResponse(err)
	}
	var key string
	keyed := false
	if g, ok, err := f.Group(1); err != nil {
		return item, malformedResponse(err)
	} else if ok {
		kind, err := g.RequireU16(1)
		if err != nil {
			return item, malformedResponse(err)
		}
		bytes, err := g.RequireBytes(2)
		if err != nil {
			return item, malformedResponse(err)
		}
		switch kind {
		case keyKindDoc:
			key, keyed = string(bytes), true
			item.Key = Key(key)
		case keyKindGrouped:
			group, err := op.Decode(context, bytes)
			if err != nil {
				return item, malformedResponse(err)
			}
			item.Group = &GroupKey{Value: group}
		default:
			return item, &Error{Code: CodeMalformed, Message: "unknown item key kind"}
		}
	}
	item.Value, err = readDocument[T](op, context, body, key, keyed)
	return item, err
}

// decodePage decodes every item of a fetched page.
func decodePage[T any](pg *fetched) ([]Item[T], error) {
	items := make([]Item[T], 0, len(pg.resp.items))
	for _, f := range pg.resp.items {
		item, err := decodeItem[T](pg.resp.op, f)
		if err != nil {
			return items, err
		}
		items = append(items, item)
	}
	return items, nil
}
