# Contributing to kdoq-go

This document is for people who change the library. Users read [README.md](README.md).

## Getting started

You need Go 1.27 or newer. Before you send a change, run from the repository root:

```sh
gofmt -l .
go vet ./...
go test ./...
```

`gofmt -l` must print nothing. `go test ./...` needs no server and no network. No linters beyond
`go vet` are used.

The CI workflow ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the same three checks,
with `-race` on the tests, on every push to `main` and every pull request.

The end-to-end suite needs a built `kdoq` server and is run separately; see
[End-to-end suite](#end-to-end-suite).

## Public surface

The library exposes only what a user needs. Two packages are public:

- `github.com/kdoqdb/kdoq-go` (the module root, imported as `kdoq`): the client, databases,
  collections, queries, cursors, mutations, administration and errors;
- `github.com/kdoqdb/kdoq-go/value`: value types, struct conversion and `Format`. It has no
  network code and does not import `kdoq`.

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
doc.go README.md CONTRIBUTING.md LICENSE   package documentation and the project documents
example_test.go                            runnable examples (Connect, documents, queries, mutations)
client.go run.go collection.go ...         the client; one file per responsibility
document_read.go                           reads a result document into a typed value
value/                                     public values and struct conversion
internal/wire/                             frames and the field grammar
internal/scram/                            SCRAM-SHA-256
internal/document/                         KQ BSON v4 documents, name dictionaries, the Stream reader
internal/source/                           the seam between internal/document and value
internal/transport/                        the transport seam: WebTransport dialer, in-memory pair
internal/session/                          setup, control stream, operation streams
internal/retry/                            backoff
internal/fakeserver/                       the in-process native server used by tests
testdata/api.txt                           the surface listing
e2e/                                       end-to-end suite against a real kdoq (build tag e2e)
```

The module is self-contained. Its dependencies are `quic-go` and `webtransport-go`; everything
else is the standard library.

`webtransport-go` is pinned to v0.9.0 with `quic-go` v0.53.0 and `qpack` v0.5.1. Later releases
refuse a server that does not offer QUIC stream reset partial delivery, and the server's
WebTransport stack does not offer it yet, so the end-to-end suite could not connect. Move the pin
only together with a server that negotiates the extension. With the pinned version the close the
server sends on revocation arrives as a QUIC application error rather than a WebTransport session
close; `internal/transport` maps both to `SessionError`.

The wire contract is the protocol reference of the
[kdoq server](https://github.com/kdoqdb/kdoq). If the library finds a gap in it, fix the contract
first.

## How a result document is read

A document arrives as KQ BSON v4 bytes. There are two ways to turn it into a Go value, and they
must agree:

- **The tree.** `document.Decode` builds the document as `value.Doc`, `value.Array` and scalars,
  and `value.Unmarshal` stores that tree in the destination. This is the reference
  implementation, and it is the only path with precise errors (the path of the field and the
  types involved).
- **The stream.** `document.Stream` implements `source.Source` over the bytes, and `value` reads
  the destination straight from it without building the tree. Values the stream does not read
  directly, such as dates and identifiers, are handed to the tree decoder one at a time.

`readDocument` in `document_read.go` tries the stream first. If that fails for any reason, it
reads the document again through the tree and returns the tree's error. The consequences for a
change:

- The stream must never accept bytes that the tree rejects, and must store what the tree stores.
  `internal/document/stream_test.go` compares the two on documents written the way the client
  writes them and the way a server writes them, including every truncation and every damaged
  byte of one. Extend it when you change `Stream`, `value/stream.go` or the tree decoder.
- A new kind of value or destination is first handled by the tree path, which `Stream.Value`
  already provides. Add a direct stream path only when a benchmark shows it matters.

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

### Benchmarks

Benchmarks do not run with `go test`. Run them with `-bench`:

```sh
go test ./value -run '^$' -bench Unmarshal -benchmem
go test ./internal/document -run '^$' -bench . -benchmem
go test . -run '^$' -bench 'Get|Page' -benchmem
```

The first two measure decoding alone and are stable. The benchmarks in the module root also
include the fake server and the in-memory transport, so their times are only good for comparing
a change against the same code before it. Allocation counts are reliable on any machine; compare
times on a quiet one, with several runs (`-count`).

### End-to-end suite

`e2e/` starts a real `kdoq` for each test with a generated self-signed certificate and its own
configuration in a temporary directory, passes the port and the certificate hash to the client
explicitly, and stops the process when the test ends. It prints the server's log when a test
fails and leaves no process or file behind.

It needs a server binary. Build `kdoq` from the [kdoq repository](https://github.com/kdoqdb/kdoq)
and point `KDOQ_BIN` at it:

```sh
KDOQ_BIN=/path/to/kdoq go test -tags e2e ./e2e/...
```

When `KDOQ_BIN` is not set, the harness tries `cargo build -p kdoq --bin kdoq` in the current
module's tree, which works only if this library sits inside a checkout of the kdoq repository.

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
