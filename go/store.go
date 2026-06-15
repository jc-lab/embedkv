package embedkv

import (
	"errors"
	"sync"
)

// Options configures storage behaviour.
type Options struct {
	// FreePattern is the fill byte when erasing blocks.
	// Use 0x00 for zero-filled / HDD storage, 0xFF for erased NAND flash.
	FreePattern byte
	// ReplicaID is the replica identifier written to the first replica's header.
	// Replica i receives ReplicaID+i (see Format).
	ReplicaID uint32
	// FormatSeq is the storage format generation counter.
	FormatSeq uint32
}

// DefaultOptions returns options suitable for file-backed or memory-backed storage.
func DefaultOptions() Options {
	return Options{FreePattern: 0x00}
}

// ErrKeyTooLong is returned when a key does not fit in one block alongside the descriptor header.
var ErrKeyTooLong = errors.New("embedkv: key too long for block size")

// ErrNoReplicas is returned when Format/Open is called with an empty device slice.
var ErrNoReplicas = errors.New("embedkv: at least one replica device is required")

// ErrReplicaMismatch is returned when replica devices disagree on geometry.
var ErrReplicaMismatch = errors.New("embedkv: replica devices have mismatched geometry")

// replica is the per-device state of one storage replica.
type replica struct {
	dev    BlockDevice
	header StorageHeader
	index  *memIndex
}

// Store is the main handle for an open embedkv storage. A store is backed by one
// or more identical replica devices (§20). After Open, call Recover() and/or
// BuildIndex() before performing reads or writes.
//
// Writes fan out to every replica (each flushed independently); reads return the
// value from the replica holding the highest-generation complete record.
type Store struct {
	replicas []*replica
	opts     Options
	mu       sync.RWMutex
}

// Format writes a fresh storage header to block 0 of every replica device.
// Replica i is stamped with replica_id = opts.ReplicaID + i.
// Call once on freshly created devices before opening them.
func Format(devs []BlockDevice, opts Options) error {
	if len(devs) == 0 {
		return ErrNoReplicas
	}
	for i, dev := range devs {
		blockSize := dev.BlockSize()
		if blockSize < StorageHeaderSize+BlockCRCSize {
			return ErrInvalidBlockSize
		}
		buf := make([]byte, blockSize)
		hdr := StorageHeader{
			BlockType:    BlockTypeStorageHeader,
			Magic:        [3]byte{'E', 'K', 'V'},
			VersionMajor: VersionMajor,
			VersionMinor: VersionMinor,
			BlockSize:    blockSize,
			BlockCount:   dev.BlockCount(),
			ReplicaID:    opts.ReplicaID + uint32(i),
			FormatSeq:    opts.FormatSeq,
		}
		marshalStorageHeader(&hdr, buf)
		writeBlockCRC(buf)
		if err := dev.WriteBlock(0, buf); err != nil {
			return err
		}
		if err := dev.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// Open validates the storage header of every replica device and returns a Store
// with empty indexes. All replicas must share the same block_size and block_count.
// The caller must call BuildIndex() (and optionally Recover()) before using the store.
func Open(devs []BlockDevice, opts Options) (*Store, error) {
	if len(devs) == 0 {
		return nil, ErrNoReplicas
	}
	replicas := make([]*replica, 0, len(devs))
	var blockSize, blockCount uint32
	for i, dev := range devs {
		hdr, err := openReplicaHeader(dev)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			blockSize = dev.BlockSize()
			blockCount = dev.BlockCount()
		} else if dev.BlockSize() != blockSize || dev.BlockCount() != blockCount {
			return nil, ErrReplicaMismatch
		}
		replicas = append(replicas, &replica{dev: dev, header: hdr, index: newMemIndex()})
	}
	return &Store{replicas: replicas, opts: opts}, nil
}

// openReplicaHeader reads and validates a single replica's storage header.
func openReplicaHeader(dev BlockDevice) (StorageHeader, error) {
	var hdr StorageHeader
	blockSize := dev.BlockSize()
	if blockSize < StorageHeaderSize+BlockCRCSize {
		return hdr, ErrInvalidBlockSize
	}

	buf := make([]byte, blockSize)
	if err := dev.ReadBlock(0, buf); err != nil {
		return hdr, err
	}
	if buf[0] != BlockTypeStorageHeader {
		return hdr, ErrInvalidHeader
	}
	if !unmarshalStorageHeader(buf, &hdr) {
		return hdr, ErrCRCMismatch
	}
	if hdr.Magic != [3]byte{'E', 'K', 'V'} {
		return hdr, ErrInvalidHeader
	}
	if hdr.BlockSize != blockSize {
		return hdr, ErrInvalidHeader
	}
	if hdr.BlockCount != dev.BlockCount() {
		return hdr, ErrInvalidHeader
	}
	if hdr.BlockCount < 1 {
		return hdr, ErrInvalidHeader
	}
	return hdr, nil
}

// BuildIndex scans all data blocks of every replica and populates each replica's
// in-memory key index with the highest-generation complete record for each key.
// Call after Open (and optionally after Recover).
func (s *Store) BuildIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.replicas {
		if err := r.buildIndex(); err != nil {
			return err
		}
	}
	return nil
}

// Recover performs a full scan of every replica independently, removing garbage
// blocks and flushing affected replicas. Recover does NOT build the index;
// call BuildIndex() afterwards.
func (s *Store) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.replicas {
		if err := r.recover(s.opts.FreePattern); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the value associated with key. Across replicas, the highest
// complete generation is returned. Returns ErrKeyNotFound if absent everywhere.
func (s *Store) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keyStr := string(key)
	var best []byte
	bestGen := uint32(0)
	found := false

	for _, r := range s.replicas {
		entry, ok := r.index.get(keyStr)
		if !ok {
			continue
		}
		if found && entry.Generation <= bestGen {
			continue
		}
		rec, err := verifyAndReadRecord(r.dev, entry.DescriptorBlock)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			continue
		}
		best = rec.value
		bestGen = entry.Generation
		found = true
	}

	if !found {
		return nil, ErrKeyNotFound
	}
	out := make([]byte, len(best))
	copy(out, best)
	return out, nil
}

// Put writes key→value with optional user_flags (default 0) to every replica.
// Each replica writes and flushes the new record before erasing the old one.
// The same generation (max existing across replicas + 1) is written to all replicas.
func (s *Store) Put(key, value []byte, userFlags ...uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	flags := uint32(0)
	if len(userFlags) > 0 {
		flags = userFlags[0]
	}

	blockSize := s.replicas[0].dev.BlockSize()
	if uint32(len(key)) > blockSize-RecordDescriptorHeaderSize-BlockCRCSize {
		return ErrKeyTooLong
	}

	keyStr := string(key)
	generation := uint32(0)
	for _, r := range s.replicas {
		if e, ok := r.index.get(keyStr); ok && e.Generation > generation {
			generation = e.Generation
		}
	}
	generation++

	for _, r := range s.replicas {
		if err := r.put(key, value, generation, flags, s.opts.FreePattern); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the record for key from every replica. Returns ErrKeyNotFound
// only when the key is absent from all replicas.
func (s *Store) Delete(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyStr := string(key)
	found := false
	for _, r := range s.replicas {
		entry, ok := r.index.get(keyStr)
		if !ok {
			continue
		}
		found = true
		if err := r.freeRecord(entry.DescriptorBlock, s.opts.FreePattern); err != nil {
			return err
		}
		if err := r.dev.Flush(); err != nil {
			return err
		}
		r.index.delete(keyStr)
	}
	if !found {
		return ErrKeyNotFound
	}
	return nil
}

// Close releases all replica devices.
func (s *Store) Close() error {
	var last error
	for _, r := range s.replicas {
		if err := r.dev.Close(); err != nil {
			last = err
		}
	}
	return last
}

// Header returns a copy of the first replica's storage header.
func (s *Store) Header() StorageHeader { return s.replicas[0].header }

// ReplicaCount returns the number of replica devices backing this store.
func (s *Store) ReplicaCount() int { return len(s.replicas) }

// Keys returns the union of keys present in any replica's index.
// The order is not guaranteed.
func (s *Store) Keys() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := make(map[string]struct{})
	for _, r := range s.replicas {
		for k := range r.index.entries {
			set[k] = struct{}{}
		}
	}
	keys := make([][]byte, 0, len(set))
	for k := range set {
		keys = append(keys, []byte(k))
	}
	return keys
}

// Iterate calls fn for every key present in any replica's index.
// Iteration order is not guaranteed.
// If fn returns an error, iteration stops and that error is returned.
func (s *Store) Iterate(fn func(key []byte) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := make(map[string]struct{})
	for _, r := range s.replicas {
		for k := range r.index.entries {
			set[k] = struct{}{}
		}
	}
	for k := range set {
		if err := fn([]byte(k)); err != nil {
			return err
		}
	}
	return nil
}

// ── per-replica operations ───────────────────────────────────────────────────

// buildIndex repopulates r.index from a full scan of r.dev.
func (r *replica) buildIndex() error {
	r.index = newMemIndex()
	buf := make([]byte, r.dev.BlockSize())
	blockCount := r.dev.BlockCount()

	for i := uint32(1); i < blockCount; i++ {
		if err := r.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] != BlockTypeRecordDescriptor {
			continue
		}
		rec, err := verifyAndReadRecord(r.dev, i)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		keyStr := string(rec.key)
		if ex, ok := r.index.get(keyStr); !ok || rec.generation > ex.Generation {
			r.index.put(&indexEntry{
				Key:             keyStr,
				Generation:      rec.generation,
				DescriptorBlock: i,
				TotalSize:       rec.header.TotalSize,
			})
		}
	}
	return nil
}

// put writes a record at the given generation, flushes, then erases any prior
// record for the same key (copy-on-write, §13).
func (r *replica) put(key, value []byte, generation, userFlags uint32, freePattern byte) error {
	keyStr := string(key)
	existing, hasExisting := r.index.get(keyStr)

	needed := neededBlocks(key, value, r.dev.BlockSize())
	freeBlocks, err := r.findFreeBlocks(needed)
	if err != nil {
		return err
	}

	descriptorBlock := freeBlocks[0]
	if err := r.writeRecord(descriptorBlock, freeBlocks[1:], key, generation, value, userFlags); err != nil {
		return err
	}
	if err := r.dev.Flush(); err != nil {
		return err
	}

	r.index.put(&indexEntry{
		Key:             keyStr,
		Generation:      generation,
		DescriptorBlock: descriptorBlock,
		TotalSize:       uint32(len(value)),
	})

	if hasExisting {
		if err := r.freeRecord(existing.DescriptorBlock, freePattern); err != nil {
			return err
		}
		if err := r.dev.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// findFreeBlocks scans from block 1 and returns n block indices that are free.
func (r *replica) findFreeBlocks(n uint32) ([]uint32, error) {
	buf := make([]byte, r.dev.BlockSize())
	result := make([]uint32, 0, n)
	for i := uint32(1); i < r.dev.BlockCount() && uint32(len(result)) < n; i++ {
		if err := r.dev.ReadBlock(i, buf); err != nil {
			return nil, err
		}
		if isFreeCandidate(buf) {
			result = append(result, i)
		}
	}
	if uint32(len(result)) < n {
		return nil, ErrStorageFull
	}
	return result, nil
}

// writeRecord serialises a record (descriptor + value chunks) into the given blocks.
func (r *replica) writeRecord(descriptorBlock uint32, chunkBlocks []uint32, key []byte, generation uint32, value []byte, userFlags uint32) error {
	blockSize := r.dev.BlockSize()
	keyLen := uint32(len(key))
	firstCap := firstPayloadCapacity(blockSize, keyLen)
	chunkCap := chunkPayloadCapacity(blockSize)
	vlen := uint32(len(value))

	firstSize := vlen
	if firstSize > firstCap {
		firstSize = firstCap
	}
	chunkCount := uint32(1) + uint32(len(chunkBlocks))
	nextChunk := NullBlockIndex
	if len(chunkBlocks) > 0 {
		nextChunk = chunkBlocks[0]
	}

	// Descriptor block (zero-padded); CRC stored in the last 4 bytes.
	buf := make([]byte, blockSize)
	hdr := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       uint8(RecordDescriptorHeaderSize),
		KeySize:          uint16(keyLen),
		Generation:       generation,
		TotalSize:        vlen,
		FirstPayloadSize: firstSize,
		ChunkCount:       chunkCount,
		NextChunk:        nextChunk,
		UserFlags:        userFlags,
	}
	marshalDescriptorHeader(&hdr, buf)
	copy(buf[RecordDescriptorHeaderSize:], key)
	copy(buf[RecordDescriptorHeaderSize+keyLen:], value[:firstSize])
	writeBlockCRC(buf)
	if err := r.dev.WriteBlock(descriptorBlock, buf); err != nil {
		return err
	}

	// Value chunk blocks
	remaining := value[firstSize:]
	for ci, blockIdx := range chunkBlocks {
		payloadSize := uint32(len(remaining))
		if payloadSize > chunkCap {
			payloadSize = chunkCap
		}
		nextIdx := NullBlockIndex
		if ci+1 < len(chunkBlocks) {
			nextIdx = chunkBlocks[ci+1]
		}

		cbuf := make([]byte, blockSize)
		ch := ValueChunkHeader{
			BlockType:       BlockTypeValueChunk,
			HeaderSize:      uint8(ValueChunkHeaderSize),
			OwnerDescriptor: descriptorBlock,
			ChunkIndex:      uint32(ci + 1),
			PayloadSize:     payloadSize,
			NextChunk:       nextIdx,
		}
		marshalChunkHeader(&ch, cbuf)
		copy(cbuf[ValueChunkHeaderSize:], remaining[:payloadSize])
		writeBlockCRC(cbuf)
		if err := r.dev.WriteBlock(blockIdx, cbuf); err != nil {
			return err
		}
		remaining = remaining[payloadSize:]
	}
	return nil
}

// freeRecord overwrites all blocks of the record at descriptorBlock with the free pattern.
func (r *replica) freeRecord(descriptorBlock uint32, freePattern byte) error {
	blockSize := r.dev.BlockSize()
	buf := make([]byte, blockSize)

	if err := r.dev.ReadBlock(descriptorBlock, buf); err != nil {
		return err
	}
	var hdr RecordDescriptorHeader
	if !unmarshalDescriptorHeader(buf, &hdr) {
		return ErrCRCMismatch
	}

	free := makeFreeBuf(blockSize, freePattern)
	if err := r.dev.WriteBlock(descriptorBlock, free); err != nil {
		return err
	}

	next := hdr.NextChunk
	visited := make(map[uint32]bool)
	cbuf := make([]byte, blockSize)
	for next != NullBlockIndex && next < r.dev.BlockCount() {
		if visited[next] {
			break
		}
		visited[next] = true
		if err := r.dev.ReadBlock(next, cbuf); err != nil {
			return err
		}
		var ch ValueChunkHeader
		if !unmarshalChunkHeader(cbuf, &ch) {
			break
		}
		following := ch.NextChunk
		if err := r.dev.WriteBlock(next, free); err != nil {
			return err
		}
		next = following
	}
	return nil
}
