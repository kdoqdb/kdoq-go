// Package transport contains the in-memory transport used by protocol tests.
package transport

import public "github.com/kdoqdb/kdoq-go/transport"

type Stream = public.Stream
type StreamError = public.StreamError
type SessionError = public.SessionError
type Session = public.Connection
type Dialer = public.Transport

// Reset sends a native reset in the in-memory model.
func Reset(s Stream, code uint64) { s.(*memoryStream).CancelWrite(code) }
