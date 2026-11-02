# kdoq-go

> **Not ready for production.** This library is under active development. Its public API
> is not stable and may change without backward compatibility.

[![CI](https://github.com/kdoqdb/kdoq-go/actions/workflows/ci.yml/badge.svg)](https://github.com/kdoqdb/kdoq-go/actions/workflows/ci.yml)

A Go client for [kdoq](https://github.com/kdoqdb/kdoq), a document database. It gives you typed
collections, queries you range over, server-side cursors, online mutations and database
administration, all over one authenticated session.

```go
accounts := db.Collection[Account]("accounts")

accounts.Put(ctx, "u1", Account{Name: "Ann", Age: 31})

for item, err := range accounts.Find(ctx, "age >= 30").All() {
	if err != nil {
		return err
	}
	fmt.Println(item.Key, item.Value.Name)
}
```

- **Typed documents.** Collections are typed with a Go struct, or with `value.Doc` when the shape
  is not known in advance. Documents are read straight into your type.
- **Queries as iterators.** `Find` returns a query you `range` over. Pages are fetched as you go,
  and leaving the loop cancels the query and closes its cursor on the server.
- **Mutations on the server.** Change documents with ordered stages instead of reading,
  modifying and writing back.
- **Safe by default.** The server's identity is verified, a lost connection is never retried
  silently, and every operation has a deadline.

## Installation

```sh
go get github.com/kdoqdb/kdoq-go
```

The module needs Go 1.27 or newer.

## Quick start

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/kdoqdb/kdoq-go"
)

type Account struct {
	ID   kdoq.Key `kdoq:",key"` // the document key; it is not stored in the body
	Name string   `kdoq:"name"`
	Age  int64    `kdoq:"age"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := kdoq.Connect(ctx, "kdoq://ann@db.example.com/shop", kdoq.ConnectOptions{
		Password: os.Getenv("KDOQ_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close(context.Background())

	db, err := client.DB("shop")
	if err != nil {
		log.Fatal(err)
	}
	accounts := db.Collection[Account]("accounts")

	if _, err := accounts.Put(ctx, "u1", Account{Name: "Ann", Age: 31}); err != nil {
		log.Fatal(err)
	}

	ann, err := accounts.Get(ctx, "u1")
	switch {
	case errors.Is(err, kdoq.ErrNotFound):
		fmt.Println("no such account")
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Println(ann.ID, ann.Name, ann.Age) // u1 Ann 31
	}
}
```

## Connecting

`kdoq.Connect` takes a connection string and returns once the server has accepted the session.

```
kdoq://[user[:password]@]host[:port][/database]
```

| Part | Meaning |
| --- | --- |
| `user` | Selects SCRAM-SHA-256 authentication. Without a user the session is anonymous. |
| `password` | The user's password. Prefer `ConnectOptions.Password` so it stays out of the string. |
| `port` | The server's port. When omitted the client uses `kdoq.DefaultPort` (27505). |
| `database` | The default database for `client.DB("")`. |

`ConnectOptions` overrides any part of the connection string. Options you leave at their zero
value keep their defaults.

```go
client, err := kdoq.Connect(ctx, "kdoq://ann@db.example.com/shop", kdoq.ConnectOptions{
	Password:         os.Getenv("KDOQ_PASSWORD"),
	ConnectTimeout:   5 * time.Second,  // transport setup and authentication; 10s by default
	OperationTimeout: 20 * time.Second, // operations whose context has no deadline; 30s by default
})
```

A mistake in the configuration, such as a password without a user, fails at once and never
touches the network. With SCRAM the client also checks the server's final signature before it
trusts the session.

### Transports and TLS

The default transport is **direct QUIC**, using ALPN `kdoq-native`. Select H2 or
WebTransport explicitly through `ConnectOptions.Transport`; there is no fallback.
The DSN host and port must name the matching server listener:

| Constructor | Server endpoint | Binding |
| --- | --- | --- |
| `transport.NewQUIC` | `native.listen.quic` | Native QUIC/TLS over UDP |
| `transport.NewH2` | `native.listen.h2` | Native HTTP/2 over TLS/TCP |
| `transport.NewWebTransport` | `http.listen.quic` | WebTransport over HTTP/3 |

```go
// Import "github.com/kdoqdb/kdoq-go/transport".
tr, err := transport.NewH2(transport.H2Config{
    TLS: transport.TLSConfig{
        CertificateHash: certificateHash, // 32-byte SHA-256 of the DER leaf certificate
    },
})
if err != nil {
    return err
}
client, err := kdoq.Connect(ctx, dsn, kdoq.ConnectOptions{Transport: tr})
```

Constructors validate configuration without network I/O. A transport is safe to
reuse across clients; each client owns a separate connection and authenticated
session. H2 keeps control and every operation on that exact physical connection.
HELLO, SCRAM, native messages and retry policy are shared across all bindings.

All transports use TLS 1.3 and verify system roots by default. Set
`TLSConfig.Config` to a `*tls.Config` with private `RootCAs` or an explicit
`ServerName`. The configuration is cloned. With `TLSConfig.CertificateHash`, the
exact leaf hash replaces chain trust, while hostname and validity-date checks
remain mandatory. An explicit `ServerName` is used for hostname verification;
otherwise it is the DSN host, including IP-address verification. Any configured
`VerifyConnection` runs as an additional check. Hash bytes are copied by the
constructor. `InsecureSkipVerify` without an explicit hash is rejected.

`QUICConfig.Config` and `WebTransportConfig.Config` accept a cloned `*quic.Config`
for network settings. Each transport enforces its ALPN and protocol requirements.
Custom transports implement the public `transport.Transport`, `Connection` and
`Stream` interfaces. Streams provide concurrent reads/writes, `CloseWrite`,
`Abort` and `Close`; session framing and authentication remain the client's job.
`StreamError` and `SessionError` identify the code domain and preserve the original
cause. In particular, H2 reset code 8 is cancellation, never native revocation.

Migration from the previous API: move `ConnectOptions.TLSConfig` and
`CertificateHash` into the selected transport's `TLSConfig`; explicitly construct
`NewWebTransport` to retain the previous binding. The default port remains 27505.

### Connection lifecycle

The client reconnects on the next operation after a connection is lost, and concurrent
operations share one attempt. A failed authentication, or a session revoked by an administrator,
closes the client for good.

```go
client, err := kdoq.Connect(ctx, dsn, kdoq.ConnectOptions{
	OnState: func(s kdoq.State, err error) { // runs in a library goroutine; do not block
		log.Printf("kdoq: %s %v", s, err)
	},
})
...
<-client.Closed() // closes when the client is closed for good
log.Println(client.Err())
```

Set `ConnectOptions.NoReconnect` to keep a lost connection lost. Reconnect always
uses the selected transport and authenticates a fresh session. `Close(ctx)` is
terminal: it cancels an outstanding reconnect, ends active operations and bounds
LOGOUT/ACK by the supplied context. It does not wait for operations to finish
successfully; uncertain writes remain uncertain. Operation cancellation aborts
local I/O promptly. Native CANCEL uses bounded control dispatch; if its queue is
exhausted by stalled control output, the session is closed rather than retaining
unbounded cancellation callbacks.

## Documents

A collection is typed with a Go struct (or a pointer to one), or with `value.Doc` for generic
documents. Opening a database or a collection never touches the network, so handles are cheap.

```go
db, _ := client.DB("shop")
accounts := db.Collection[Account]("accounts")

_, err := accounts.Put(ctx, "u1", Account{Name: "Ann", Age: 31}) // create or replace
ann, err := accounts.Get(ctx, "u1")
key, err := accounts.Insert(ctx, Account{Name: "Bob"})           // the server generates the key
_, err = accounts.Delete(ctx, key)

docs := db.Docs("events") // shorthand for Collection[value.Doc]
```

### Keys

A `kdoq.Key` holds any bytes; a string constant converts implicitly. `Key.String` prints text
when the bytes are valid UTF-8 and hexadecimal otherwise. The field tagged `kdoq:",key"` carries
the key: `Get` and queries fill it in, `Insert` uses it when `WriteOptions.Key` is not set, and it
is never stored in the document body. It must be a string type. `Put`, `Get` and `Delete` take
the key as an argument.

### Struct fields

| Rule | |
| --- | --- |
| Name | The `kdoq` tag, then the `json` tag, then the Go field name. |
| `kdoq:"-"` | Skips the field. |
| `omitempty`, `omitzero` | Leave the field out when it is empty or the zero value, as in `encoding/json`. |
| Embedded structs | Their fields are promoted into the document. |
| Unknown fields | Fields of a stored document that the struct lacks are ignored when reading. |
| Numbers | Widen to any integer or floating-point type that holds them exactly. A conversion that would overflow or lose a fraction is an error, never a silent truncation. |

A type of your own can choose its document form by implementing `value.Marshaler` and
`value.Unmarshaler`.

### Durability

`Put`, `Insert` and `Delete` accept write options. A write is durable when acknowledged unless
you trade that for speed:

```go
_, err := accounts.Put(ctx, "u1", acct, kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed})
```

`WriteOptions.Key` sets the key `Insert` uses instead of letting the server generate one.

## Values

Document values map to exact Go types: `int32`, `int64`, `uint32`, `uint64`, `float64`, `string`,
`bool`, `time.Time` for date-times, and the types of the
`github.com/kdoqdb/kdoq-go/value` package for the rest:

| Type | Holds |
| --- | --- |
| `value.Decimal128` | A 128-bit decimal. |
| `value.Timestamp` | A server timestamp. |
| `value.CalendarDate` | A date without a time of day. |
| `value.ObjectID` | A 12-byte identifier. |
| `value.Binary`, `value.Code`, `value.RegExp` | Binary data with a subtype, code, a regular expression. |
| `value.Doc`, `value.D` | A document as a map, or as an ordered list of fields. |
| `value.Array` | An array. |

The `value` package has no network code, so you can parse, convert and print values without a
connection. `value.Format` prints any value as readable text.

## Queries

`Find` takes a filter and returns a query you range over:

```go
for item, err := range accounts.Find(ctx, "age >= 30", kdoq.FindOptions{PageSize: 100}).All() {
	if err != nil {
		return err
	}
	fmt.Println(item.Key, item.Value.Name)
	if enough() {
		break // cancels the query and closes its cursor
	}
}
```

| Method | Returns |
| --- | --- |
| `Query.All()` | Every result as an `Item` with `Key` and `Value`. |
| `Query.Values()` | Only the values. |
| `Query.Collect()` | A slice of all results. |
| `Collection.Count(ctx, filter)` | The number of matching documents, from metadata without a scan. |

`FindOptions` sets the `PageSize` (1000 by default, at most 10000), windows the result with
`Skip` and `Limit`, caps the resources of the query with `Budget`, and asks for statistics,
progress or diagnostics through `Observe`, `OnProgress` and `OnDiagnostic`. A page that is cut off
surfaces as a `*kdoq.IncompleteError`, never as a complete page.

### Aggregation

A pipeline that changes the shape of the result goes through `Aggregate`, which has its own
result type:

```go
byCountry := []kdoq.PipelineStage{kdoq.Reduce("country", "0", "$prev + 1")}
for item, err := range accounts.Aggregate[int64](ctx, "", byCountry).All() {
	if err != nil {
		return err
	}
	fmt.Println(item.Group.Value, item.Value) // the grouping key and the count
}
```

The stages are `Project`, `Match`, `Reduce` and `Count`.

### Cursors

A cursor is advanced by hand, for example behind a "load more" button. `FindCursor` and
`AggregateCursor` return one, and `client.AttachCursor[T](id)` continues a cursor from another
connection of the same identity, so a stateless service can page across requests.

```go
cursor, err := accounts.FindCursor(ctx, "age >= 30", kdoq.FindOptions{PageSize: 50})
if err != nil {
	return err
}
defer cursor.Close(ctx)

for {
	page, err := cursor.Next(ctx)
	if err != nil {
		return err
	}
	for _, item := range page.Items {
		fmt.Println(item.Key)
	}
	if page.Done {
		break
	}
}
```

## Mutations

Change documents on the server with ordered stages instead of reading, modifying and writing
back. A statement addresses one document with `ByKey`, or the documents matching a filter with
`Where`.

```go
res, err := accounts.Update(ctx, kdoq.ByKey("u1"), []kdoq.Stage{
	kdoq.Set(value.D{{Key: "age", Value: kdoq.Expr("age + 1")}}),
	kdoq.Unset("email"),
})

// Every matching account, not only the first.
_, err = accounts.Update(ctx, kdoq.Where("age < 18").Multi(), []kdoq.Stage{
	kdoq.Set(value.Doc{"minor": true}),
})

created, err := accounts.Upsert(ctx, kdoq.ByKey("u2"), stages) // created.Inserted
```

The stages are `Set`, `SetPath`, `Unset` and `Replace`. `Expr` makes a value an expression of the
query language instead of a literal. `MutationResult` reports `Matched`, `Modified`, `Inserted`
and the document `Key`.

`Bulk` applies many statements in one call, ordered or unordered, with a result for each. A
failure after some statements were accepted is a `*kdoq.BulkError` that carries the results
received so far.

## Errors

A failure reported by the server is a `*kdoq.Error`. It matches a sentinel with `errors.Is`
(`ErrNotFound`, `ErrAlreadyExists`, `ErrForbidden`, `ErrOverloaded`, `ErrConflict`, ...) and tells
whether the operation was **not applied** or its **outcome is unknown**. A timeout is
`context.DeadlineExceeded`. `*ConfigError`, `*ConnectionError`, `*IncompleteError` and
`*BulkError` have their own types.

```go
_, err := accounts.Insert(ctx, acct, kdoq.WriteOptions{Key: "u1"})
var kerr *kdoq.Error
switch {
case errors.Is(err, kdoq.ErrAlreadyExists):
	// pick another key
case errors.As(err, &kerr) && kerr.Outcome == kdoq.OutcomeUnknown:
	// the write may or may not have happened; check before retrying
}
```

### Deadlines and retries

- Every operation takes a `context.Context`. Without a deadline, `ConnectOptions.OperationTimeout`
  applies. Cancelling the context cancels the operation on the server.
- Overload and admission-timeout errors are retried with backoff inside the deadline. Tune this
  with `ConnectOptions.Retry`.
- A lost connection is **never** retried. The library cannot know whether a write was applied, so
  that decision stays with you.

## Administration

| Call | Purpose |
| --- | --- |
| `client.Databases()` | List, create, load and unload databases. |
| `db.Collections()` | List collections. |
| `collection.Create`, `Load`, `Unload`, `Info` | Manage one collection. |
| `collection.WALSync`, `collection.Checkpoint` | Control persistence. |
| `client.Sessions()` | List sessions and revoke them. |
| `client.Server()` | Report health and capabilities. |

An operation that the identity's access level does not allow fails with `ErrForbidden`. A server
that lacks a capability you require fails the connection; choose what is required and what is
merely wanted with `ConnectOptions.Capabilities`.

## Concurrency and testing

The client, databases and collections are safe for concurrent use. A query and a cursor belong to
one goroutine, and their progress and diagnostic callbacks run in it.

`Database.Collection`, `Collection.Aggregate`, `Collection.AggregateCursor` and
`Client.AttachCursor` are generic methods, so they cannot be part of an interface. The other
methods of `*Collection[T]` can, so you can hide a collection behind your own interface and mock
it in unit tests:

```go
type AccountStore interface {
	Get(ctx context.Context, key kdoq.Key) (Account, error)
	Put(ctx context.Context, key kdoq.Key, doc Account, opts ...kdoq.WriteOption) (kdoq.Key, error)
}
```

## Documentation

- Package documentation: `go doc github.com/kdoqdb/kdoq-go`, or on
  [pkg.go.dev](https://pkg.go.dev/github.com/kdoqdb/kdoq-go).
- Runnable examples: [example_test.go](example_test.go).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
