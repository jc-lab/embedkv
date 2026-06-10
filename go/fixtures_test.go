package embedkv

// fixtures_test.go — testdata fixture generation and Go-side compatibility verification.
//
// Generate fixtures:
//   go test -run TestGenerateFixtures ./go/...
//
// Verify fixtures (Go reads its own output):
//   go test -run TestReadFixtures ./go/...

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const (
	fixtureBlockSize  = uint32(256)
	fixtureBlockCount = uint32(8) // default; individual fixtures may override
)

// ── generator helpers ─────────────────────────────────────────────────────────

func openStore(t *testing.T, dev BlockDevice) *Store {
	t.Helper()
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	return s
}

func writeFixtureFile(t *testing.T, relPath string, data []byte) {
	t.Helper()
	path := filepath.Join("..", relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes)", path, len(data))
}

func readFixtureFile(t *testing.T, relPath string) []byte {
	t.Helper()
	path := filepath.Join("..", relPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// ── fixture builders ──────────────────────────────────────────────────────────

func buildSmallValue(t *testing.T) []byte {
	t.Helper()
	dev := NewMemDevice(fixtureBlockSize, 8)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dev)
	if err := s.Put([]byte("hello"), []byte("world")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	return append([]byte(nil), dev.Bytes()...)
}

func buildLargeValue(t *testing.T) []byte {
	t.Helper()
	dev := NewMemDevice(fixtureBlockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dev)
	val := make([]byte, 500)
	for i := range val {
		val[i] = byte(i % 256)
	}
	if err := s.Put([]byte("bigkey"), val); err != nil {
		t.Fatal(err)
	}
	s.Close()
	return append([]byte(nil), dev.Bytes()...)
}

func buildMultiKey(t *testing.T) []byte {
	t.Helper()
	dev := NewMemDevice(fixtureBlockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dev)
	pairs := [][2]string{
		{"alpha", "value-alpha"},
		{"beta", "value-beta"},
		{"gamma", "value-gamma"},
	}
	for _, kv := range pairs {
		if err := s.Put([]byte(kv[0]), []byte(kv[1])); err != nil {
			t.Fatalf("put %q: %v", kv[0], err)
		}
	}
	s.Close()
	return append([]byte(nil), dev.Bytes()...)
}

// buildRecoveryPartialWrite: gen-1 complete; gen-2 descriptor valid but chunk CRC=0.
func buildRecoveryPartialWrite(t *testing.T) []byte {
	t.Helper()
	dev := NewMemDevice(fixtureBlockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}

	// Write gen-1 record
	s := openStore(t, dev)
	if err := s.Put([]byte("power"), []byte("gen1-data")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Inject a valid gen-2 descriptor pointing to block 3 as chunk
	key := []byte("power")
	keyLen := uint32(len(key))
	firstCap := fixtureBlockSize - RecordDescriptorHeaderSize - keyLen

	descBuf := make([]byte, fixtureBlockSize)
	dh := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       uint8(RecordDescriptorHeaderSize),
		KeySize:          uint16(keyLen),
		Generation:       2,
		TotalSize:        firstCap + 10, // needs one extra chunk
		FirstPayloadSize: firstCap,
		ChunkCount:       2,
		NextChunk:        3, // points to block 3
	}
	marshalDescriptorHeader(&dh, descBuf)
	copy(descBuf[RecordDescriptorHeaderSize:], key)
	dh.BlockCRC32 = computeBlockCRC32(descBuf, 28)
	marshalDescriptorHeader(&dh, descBuf)
	dev.WriteBlock(2, descBuf) // gen-2 descriptor in block 2

	// Block 3: valid block_type but zero CRC → corrupt chunk
	chunkBuf := make([]byte, fixtureBlockSize)
	chunkBuf[0] = BlockTypeValueChunk
	chunkBuf[1] = uint8(ValueChunkHeaderSize)
	// CRC field stays zero → invalid CRC
	dev.WriteBlock(3, chunkBuf)

	return append([]byte(nil), dev.Bytes()...)
}

// buildRecoveryPartialErase: gen-2 complete; orphan chunk (owner=99) in block 3.
func buildRecoveryPartialErase(t *testing.T) []byte {
	t.Helper()
	dev := NewMemDevice(fixtureBlockSize, 16)
	if err := Format(dev, DefaultOptions()); err != nil {
		t.Fatal(err)
	}

	// Write gen-1, then gen-2 (update)
	s := openStore(t, dev)
	if err := s.Put([]byte("power"), []byte("gen1-data")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("power"), []byte("gen2-data")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Find a free block to inject the orphan chunk
	buf := make([]byte, fixtureBlockSize)
	freeIdx := uint32(0)
	for i := uint32(1); i < dev.BlockCount(); i++ {
		dev.ReadBlock(i, buf)
		if buf[0] == 0x00 {
			freeIdx = i
			break
		}
	}
	if freeIdx == 0 {
		t.Fatal("no free block found")
	}

	// Orphan chunk: valid CRC, owner_descriptor = 99 (does not exist)
	chunkBuf := make([]byte, fixtureBlockSize)
	ch := ValueChunkHeader{
		BlockType:       BlockTypeValueChunk,
		HeaderSize:      uint8(ValueChunkHeaderSize),
		OwnerDescriptor: 99,
		ChunkIndex:      1,
		PayloadSize:     4,
		NextChunk:       NullBlockIndex,
	}
	marshalChunkHeader(&ch, chunkBuf)
	copy(chunkBuf[ValueChunkHeaderSize:], []byte("orph"))
	ch.BlockCRC32 = computeBlockCRC32(chunkBuf, 20)
	marshalChunkHeader(&ch, chunkBuf)
	dev.WriteBlock(freeIdx, chunkBuf)

	return append([]byte(nil), dev.Bytes()...)
}

// ── TestGenerateFixtures ──────────────────────────────────────────────────────

// TestGenerateFixtures writes all testdata fixtures to testdata/.
// Run with:  go test -run TestGenerateFixtures ./go/...
func TestGenerateFixtures(t *testing.T) {
	writeFixtureFile(t, "testdata/small_value.bin", buildSmallValue(t))
	writeFixtureFile(t, "testdata/large_value.bin", buildLargeValue(t))
	writeFixtureFile(t, "testdata/multi_key.bin", buildMultiKey(t))
	writeFixtureFile(t, "testdata/recovery/partial_write.bin", buildRecoveryPartialWrite(t))
	writeFixtureFile(t, "testdata/recovery/partial_erase.bin", buildRecoveryPartialErase(t))
}

// ── TestReadFixtures ──────────────────────────────────────────────────────────

// TestReadFixtures reads each testdata fixture and verifies expected contents.
// This exercises the same paths the Rust implementation must pass.
func TestReadFixtures(t *testing.T) {
	t.Run("small_value", testFixtureSmallValue)
	t.Run("large_value", testFixtureLargeValue)
	t.Run("multi_key", testFixtureMultiKey)
	t.Run("recovery/partial_write", testFixtureRecoveryPartialWrite)
	t.Run("recovery/partial_erase", testFixtureRecoveryPartialErase)
}

func loadFixture(t *testing.T, path string) *MemDevice {
	t.Helper()
	data := readFixtureFile(t, path)
	dev, err := NewMemDeviceFromBytes(fixtureBlockSize, data)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return dev
}

func testFixtureSmallValue(t *testing.T) {
	dev := loadFixture(t, "testdata/small_value.bin")
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Get([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Fatalf("got %q, want %q", got, "world")
	}
}

func testFixtureLargeValue(t *testing.T) {
	dev := loadFixture(t, "testdata/large_value.bin")
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	expected := make([]byte, 500)
	for i := range expected {
		expected[i] = byte(i % 256)
	}

	got, err := s.Get([]byte("bigkey"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("large value mismatch (got %d bytes)", len(got))
	}
}

func testFixtureMultiKey(t *testing.T) {
	dev := loadFixture(t, "testdata/multi_key.bin")
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pairs := [][2]string{
		{"alpha", "value-alpha"},
		{"beta", "value-beta"},
		{"gamma", "value-gamma"},
	}
	for _, kv := range pairs {
		got, err := s.Get([]byte(kv[0]))
		if err != nil {
			t.Fatalf("get %q: %v", kv[0], err)
		}
		if string(got) != kv[1] {
			t.Fatalf("key %q: got %q want %q", kv[0], got, kv[1])
		}
	}
}

func testFixtureRecoveryPartialWrite(t *testing.T) {
	dev := loadFixture(t, "testdata/recovery/partial_write.bin")
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal("Recover:", err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Get([]byte("power"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "gen1-data" {
		t.Fatalf("got %q, want gen1-data", got)
	}

	// Verify blocks 2 and 3 were freed
	buf := make([]byte, fixtureBlockSize)
	dev.ReadBlock(2, buf)
	if buf[0] != 0x00 {
		t.Fatalf("block 2 not freed after recover: 0x%02x", buf[0])
	}
	dev.ReadBlock(3, buf)
	if buf[0] != 0x00 {
		t.Fatalf("block 3 not freed after recover: 0x%02x", buf[0])
	}
}

func testFixtureRecoveryPartialErase(t *testing.T) {
	dev := loadFixture(t, "testdata/recovery/partial_erase.bin")
	s, err := Open(dev, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal("Recover:", err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Get([]byte("power"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "gen2-data" {
		t.Fatalf("got %q, want gen2-data", got)
	}
}
