use crate::crc::verify_block_crc;
use crate::format::*;

// ── StorageHeader ──────────────────────────────────────────────────────────

/// 28-byte storage header stored at the start of block 0.
/// The block's CRC32 is stored separately in the last 4 bytes of the block.
#[derive(Debug, Clone, Default)]
pub struct StorageHeader {
    pub block_type: u8,
    pub magic: [u8; 3],
    pub version_major: u16,
    pub version_minor: u16,
    pub block_size: u32,
    pub block_count: u32,
    pub replica_id: u32,
    pub format_seq: u32,
    pub flags: u32,
}

pub fn marshal_storage_header(h: &StorageHeader, buf: &mut [u8]) {
    buf[0] = h.block_type;
    buf[1] = h.magic[0];
    buf[2] = h.magic[1];
    buf[3] = h.magic[2];
    buf[4..6].copy_from_slice(&h.version_major.to_le_bytes());
    buf[6..8].copy_from_slice(&h.version_minor.to_le_bytes());
    buf[8..12].copy_from_slice(&h.block_size.to_le_bytes());
    buf[12..16].copy_from_slice(&h.block_count.to_le_bytes());
    buf[16..20].copy_from_slice(&h.replica_id.to_le_bytes());
    buf[20..24].copy_from_slice(&h.format_seq.to_le_bytes());
    buf[24..28].copy_from_slice(&h.flags.to_le_bytes());
}

/// Deserializes the storage header and verifies the block's trailing CRC32.
/// Returns `true` only when the CRC is valid.
pub fn unmarshal_storage_header(buf: &[u8], h: &mut StorageHeader) -> bool {
    h.block_type = buf[0];
    h.magic = [buf[1], buf[2], buf[3]];
    h.version_major = u16::from_le_bytes([buf[4], buf[5]]);
    h.version_minor = u16::from_le_bytes([buf[6], buf[7]]);
    h.block_size = u32::from_le_bytes([buf[8], buf[9], buf[10], buf[11]]);
    h.block_count = u32::from_le_bytes([buf[12], buf[13], buf[14], buf[15]]);
    h.replica_id = u32::from_le_bytes([buf[16], buf[17], buf[18], buf[19]]);
    h.format_seq = u32::from_le_bytes([buf[20], buf[21], buf[22], buf[23]]);
    h.flags = u32::from_le_bytes([buf[24], buf[25], buf[26], buf[27]]);
    verify_block_crc(buf)
}

// ── RecordDescriptorHeader ─────────────────────────────────────────────────

/// Fixed 32-byte header of a record descriptor block.
/// Key bytes follow at offset 32; value payload starts at 32 + key_size.
/// The block's CRC32 is stored separately in the last 4 bytes of the block.
#[derive(Debug, Clone, Default)]
pub struct RecordDescriptorHeader {
    pub block_type: u8,
    pub header_size: u8,
    pub key_size: u16,
    pub generation: u32,
    pub total_size: u32,
    pub first_payload_size: u32,
    pub chunk_count: u32,
    pub next_chunk: u32,
    pub flags: u32,      // internal flags, reserved — always 0
    pub user_flags: u32, // user-defined flags; preserved verbatim
}

pub fn marshal_descriptor_header(h: &RecordDescriptorHeader, buf: &mut [u8]) {
    buf[0] = h.block_type;
    buf[1] = h.header_size;
    buf[2..4].copy_from_slice(&h.key_size.to_le_bytes());
    buf[4..8].copy_from_slice(&h.generation.to_le_bytes());
    buf[8..12].copy_from_slice(&h.total_size.to_le_bytes());
    buf[12..16].copy_from_slice(&h.first_payload_size.to_le_bytes());
    buf[16..20].copy_from_slice(&h.chunk_count.to_le_bytes());
    buf[20..24].copy_from_slice(&h.next_chunk.to_le_bytes());
    buf[24..28].copy_from_slice(&h.flags.to_le_bytes());
    buf[28..32].copy_from_slice(&h.user_flags.to_le_bytes());
}

/// Deserializes the descriptor header and verifies the block's trailing CRC32.
/// Returns `true` only when the CRC is valid.
pub fn unmarshal_descriptor_header(buf: &[u8], h: &mut RecordDescriptorHeader) -> bool {
    h.block_type = buf[0];
    h.header_size = buf[1];
    h.key_size = u16::from_le_bytes([buf[2], buf[3]]);
    h.generation = u32::from_le_bytes([buf[4], buf[5], buf[6], buf[7]]);
    h.total_size = u32::from_le_bytes([buf[8], buf[9], buf[10], buf[11]]);
    h.first_payload_size = u32::from_le_bytes([buf[12], buf[13], buf[14], buf[15]]);
    h.chunk_count = u32::from_le_bytes([buf[16], buf[17], buf[18], buf[19]]);
    h.next_chunk = u32::from_le_bytes([buf[20], buf[21], buf[22], buf[23]]);
    h.flags = u32::from_le_bytes([buf[24], buf[25], buf[26], buf[27]]);
    h.user_flags = u32::from_le_bytes([buf[28], buf[29], buf[30], buf[31]]);
    verify_block_crc(buf)
}

// ── ValueChunkHeader ───────────────────────────────────────────────────────

/// Fixed 20-byte header of a value chunk block.
/// The block's CRC32 is stored separately in the last 4 bytes of the block.
#[derive(Debug, Clone, Default)]
pub struct ValueChunkHeader {
    pub block_type: u8,
    pub header_size: u8,
    pub flags: u16,
    pub owner_descriptor: u32,
    pub chunk_index: u32,
    pub payload_size: u32,
    pub next_chunk: u32,
}

pub fn marshal_chunk_header(h: &ValueChunkHeader, buf: &mut [u8]) {
    buf[0] = h.block_type;
    buf[1] = h.header_size;
    buf[2..4].copy_from_slice(&h.flags.to_le_bytes());
    buf[4..8].copy_from_slice(&h.owner_descriptor.to_le_bytes());
    buf[8..12].copy_from_slice(&h.chunk_index.to_le_bytes());
    buf[12..16].copy_from_slice(&h.payload_size.to_le_bytes());
    buf[16..20].copy_from_slice(&h.next_chunk.to_le_bytes());
}

/// Deserializes the chunk header and verifies the block's trailing CRC32.
/// Returns `true` only when the CRC is valid.
pub fn unmarshal_chunk_header(buf: &[u8], h: &mut ValueChunkHeader) -> bool {
    h.block_type = buf[0];
    h.header_size = buf[1];
    h.flags = u16::from_le_bytes([buf[2], buf[3]]);
    h.owner_descriptor = u32::from_le_bytes([buf[4], buf[5], buf[6], buf[7]]);
    h.chunk_index = u32::from_le_bytes([buf[8], buf[9], buf[10], buf[11]]);
    h.payload_size = u32::from_le_bytes([buf[12], buf[13], buf[14], buf[15]]);
    h.next_chunk = u32::from_le_bytes([buf[16], buf[17], buf[18], buf[19]]);
    verify_block_crc(buf)
}

// ── Block classification ───────────────────────────────────────────────────

#[derive(Debug, PartialEq, Eq)]
pub enum BlockClass {
    Free,
    Valid,
    Garbage,
}

pub fn classify_block(buf: &[u8]) -> BlockClass {
    if buf.is_empty() {
        return BlockClass::Free;
    }
    let len = buf.len() as u32;
    match buf[0] {
        BLOCK_TYPE_STORAGE_HEADER if len >= STORAGE_HEADER_SIZE + BLOCK_CRC_SIZE => {
            let mut h = StorageHeader::default();
            if unmarshal_storage_header(buf, &mut h) {
                return BlockClass::Valid;
            }
        }
        BLOCK_TYPE_RECORD_DESCRIPTOR if len >= RECORD_DESCRIPTOR_HEADER_SIZE + BLOCK_CRC_SIZE => {
            let mut h = RecordDescriptorHeader::default();
            if unmarshal_descriptor_header(buf, &mut h) {
                return BlockClass::Valid;
            }
        }
        BLOCK_TYPE_VALUE_CHUNK if len >= VALUE_CHUNK_HEADER_SIZE + BLOCK_CRC_SIZE => {
            let mut h = ValueChunkHeader::default();
            if unmarshal_chunk_header(buf, &mut h) {
                return BlockClass::Valid;
            }
        }
        _ => {}
    }
    if buf[0] == 0x00 || buf[0] == 0xFF {
        BlockClass::Free
    } else {
        BlockClass::Garbage
    }
}

pub fn is_free_candidate(buf: &[u8]) -> bool {
    classify_block(buf) == BlockClass::Free
}

/// Value bytes that fit in a descriptor block after the header, the key, and
/// the trailing block CRC.
pub fn first_payload_capacity(block_size: u32, key_size: u32) -> u32 {
    block_size - RECORD_DESCRIPTOR_HEADER_SIZE - key_size - BLOCK_CRC_SIZE
}

/// Value bytes that fit in a value chunk block after the header and the
/// trailing block CRC.
pub fn chunk_payload_capacity(block_size: u32) -> u32 {
    block_size - VALUE_CHUNK_HEADER_SIZE - BLOCK_CRC_SIZE
}

/// Returns a block-sized buffer filled with `pattern` (0x00 or 0xFF).
pub fn make_free_buf(block_size: u32, pattern: u8) -> Vec<u8> {
    vec![pattern; block_size as usize]
}
