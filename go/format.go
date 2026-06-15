package embedkv

const (
	BlockTypeStorageHeader    = uint8(0x01)
	BlockTypeRecordDescriptor = uint8(0x02)
	BlockTypeValueChunk       = uint8(0x03)

	// Header sizes no longer include the block CRC32. The CRC32 lives in the
	// last 4 bytes of every non-free block (offset block_size-4), independent
	// of block type.
	StorageHeaderSize          = uint32(28)
	RecordDescriptorHeaderSize = uint32(32)
	ValueChunkHeaderSize       = uint32(20)

	// BlockCRCSize is the size of the trailing block_crc32 field present at the
	// end (block_size-4) of every non-free block.
	BlockCRCSize = uint32(4)

	// NullBlockIndex is used in next_chunk fields to indicate no further block.
	NullBlockIndex = uint32(0xFFFFFFFF)

	VersionMajor = uint16(1)
	VersionMinor = uint16(0)
)
