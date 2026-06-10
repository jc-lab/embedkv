package embedkv

const (
	BlockTypeStorageHeader    = uint8(0x01)
	BlockTypeRecordDescriptor = uint8(0x02)
	BlockTypeValueChunk       = uint8(0x03)

	StorageHeaderSize          = uint32(32)
	RecordDescriptorHeaderSize = uint32(32)
	ValueChunkHeaderSize       = uint32(24)

	// NullBlockIndex is used in next_chunk fields to indicate no further block.
	NullBlockIndex = uint32(0xFFFFFFFF)

	VersionMajor = uint16(1)
	VersionMinor = uint16(0)
)
