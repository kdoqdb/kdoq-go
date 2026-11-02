package kdoq

import (
	"time"

	"github.com/kdoqdb/kdoq-go/transport"
)

// Capability is a set of protocol capability bits.
type Capability uint64

// The capability bits negotiated in HELLO.
const (
	// CapAdministration covers server and session administration.
	CapAdministration Capability = 1
	// CapDocuments covers databases, collections, documents and persistence.
	CapDocuments Capability = 4
	// CapQueries covers queries and cursors.
	CapQueries Capability = 8
	// CapMutations covers online mutations.
	CapMutations Capability = 16
)

// Has reports whether every bit of c2 is set in c.
func (c Capability) Has(c2 Capability) bool { return c&c2 == c2 }

// Capabilities states which capabilities a connection requires and which it
// merely wants. The zero value selects the defaults: documents and queries are
// required, administration and mutations optional. A server that lacks a
// required capability fails the connection.
type Capabilities struct {
	Required, Optional Capability
}

func (c Capabilities) orDefault() Capabilities {
	if c == (Capabilities{}) {
		return Capabilities{Required: CapDocuments | CapQueries, Optional: CapAdministration | CapMutations}
	}
	return c
}

// ConnectOption configures Connect. Only the library implements it; the
// ConnectOptions struct is an option itself.
type ConnectOption interface{ applyConnect(*ConnectOptions) }

// ConnectOptions configure a connection. Non-zero fields override the DSN and
// earlier options; zero values mean the defaults.
type ConnectOptions struct {
	Host, Username, Password, Database string
	Port                               int
	// Transport selects the connection binding. Nil defaults to direct QUIC.
	Transport transport.Transport
	// Capabilities selects the required and optional capabilities.
	Capabilities Capabilities
	// ConnectTimeout bounds transport setup, HELLO and AUTH (10s).
	ConnectTimeout time.Duration
	// OperationTimeout applies to operations whose context has no deadline (30s).
	OperationTimeout time.Duration
	// Retry sets the retry policy; nil retries up to three attempts.
	Retry *RetryPolicy
	// NoReconnect keeps a lost connection lost.
	NoReconnect bool
	// OnState is called from a library goroutine on every state change and
	// must not block.
	OnState func(State, error)

	nonce string
}

func (o ConnectOptions) applyConnect(t *ConnectOptions) {
	setString(&t.Host, o.Host)
	setString(&t.Username, o.Username)
	setString(&t.Password, o.Password)
	setString(&t.Database, o.Database)
	if o.Port != 0 {
		t.Port = o.Port
	}
	if o.Transport != nil {
		t.Transport = o.Transport
	}
	if o.Capabilities != (Capabilities{}) {
		t.Capabilities = o.Capabilities
	}
	if o.ConnectTimeout != 0 {
		t.ConnectTimeout = o.ConnectTimeout
	}
	if o.OperationTimeout != 0 {
		t.OperationTimeout = o.OperationTimeout
	}
	if o.Retry != nil {
		t.Retry = o.Retry
	}
	if o.NoReconnect {
		t.NoReconnect = true
	}
	if o.OnState != nil {
		t.OnState = o.OnState
	}
	setString(&t.nonce, o.nonce)
}

func setString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// RetryPolicy controls the retry of overload and admission-timeout errors.
type RetryPolicy struct {
	// Attempts is the total number of attempts (3); 1 disables retries.
	Attempts int
	Backoff  Backoff
}

// Backoff spaces retries out: Initial, multiplied by Factor each time, capped
// at Max, with jitter unless NoJitter is set (100ms, 5s, 2).
type Backoff struct {
	Initial, Max time.Duration
	Factor       float64
	NoJitter     bool
}

// State is the connection state of a client.
type State int

const (
	// StateConnected means a session is established.
	StateConnected State = iota
	// StateDisconnected means the connection was lost; the next operation
	// reconnects.
	StateDisconnected
	// StateClosed means the client is closed for good.
	StateClosed
)

// String returns the name of the state.
func (s State) String() string {
	switch s {
	case StateConnected:
		return "connected"
	case StateDisconnected:
		return "disconnected"
	}
	return "closed"
}

// AccessLevel is the level of the authenticated identity.
type AccessLevel uint8

// The access levels, in increasing order.
const (
	AccessConnect AccessLevel = iota
	AccessRead
	AccessWrite
	AccessAdmin
)

// SessionID identifies a native session.
type SessionID uint64

// SessionInfo describes a session. The client knows only the identifier and
// the user name of its own session; the other fields come from the server's
// session administration.
type SessionInfo struct {
	ID        SessionID
	Username  string
	Peer      string
	CreatedAt time.Time
}

// Durability selects how a write is acknowledged.
type Durability uint8

const (
	// DurabilityDefault leaves the choice to the server (durable).
	DurabilityDefault Durability = iota
	// DurabilityDurable acknowledges after the write is durable.
	DurabilityDurable
	// DurabilityRelaxed acknowledges before the write is durable.
	DurabilityRelaxed
)

// WriteOption configures Put, Insert and Delete. Only the library implements
// it; the WriteOptions struct is an option itself.
type WriteOption interface{ applyWrite(*WriteOptions) }

// WriteOptions configure a write. Zero values mean the defaults.
type WriteOptions struct {
	Durability Durability
	// Key is the key Insert uses; it takes precedence over the `kdoq:",key"`
	// field. An empty Key means none. Put and Delete ignore it.
	Key Key
}

func (o WriteOptions) applyWrite(t *WriteOptions) {
	if o.Durability != DurabilityDefault {
		t.Durability = o.Durability
	}
	if o.Key != "" {
		t.Key = o.Key
	}
}

func writeOptions(opts []WriteOption) WriteOptions {
	var o WriteOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyWrite(&o)
		}
	}
	return o
}
