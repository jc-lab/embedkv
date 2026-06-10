/// Block type for storage header (block 0).
pub const BLOCK_TYPE_STORAGE_HEADER: u8 = 0x01;
/// Block type for record descriptor blocks.
pub const BLOCK_TYPE_RECORD_DESCRIPTOR: u8 = 0x02;
/// Block type for value chunk blocks.
pub const BLOCK_TYPE_VALUE_CHUNK: u8 = 0x03;

/// Size of the StorageHeader struct in bytes.
pub const STORAGE_HEADER_SIZE: u32 = 32;
/// Size of the RecordDescriptorHeader struct in bytes.
pub const RECORD_DESCRIPTOR_HEADER_SIZE: u32 = 32;
/// Size of the ValueChunkHeader struct in bytes.
pub const VALUE_CHUNK_HEADER_SIZE: u32 = 24;

/// Sentinel value meaning "no next chunk".
pub const NULL_BLOCK_INDEX: u32 = 0xFFFF_FFFF;

pub const VERSION_MAJOR: u16 = 1;
pub const VERSION_MINOR: u16 = 0;

/// Magic bytes stored at offset 1..4 in the storage header.
pub const MAGIC: [u8; 3] = [b'E', b'K', b'V'];
