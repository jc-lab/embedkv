package embedkv

import "encoding/binary"

var le = binary.LittleEndian

// StorageHeader is the 32-byte header stored in block 0.
type StorageHeader struct {
	BlockType    uint8
	Magic        [3]byte
	VersionMajor uint16
	VersionMinor uint16
	BlockSize    uint32
	BlockCount   uint32
	ReplicaID    uint32
	FormatSeq    uint32
	Flags        uint32
	BlockCRC32   uint32
}

func marshalStorageHeader(h *StorageHeader, buf []byte) {
	buf[0] = h.BlockType
	buf[1] = h.Magic[0]
	buf[2] = h.Magic[1]
	buf[3] = h.Magic[2]
	le.PutUint16(buf[4:], h.VersionMajor)
	le.PutUint16(buf[6:], h.VersionMinor)
	le.PutUint32(buf[8:], h.BlockSize)
	le.PutUint32(buf[12:], h.BlockCount)
	le.PutUint32(buf[16:], h.ReplicaID)
	le.PutUint32(buf[20:], h.FormatSeq)
	le.PutUint32(buf[24:], h.Flags)
	le.PutUint32(buf[28:], h.BlockCRC32)
}

// unmarshalStorageHeader deserializes the header and verifies its CRC32.
// Returns true only when the CRC is valid.
func unmarshalStorageHeader(buf []byte, h *StorageHeader) bool {
	h.BlockType = buf[0]
	h.Magic[0] = buf[1]
	h.Magic[1] = buf[2]
	h.Magic[2] = buf[3]
	h.VersionMajor = le.Uint16(buf[4:])
	h.VersionMinor = le.Uint16(buf[6:])
	h.BlockSize = le.Uint32(buf[8:])
	h.BlockCount = le.Uint32(buf[12:])
	h.ReplicaID = le.Uint32(buf[16:])
	h.FormatSeq = le.Uint32(buf[20:])
	h.Flags = le.Uint32(buf[24:])
	h.BlockCRC32 = le.Uint32(buf[28:])
	return computeBlockCRC32(buf, 28) == h.BlockCRC32
}

// RecordDescriptorHeader is the fixed 32-byte header of a record descriptor block.
// Key bytes follow immediately at offset 32; value payload starts at offset 32+KeySize.
type RecordDescriptorHeader struct {
	BlockType        uint8
	HeaderSize       uint8
	KeySize          uint16 // key length in bytes (UTF-8)
	Generation       uint32
	TotalSize        uint32
	FirstPayloadSize uint32
	ChunkCount       uint32
	NextChunk        uint32
	Flags            uint32
	BlockCRC32       uint32 // offset 28
}

func marshalDescriptorHeader(h *RecordDescriptorHeader, buf []byte) {
	buf[0] = h.BlockType
	buf[1] = h.HeaderSize
	le.PutUint16(buf[2:], h.KeySize)
	le.PutUint32(buf[4:], h.Generation)
	le.PutUint32(buf[8:], h.TotalSize)
	le.PutUint32(buf[12:], h.FirstPayloadSize)
	le.PutUint32(buf[16:], h.ChunkCount)
	le.PutUint32(buf[20:], h.NextChunk)
	le.PutUint32(buf[24:], h.Flags)
	le.PutUint32(buf[28:], h.BlockCRC32)
}

// unmarshalDescriptorHeader deserializes the header and verifies its CRC32.
// Returns true only when the CRC is valid.
func unmarshalDescriptorHeader(buf []byte, h *RecordDescriptorHeader) bool {
	h.BlockType = buf[0]
	h.HeaderSize = buf[1]
	h.KeySize = le.Uint16(buf[2:])
	h.Generation = le.Uint32(buf[4:])
	h.TotalSize = le.Uint32(buf[8:])
	h.FirstPayloadSize = le.Uint32(buf[12:])
	h.ChunkCount = le.Uint32(buf[16:])
	h.NextChunk = le.Uint32(buf[20:])
	h.Flags = le.Uint32(buf[24:])
	h.BlockCRC32 = le.Uint32(buf[28:])
	return computeBlockCRC32(buf, 28) == h.BlockCRC32
}

// ValueChunkHeader is the fixed 24-byte header of a value chunk block.
type ValueChunkHeader struct {
	BlockType       uint8
	HeaderSize      uint8
	Flags           uint16
	OwnerDescriptor uint32
	ChunkIndex      uint32
	PayloadSize     uint32
	NextChunk       uint32
	BlockCRC32      uint32
}

func marshalChunkHeader(h *ValueChunkHeader, buf []byte) {
	buf[0] = h.BlockType
	buf[1] = h.HeaderSize
	le.PutUint16(buf[2:], h.Flags)
	le.PutUint32(buf[4:], h.OwnerDescriptor)
	le.PutUint32(buf[8:], h.ChunkIndex)
	le.PutUint32(buf[12:], h.PayloadSize)
	le.PutUint32(buf[16:], h.NextChunk)
	le.PutUint32(buf[20:], h.BlockCRC32)
}

// unmarshalChunkHeader deserializes the header and verifies its CRC32.
// Returns true only when the CRC is valid.
func unmarshalChunkHeader(buf []byte, h *ValueChunkHeader) bool {
	h.BlockType = buf[0]
	h.HeaderSize = buf[1]
	h.Flags = le.Uint16(buf[2:])
	h.OwnerDescriptor = le.Uint32(buf[4:])
	h.ChunkIndex = le.Uint32(buf[8:])
	h.PayloadSize = le.Uint32(buf[12:])
	h.NextChunk = le.Uint32(buf[16:])
	h.BlockCRC32 = le.Uint32(buf[20:])
	return computeBlockCRC32(buf, 20) == h.BlockCRC32
}

// classifyBlock returns whether buf is a free, non-free valid, or garbage block.
// Per ARCH §9: check type+CRC first; if valid → non-free. If first byte is 0x00/0xFF → free.
// Otherwise → garbage (caller decides what to do).
type blockClass int

const (
	blockClassFree    blockClass = iota
	blockClassValid              // valid non-free block with good CRC
	blockClassGarbage            // corrupted or unknown
)

func classifyBlock(buf []byte) blockClass {
	if len(buf) == 0 {
		return blockClassFree
	}
	first := buf[0]
	switch first {
	case BlockTypeStorageHeader:
		var h StorageHeader
		if unmarshalStorageHeader(buf, &h) {
			return blockClassValid
		}
	case BlockTypeRecordDescriptor:
		var h RecordDescriptorHeader
		if unmarshalDescriptorHeader(buf, &h) {
			return blockClassValid
		}
	case BlockTypeValueChunk:
		var h ValueChunkHeader
		if unmarshalChunkHeader(buf, &h) {
			return blockClassValid
		}
	}
	if first == 0x00 || first == 0xFF {
		return blockClassFree
	}
	return blockClassGarbage
}

// isFreeCandidate returns true if the block can be allocated as free space.
func isFreeCandidate(buf []byte) bool {
	return classifyBlock(buf) == blockClassFree
}

// makeFreeBuf returns a block-sized buffer filled with the given free pattern (0x00 or 0xFF).
func makeFreeBuf(blockSize uint32, pattern byte) []byte {
	buf := make([]byte, blockSize)
	if pattern != 0x00 {
		for i := range buf {
			buf[i] = pattern
		}
	}
	return buf
}
