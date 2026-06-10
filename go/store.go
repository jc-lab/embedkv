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
	// ReplicaID identifies this storage replica (stored in the header).
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

// Store is the main handle for an open embedkv storage.
// After Open, call Recover() and/or BuildIndex() before performing reads or writes.
type Store struct {
	dev    BlockDevice
	opts   Options
	header StorageHeader
	index  *memIndex
	mu     sync.RWMutex
}

// Format writes a new storage header to block 0.
// Call once on a freshly created device before opening it.
func Format(dev BlockDevice, opts Options) error {
	blockSize := dev.BlockSize()
	if blockSize < StorageHeaderSize {
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
		ReplicaID:    opts.ReplicaID,
		FormatSeq:    opts.FormatSeq,
	}
	marshalStorageHeader(&hdr, buf)
	hdr.BlockCRC32 = computeBlockCRC32(buf, 28)
	marshalStorageHeader(&hdr, buf)
	return dev.WriteBlock(0, buf)
}

// Open validates the storage header and returns a Store with an empty index.
// The caller must call BuildIndex() (and optionally Recover()) before using the store.
func Open(dev BlockDevice, opts Options) (*Store, error) {
	blockSize := dev.BlockSize()
	if blockSize < StorageHeaderSize {
		return nil, ErrInvalidBlockSize
	}

	buf := make([]byte, blockSize)
	if err := dev.ReadBlock(0, buf); err != nil {
		return nil, err
	}

	if buf[0] != BlockTypeStorageHeader {
		return nil, ErrInvalidHeader
	}

	var hdr StorageHeader
	if !unmarshalStorageHeader(buf, &hdr) {
		return nil, ErrCRCMismatch
	}

	if hdr.Magic != [3]byte{'E', 'K', 'V'} {
		return nil, ErrInvalidHeader
	}
	if hdr.BlockSize != blockSize {
		return nil, ErrInvalidHeader
	}
	if hdr.BlockCount != dev.BlockCount() {
		return nil, ErrInvalidHeader
	}
	if hdr.BlockCount < 1 {
		return nil, ErrInvalidHeader
	}

	return &Store{dev: dev, opts: opts, header: hdr, index: newMemIndex()}, nil
}

// BuildIndex scans all data blocks and populates the in-memory key index with
// the highest-generation complete record for each key. Call after Open (and
// optionally after Recover).
func (s *Store) BuildIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.index = newMemIndex()
	buf := make([]byte, s.dev.BlockSize())
	blockCount := s.dev.BlockCount()

	for i := uint32(1); i < blockCount; i++ {
		if err := s.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] != BlockTypeRecordDescriptor {
			continue
		}
		rec, err := verifyAndReadRecord(s.dev, i)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		keyStr := string(rec.key)
		if ex, ok := s.index.get(keyStr); !ok || rec.generation > ex.Generation {
			s.index.put(&indexEntry{
				Key:             keyStr,
				Generation:      rec.generation,
				DescriptorBlock: i,
				TotalSize:       rec.header.TotalSize,
			})
		}
	}
	return nil
}

// Get returns the value associated with key. Returns ErrKeyNotFound if absent.
func (s *Store) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(key)
}

func (s *Store) get(key []byte) ([]byte, error) {
	entry, ok := s.index.get(string(key))
	if !ok {
		return nil, ErrKeyNotFound
	}
	rec, err := verifyAndReadRecord(s.dev, entry.DescriptorBlock)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, ErrCorruptRecord
	}
	out := make([]byte, len(rec.value))
	copy(out, rec.value)
	return out, nil
}

// Put writes key→value, creating a new record or copy-on-write updating an existing one.
// The new record is written and flushed before the old record is erased.
func (s *Store) Put(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.put(key, value)
}

func (s *Store) put(key, value []byte) error {
	if uint32(len(key)) >= s.dev.BlockSize()-RecordDescriptorHeaderSize {
		return ErrKeyTooLong
	}

	keyStr := string(key)
	existing, hasExisting := s.index.get(keyStr)

	generation := uint32(1)
	if hasExisting {
		generation = existing.Generation + 1
	}

	needed := neededBlocks(key, value, s.dev.BlockSize())
	freeBlocks, err := s.findFreeBlocks(needed)
	if err != nil {
		return err
	}

	descriptorBlock := freeBlocks[0]
	if err := s.writeRecord(descriptorBlock, freeBlocks[1:], key, generation, value); err != nil {
		return err
	}
	if err := s.dev.Flush(); err != nil {
		return err
	}

	s.index.put(&indexEntry{
		Key:             keyStr,
		Generation:      generation,
		DescriptorBlock: descriptorBlock,
		TotalSize:       uint32(len(value)),
	})

	if hasExisting {
		if err := s.freeRecord(existing.DescriptorBlock); err != nil {
			return err
		}
		if err := s.dev.Flush(); err != nil {
			return err
		}
	}

	return nil
}

// Delete removes the record for key. Returns ErrKeyNotFound if absent.
func (s *Store) Delete(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyStr := string(key)
	entry, ok := s.index.get(keyStr)
	if !ok {
		return ErrKeyNotFound
	}
	if err := s.freeRecord(entry.DescriptorBlock); err != nil {
		return err
	}
	if err := s.dev.Flush(); err != nil {
		return err
	}
	s.index.delete(keyStr)
	return nil
}

// Close releases the underlying device.
func (s *Store) Close() error {
	return s.dev.Close()
}

// Header returns a copy of the storage header.
func (s *Store) Header() StorageHeader { return s.header }

// findFreeBlocks scans from block 1 and returns n block indices that are free.
func (s *Store) findFreeBlocks(n uint32) ([]uint32, error) {
	buf := make([]byte, s.dev.BlockSize())
	result := make([]uint32, 0, n)
	for i := uint32(1); i < s.dev.BlockCount() && uint32(len(result)) < n; i++ {
		if err := s.dev.ReadBlock(i, buf); err != nil {
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
func (s *Store) writeRecord(descriptorBlock uint32, chunkBlocks []uint32, key []byte, generation uint32, value []byte) error {
	blockSize := s.dev.BlockSize()
	keyLen := uint32(len(key))
	firstCap := blockSize - RecordDescriptorHeaderSize - keyLen
	chunkCap := blockSize - ValueChunkHeaderSize
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

	// Descriptor block (zero-padded)
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
	}
	marshalDescriptorHeader(&hdr, buf)
	// Write key bytes after header
	copy(buf[RecordDescriptorHeaderSize:], key)
	// Write first value payload after key
	copy(buf[RecordDescriptorHeaderSize+keyLen:], value[:firstSize])
	hdr.BlockCRC32 = computeBlockCRC32(buf, 28)
	marshalDescriptorHeader(&hdr, buf)
	if err := s.dev.WriteBlock(descriptorBlock, buf); err != nil {
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
		ch.BlockCRC32 = computeBlockCRC32(cbuf, 20)
		marshalChunkHeader(&ch, cbuf)
		if err := s.dev.WriteBlock(blockIdx, cbuf); err != nil {
			return err
		}
		remaining = remaining[payloadSize:]
	}
	return nil
}

// freeRecord overwrites all blocks of the record at descriptorBlock with the free pattern.
func (s *Store) freeRecord(descriptorBlock uint32) error {
	blockSize := s.dev.BlockSize()
	buf := make([]byte, blockSize)

	if err := s.dev.ReadBlock(descriptorBlock, buf); err != nil {
		return err
	}
	var hdr RecordDescriptorHeader
	if !unmarshalDescriptorHeader(buf, &hdr) {
		return ErrCRCMismatch
	}

	free := makeFreeBuf(blockSize, s.opts.FreePattern)
	if err := s.dev.WriteBlock(descriptorBlock, free); err != nil {
		return err
	}

	next := hdr.NextChunk
	visited := make(map[uint32]bool)
	cbuf := make([]byte, blockSize)
	for next != NullBlockIndex && next < s.dev.BlockCount() {
		if visited[next] {
			break
		}
		visited[next] = true
		if err := s.dev.ReadBlock(next, cbuf); err != nil {
			return err
		}
		var ch ValueChunkHeader
		if !unmarshalChunkHeader(cbuf, &ch) {
			break
		}
		following := ch.NextChunk
		if err := s.dev.WriteBlock(next, free); err != nil {
			return err
		}
		next = following
	}
	return nil
}
