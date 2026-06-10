package embedkv

// Fuzz tests for embedkv.
//
// Run all fuzz tests in parallel for a fixed duration:
//
//	go test ./go/... -fuzz=. -fuzztime=30s
//
// Run a single fuzz function:
//
//	go test ./go/... -fuzz=FuzzCRCValidDescriptor -fuzztime=60s
//
// Invariants under all fuzz inputs:
//   - No panic
//   - No excessive (unbounded) memory allocation
//   - No out-of-bounds buffer access
//   - All public APIs return only well-defined errors or nil

import (
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

const (
	fuzzBlockSize  = uint32(128)
	fuzzBlockCount = uint32(16)
)

// patchDescriptorCRC zeroes the CRC field at offset 28 and writes the correct CRC.
// This lets the fuzzer exercise all descriptor-parsing paths after the CRC gate.
func patchDescriptorCRC(buf []byte) {
	le.PutUint32(buf[28:], 0)
	le.PutUint32(buf[28:], computeBlockCRC32(buf, 28))
}

// buildSeedDescriptor returns a block-sized buffer with a valid single-chunk descriptor.
func buildSeedDescriptor(blockSize uint32, key, value []byte) []byte {
	buf := make([]byte, blockSize)
	keyLen := uint32(len(key))
	valLen := uint32(len(value))
	hdr := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       uint8(RecordDescriptorHeaderSize),
		KeySize:          uint16(keyLen),
		Generation:       1,
		TotalSize:        valLen,
		FirstPayloadSize: valLen,
		ChunkCount:       1,
		NextChunk:        NullBlockIndex,
	}
	marshalDescriptorHeader(&hdr, buf)
	copy(buf[RecordDescriptorHeaderSize:], key)
	copy(buf[RecordDescriptorHeaderSize+keyLen:], value)
	patchDescriptorCRC(buf)
	return buf
}

// buildSeedChunk returns a block-sized buffer with a valid value chunk.
func buildSeedChunk(blockSize, ownerBlock, chunkIndex uint32, payload []byte) []byte {
	buf := make([]byte, blockSize)
	payLen := uint32(len(payload))
	ch := ValueChunkHeader{
		BlockType:       BlockTypeValueChunk,
		HeaderSize:      uint8(ValueChunkHeaderSize),
		OwnerDescriptor: ownerBlock,
		ChunkIndex:      chunkIndex,
		PayloadSize:     payLen,
		NextChunk:       NullBlockIndex,
	}
	marshalChunkHeader(&ch, buf)
	copy(buf[ValueChunkHeaderSize:], payload)
	le.PutUint32(buf[20:], 0)
	le.PutUint32(buf[20:], computeBlockCRC32(buf, 20))
	return buf
}

// newFuzzStorage returns a formatted MemDevice ready for fuzz use.
func newFuzzStorage() *MemDevice {
	dev := NewMemDevice(fuzzBlockSize, fuzzBlockCount)
	Format(dev, DefaultOptions())
	return dev
}

// ── FuzzRawBlock ─────────────────────────────────────────────────────────────

// FuzzRawBlock passes arbitrary bytes as the content of block 1 and exercises
// classifyBlock + verifyAndReadRecord.  The storage header in block 0 is always
// valid so Open/BuildIndex also run.
func FuzzRawBlock(f *testing.F) {
	// seed: all zeros
	f.Add(make([]byte, fuzzBlockSize))
	// seed: all 0xFF
	ff := make([]byte, fuzzBlockSize)
	for i := range ff {
		ff[i] = 0xFF
	}
	f.Add(ff)
	// seed: valid single-chunk descriptor
	f.Add(buildSeedDescriptor(fuzzBlockSize, []byte("k"), []byte("v")))
	// seed: valid chunk block
	f.Add(buildSeedChunk(fuzzBlockSize, 1, 1, []byte("payload")))
	// seed: descriptor with block_type but zero remaining bytes (invalid CRC)
	corrupt := make([]byte, fuzzBlockSize)
	corrupt[0] = BlockTypeRecordDescriptor
	f.Add(corrupt)

	f.Fuzz(func(t *testing.T, rawBlock []byte) {
		block := make([]byte, fuzzBlockSize)
		copy(block, rawBlock)

		dev := newFuzzStorage()
		dev.WriteBlock(1, block)

		// classifyBlock must never panic
		classifyBlock(block)

		// verifyAndReadRecord must never panic or OOM
		rec, _ := verifyAndReadRecord(dev, 1)
		_ = rec

		// BuildIndex on a storage that contains this block must not panic
		s, err := Open(dev, DefaultOptions())
		if err != nil {
			return
		}
		s.BuildIndex()
		s.Recover()
		s.Get([]byte("k"))
		s.Close()
	})
}

// ── FuzzCRCValidDescriptor ───────────────────────────────────────────────────

// FuzzCRCValidDescriptor constructs a descriptor block whose CRC is always
// valid, then fuzzes every header field independently.  This exercises all
// validation paths that run *after* the CRC gate, which is where dangerous
// field values (huge TotalSize, ChunkCount, etc.) could cause OOM or panics.
//
// The fuzzer fills the chain blocks (2..N) with the same bytes so that any
// next_chunk pointer that falls in range will encounter the fuzzed data.
func FuzzCRCValidDescriptor(f *testing.F) {
	// seed: minimal valid single-chunk record
	f.Add(uint16(1), uint32(1), uint32(1), uint32(1), uint32(1), uint32(NullBlockIndex), uint32(0))
	// seed: exact-capacity value (fills descriptor payload completely)
	cap0 := uint32(fuzzBlockSize - RecordDescriptorHeaderSize - 1) // key=1 byte
	f.Add(uint16(1), uint32(1), cap0, cap0, uint32(1), uint32(NullBlockIndex), uint32(0))
	// seed: ChunkCount=MAX → must not cause huge allocation
	f.Add(uint16(0), uint32(1), uint32(0xFFFFFFFF), uint32(0), uint32(0xFFFFFFFF), uint32(2), uint32(0))
	// seed: TotalSize=MAX → must not cause huge allocation
	f.Add(uint16(0), uint32(1), uint32(0xFFFFFFFF), uint32(0), uint32(2), uint32(2), uint32(0))
	// seed: KeySize at limit → should return nil (key too big for block)
	f.Add(uint16(0xFFFF), uint32(1), uint32(0), uint32(0), uint32(1), uint32(NullBlockIndex), uint32(0))

	f.Fuzz(func(t *testing.T,
		keySize uint16,
		generation, totalSize, firstPayloadSize, chunkCount, nextChunk, flags uint32,
	) {
		buf := make([]byte, fuzzBlockSize)
		buf[0] = BlockTypeRecordDescriptor
		buf[1] = uint8(RecordDescriptorHeaderSize) // header_size always 32
		le.PutUint16(buf[2:], keySize)
		le.PutUint32(buf[4:], generation)
		le.PutUint32(buf[8:], totalSize)
		le.PutUint32(buf[12:], firstPayloadSize)
		le.PutUint32(buf[16:], chunkCount)
		le.PutUint32(buf[20:], nextChunk)
		le.PutUint32(buf[24:], flags)
		// Payload area (key + value) is left as zeros.
		patchDescriptorCRC(buf)

		dev := NewMemDevice(fuzzBlockSize, fuzzBlockCount)
		// Write the same fuzzed descriptor into all data blocks so that any
		// next_chunk pointer that lands in range encounters defined content.
		for i := uint32(1); i < fuzzBlockCount; i++ {
			dev.WriteBlock(i, buf)
		}

		rec, _ := verifyAndReadRecord(dev, 1)
		_ = rec
	})
}

// ── FuzzCRCValidChunk ────────────────────────────────────────────────────────

// FuzzCRCValidChunk fuzzes value chunk blocks with valid CRCs to ensure the
// chunk-parsing path handles all field combinations gracefully.
func FuzzCRCValidChunk(f *testing.F) {
	f.Add(uint32(1), uint32(1), uint32(1), uint32(NullBlockIndex))
	f.Add(uint32(0xFFFFFFFF), uint32(0xFFFFFFFF), uint32(0xFFFFFFFF), uint32(0xFFFFFFFF))
	f.Add(uint32(1), uint32(0), uint32(0), uint32(2))

	f.Fuzz(func(t *testing.T, ownerDescriptor, chunkIndex, payloadSize, nextChunk uint32) {
		buf := make([]byte, fuzzBlockSize)
		buf[0] = BlockTypeValueChunk
		buf[1] = uint8(ValueChunkHeaderSize)
		le.PutUint32(buf[4:], ownerDescriptor)
		le.PutUint32(buf[8:], chunkIndex)
		le.PutUint32(buf[12:], payloadSize)
		le.PutUint32(buf[16:], nextChunk)
		le.PutUint32(buf[20:], 0)
		le.PutUint32(buf[20:], computeBlockCRC32(buf, 20))

		dev := NewMemDevice(fuzzBlockSize, fuzzBlockCount)
		// Put a CRC-valid descriptor in block 1 that points to block 2 as next_chunk,
		// and the fuzzed chunk in block 2.
		descBuf := buildSeedDescriptor(fuzzBlockSize, []byte("k"), make([]byte, fuzzBlockSize-RecordDescriptorHeaderSize-2))
		// Patch the descriptor to be multi-chunk pointing at block 2
		var hdr RecordDescriptorHeader
		unmarshalDescriptorHeader(descBuf, &hdr)
		hdr.ChunkCount = 2
		hdr.NextChunk = 2
		hdr.FirstPayloadSize = fuzzBlockSize - RecordDescriptorHeaderSize - 1
		hdr.TotalSize = hdr.FirstPayloadSize + 1
		marshalDescriptorHeader(&hdr, descBuf)
		patchDescriptorCRC(descBuf)
		dev.WriteBlock(1, descBuf)
		dev.WriteBlock(2, buf)

		rec, _ := verifyAndReadRecord(dev, 1)
		_ = rec
	})
}

// ── FuzzStoragePipeline ──────────────────────────────────────────────────────

// FuzzStoragePipeline injects arbitrary bytes into the entire storage and runs
// the full Open → Recover → BuildIndex pipeline.  All code paths that touch
// the device must be safe regardless of content.
func FuzzStoragePipeline(f *testing.F) {
	const storageSize = fuzzBlockSize * fuzzBlockCount

	// seed: valid empty storage
	{
		dev := newFuzzStorage()
		f.Add(dev.Bytes())
	}
	// seed: all zeros
	f.Add(make([]byte, storageSize))
	// seed: valid storage with one record
	{
		dev := newFuzzStorage()
		s, _ := Open(dev, DefaultOptions())
		s.BuildIndex()
		s.Put([]byte("key"), []byte("value"))
		s.Close()
		f.Add(dev.Bytes())
	}
	// seed: valid storage with a large multi-chunk record
	{
		dev := newFuzzStorage()
		s, _ := Open(dev, DefaultOptions())
		s.BuildIndex()
		bigVal := make([]byte, int(fuzzBlockSize)*5)
		for i := range bigVal {
			bigVal[i] = byte(i)
		}
		s.Put([]byte("big"), bigVal)
		s.Close()
		f.Add(dev.Bytes())
	}

	f.Fuzz(func(t *testing.T, storageBytes []byte) {
		data := make([]byte, storageSize)
		copy(data, storageBytes)

		dev, err := NewMemDeviceFromBytes(fuzzBlockSize, data)
		if err != nil {
			return
		}

		s, err := Open(dev, DefaultOptions())
		if err != nil {
			return // invalid storage header — expected
		}

		// All of these must not panic regardless of block content
		s.Recover()
		s.BuildIndex()
		s.Get([]byte("key"))
		s.Get([]byte("big"))
		s.Close()
	})
}

// ── FuzzPutGetDelete ─────────────────────────────────────────────────────────

// FuzzPutGetDelete verifies that Put/Get/Delete handle arbitrary key and value
// bytes without panicking and that a successful Put always results in a
// readable value equal to what was written.
func FuzzPutGetDelete(f *testing.F) {
	f.Add([]byte("hello"), []byte("world"))
	f.Add([]byte("k"), make([]byte, 300)) // multi-chunk
	f.Add([]byte("empty"), []byte{})
	f.Add([]byte{0x00}, []byte{0xFF})
	f.Add([]byte("a"), make([]byte, int(fuzzBlockSize-RecordDescriptorHeaderSize-2))) // fits exactly
	// key at the length limit
	maxKey := make([]byte, fuzzBlockSize-RecordDescriptorHeaderSize-1)
	f.Add(maxKey, []byte("v"))

	f.Fuzz(func(t *testing.T, key, value []byte) {
		dev := newFuzzStorage()
		s, err := Open(dev, DefaultOptions())
		if err != nil {
			t.Fatal("Open on fresh storage failed:", err)
		}
		if err := s.BuildIndex(); err != nil {
			t.Fatal("BuildIndex failed:", err)
		}
		defer s.Close()

		err = s.Put(key, value)
		if err != nil {
			// Only ErrKeyTooLong or ErrStorageFull are acceptable failures
			if err != ErrKeyTooLong && err != ErrStorageFull {
				t.Errorf("Put returned unexpected error: %v", err)
			}
			return
		}

		got, err := s.Get(key)
		if err != nil {
			t.Errorf("Get after successful Put failed: %v", err)
			return
		}
		if string(got) != string(value) {
			t.Errorf("value mismatch: got len=%d want len=%d", len(got), len(value))
		}

		// Second Put (update) must also succeed
		err = s.Put(key, value)
		if err != nil && err != ErrStorageFull {
			t.Errorf("second Put (update) unexpected error: %v", err)
			return
		}
		if err == nil {
			got2, err2 := s.Get(key)
			if err2 != nil {
				t.Errorf("Get after update failed: %v", err2)
				return
			}
			if string(got2) != string(value) {
				t.Errorf("value mismatch after update")
			}
		}

		// Delete must succeed after Put
		if err := s.Delete(key); err != nil {
			t.Errorf("Delete after Put failed: %v", err)
			return
		}
		if _, err := s.Get(key); err != ErrKeyNotFound {
			t.Errorf("Get after Delete expected ErrKeyNotFound, got %v", err)
		}
	})
}
