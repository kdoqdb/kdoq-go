# kdoq-go

A Go client for the native protocol of kdoq, a document database. It gives Go code typed collections, streaming queries that cancel when you leave the
loop, server-side cursors, online mutations and database administration, over one authenticated
session.

```sh
go get github.com/kdoqdb/kdoq-go
```

The module needs Go 1.27 or newer. The package documentation (`go doc github.com/kdoqdb/kdoq-go`)
and the runnable examples in `example_test.go` are the reference; this page is the tour.

## Connect

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

client, err := kdoq.Connect(ctx, "kdoq://ann@db.example.com/shop", kdoq.ConnectOptions{
	Password: os.Getenv("KDOQ_PASSWORD"), // options override the DSN, so the password stays out of it
})
if err != nil {
	log.Fatal(err)
}
defer client.Close(context.Background())
```

The DSN is `kdoq://[user[:pass]@]host[:port][/database]`. A user name selects SCRAM-SHA-256, and
the client checks the server's final signature before it trusts the session; without a user the
session is anonymous. A mistake in the configuration, such as a password without a user, fails
at once without touching the network.

A server with a self-signed certificate is trusted by pinning the SHA-256 hash of its DER
certificate (`ConnectOptions.CertificateHash`), which is safer than switching verification off.
Pass your own `*tls.Config` (`ConnectOptions.TLSConfig`) to use other certificate authorities or
a different server name.

### Port

The client connects to port **27505** when the DSN names none (`kdoq.DefaultPort`). The server's
current default is 8443, so either put the port in the DSN or set the server's native listener
to the client's default in the server's configuration file:

```yaml
http:
  listen:
    quic: 0.0.0.0:27505   # the native WebTransport listener
```

The native listener is the `quic` entry of `http.listen`; it needs the server's TLS certificate
and key to be configured.

## Documents

A collection is typed with a Go struct, or with `value.Doc` for generic documents. Opening a
database or a collection never touches the network.

```go
type Account struct {
	ID    kdoq.Key `kdoq:",key"`       // the document key
	Name  string   `kdoq:"name"`
	Age   int64    `json:"age"`        // the json tag is used when there is no kdoq tag
	Email string   `kdoq:"email,omitempty"`
}

db, _ := client.DB("shop")
accounts := db.Collection[Account]("accounts")

_, err := accounts.Put(ctx, "u1", Account{Name: "Ann", Age: 31})
ann, err := accounts.Get(ctx, "u1")
if errors.Is(err, kdoq.ErrNotFound) {
	// no such document
}
key, err := accounts.Insert(ctx, Account{Name: "Bob"}) // the server generates the key
_, err = accounts.Delete(ctx, key)
```

`Put`, `Insert` and `Delete` take a `kdoq.WriteOptions{Durability: kdoq.DurabilityRelaxed}` when
you want to trade durability for speed. A `Key` holds any bytes; a string constant converts
implicitly. `Key.String` prints text when the bytes are valid UTF-8 and hexadecimal otherwise.

Field names come from the `kdoq` tag, then the `json` tag, then the field name; `-` skips a field
and `omitempty` works as in `encoding/json`. Unknown fields in a document are ignored when
reading into a struct, and a numeric conversion that would overflow is an error, never a silent
truncation.

## Values

Document values map to exact Go types: `int32`, `int64`, `uint32`, `uint64`, `float64`,
`time.Time` for date-times, and the types of the `kdoq/value` package for the rest: `Decimal128`,
`Timestamp`, `CalendarDate` (a date without a time of day), `ObjectID`, `Binary`, `Code`, ordered
documents (`value.D`) and arrays. The `value` package has no network code, so you can parse,
convert and print values without a connection; `value.Format` prints any value as readable text.
A type of your own can choose its document form with `value.Marshaler` and `value.Unmarshaler`.

## Queries

`Find` returns a query you range over. Pages are fetched as you go, and leaving the loop cancels
the query and closes its server cursor.

```go
for item, err := range accounts.Find(ctx, "age >= 30", kdoq.FindOptions{PageSize: 100}).All() {
	if err != nil {
		return err
	}
	fmt.Println(item.Key, item.Value.Name)
	if enough() {
		break // cancels the query
	}
}
```

`Values` skips the keys, `Collect` returns a slice, `FindOptions` windows the result (`Skip`,
`Limit`), sets a resource `Budget`, and asks for statistics, progress or diagnostics. A page that
is cut off surfaces as `*kdoq.IncompleteError`, never as a complete page.

A pipeline that changes the shape of the result goes through `Aggregate`, which has its own result
type:

```go
byCountry := []kdoq.PipelineStage{kdoq.Reduce("country", "0", "$prev + 1")}
for item, err := range accounts.Aggregate[int64](ctx, "", byCountry).All() {
	fmt.Println(item.Group.Value, item.Value) // the grouping key and the count
}
total, err := accounts.Count(ctx, "age >= 30") // from metadata, no scan
```

A **cursor** is advanced by hand, for a "load more" button. `FindCursor` and `AggregateCursor`
return one, and `client.AttachCursor[T](id)` continues a cursor from another connection of the
same identity, so a stateless service can page across requests.

## Mutations

Change documents on the server with ordered stages instead of reading, modifying and writing
back:

```go
res, err := accounts.Update(ctx, kdoq.ByKey("u1"), []kdoq.Stage{
	kdoq.Set(value.D{{Key: "age", Value: kdoq.Expr("age + 1")}}),
	kdoq.Unset("email"),
})
_, err = accounts.Update(ctx, kdoq.Where("age < 18").Multi(), []kdoq.Stage{kdoq.Set(value.Doc{"minor": true})})
created, err := accounts.Upsert(ctx, kdoq.ByKey("u2"), stages) // created.Inserted
```

`Bulk` applies many statements in one call, ordered or unordered, with a result per statement. A
failure after some statements were accepted is a `*kdoq.BulkError` that carries the results
received so far.

## Errors

Every failure from the server is a `*kdoq.Error` that matches a sentinel with `errors.Is`
(`ErrNotFound`, `ErrAlreadyExists`, `ErrForbidden`, `ErrOverloaded`, ...) and reports whether the
operation was not applied or its outcome is unknown. A timeout is `context.DeadlineExceeded`.
`*ConfigError`, `*ConnectionError`, `*IncompleteError` and `*BulkError` have their own types.

- Every operation takes a `context.Context`. Without a deadline, `ConnectOptions.OperationTimeout`
  (30 seconds) applies. Cancelling the context cancels the operation on the server.
- Overload and admission-timeout errors are retried with backoff inside the deadline
  (`ConnectOptions.Retry`). A lost connection is **never** retried: the library cannot know whether
  a write was applied, so that decision stays with you.
- After a lost connection the client reconnects on the next operation; concurrent operations share
  one attempt. An authentication or revocation failure closes the client for good: watch
  `client.Closed()` and read `client.Err()`, or pass `ConnectOptions.OnState`.

## Administration

`client.Databases()`, `db.Collections()`, `client.Sessions()` and `client.Server()` list, create,
load and unload databases and collections, list and revoke sessions, and report health and
capabilities. `collection.WALSync` and `collection.Checkpoint` control persistence. An operation
the identity's access level does not allow fails with `ErrForbidden`.

## Concurrency and testing

The client, databases and collections are safe for concurrent use. A query and a cursor belong to
one goroutine, and their progress and diagnostic callbacks run in it.

`Database.Collection`, `Collection.Aggregate`, `Collection.AggregateCursor` and
`Client.AttachCursor` are generic methods and cannot be part of an interface; the other methods
of `*Collection[T]` can, so you can hide a collection behind your own interface and mock it in
unit tests.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
