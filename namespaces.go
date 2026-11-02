package kdoq

import (
	"context"

	"github.com/kdoqdb/kdoq-go/internal/session"
	"github.com/kdoqdb/kdoq-go/internal/wire"
)

// Operation codes of the namespace and persistence operations.
const (
	opDatabaseList     = 0x0201
	opDatabaseGet      = 0x0202
	opDatabaseCreate   = 0x0203
	opDatabaseLoad     = 0x0204
	opDatabaseUnload   = 0x0205
	opCollectionList   = 0x0301
	opCollectionGet    = 0x0302
	opCollectionCreate = 0x0303
	opCollectionLoad   = 0x0304
	opCollectionUnload = 0x0305
	opWALSync          = 0x0601
	opCheckpoint       = 0x0602
)

// ResourceState is the lifecycle state of a database or collection.
type ResourceState string

// The resource states reported by the server.
const (
	ResourceLoaded      ResourceState = "loaded"
	ResourceUnavailable ResourceState = "unavailable"
	ResourceUnloaded    ResourceState = "unloaded"
	ResourceLoading     ResourceState = "loading"
	ResourceUnloading   ResourceState = "unloading"
)

var resourceStates = map[uint16]ResourceState{
	1: ResourceLoaded, 2: ResourceUnavailable, 3: ResourceUnloaded, 4: ResourceLoading, 5: ResourceUnloading,
}

// DatabaseInfo describes a database. Diagnostic explains an unavailable one.
type DatabaseInfo struct {
	Name       string
	State      ResourceState
	Diagnostic string
}

// CollectionInfo describes a collection.
type CollectionInfo struct {
	DatabaseInfo
	// LockReleased reports that the collection's lock is released.
	LockReleased bool
	// Excluded reports that the collection is individually excluded.
	Excluded bool
}

func databaseInfo(f *wire.Fields) (DatabaseInfo, error) {
	name, err := f.RequireString(1)
	if err != nil {
		return DatabaseInfo{}, malformedResponse(err)
	}
	code, err := f.RequireU16(2)
	if err != nil {
		return DatabaseInfo{}, malformedResponse(err)
	}
	state, ok := resourceStates[code]
	if !ok {
		state = ResourceUnavailable
	}
	diagnostic, _, err := f.String(3)
	if err != nil {
		return DatabaseInfo{}, malformedResponse(err)
	}
	return DatabaseInfo{Name: name, State: state, Diagnostic: diagnostic}, nil
}

func collectionInfo(f *wire.Fields) (CollectionInfo, error) {
	base, err := databaseInfo(f)
	if err != nil {
		return CollectionInfo{}, err
	}
	released, err := f.RequireU8(4)
	if err != nil {
		return CollectionInfo{}, malformedResponse(err)
	}
	excluded, err := f.RequireU8(5)
	if err != nil {
		return CollectionInfo{}, malformedResponse(err)
	}
	return CollectionInfo{DatabaseInfo: base, LockReleased: released == 1, Excluded: excluded == 1}, nil
}

// single returns the only item of a response.
func (r *response) single(what string) (*wire.Fields, error) {
	if len(r.items) != 1 {
		return nil, &Error{Code: CodeMalformed, Message: what + " did not return exactly one item"}
	}
	return r.items[0], nil
}

// command runs an operation with no item in its answer. State changes may have
// been applied when the response is lost, so they are writes.
func (c *Client) command(ctx context.Context, op uint16, payload []byte, need Capability, write bool) error {
	_, err := c.unary(ctx, session.Spec{Operation: op, Payload: payload, Write: write}, need)
	return err
}

func databasePayload(name string) []byte { return new(wire.Builder).String(1, name).Bytes() }

// Databases manages the databases of the server.
type Databases struct{ client *Client }

// Databases returns the database administration of the server.
func (c *Client) Databases() *Databases { return &Databases{client: c} }

// List returns every database, unloaded ones included, ordered by name.
func (d *Databases) List(ctx context.Context) ([]DatabaseInfo, error) {
	resp, err := d.client.unary(ctx, session.Spec{Operation: opDatabaseList}, CapDocuments)
	if err != nil {
		return nil, err
	}
	out := make([]DatabaseInfo, 0, len(resp.items))
	for _, item := range resp.items {
		info, err := databaseInfo(item)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// Get returns one database.
func (d *Databases) Get(ctx context.Context, name string) (DatabaseInfo, error) {
	resp, err := d.client.unary(ctx, session.Spec{Operation: opDatabaseGet, Payload: databasePayload(name)}, CapDocuments)
	if err != nil {
		return DatabaseInfo{}, err
	}
	item, err := resp.single("DATABASE_GET")
	if err != nil {
		return DatabaseInfo{}, err
	}
	return databaseInfo(item)
}

// Create creates a database. It needs the Admin access level.
func (d *Databases) Create(ctx context.Context, name string) error {
	return d.client.command(ctx, opDatabaseCreate, databasePayload(name), CapDocuments, true)
}

// Load loads an existing database. It needs the Admin access level.
func (d *Databases) Load(ctx context.Context, name string) error {
	return d.client.command(ctx, opDatabaseLoad, databasePayload(name), CapDocuments, true)
}

// Unload unloads a database. It needs the Admin access level.
func (d *Databases) Unload(ctx context.Context, name string) error {
	return d.client.command(ctx, opDatabaseUnload, databasePayload(name), CapDocuments, true)
}

// Info returns the database.
func (d *Database) Info(ctx context.Context) (DatabaseInfo, error) {
	return d.client.Databases().Get(ctx, d.name)
}

// Create creates the database. It needs the Admin access level.
func (d *Database) Create(ctx context.Context) error {
	return d.client.Databases().Create(ctx, d.name)
}

// Load loads the database. It needs the Admin access level.
func (d *Database) Load(ctx context.Context) error {
	return d.client.Databases().Load(ctx, d.name)
}

// Unload unloads the database. It needs the Admin access level.
func (d *Database) Unload(ctx context.Context) error {
	return d.client.Databases().Unload(ctx, d.name)
}

// Collections manages the collections of one database.
type Collections struct{ database *Database }

// Collections returns the collection administration of the database.
func (d *Database) Collections() *Collections { return &Collections{database: d} }

// List returns the collections of the database, unloaded ones included,
// ordered by name.
func (c *Collections) List(ctx context.Context) ([]CollectionInfo, error) {
	client := c.database.client
	resp, err := client.unary(ctx, session.Spec{Operation: opCollectionList, Payload: databasePayload(c.database.name)}, CapDocuments)
	if err != nil {
		return nil, err
	}
	out := make([]CollectionInfo, 0, len(resp.items))
	for _, item := range resp.items {
		info, err := collectionInfo(item)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// Get returns one collection.
func (c *Collections) Get(ctx context.Context, name string) (CollectionInfo, error) {
	return c.database.Docs(name).Info(ctx)
}

// Create creates a collection. It needs the Admin access level.
func (c *Collections) Create(ctx context.Context, name string) error {
	return c.database.Docs(name).Create(ctx)
}

// Load loads an existing collection. It needs the Admin access level.
func (c *Collections) Load(ctx context.Context, name string) error {
	return c.database.Docs(name).Load(ctx)
}

// Unload unloads a collection. It needs the Admin access level.
func (c *Collections) Unload(ctx context.Context, name string) error {
	return c.database.Docs(name).Unload(ctx)
}

func (c *Collection[T]) namespacePayload() []byte {
	var b wire.Builder
	c.namespace(&b)
	return b.Bytes()
}

// Info returns the collection.
func (c *Collection[T]) Info(ctx context.Context) (CollectionInfo, error) {
	resp, err := c.client.unary(ctx, session.Spec{Operation: opCollectionGet, Payload: c.namespacePayload()}, CapDocuments)
	if err != nil {
		return CollectionInfo{}, err
	}
	item, err := resp.single("COLLECTION_GET")
	if err != nil {
		return CollectionInfo{}, err
	}
	return collectionInfo(item)
}

// Create creates the collection. It needs the Admin access level.
func (c *Collection[T]) Create(ctx context.Context) error {
	return c.client.command(ctx, opCollectionCreate, c.namespacePayload(), CapDocuments, true)
}

// Load loads the collection. It needs the Admin access level.
func (c *Collection[T]) Load(ctx context.Context) error {
	return c.client.command(ctx, opCollectionLoad, c.namespacePayload(), CapDocuments, true)
}

// Unload unloads the collection. It needs the Admin access level.
func (c *Collection[T]) Unload(ctx context.Context) error {
	return c.client.command(ctx, opCollectionUnload, c.namespacePayload(), CapDocuments, true)
}

// WALSync synchronizes the write-ahead log appends that relaxed writes
// accumulated. It needs the Admin access level.
func (c *Collection[T]) WALSync(ctx context.Context) error {
	return c.client.command(ctx, opWALSync, c.namespacePayload(), CapDocuments, true)
}

// Checkpoint publishes the checkpoint of the collection. It needs the Admin
// access level.
func (c *Collection[T]) Checkpoint(ctx context.Context) error {
	return c.client.command(ctx, opCheckpoint, c.namespacePayload(), CapDocuments, true)
}
