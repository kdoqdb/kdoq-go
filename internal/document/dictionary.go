// Package document encodes and decodes KQ BSON v4 values against name
// dictionaries (docs/architecture/document-v4.md, docs/reference/proto/values.md).
// Users never see it: the client builds dictionaries from input documents and
// resolves output contexts here.
package document

import "github.com/kdoqdb/kdoq-go/internal/wire"

// MaxDepth is the container nesting limit.
const MaxDepth = 64

// Context is the dictionary context the client uses for input of one stream.
const Context = 1

// Dictionary assigns field IDs to names in first-use order and remembers the
// entries that have not been announced yet.
type Dictionary struct {
	context   uint64
	ids       map[string]uint32
	pending   []Entry
	announced bool
}

// Entry is one dictionary mapping.
type Entry struct {
	ID   uint32
	Name string
}

// NewDictionary returns an empty dictionary.
func NewDictionary() *Dictionary { return NewDictionaryIn(Context) }

// NewDictionaryIn returns an empty dictionary that announces its names in the
// given context, as a server does for the output of a query.
func NewDictionaryIn(context uint64) *Dictionary {
	return &Dictionary{context: context, ids: map[string]uint32{}}
}

func (d *Dictionary) idOf(name string) uint32 {
	if id, ok := d.ids[name]; ok {
		return id
	}
	id := uint32(len(d.ids))
	d.ids[name] = id
	d.pending = append(d.pending, Entry{ID: id, Name: name})
	return id
}

// Frames returns the DICTIONARY frames for the names added since the previous
// call, split so that no frame carries more than 16 KiB of entries. The first
// call always returns a frame, even without entries, so that the context is
// declared before a document refers to it.
func (d *Dictionary) Frames() [][]byte {
	if len(d.pending) == 0 && d.announced {
		return nil
	}
	d.announced = true
	if len(d.pending) == 0 {
		var b wire.Builder
		b.U64(1, d.context)
		return [][]byte{wire.Encode(wire.KindDictionary, b.Bytes())}
	}
	const limit = 16 * 1024
	var frames [][]byte
	var current []Entry
	size := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		var b wire.Builder
		b.U64(1, d.context)
		for _, e := range current {
			var entry wire.Builder
			entry.U32(1, e.ID).String(2, e.Name)
			b.Group(2, &entry)
		}
		frames = append(frames, wire.Encode(wire.KindDictionary, b.Bytes()))
		current, size = nil, 0
	}
	for _, e := range d.pending {
		cost := len(e.Name) + 16
		if size+cost > limit {
			flush()
		}
		current = append(current, e)
		size += cost
	}
	flush()
	d.pending = nil
	return frames
}

// Names is an output dictionary context: field ID to name.
type Names map[uint32]string

// Contexts holds the output dictionaries of one operation stream.
type Contexts map[uint64]Names

// Add merges the entries of a DICTIONARY frame payload.
func (c Contexts) Add(payload []byte) error {
	f, err := wire.Parse(payload)
	if err != nil {
		return err
	}
	id, err := f.RequireU64(1)
	if err != nil {
		return err
	}
	names := c[id]
	if names == nil {
		names = Names{}
		c[id] = names
	}
	entries, err := f.Groups(2)
	if err != nil {
		return err
	}
	for _, e := range entries {
		fid, err := e.RequireU32(1)
		if err != nil {
			return err
		}
		name, err := e.RequireString(2)
		if err != nil {
			return err
		}
		names[fid] = name
	}
	return nil
}
