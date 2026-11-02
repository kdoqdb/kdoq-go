package kdoq

import "github.com/kdoqdb/kdoq-go/value"

// Database is a handle on a database. Creating one touches no network and it
// is safe for concurrent use.
type Database struct {
	client *Client
	name   string
}

// Name returns the database name.
func (d *Database) Name() string { return d.name }

// Collection returns a handle on a collection whose documents are read and
// written as T: value.Doc, a struct, a pointer to a struct, or a type that
// implements value.Marshaler and value.Unmarshaler. It never touches the
// server.
func (d *Database) Collection[T any](name string) *Collection[T] {
	return &Collection[T]{client: d.client, database: d.name, name: name}
}

// Docs returns a collection of generic documents.
func (d *Database) Docs(name string) *Collection[value.Doc] {
	return d.Collection[value.Doc](name)
}
