package fakeserver

import (
	"sort"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Pipeline stage kinds, as the protocol numbers them.
const (
	StageProject = 1
	StageReduce  = 2
	StageCount   = 3
	StageMatch   = 4
)

// Stage is one pipeline stage of a received query.
type Stage struct {
	Kind int
	// Expr is the expression of a project or match stage; Reduce holds the
	// key, initial and accumulate expressions of a reduction.
	Expr   string
	Reduce [3]string
}

// QueryRequest is a parsed QUERY_CREATE, QUERY_COUNT or CURSOR_READ.
type QueryRequest struct {
	Operation          uint16
	Database           string
	Collection         string
	Filter             string
	HasFilter          bool
	Pipeline           []Stage
	CursorID           uint64
	PageSize           *uint32
	IdleTTLms          *uint64
	Skip, Limit        uint64
	Progress           bool
	Diagnostics        bool
	Statistics         bool
	Budget             map[int]uint64
	HasObserve         bool
	HasBudget          bool
	HasWindow, HasPage bool
}

// Result is one result item a query produces. Key is the document key of a
// plain item; HasGroup selects a grouping key instead; neither means the item
// has no key.
type Result struct {
	Key      []byte
	HasGroup bool
	Group    any
	Value    any
}

// DiagnosticFrame is a DIAGNOSTIC the server sends when diagnostics are on.
type DiagnosticFrame struct {
	Expression string
	Stage      *uint64
	Start, End uint64
	Reason     string
}

// QueryResult is what the Query hook returns for a request.
type QueryResult struct {
	Items       []Result
	Diagnostics []DiagnosticFrame
}

type cursor struct {
	owner string
	items []Result
	pos   int
}

// ParseQuery parses the payload of a query operation.
func ParseQuery(call *Call) (*QueryRequest, error) {
	p := call.Payload
	req := &QueryRequest{Operation: call.Operation, Budget: map[int]uint64{}}
	var options *wire.Fields
	var err error
	switch call.Operation {
	case 0x0501, 0x0502:
		if req.Database, err = p.RequireString(1); err != nil {
			return nil, err
		}
		if req.Collection, err = p.RequireString(2); err != nil {
			return nil, err
		}
		req.Filter, req.HasFilter, _ = p.String(3)
		optionsID := 4
		if call.Operation == 0x0501 {
			optionsID = 5
			if err := parsePipeline(p, req); err != nil {
				return nil, err
			}
		}
		options, _, err = p.Group(optionsID)
	case 0x0503:
		if req.CursorID, err = p.RequireU64(1); err != nil {
			return nil, err
		}
		options, _, err = p.Group(2)
	}
	if err != nil {
		return nil, err
	}
	if options == nil {
		return req, nil
	}
	return req, parseOptions(options, req)
}

func parsePipeline(p *wire.Fields, req *QueryRequest) error {
	// The pipeline group is ordered: Fields keeps values per id, so the
	// order across ids is recovered from the raw bytes.
	raw, ok := p.Bytes(4)
	if !ok {
		return nil
	}
	for pos := 0; pos < len(raw); {
		id := int(raw[pos] & 0x3f)
		length, n, err := wire.ReadU32V(raw[pos+1:])
		if err != nil {
			return err
		}
		body := raw[pos+1+n : pos+1+n+int(length)]
		pos += 1 + n + int(length)
		stage := Stage{Kind: id}
		switch id {
		case StageProject, StageMatch:
			stage.Expr = string(body)
		case StageReduce:
			f, err := wire.Parse(body)
			if err != nil {
				return err
			}
			for i := range stage.Reduce {
				stage.Reduce[i], _, _ = f.String(i + 1)
			}
		}
		req.Pipeline = append(req.Pipeline, stage)
	}
	return nil
}

func parseOptions(o *wire.Fields, req *QueryRequest) error {
	if page, ok, err := o.Group(1); err != nil {
		return err
	} else if ok {
		req.HasPage = true
		if v, ok, _ := page.U32(1); ok {
			req.PageSize = &v
		}
		if v, ok, _ := page.U64(2); ok {
			req.IdleTTLms = &v
		}
	}
	if w, ok, err := o.Group(2); err != nil {
		return err
	} else if ok {
		req.HasWindow = true
		req.Skip, _, _ = w.U64(1)
		req.Limit, _, _ = w.U64(2)
	}
	if ob, ok, err := o.Group(3); err != nil {
		return err
	} else if ok {
		req.HasObserve = true
		for id, dst := range map[int]*bool{1: &req.Progress, 2: &req.Diagnostics, 3: &req.Statistics} {
			v, _, _ := ob.U8(id)
			*dst = v == 1
		}
	}
	if b, ok, err := o.Group(4); err != nil {
		return err
	} else if ok {
		req.HasBudget = true
		for id := 1; id <= 6; id++ {
			if v, ok, _ := b.U64(id); ok {
				req.Budget[id] = v
			}
		}
	}
	return nil
}

// QueryRequests returns every query operation received so far.
func (s *Server) QueryRequests() []QueryRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]QueryRequest(nil), s.requests...)
}

// OpenCursors returns the number of cursors that are still open.
func (s *Server) OpenCursors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cursors)
}

// ClosedCursors returns the identifier of every CURSOR_CLOSE received, in order.
func (s *Server) ClosedCursors() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.closedIDs...)
}

// CursorIDs returns the identifiers of the open cursors in ascending order.
func (s *Server) CursorIDs() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint64, 0, len(s.cursors))
	for id := range s.cursors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// source returns the results a request selects: the Query hook's, or every
// stored document of the collection ordered by key.
func (s *Server) source(req *QueryRequest) QueryResult {
	if s.Query != nil {
		return s.Query(req)
	}
	return s.DefaultQuery(req)
}

// DefaultQuery selects every stored document of the collection ordered by
// key, whatever the filter says. A Query hook may wrap it.
func (s *Server) DefaultQuery(req *QueryRequest) QueryResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := docID(req.Database, req.Collection, nil)
	var keys []string
	for id := range s.docs {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			keys = append(keys, id[len(prefix):])
		}
	}
	sort.Strings(keys)
	var out QueryResult
	for _, k := range keys {
		raw := s.docs[prefix+k]
		v, err := document.Decode(raw.Names, raw.Bytes)
		if err != nil {
			continue
		}
		out.Items = append(out.Items, Result{Key: []byte(k), Value: v})
	}
	return out
}

func (s *Server) queries(call *Call) {
	req, err := ParseQuery(call)
	if err != nil {
		call.Fail(1, false)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, *req)
	s.mu.Unlock()
	switch call.Operation {
	case 0x0501:
		s.queryCreate(call, req)
	case 0x0502:
		s.queryCount(call, req)
	case 0x0503:
		s.cursorRead(call, req)
	case 0x0504:
		s.cursorClose(call)
	}
}

func (s *Server) queryCount(call *Call, req *QueryRequest) {
	n := uint64(len(s.source(req).Items))
	call.Begin(nil)
	s.observe(call, req, QueryResult{}, n)
	call.Item(new(wire.Builder).U64(1, n))
	call.End(statisticsEnd(req, false)(0))
}

func window(items []Result, skip, limit uint64) []Result {
	if skip >= uint64(len(items)) {
		return nil
	}
	items = items[skip:]
	if limit > 0 && limit < uint64(len(items)) {
		items = items[:limit]
	}
	return items
}

func (s *Server) queryCreate(call *Call, req *QueryRequest) {
	result := s.source(req)
	items := window(result.Items, req.Skip, req.Limit)
	s.mu.Lock()
	s.nextCursor++
	id := s.nextCursor
	s.cursors[id] = &cursor{owner: call.Conn.Username, items: items}
	s.mu.Unlock()
	call.Begin(new(wire.Builder).U64(1, id).U64(2, id))
	s.observe(call, req, result, uint64(len(items)))
	s.sendPage(call, id, req.PageSize, statisticsEnd(req, true))
}

func (s *Server) cursorRead(call *Call, req *QueryRequest) {
	s.mu.Lock()
	c := s.cursors[req.CursorID]
	s.mu.Unlock()
	if c == nil || c.owner != call.Conn.Username {
		call.Fail(6, false)
		return
	}
	call.Begin(new(wire.Builder).U64(1, req.CursorID).U64(2, req.CursorID))
	s.sendPage(call, req.CursorID, req.PageSize, nil)
}

func (s *Server) cursorClose(call *Call) {
	id, err := call.Payload.RequireU64(1)
	if err != nil {
		call.Fail(1, false)
		return
	}
	s.mu.Lock()
	s.closedIDs = append(s.closedIDs, id)
	if c := s.cursors[id]; c != nil && c.owner == call.Conn.Username {
		delete(s.cursors, id)
	}
	s.mu.Unlock()
	reply(call)
}

// observe sends the observation frames the request asked for.
func (s *Server) observe(call *Call, req *QueryRequest, result QueryResult, total uint64) {
	if req.Diagnostics {
		for _, d := range result.Diagnostics {
			b := new(wire.Builder).String(1, d.Expression)
			if d.Stage != nil {
				b.U64(2, *d.Stage)
			}
			b.Group(3, new(wire.Builder).U64(1, d.Start).U64(2, d.End)).String(4, d.Reason)
			call.frame(wire.KindDiagnostic, b)
		}
	}
	if req.Progress {
		call.frame(wire.KindProgress, new(wire.Builder).U64(1, total).U64(2, total))
	}
}

// sendPage writes up to size items of a cursor (all of them when size is
// nil) and the END that names the cursor if it stays open.
func (s *Server) sendPage(call *Call, id uint64, size *uint32, end func(open uint64) *wire.Builder) {
	s.mu.Lock()
	c := s.cursors[id]
	n := len(c.items) - c.pos
	if size != nil && int(*size) < n {
		n = int(*size)
	}
	page := c.items[c.pos : c.pos+n]
	c.pos += n
	open := uint64(0)
	if c.pos < len(c.items) || (size != nil && *size == 0) {
		open = id
	} else {
		delete(s.cursors, id)
	}
	s.mu.Unlock()
	call.WriteResults(id, page)
	if end == nil {
		call.End(new(wire.Builder).U64(1, open))
		return
	}
	call.End(end(open))
}

func statisticsEnd(req *QueryRequest, withCursor bool) func(uint64) *wire.Builder {
	return func(open uint64) *wire.Builder {
		b := new(wire.Builder)
		if withCursor {
			b.U64(1, open)
		}
		if !req.Statistics {
			return b
		}
		id := 2
		if !withCursor {
			id = 1
		}
		stats := new(wire.Builder).
			Group(1, new(wire.Builder).U64(1, 11).U64(2, 12).U64(3, 13)).
			Group(2, new(wire.Builder).U64(1, 21).U64(2, 22).U64(3, 23).U64(4, 24)).
			Group(3, new(wire.Builder).U64(1, 31).U64(2, 32).U64(3, 33)).
			Group(4, new(wire.Builder).U64(1, 41).U64(2, 42))
		for _, st := range req.Pipeline {
			if st.Kind == StageReduce {
				stats.Group(5, new(wire.Builder).U64(1, 51).U64(2, 52))
			}
		}
		return b.Group(id, stats)
	}
}

// WriteResults writes the DICTIONARY and ITEM frames of results in an output
// context. Tests that answer a query by hand use it too.
func (c *Call) WriteResults(context uint64, results []Result) {
	dict := document.NewDictionaryIn(context)
	for _, r := range results {
		body, err := document.Encode(dict, r.Value)
		if err != nil {
			continue
		}
		item := new(wire.Builder)
		switch {
		case r.HasGroup:
			group, err := document.Encode(dict, r.Group)
			if err != nil {
				continue
			}
			item.Group(1, new(wire.Builder).U16(1, 2).Binary(2, group))
		case r.Key != nil:
			item.Group(1, new(wire.Builder).U16(1, 1).Binary(2, r.Key))
		}
		item.U64(2, context).Binary(3, body)
		for _, f := range dict.Frames() {
			_, _ = c.stream.Write(f)
		}
		c.Item(item)
	}
}

// SendProgress writes a PROGRESS frame.
func (c *Call) SendProgress(processed, total uint64) {
	c.frame(wire.KindProgress, new(wire.Builder).U64(1, processed).U64(2, total))
}
