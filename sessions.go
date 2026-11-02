package kdoq

import (
	"context"
	"time"

	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Operation codes of the server and session operations.
const (
	opServerHealth       = 0x0001
	opServerCapabilities = 0x0002
	opSessionList        = 0x0101
	opSessionGet         = 0x0102
	opSessionRevoke      = 0x0103
)

// Sessions administers native sessions. Below the Admin access level only the
// sessions of the caller's own identity are visible.
type Sessions struct{ client *Client }

// Sessions returns the session administration of the server.
func (c *Client) Sessions() *Sessions { return &Sessions{client: c} }

func sessionInfo(f *wire.Fields) (SessionInfo, error) {
	id, err := f.RequireU64(1)
	if err != nil {
		return SessionInfo{}, malformedResponse(err)
	}
	user, err := f.RequireString(2)
	if err != nil {
		return SessionInfo{}, malformedResponse(err)
	}
	peer, err := f.RequireString(3)
	if err != nil {
		return SessionInfo{}, malformedResponse(err)
	}
	created, err := f.RequireU64(4)
	if err != nil {
		return SessionInfo{}, malformedResponse(err)
	}
	return SessionInfo{ID: SessionID(id), Username: user, Peer: peer, CreatedAt: time.UnixMilli(int64(created))}, nil
}

func sessionPayload(id SessionID) []byte { return new(wire.Builder).U64(1, uint64(id)).Bytes() }

// List returns the sessions the caller may see, ordered by identifier.
func (s *Sessions) List(ctx context.Context) ([]SessionInfo, error) {
	resp, err := s.client.unary(ctx, session.Spec{Operation: opSessionList}, CapAdministration)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(resp.items))
	for _, item := range resp.items {
		info, err := sessionInfo(item)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// Get returns one session. A session outside the caller's scope is ErrNotFound.
func (s *Sessions) Get(ctx context.Context, id SessionID) (SessionInfo, error) {
	resp, err := s.client.unary(ctx, session.Spec{Operation: opSessionGet, Payload: sessionPayload(id)}, CapAdministration)
	if err != nil {
		return SessionInfo{}, err
	}
	item, err := resp.single("SESSION_GET")
	if err != nil {
		return SessionInfo{}, err
	}
	return sessionInfo(item)
}

// Revoke ends a session. Revoking another identity's session needs the Admin
// access level.
func (s *Sessions) Revoke(ctx context.Context, id SessionID) error {
	return s.client.command(ctx, opSessionRevoke, sessionPayload(id), CapAdministration, true)
}

// Server reports on the server itself.
type Server struct{ client *Client }

// Server returns the server administration.
func (c *Client) Server() *Server { return &Server{client: c} }

// ServerCapabilities describes what the server implements.
type ServerCapabilities struct {
	// Operations lists the supported operation codes.
	Operations []uint32
	// Encoding is the value encoding identity (4 is KQ BSON v4).
	Encoding uint32
	// Capabilities are the capability bits the server implements.
	Capabilities Capability
}

// Health reports whether the server is serving. It does not say that every
// collection is available.
func (s *Server) Health(ctx context.Context) (bool, error) {
	resp, err := s.client.unary(ctx, session.Spec{Operation: opServerHealth}, CapAdministration)
	if err != nil {
		return false, err
	}
	item, err := resp.single("SERVER_HEALTH")
	if err != nil {
		return false, err
	}
	ready, err := item.RequireU8(1)
	if err != nil {
		return false, malformedResponse(err)
	}
	return ready == 1, nil
}

// Capabilities lists the operations, value encoding and capability bits of the
// server.
func (s *Server) Capabilities(ctx context.Context) (ServerCapabilities, error) {
	resp, err := s.client.unary(ctx, session.Spec{Operation: opServerCapabilities}, CapAdministration)
	if err != nil {
		return ServerCapabilities{}, err
	}
	item, err := resp.single("SERVER_CAPABILITIES")
	if err != nil {
		return ServerCapabilities{}, err
	}
	ops, err := item.U16List(1)
	if err != nil {
		return ServerCapabilities{}, malformedResponse(err)
	}
	encoding, err := item.RequireU16(2)
	if err != nil {
		return ServerCapabilities{}, malformedResponse(err)
	}
	bits, err := item.RequireU64(3)
	if err != nil {
		return ServerCapabilities{}, malformedResponse(err)
	}
	out := ServerCapabilities{Encoding: uint32(encoding), Capabilities: Capability(bits)}
	for _, op := range ops {
		out.Operations = append(out.Operations, uint32(op))
	}
	return out, nil
}
