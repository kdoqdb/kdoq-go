// Package kdoq is a client of the kdoq native protocol.
//
// A program connects with a DSN, opens a database and a collection typed with
// a Go struct, and reads and writes documents, runs queries as iterators,
// applies mutations and manages databases, collections and sessions. The
// value types and the conversion between Go values and documents live in the
// kdoq/value package, which has no network code and can be used on its own.
//
// # Connecting
//
// [Connect] takes a DSN of the form kdoq://[user[:pass]@]host[:port][/db] and
// returns after the server accepted the session. A user name selects
// SCRAM-SHA-256 and the client verifies the server's final signature; without
// a user the session is anonymous. [ConnectOptions] overrides any field of the
// DSN, so credentials can stay out of the string. Direct QUIC is the default;
// [ConnectOptions].Transport selects H2 or WebTransport without fallback.
// Transports and their TLS settings live in the kdoq/transport package. A
// certificate hash replaces chain trust but still checks hostname and dates.
//
// When neither the DSN nor [ConnectOptions] names a port, the client uses
// [DefaultPort] (27505).
//
// # Contexts, timeouts and errors
//
// Every operation takes a [context.Context]. A context without a deadline gets
// [ConnectOptions].OperationTimeout, and cancelling a context cancels the
// operation on the server and closes any cursor it opened. A timeout is
// [context.DeadlineExceeded].
//
// A failure is an [*Error] that matches a sentinel through [errors.Is], for
// example [ErrNotFound], and tells whether the operation was not applied or
// its outcome is unknown. [*ConfigError], [*ConnectionError],
// [*IncompleteError] and [*BulkError] keep their own types. Only overload and
// admission-timeout errors are retried, with backoff inside the deadline; a
// lost connection is never retried, because the library cannot know whether a
// write was applied. The client reconnects lazily on the next operation.
//
// # Collections and documents
//
// [Database.Collection] returns a [Collection] typed by T, which is
// [value.Doc], a struct, a pointer to a struct, or a type that implements
// [value.Marshaler] and [value.Unmarshaler]. Handles are cheap and never touch
// the server. [Collection.Get], [Collection.Put], [Collection.Insert] and
// [Collection.Delete] address a document by [Key]; a key holds any bytes.
//
// # Queries
//
// [Collection.Find] returns a [Query] whose [Query.All] is a range-over-func
// iterator. Pages are fetched transparently, and leaving the loop cancels the
// query and closes its cursor. [Collection.FindCursor] gives a [Cursor] that
// is advanced page by page, and [Client.AttachCursor] continues a cursor from
// another connection of the same identity. Anything that changes the shape of
// the result, such as a projection, a reduction or a count, goes through
// [Collection.Aggregate], which has its own result type.
//
// # Mutations
//
// [Collection.Update], [Collection.Upsert] and [Collection.Bulk] change
// documents on the server with ordered stages ([Set], [Unset], [Replace]) whose
// values can be expressions ([Expr]). A statement addresses one document
// ([ByKey]) or the documents matching a filter ([Where]).
//
// # Concurrency
//
// A [Client], [Database] and [Collection] are safe for concurrent use. A
// [Query] and a [Cursor] belong to one goroutine; progress and diagnostic
// callbacks run in that goroutine. The state callback of [ConnectOptions] runs
// in a goroutine of the library and must not block.
package kdoq
