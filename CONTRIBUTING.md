# Contributing to kdoq-go

This document is for people who change the library. Users read [README.md](README.md).

## Public surface

The library exposes only what a user needs. Two packages are public:

- `kdoq` (the module root): the client, databases, collections, queries, cursors, mutations,
  administration and errors;
- `kdoq/value`: value types, struct conversion and `Format`. It has no network code and does not
  import `kdoq`.

Everything else lives under `internal/` or in test files. **A new exported identifier is a
deliberate, reviewed decision**, not a side effect of an implementation:

- `testdata/api.txt` lists every exported identifier of both public packages with its signature.
  `TestPublicSurface` fails on any difference, and `TestNoUndeclaredPublicPackage` fails when a
  package outside `internal/` is not declared public.
- To change the surface, change the code, then run
  `go test -run TestPublicSurface -update-api` and review the diff of `testdata/api.txt` together
  with the code. Describe the new identifier in the change.
- Every exported identifier has a documentation comment that starts with its name.
- Options are variadic values of a sealed interface that only this module can implement. The
  options structs implement it, so adding a functional `WithX` option later does not change a
  signature.
- A value that crosses the public boundary is a plain type of `kdoq` or `kdoq/value`. Frames,
  SCRAM, sessions, transport, the binary document encoding and retries stay internal and know no
  public type.

## Layout

```text
doc.go README.md CONTRIBUTING.md       package documentation and the two documents
example_test.go                        runnable examples (Connect, documents, queries, mutations)
client.go run.go collection.go ...     the client; one file per responsibility
value/                                 public values and struct conversion
internal/wire/                         frames and the field grammar
internal/scram/                        SCRAM-SHA-256
internal/document/                     KQ BSON v4 documents and name dictionaries
internal/transport/                    the transport seam: WebTransport dialer, in-memory pair
internal/session/                      setup, control stream, operation streams
internal/retry/                        backoff
internal/fakeserver/                   the in-process native server used by tests
testdata/api.txt                       the surface listing
e2e/                                   end-to-end suite against a real kdoq (build tag e2e)
```

The module is self-contained: no workspace file at the repository root and nothing outside the
module is imported, so it can move to its own repository. Dependencies are `quic-go` and
`webtransport-go`; everything else is the standard library.

`webtransport-go` is pinned to v0.9.0 with `quic-go` v0.53.0 and `qpack` v0.5.1. Later releases
refuse a server that does not offer QUIC stream reset partial delivery, and the server's
WebTransport stack does not offer it yet, so the end-to-end suite could not connect. Move the pin
only together with a server that negotiates the extension. With the pinned version the close the
server sends on revocation arrives as a QUIC application error rather than a WebTransport session
close; `internal/transport` maps both to `SessionError`.

The wire contract is the server's protocol reference. If the library finds a gap in it, fix the
contract first.

## Tests

- Tests exercise behavior through the public surface: what a caller sees after a call, an error,
  a cancellation or a reconnect. Public and end-to-end tests are in `package kdoq_test`.
  Transport substitution is reachable only from `export_test.go`.
- The main seam is the public client against the in-process fake server. The value package has
  its own black-box tests, and `internal/wire/reference_test.go` holds the reference bytes copied
  from the server's protocol tests.
- Tests do not depend on wall-clock time. Deadlines, backoff and idle intervals are injectable;
  tests synchronize on channels and events, not on sleeps. A wait that cannot be avoided is
  called out in the test with its reason.
- The fake server is a model of the protocol, so it can drift from the real one. The end-to-end
  suite runs the same kinds of scenarios against a real server; run it when you touch the wire
  layer, the transport or the behavior of an operation, and fix the fake when it disagrees.

### End-to-end suite

`e2e/` starts a real `kdoq` for each test with a generated self-signed certificate and its own
configuration in a temporary directory, passes the port and the certificate hash to the client
explicitly, and stops the process when the test ends. It prints the server's log when a test
fails and leaves no process or file behind. It needs a server binary: set `KDOQ_BIN`, otherwise
the suite builds it with `cargo build -p kdoq --bin kdoq` in the enclosing repository.

```sh
KDOQ_BIN=/path/to/kdoq go test -tags e2e ./e2e/...
```

## Checks

Run from `libs/kdoq-go`:

```sh
gofmt -l .
go vet ./...
go test ./...
go test -tags e2e ./e2e/...
```

`gofmt -l` must print nothing. The default `go test ./...` needs no server and no network; the
last command needs a server binary. No linters beyond `go vet` are used, and there is no
`go.work` at the repository root.
