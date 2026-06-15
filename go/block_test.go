package embedkv

import (
	"testing"
)

func TestStorageHeaderRoundtrip(t *testing.T) {
	const blockSize = 512
	buf := make([]byte, blockSize)

	in := StorageHeader{
		BlockType:    BlockTypeStorageHeader,
		Magic:        [3]byte{'E', 'K', 'V'},
		VersionMajor: 1,
		VersionMinor: 0,
		BlockSize:    blockSize,
		BlockCount:   256,
		ReplicaID:    42,
		FormatSeq:    7,
	}
	marshalStorageHeader(&in, buf)

	var out StorageHeader
	unmarshalStorageHeader(buf, &out)

	if in != out {
		t.Fatalf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}
}

func TestDescriptorHeaderRoundtrip(t *testing.T) {
	const blockSize = 512
	buf := make([]byte, blockSize)

	k := []byte("testkey")
	in := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       uint8(RecordDescriptorHeaderSize),
		KeySize:          uint16(len(k)),
		Generation:       3,
		TotalSize:        100,
		FirstPayloadSize: 100,
		ChunkCount:       1,
		NextChunk:        NullBlockIndex,
	}
	marshalDescriptorHeader(&in, buf)

	var out RecordDescriptorHeader
	unmarshalDescriptorHeader(buf, &out)

	if in != out {
		t.Fatalf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}
}

func TestChunkHeaderRoundtrip(t *testing.T) {
	const blockSize = 512
	buf := make([]byte, blockSize)

	in := ValueChunkHeader{
		BlockType:       BlockTypeValueChunk,
		HeaderSize:      uint8(ValueChunkHeaderSize),
		OwnerDescriptor: 1,
		ChunkIndex:      1,
		PayloadSize:     488,
		NextChunk:       NullBlockIndex,
	}
	marshalChunkHeader(&in, buf)

	var out ValueChunkHeader
	unmarshalChunkHeader(buf, &out)

	if in != out {
		t.Fatalf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}
}

func TestCRCStorageHeader(t *testing.T) {
	const blockSize = 512
	buf := make([]byte, blockSize)

	hdr := StorageHeader{
		BlockType:    BlockTypeStorageHeader,
		Magic:        [3]byte{'E', 'K', 'V'},
		VersionMajor: 1,
		BlockSize:    blockSize,
		BlockCount:   64,
	}
	marshalStorageHeader(&hdr, buf)
	writeBlockCRC(buf) // block CRC lives in the last 4 bytes

	var out StorageHeader
	if !unmarshalStorageHeader(buf, &out) {
		t.Fatal("CRC mismatch on read-back")
	}

	buf[5] ^= 0xFF
	if verifyBlockCRC(buf) {
		t.Fatal("expected CRC failure after corruption")
	}
}

func TestCRCDescriptorHeader(t *testing.T) {
	const blockSize = 256
	buf := make([]byte, blockSize)
	k := []byte("hello")

	hdr := RecordDescriptorHeader{
		BlockType:        BlockTypeRecordDescriptor,
		HeaderSize:       uint8(RecordDescriptorHeaderSize),
		KeySize:          uint16(len(k)),
		Generation:       1,
		TotalSize:        5,
		FirstPayloadSize: 5,
		ChunkCount:       1,
		NextChunk:        NullBlockIndex,
	}
	marshalDescriptorHeader(&hdr, buf)
	copy(buf[RecordDescriptorHeaderSize:], k)
	copy(buf[RecordDescriptorHeaderSize+uint32(len(k)):], []byte("world"))
	writeBlockCRC(buf)

	var out RecordDescriptorHeader
	if !unmarshalDescriptorHeader(buf, &out) {
		t.Fatal("descriptor CRC mismatch")
	}

	buf[40] ^= 0x01
	if verifyBlockCRC(buf) {
		t.Fatal("expected CRC failure after payload corruption")
	}
}

func TestClassifyBlock(t *testing.T) {
	const blockSize = 256
	dev := NewMemDevice(blockSize, 8)
	opts := DefaultOptions()

	if err := Format([]BlockDevice{dev}, opts); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, blockSize)
	dev.ReadBlock(0, buf)
	if classifyBlock(buf) != blockClassValid {
		t.Fatal("storage header should be valid")
	}

	dev.ReadBlock(1, buf)
	if classifyBlock(buf) != blockClassFree {
		t.Fatal("untouched block should be free")
	}

	// Corrupt descriptor: valid type byte but wrong CRC
	buf2 := make([]byte, blockSize)
	buf2[0] = BlockTypeRecordDescriptor
	buf2[1] = 32
	dev.WriteBlock(2, buf2)
	dev.ReadBlock(2, buf2)
	if classifyBlock(buf2) != blockClassGarbage {
		t.Fatal("corrupt descriptor should be garbage")
	}
}

