package fakeserver

import (
	"fmt"
	"sort"

	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Resource states of databases and collections.
const (
	StateLoaded      = 1
	StateUnavailable = 2
	StateUnloaded    = 3
)

type collectionEntry struct {
	state                  uint16
	diagnostic             string
	lockReleased, excluded bool
}

type databaseEntry struct {
	state       uint16
	diagnostic  string
	collections map[string]*collectionEntry
}

// SetDatabaseState sets the state and diagnostic reported for a database,
// creating it when it does not exist.
func (s *Server) SetDatabaseState(name string, state uint16, diagnostic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.database(name)
	d.state, d.diagnostic = state, diagnostic
}

// SetCollectionFlags sets the lock-released and excluded flags of a
// collection, creating it when it does not exist.
func (s *Server) SetCollectionFlags(db, name string, lockReleased, excluded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.collection(db, name)
	c.lockReleased, c.excluded = lockReleased, excluded
}

// Persistence returns the WAL_SYNC and CHECKPOINT calls received, as
// "wal_sync db.collection" and "checkpoint db.collection".
func (s *Server) Persistence() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.persisted...)
}

// database and collection create entries on demand; s.mu must be held.
func (s *Server) database(name string) *databaseEntry {
	d := s.catalog[name]
	if d == nil {
		d = &databaseEntry{state: StateLoaded, collections: map[string]*collectionEntry{}}
		s.catalog[name] = d
	}
	return d
}

func (s *Server) collection(db, name string) *collectionEntry {
	d := s.database(db)
	c := d.collections[name]
	if c == nil {
		c = &collectionEntry{state: StateLoaded}
		d.collections[name] = c
	}
	return c
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Server) health(call *Call) {
	call.Begin(nil)
	call.Item(new(wire.Builder).U8(1, 1))
	call.End(nil)
}

func (s *Server) serverCapabilities(call *Call) {
	ops := []uint16{0x0001, 0x0002, 0x0101, 0x0102, 0x0103}
	for _, r := range [][2]uint16{{0x0201, 0x0205}, {0x0301, 0x0305}, {0x0401, 0x0407}, {0x0501, 0x0504}, {0x0601, 0x0602}} {
		for op := r[0]; op <= r[1]; op++ {
			ops = append(ops, op)
		}
	}
	call.Begin(nil)
	call.Item(new(wire.Builder).U16List(1, ops...).U16(2, 4).U64(3, s.cfg.Capabilities))
	call.End(nil)
}

func (s *Server) namespaces(call *Call) {
	p := call.Payload
	isDatabase := call.Operation >= 0x0201 && call.Operation <= 0x0205
	db, hasDB, _ := p.String(1)
	if !hasDB && call.Operation != 0x0201 {
		call.Fail(1, false)
		return
	}
	coll, _, _ := p.String(2)
	op := call.Operation & 0x00ff
	s.mu.Lock()
	defer s.mu.Unlock()
	switch op {
	case 1: // list
		s.nsList(call, db, isDatabase)
	case 2: // get
		s.nsGet(call, db, coll, isDatabase)
	case 3: // create
		s.nsCreate(call, db, coll, isDatabase)
	case 4, 5: // load, unload
		s.nsSetState(call, db, coll, isDatabase, op == 4)
	}
}

func databaseItem(name string, d *databaseEntry) *wire.Builder {
	b := new(wire.Builder).String(1, name).U16(2, d.state)
	if d.diagnostic != "" {
		b.String(3, d.diagnostic)
	}
	return b
}

func collectionItem(name string, c *collectionEntry) *wire.Builder {
	b := new(wire.Builder).String(1, name).U16(2, c.state)
	if c.diagnostic != "" {
		b.String(3, c.diagnostic)
	}
	return b.Bool(4, c.lockReleased).Bool(5, c.excluded)
}

func (s *Server) nsList(call *Call, db string, isDatabase bool) {
	if isDatabase {
		call.Begin(nil)
		for _, name := range sortedNames(s.catalog) {
			call.Item(databaseItem(name, s.catalog[name]))
		}
		call.End(nil)
		return
	}
	d := s.catalog[db]
	if d == nil {
		call.Fail(6, false)
		return
	}
	call.Begin(nil)
	for _, name := range sortedNames(d.collections) {
		call.Item(collectionItem(name, d.collections[name]))
	}
	call.End(nil)
}

func (s *Server) nsGet(call *Call, db, coll string, isDatabase bool) {
	d := s.catalog[db]
	if d == nil {
		call.Fail(6, false)
		return
	}
	call.Begin(nil)
	if isDatabase {
		call.Item(databaseItem(db, d))
	} else if c := d.collections[coll]; c != nil {
		call.Item(collectionItem(coll, c))
	} else {
		call.Fail(6, false)
		return
	}
	call.End(nil)
}

func (s *Server) nsCreate(call *Call, db, coll string, isDatabase bool) {
	switch {
	case isDatabase && s.catalog[db] != nil:
		call.Fail(11, false)
	case isDatabase:
		s.database(db)
		reply(call)
	case s.catalog[db] == nil:
		call.Fail(6, false)
	case s.catalog[db].collections[coll] != nil:
		call.Fail(11, false)
	default:
		s.collection(db, coll)
		reply(call)
	}
}

func (s *Server) nsSetState(call *Call, db, coll string, isDatabase, load bool) {
	state := uint16(StateUnloaded)
	if load {
		state = StateLoaded
	}
	d := s.catalog[db]
	switch {
	case d == nil:
		call.Fail(6, false)
	case isDatabase:
		d.state = state
		reply(call)
	case d.collections[coll] == nil:
		call.Fail(6, false)
	default:
		d.collections[coll].state = state
		reply(call)
	}
}

func reply(call *Call) {
	call.Begin(nil)
	call.End(nil)
}

func (s *Server) persistence(call *Call) {
	db, err1 := call.Payload.RequireString(1)
	coll, err2 := call.Payload.RequireString(2)
	if err1 != nil || err2 != nil {
		call.Fail(1, false)
		return
	}
	name := "wal_sync"
	if call.Operation == 0x0602 {
		name = "checkpoint"
	}
	s.mu.Lock()
	s.persisted = append(s.persisted, fmt.Sprintf("%s %s.%s", name, db, coll))
	s.mu.Unlock()
	reply(call)
}

// visible returns the connections whose sessions the caller may see: its own
// identity, or every one at Admin level. s.mu must not be held.
func (s *Server) visible(caller *Conn) []*Conn {
	var out []*Conn
	for _, c := range s.Conns() {
		select {
		case <-c.Done():
			continue
		default:
		}
		if c.SessionID == 0 {
			continue
		}
		if caller.level >= 3 || c.Username == caller.Username {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

func sessionItem(c *Conn) *wire.Builder {
	return new(wire.Builder).U64(1, c.SessionID).String(2, c.Username).String(3, c.peer()).U64(4, uint64(c.created.UnixMilli()))
}

func (c *Conn) peer() string { return fmt.Sprintf("127.0.0.1:%d", 40000+c.SessionID) }

func (s *Server) sessions(call *Call) {
	caller := call.Conn
	visible := s.visible(caller)
	if call.Operation == 0x0101 {
		call.Begin(nil)
		for _, c := range visible {
			call.Item(sessionItem(c))
		}
		call.End(nil)
		return
	}
	id, err := call.Payload.RequireU64(1)
	if err != nil {
		call.Fail(1, false)
		return
	}
	var target *Conn
	for _, c := range visible {
		if c.SessionID == id {
			target = c
		}
	}
	switch {
	case target == nil:
		call.Fail(6, false)
	case call.Operation == 0x0102:
		call.Begin(nil)
		call.Item(sessionItem(target))
		call.End(nil)
	default:
		reply(call)
		target.Revoke()
	}
}
