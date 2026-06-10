package embedkv

import (
	"bytes"
	"testing"
)

// openWithRecovery is a helper that runs the full Open → Recover → BuildIndex sequence.
func openWithRecovery(t *testing.T, dev BlockDevice) *Store {
	t.Helper()
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal("Open:", err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal("Recover:", err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal("BuildIndex:", err)
	}
	return s
}

// openNormal is a helper that runs Open → BuildIndex (no recovery).
func openNormal(t *testing.T, dev BlockDevice) *Store {
	t.Helper()
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal("Open:", err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal("BuildIndex:", err)
	}
	return s
}

func TestRecoverCleanStorage(t *testing.T) {
	dev := NewMemDevice(256, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openNormal(t, dev)
	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2 := openWithRecovery(t, dev)
	defer s2.Close()

	got, err := s2.Get([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q", got)
	}
}

// TestRecoverRemovesGarbage: descriptor written for gen 2 but chunk has bad CRC.
// Recovery should fall back to gen 1.
func TestRecoverRemovesGarbage(t *testing.T) {
	const blockSize = 256
	dev := NewMemDevice(blockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openNormal(t, dev)

	key := []byte("key")
	v1 := []byte("gen1-value")
	if err := s.Put(key, v1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Find a free block
	buf := make([]byte, blockSize)
	freeIdx := uint32(0)
	for i := uint32(1); i < dev.BlockCount(); i++ {
		dev.ReadBlock(i, buf)
		if buf[0] == 0x00 {
			freeIdx = i
			break
		}
	}

	corruptChunkIdx := freeIdx + 1

	// Write a valid gen-2 descriptor pointing to a bad chunk
	descBuf := make([]byte, blockSize)
	dh := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       36,
		KeySize:          uint16(len(key)),

		Generation:       2,
		TotalSize:        100,
		FirstPayloadSize: 100,
		ChunkCount:       1,
		NextChunk:        corruptChunkIdx,
	}
	marshalDescriptorHeader(&dh, descBuf)
	copy(descBuf[RecordDescriptorHeaderSize:], key)
	copy(descBuf[RecordDescriptorHeaderSize+uint32(len(key)):], bytes.Repeat([]byte("X"), 100))
	dh.BlockCRC32 = computeBlockCRC32(descBuf, 32)
	marshalDescriptorHeader(&dh, descBuf)
	dev.WriteBlock(freeIdx, descBuf)

	// Write garbage into chunk slot (invalid CRC)
	garbageBuf := make([]byte, blockSize)
	garbageBuf[0] = BlockTypeValueChunk
	garbageBuf[1] = 24
	dev.WriteBlock(corruptChunkIdx, garbageBuf)

	s2 := openWithRecovery(t, dev)
	defer s2.Close()

	got, err := s2.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, v1) {
		t.Fatalf("got %q, want %q", got, v1)
	}

	// Garbage blocks must now be free
	dev.ReadBlock(freeIdx, buf)
	if buf[0] != 0x00 {
		t.Fatalf("garbage descriptor not freed: 0x%02x", buf[0])
	}
	dev.ReadBlock(corruptChunkIdx, buf)
	if buf[0] != 0x00 {
		t.Fatalf("garbage chunk not freed: 0x%02x", buf[0])
	}
}

// TestRecoverPowerLossBeforeFirstFlush: gen 2 descriptor written but chunk missing.
// Recovery should use gen 1.
func TestRecoverPowerLossBeforeFirstFlush(t *testing.T) {
	const blockSize = 256
	dev := NewMemDevice(blockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openNormal(t, dev)

	key := []byte("key")
	v1 := []byte("generation-1")
	if err := s.Put(key, v1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	buf := make([]byte, blockSize)
	freeIdx := uint32(0)
	for i := uint32(1); i < dev.BlockCount(); i++ {
		dev.ReadBlock(i, buf)
		if buf[0] == 0x00 {
			freeIdx = i
			break
		}
	}

	// Write gen-2 descriptor whose next_chunk points to a still-free block
	descBuf := make([]byte, blockSize)
	dh := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       36,
		KeySize:          uint16(len(key)),

		Generation:       2,
		TotalSize:        500,
		FirstPayloadSize: uint32(blockSize) - RecordDescriptorHeaderSize - uint32(len(key)),
		ChunkCount:       2,
		NextChunk:        freeIdx + 1, // zero-filled → invalid chunk
	}
	marshalDescriptorHeader(&dh, descBuf)
	copy(descBuf[RecordDescriptorHeaderSize:], key)
	dh.BlockCRC32 = computeBlockCRC32(descBuf, 32)
	marshalDescriptorHeader(&dh, descBuf)
	dev.WriteBlock(freeIdx, descBuf)

	s2 := openWithRecovery(t, dev)
	defer s2.Close()

	got, err := s2.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, v1) {
		t.Fatalf("expected gen1 %q, got %q", v1, got)
	}
}

// TestRecoverOldBlockPartiallyFreed (ARCH §23.5): gen 2 complete, orphan gen-1 chunk remains.
// Recovery should select gen 2 and free the orphan.
func TestRecoverOldBlockPartiallyFreed(t *testing.T) {
	const blockSize = 256
	dev := NewMemDevice(blockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openNormal(t, dev)
	key := []byte("x")
	if err := s.Put(key, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(key, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	buf := make([]byte, blockSize)
	freeIdx := uint32(0)
	for i := uint32(1); i < dev.BlockCount(); i++ {
		dev.ReadBlock(i, buf)
		if buf[0] == 0x00 {
			freeIdx = i
			break
		}
	}

	// Write an orphan value chunk (owner descriptor does not exist)
	chunkBuf := make([]byte, blockSize)
	ch := ValueChunkHeader{
		BlockType:       BlockTypeValueChunk,
		HeaderSize:      24,
		OwnerDescriptor: 99, // non-existent block
		ChunkIndex:      1,
		PayloadSize:     4,
		NextChunk:       NullBlockIndex,
	}
	marshalChunkHeader(&ch, chunkBuf)
	copy(chunkBuf[ValueChunkHeaderSize:], []byte("orph"))
	ch.BlockCRC32 = computeBlockCRC32(chunkBuf, 20)
	marshalChunkHeader(&ch, chunkBuf)
	dev.WriteBlock(freeIdx, chunkBuf)

	s2 := openWithRecovery(t, dev)
	defer s2.Close()

	got, err := s2.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Fatalf("got %q, want v2", got)
	}

	dev.ReadBlock(freeIdx, buf)
	if buf[0] != 0x00 {
		t.Fatalf("orphan chunk not freed: 0x%02x", buf[0])
	}
}

// TestRecoverReplicaSelectsBestGeneration (ARCH §20.3): gen 6 incomplete, gen 5 selected.
func TestRecoverReplicaSelectsBestGeneration(t *testing.T) {
	const blockSize = 256

	newFormatted := func() *MemDevice {
		dev := NewMemDevice(blockSize, 16)
		if err := Format(dev, DefaultOptions()); err != nil {
			t.Fatal(err)
		}
		return dev
	}

	dev0 := newFormatted()
	dev1 := newFormatted()
	dev2 := newFormatted()

	key := []byte("item")

	writeGens := func(dev BlockDevice, n int) {
		s, err := Open(dev, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.BuildIndex(); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			if err := s.put(key, []byte{byte(i)}); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
	}

	writeGens(dev0, 5) // gen 5 complete
	writeGens(dev2, 4) // gen 4 complete

	// dev1: gen 5 complete + broken gen 6 descriptor
	writeGens(dev1, 5)
	{
		buf := make([]byte, blockSize)
		freeIdx := uint32(0)
		for i := uint32(1); i < dev1.BlockCount(); i++ {
			dev1.ReadBlock(i, buf)
			if buf[0] == 0x00 {
				freeIdx = i
				break
			}
		}
		descBuf := make([]byte, blockSize)
		dh := RecordDescriptorHeader{
			BlockType:        BlockTypeRecordDescriptor,
			HeaderSize:       36,
			KeySize:          uint16(len(key)),
	
			Generation:       6,
			TotalSize:        300,
			FirstPayloadSize: uint32(blockSize) - RecordDescriptorHeaderSize - uint32(len(key)),
			ChunkCount:       2,
			NextChunk:        freeIdx + 1, // zero-filled → invalid
		}
		marshalDescriptorHeader(&dh, descBuf)
		copy(descBuf[RecordDescriptorHeaderSize:], key)
		dh.BlockCRC32 = computeBlockCRC32(descBuf, 32)
		marshalDescriptorHeader(&dh, descBuf)
		dev1.WriteBlock(freeIdx, descBuf)
	}

	r, err := RecoverReplicas([]BlockDevice{dev0, dev1, dev2}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := r.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{5}) {
		t.Fatalf("expected gen5 value [5], got %v", got)
	}
}
