package embedkv

// indexEntry tracks the latest complete record for a key.
type indexEntry struct {
	Key             string
	Generation      uint32
	DescriptorBlock uint32
	TotalSize       uint32
}

// memIndex maps the actual key string to its latest complete record.
type memIndex struct {
	entries map[string]*indexEntry
}

func newMemIndex() *memIndex {
	return &memIndex{entries: make(map[string]*indexEntry)}
}

func (idx *memIndex) put(e *indexEntry) {
	idx.entries[e.Key] = e
}

func (idx *memIndex) get(key string) (*indexEntry, bool) {
	e, ok := idx.entries[key]
	return e, ok
}

func (idx *memIndex) delete(key string) {
	delete(idx.entries, key)
}
