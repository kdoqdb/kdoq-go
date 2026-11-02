package fakeserver

import (
	"fmt"
	"sort"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

func docID(db, coll string, key []byte) string {
	return fmt.Sprintf("%s\x00%s\x00%s", db, coll, key)
}

// Put stores raw document bytes under a key, as if written earlier.
func (s *Server) Put(db, coll string, key []byte, names document.Names, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[docID(db, coll, key)] = RawDoc{Names: names, Bytes: body}
}

// Doc returns the raw document stored under a key.
func (s *Server) Doc(db, coll string, key []byte) (RawDoc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[docID(db, coll, key)]
	return d, ok
}

func (s *Server) documents(call *Call) {
	p := call.Payload
	db, err1 := p.RequireString(1)
	coll, err2 := p.RequireString(2)
	if err1 != nil || err2 != nil {
		call.Fail(1, false)
		return
	}
	key, hasKey := p.Bytes(3)
	switch call.Operation {
	case 0x0401:
		s.get(call, db, coll, key)
	case 0x0402, 0x0403:
		s.put(call, db, coll, key, hasKey)
	case 0x0404:
		s.mu.Lock()
		delete(s.docs, docID(db, coll, key))
		s.mu.Unlock()
		replyKey(call, key)
	}
}

func replyKey(call *Call, key []byte) {
	call.Begin(nil)
	call.Item(new(wire.Builder).Binary(1, key))
	call.End(nil)
}

func (s *Server) get(call *Call, db, coll string, key []byte) {
	doc, ok := s.Doc(db, coll, key)
	if !ok {
		call.Fail(6, false)
		return
	}
	call.Begin(new(wire.Builder).U64(1, document.Context))
	var ids []uint32
	for id := range doc.Names {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	dict := new(wire.Builder).U64(1, document.Context)
	for _, id := range ids {
		dict.Group(2, new(wire.Builder).U32(1, id).String(2, doc.Names[id]))
	}
	_, _ = call.stream.Write(wire.Encode(wire.KindDictionary, dict.Bytes()))
	call.Item(new(wire.Builder).Binary(1, key).U64(2, document.Context).Binary(3, doc.Bytes))
	call.End(nil)
}

func (s *Server) put(call *Call, db, coll string, key []byte, hasKey bool) {
	_, body, err := call.Document()
	if err != nil {
		call.Fail(9, false)
		return
	}
	names := document.Names{}
	for _, n := range call.contexts {
		for id, name := range n {
			names[id] = name
		}
	}
	if call.Operation == 0x0403 {
		if !hasKey {
			s.mu.Lock()
			s.seq++
			key = []byte(fmt.Sprintf("generated-%d", s.seq))
			s.mu.Unlock()
		}
		if _, exists := s.Doc(db, coll, key); exists {
			call.Fail(11, false)
			return
		}
	}
	s.Put(db, coll, key, names, body)
	replyKey(call, key)
}
