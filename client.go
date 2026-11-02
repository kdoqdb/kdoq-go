package kdoq

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kdoqdb/kdoq-go/internal/retry"
	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/transport"
)

const (
	defaultConnectTimeout   = 10 * time.Second
	defaultOperationTimeout = 30 * time.Second
	frameCeiling            = 64 << 20
)

// config is the resolved configuration of a client.
type config struct {
	host, username, password, database string
	port                               int
	tls                                *tls.Config
	capabilities                       Capabilities
	connectTimeout, operationTimeout   time.Duration
	retry                              retry.Policy
	noReconnect                        bool
	dialer                             transport.Dialer
	nonce                              string
}

// Client is a connection to a kdoq server. It is safe for concurrent use. The
// connection is established by Connect and re-established lazily, by the next
// operation, after it is lost.
type Client struct {
	cfg      config
	notifier *notifier

	mu            sync.Mutex
	state         State
	conn          *session.Conn
	access        AccessLevel
	sessionID     SessionID
	connecting    *attempt
	everConnected bool
	fatal         error
	closed        chan struct{}
	closing       bool
}

// attempt is one connection attempt shared by every operation waiting for it.
type attempt struct {
	done chan struct{}
	conn *session.Conn
	err  error
}

// Connect connects to the server named by dsn (kdoq://[user[:pass]@]host[:port][/db])
// and returns after the session is ready. Options override the DSN. A user
// selects SCRAM-SHA-256, whose server signature is verified before the
// session is trusted; without a user the connection is anonymous. A
// configuration mistake is a *ConfigError returned before any network traffic.
func Connect(ctx context.Context, dsn string, opts ...ConnectOption) (*Client, error) {
	cfg, onState, err := resolve(dsn, opts)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, notifier: newNotifier(onState), closed: make(chan struct{})}
	openCtx, cancel := context.WithTimeout(ctx, cfg.connectTimeout)
	defer cancel()
	conn, err := c.open(openCtx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	c.mu.Lock()
	c.install(conn)
	c.mu.Unlock()
	return c, nil
}

func resolve(dsn string, opts []ConnectOption) (config, func(State, error), error) {
	var parsed DSN
	if dsn != "" {
		var err error
		if parsed, err = ParseDSN(dsn); err != nil {
			return config{}, nil, err
		}
	}
	o := ConnectOptions{
		Host: parsed.Host, Port: parsed.Port, Username: parsed.Username,
		Password: parsed.Password, Database: parsed.Database,
	}
	for _, opt := range opts {
		if opt != nil {
			opt.applyConnect(&o)
		}
	}
	if o.Host == "" {
		return config{}, nil, &ConfigError{Message: "no host: pass a DSN or the Host option"}
	}
	if o.Password != "" && o.Username == "" {
		return config{}, nil, &ConfigError{Message: "a password needs a user"}
	}
	if o.Port == 0 {
		o.Port = DefaultPort
	}
	if o.Port < 1 || o.Port > 65535 {
		return config{}, nil, &ConfigError{Message: "invalid port " + strconv.Itoa(o.Port)}
	}
	if o.CertificateHash != nil && len(o.CertificateHash) != sha256.Size {
		return config{}, nil, &ConfigError{Message: "the certificate hash must be a SHA-256 digest of 32 bytes"}
	}
	if o.Retry != nil && o.Retry.Attempts < 0 {
		return config{}, nil, &ConfigError{Message: "retry attempts cannot be negative"}
	}
	cfg := config{
		host: o.Host, username: o.Username, password: o.Password, database: o.Database, port: o.Port,
		tls:          tlsConfig(o.TLSConfig, o.CertificateHash),
		capabilities: o.Capabilities.orDefault(),
		noReconnect:  o.NoReconnect,
		dialer:       o.dialer,
		nonce:        o.nonce,
	}
	cfg.connectTimeout = orDuration(o.ConnectTimeout, defaultConnectTimeout)
	cfg.operationTimeout = orDuration(o.OperationTimeout, defaultOperationTimeout)
	if o.Retry != nil {
		cfg.retry = retry.Policy{
			Attempts: o.Retry.Attempts, Initial: o.Retry.Backoff.Initial, Max: o.Retry.Backoff.Max,
			Factor: o.Retry.Backoff.Factor, NoJitter: o.Retry.Backoff.NoJitter,
		}
	}
	if cfg.dialer == nil {
		cfg.dialer = transport.WebTransport{}
	}
	return cfg, o.OnState, nil
}

func orDuration(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// tlsConfig returns the configuration the transport verifies the server with.
// A certificate hash pins the leaf certificate and replaces chain verification.
func tlsConfig(base *tls.Config, hash []byte) *tls.Config {
	var cfg *tls.Config
	if base != nil {
		cfg = base.Clone()
	} else {
		cfg = &tls.Config{}
	}
	if cfg.MinVersion < tls.VersionTLS13 {
		cfg.MinVersion = tls.VersionTLS13
	}
	if hash == nil {
		return cfg
	}
	pinned := append([]byte(nil), hash...)
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the server sent no certificate")
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(sum[:], pinned) != 1 {
			return errors.New("the server certificate does not match the pinned hash")
		}
		return nil
	}
	return cfg
}

// open dials the transport and runs the session setup.
func (c *Client) open(ctx context.Context) (*session.Conn, error) {
	cfg := c.cfg
	dial := transport.DialConfig{
		URL:    "https://" + net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)) + "/native",
		TLS:    cfg.tls,
		Header: http.Header{},
	}
	sess, err := cfg.dialer.Dial(ctx, dial)
	if err != nil {
		return nil, connectFailure("cannot connect to "+dial.URL, err)
	}
	conn, err := session.Open(ctx, sess, session.Settings{
		SCRAM: cfg.username != "", Username: cfg.username, Password: cfg.password,
		Required: uint64(cfg.capabilities.Required), Optional: uint64(cfg.capabilities.Optional),
		FrameCeiling: frameCeiling, Nonce: cfg.nonce,
	})
	if err != nil {
		return nil, setupFailure(err)
	}
	return conn, nil
}

// install records an established connection. c.mu must be held.
func (c *Client) install(conn *session.Conn) {
	info := conn.Info()
	c.conn = conn
	c.access = AccessLevel(info.AccessLevel)
	c.sessionID = SessionID(info.SessionID)
	c.everConnected = true
	c.state = StateConnected
	c.notifier.emit(StateConnected, nil)
	go c.watch(conn)
}

// watch notices the end of a connection.
func (c *Client) watch(conn *session.Conn) {
	<-conn.Done()
	c.lost(conn)
}

// lost handles the end of conn: revocation closes the client, any other loss
// leaves it disconnected until the next operation.
func (c *Client) lost(conn *session.Conn) {
	c.mu.Lock()
	if c.conn != conn || c.state == StateClosed {
		c.mu.Unlock()
		return
	}
	if conn.Revoked() {
		c.mu.Unlock()
		c.fail(&Error{Code: CodeRevoked, Message: "the session was revoked"})
		return
	}
	c.conn = nil
	c.state = StateDisconnected
	failure := conn.Failure()
	c.mu.Unlock()
	var cause error = &ConnectionError{Message: "the connection was lost", Err: failure}
	c.notifier.emit(StateDisconnected, cause)
}

// fail closes the client for good after a failure that retrying cannot cure.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.state == StateClosed {
		c.mu.Unlock()
		return
	}
	c.state = StateClosed
	c.fatal = err
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	c.notifier.emit(StateClosed, err)
	close(c.closed)
}

// ensure returns a live connection, reconnecting if needed. Concurrent callers
// share one attempt; the attempt does not depend on any caller's context, so a
// failure fails only the operations waiting for it.
func (c *Client) ensure(ctx context.Context) (*session.Conn, error) {
	c.mu.Lock()
	if c.state == StateClosed {
		err := c.closedError()
		c.mu.Unlock()
		return nil, err
	}
	if conn := c.conn; conn != nil {
		if conn.Alive() {
			c.mu.Unlock()
			return conn, nil
		}
		c.mu.Unlock()
		c.lost(conn)
		return c.ensure(ctx)
	}
	if c.everConnected && c.cfg.noReconnect {
		c.mu.Unlock()
		return nil, &ConnectionError{Message: "the connection was lost and reconnect is disabled"}
	}
	a := c.connecting
	if a == nil {
		a = &attempt{done: make(chan struct{})}
		c.connecting = a
		go c.reconnect(a)
	}
	c.mu.Unlock()
	select {
	case <-a.done:
		return a.conn, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) reconnect(a *attempt) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.connectTimeout)
	defer cancel()
	conn, err := c.open(ctx)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		err = &ConnectionError{Message: "connecting timed out", Err: err}
	}
	c.mu.Lock()
	switch {
	case err != nil:
	case c.state == StateClosed:
		conn.Close()
		conn, err = nil, c.closedError()
	default:
		c.install(conn)
	}
	c.connecting = nil
	c.mu.Unlock()
	if err != nil && isFatal(err) {
		c.fail(err)
	}
	a.conn, a.err = conn, err
	close(a.done)
}

func (c *Client) closedError() error {
	if c.fatal != nil {
		return c.fatal
	}
	return &ConnectionError{Message: "the client is closed"}
}

// State returns the connection state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Session returns the current session, its access level and true; false while
// disconnected.
func (c *Client) Session() (SessionInfo, AccessLevel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.conn.Alive() {
		return SessionInfo{}, 0, false
	}
	return SessionInfo{ID: c.sessionID, Username: c.cfg.username}, c.access, true
}

// Capabilities returns the capabilities the server selected for the current
// session, and zero while disconnected.
func (c *Client) Capabilities() Capability {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0
	}
	return Capability(c.conn.Info().Capabilities)
}

// Closed returns a channel that is closed when the client is closed, either by
// Close or by a failure that ends it for good (see Err).
func (c *Client) Closed() <-chan struct{} { return c.closed }

// Err returns the failure that closed the client, or nil while it is open and
// after a normal Close.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fatal
}

// Close logs out (LOGOUT, then the acknowledgment, bounded by ctx) and closes
// the transport. It is idempotent.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.state == StateClosed || c.closing {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	conn := c.conn
	c.conn = nil
	c.state = StateClosed
	c.mu.Unlock()
	var err error
	if conn != nil {
		err = conn.Logout(ctx)
	}
	c.notifier.emit(StateClosed, nil)
	close(c.closed)
	return err
}

// DB returns a handle on a database without touching the network. An empty
// name selects the database of the DSN or options.
func (c *Client) DB(name string) (*Database, error) {
	if name == "" {
		name = c.cfg.database
	}
	if name == "" {
		return nil, &ConfigError{Message: "no database name: pass one or set a default in the DSN or options"}
	}
	return &Database{client: c, name: name}, nil
}
