// Package fakeserver is an in-process native server for tests. It speaks the
// protocol over the in-memory transport, keeps documents as raw v4 bytes and
// lets a test intercept, delay, fail or drop any operation, so that tests
// synchronize on events instead of waiting.
package fakeserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kdoqdb/kdoq-go/internal/document"
	"github.com/kdoqdb/kdoq-go/internal/scram"
	"github.com/kdoqdb/kdoq-go/internal/transport"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Config selects what the server offers.
type Config struct {
	// Users maps user names to passwords. When empty the server is anonymous
	// and offers NONE; otherwise it requires SCRAM-SHA-256.
	Users map[string]string
	// Level is the access level of every identity (default Admin, 3).
	Level *uint8
	// Capabilities are the capability bits the server implements (default 1|4|8|16).
	Capabilities uint64
	// ForgeSignature makes the server-final message carry a wrong signature.
	ForgeSignature bool
	// FrameCeiling is the server's receive ceiling (default 64 MiB).
	FrameCeiling uint64
}

// Server is one fake server; every Dial creates a new connection.
type Server struct {
	cfg Config

	// DialHook runs before a connection is created; a non-nil error fails the
	// dial. It may block.
	DialHook func(cfg transport.DialConfig) error
	// Intercept sees every operation after the command and its input were read.
	// Returning true means the hook answered; false selects the default handler.
	Intercept func(*Call) bool
	// Query, when set, computes the results of every QUERY_CREATE and
	// QUERY_COUNT; by default a query selects every stored document of the
	// collection, ordered by key, whatever its filter says.
	Query func(*QueryRequest) QueryResult
	// Connected receives every connection once READY has been sent.
	Connected chan *Conn

	mu    sync.Mutex
	users map[string]string
	dials []transport.DialConfig
	conns []*Conn
	docs  map[string]RawDoc
	seq   int

	catalog    map[string]*databaseEntry
	persisted  []string
	requests   []QueryRequest
	cursors    map[uint64]*cursor
	nextCursor uint64
	closedIDs  []uint64
}

// New returns a server.
func New(cfg Config) *Server {
	if cfg.Capabilities == 0 {
		cfg.Capabilities = 1 | 4 | 8 | 16
	}
	if cfg.FrameCeiling == 0 {
		cfg.FrameCeiling = 64 << 20
	}
	users := make(map[string]string, len(cfg.Users))
	for u, p := range cfg.Users {
		users[u] = p
	}
	return &Server{cfg: cfg, users: users, Connected: make(chan *Conn, 64), docs: map[string]RawDoc{}, catalog: map[string]*databaseEntry{}, cursors: map[uint64]*cursor{}}
}

// SetPassword changes (or adds) a user's password for later connections.
func (s *Server) SetPassword(user, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user] = password
}

func (s *Server) password(user string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.users[user]
	return p, ok
}

// Dials returns the configuration of every dial so far.
func (s *Server) Dials() []transport.DialConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transport.DialConfig(nil), s.dials...)
}

// Conns returns every connection accepted so far.
func (s *Server) Conns() []*Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Conn(nil), s.conns...)
}

// Dial implements transport.Dialer.
func (s *Server) Dial(ctx context.Context, cfg transport.DialConfig) (transport.Session, error) {
	s.mu.Lock()
	s.dials = append(s.dials, cfg)
	hook := s.DialHook
	s.mu.Unlock()
	if hook != nil {
		if err := hook(cfg); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := transport.Pipe()
	c := &Conn{srv: s, sess: server, calls: map[uint64]*Call{}, Cancels: make(chan uint64, 64)}
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	go c.serve()
	return client, nil
}

// Conn is one server-side connection.
type Conn struct {
	srv  *Server
	sess *transport.Memory

	// Cancels receives the target request ID of every CANCEL.
	Cancels chan uint64
	// Username is the authenticated user ("" for anonymous).
	Username string
	// SessionID is the session identifier sent in READY.
	SessionID uint64
	created   time.Time

	writeMu sync.Mutex
	control transport.Stream
	mu      sync.Mutex
	calls   map[uint64]*Call
	caps    uint64
	level   uint8
	ceiling uint64
}

func (c *Conn) send(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.control.Write(frame)
	return err
}

// Close ends the connection with a close code.
func (c *Conn) Close(code uint32) { _ = c.sess.Close(code) }

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.sess.Done() }

// Lose drops the connection as if the network failed.
func (c *Conn) Lose() { c.sess.Abort(errors.New("network lost")) }

// Revoke ends the session as a revocation does: an ERROR on the control
// stream and close code 8.
func (c *Conn) Revoke() {
	_ = c.send(wire.EncodeError(wire.ErrorFrame{Code: 8}))
	c.Close(8)
}

// fail sends an ERROR and ends the control stream; the client then closes the
// connection, as it does after any failed setup.
func (c *Conn) fail(code uint16) {
	_ = c.send(wire.EncodeError(wire.ErrorFrame{Code: code}))
	_ = c.control.CloseWrite()
}

func (c *Conn) serve() {
	ctx := context.Background()
	stream, err := c.sess.Accept(ctx)
	if err != nil {
		return
	}
	c.control = stream
	if !c.setup() {
		return
	}
	c.srv.Connected <- c
	go c.acceptOperations()
	c.controlLoop()
}

func (c *Conn) setup() bool {
	read := func(kind uint16) (*wire.Fields, bool) {
		frame, err := wire.ReadFrame(c.control, wire.ControlFrameLimit+8)
		if err != nil || frame.Kind != kind {
			c.fail(1)
			return nil, false
		}
		f, err := wire.Parse(frame.Payload)
		if err != nil {
			c.fail(1)
			return nil, false
		}
		return f, true
	}
	hello, ok := read(wire.KindHello)
	if !ok {
		return false
	}
	required, _ := hello.RequireU64(4)
	optional, _ := hello.RequireU64(5)
	clientCeiling, _ := hello.RequireU64(7)
	methods, _ := hello.U16List(6)
	s := c.srv
	if required&^s.cfg.Capabilities != 0 {
		c.fail(2)
		return false
	}
	wantMethod := uint16(session1)
	if len(s.cfg.Users) == 0 {
		wantMethod = 0
	}
	offered := false
	for _, m := range methods {
		offered = offered || m == wantMethod
	}
	if !offered {
		c.fail(2)
		return false
	}
	c.caps = (required | optional) & s.cfg.Capabilities
	c.ceiling = min(clientCeiling, s.cfg.FrameCeiling)
	c.level = 3
	if s.cfg.Level != nil {
		c.level = *s.cfg.Level
	}
	var reply wire.Builder
	reply.U32(1, 1).U16(2, 4).U64(3, c.caps).U16(4, wantMethod).U64(5, s.cfg.FrameCeiling)
	if err := c.send(wire.Encode(wire.KindHello, reply.Bytes())); err != nil {
		return false
	}
	if wantMethod == session1 && !c.authenticate(read) {
		return false
	}
	s.mu.Lock()
	s.seq++
	c.SessionID = uint64(s.seq)
	c.created = time.Now()
	s.mu.Unlock()
	var ready wire.Builder
	ready.U64(1, c.SessionID).U8(2, c.level)
	return c.send(wire.Encode(wire.KindReady, ready.Bytes())) == nil
}

const session1 = 1

func (c *Conn) authenticate(read func(uint16) (*wire.Fields, bool)) bool {
	first, ok := read(wire.KindAuth)
	if !ok {
		return false
	}
	message, _ := first.RequireString(1)
	user, err := scram.Username(message)
	password, known := c.srv.password(user)
	if err != nil || !known {
		c.fail(3)
		return false
	}
	exchange, err := scram.NewServer(password, nil, 4096, "")
	if err != nil {
		c.fail(3)
		return false
	}
	serverFirst, err := exchange.First(message)
	if err != nil {
		c.fail(3)
		return false
	}
	var b wire.Builder
	b.String(1, serverFirst)
	if c.send(wire.Encode(wire.KindAuth, b.Bytes())) != nil {
		return false
	}
	second, ok := read(wire.KindAuth)
	if !ok {
		return false
	}
	final, _ := second.RequireString(1)
	serverFinal, err := exchange.Final(final)
	if err != nil {
		c.fail(3)
		return false
	}
	if c.srv.cfg.ForgeSignature {
		serverFinal = scram.ForgedFinal()
	}
	c.Username = user
	var out wire.Builder
	out.String(1, serverFinal)
	return c.send(wire.Encode(wire.KindAuth, out.Bytes())) == nil
}

func (c *Conn) controlLoop() {
	for {
		frame, err := wire.ReadFrame(c.control, wire.ControlFrameLimit+8)
		if err != nil {
			return
		}
		f, err := wire.Parse(frame.Payload)
		if err != nil {
			c.fail(1)
			return
		}
		controlID, _ := f.RequireU64(1)
		switch frame.Kind {
		case wire.KindCancel:
			target, _ := f.RequireU64(2)
			c.mu.Lock()
			call := c.calls[target]
			c.mu.Unlock()
			if call != nil {
				call.markCancelled()
			}
			select {
			case c.Cancels <- target:
			default:
			}
			var ack wire.Builder
			ack.U64(1, controlID).U16(2, wire.KindCancel)
			if call != nil {
				ack.U8(3, 1)
			} else {
				ack.U8(3, 0)
			}
			_ = c.send(wire.Encode(wire.KindControlAck, ack.Bytes()))
		case wire.KindLogout:
			var ack wire.Builder
			ack.U64(1, controlID).U16(2, wire.KindLogout)
			_ = c.send(wire.Encode(wire.KindControlAck, ack.Bytes()))
			c.Close(0)
			return
		default:
			c.fail(1)
			return
		}
	}
}

func (c *Conn) acceptOperations() {
	for {
		stream, err := c.sess.Accept(context.Background())
		if err != nil {
			return
		}
		go c.operation(stream)
	}
}

// RawDoc is a stored document: raw v4 bytes and the names of its dictionary.
type RawDoc struct {
	Names document.Names
	Bytes []byte
}
