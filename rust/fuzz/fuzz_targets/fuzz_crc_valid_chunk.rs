//! fuzz_crc_valid_chunk — value chunk block with a valid CRC but arbitrary fields.
//!
//! Tests the chunk-parsing path (payload_size, owner_descriptor, chunk_index, etc.)
//! combined with a descriptor that points at this chunk.
//!
//!   cargo +nightly fuzz run fuzz_crc_valid_chunk -- -max_total_time=30
#![no_main]

use arbitrary::Arbitrary;
use embedkv::{
    crc::write_block_crc,
    format::{
        BLOCK_CRC_SIZE, BLOCK_TYPE_RECORD_DESCRIPTOR, BLOCK_TYPE_VALUE_CHUNK,
        RECORD_DESCRIPTOR_HEADER_SIZE, VALUE_CHUNK_HEADER_SIZE,
    },
    open, BlockDevice, MemDevice, Options,
};
use libfuzzer_sys::fuzz_target;

const BLOCK_SIZE: u32 = 128;
const BLOCK_COUNT: u32 = 16;

#[derive(Arbitrary, Debug)]
struct ChunkFields {
    owner_descriptor: u32,
    chunk_index: u32,
    payload_size: u32,
    next_chunk: u32,
    flags: u16,
}

fn build_crc_valid_chunk(fields: &ChunkFields) -> Vec<u8> {
    let mut buf = vec![0u8; BLOCK_SIZE as usize];
    buf[0] = BLOCK_TYPE_VALUE_CHUNK;
    buf[1] = VALUE_CHUNK_HEADER_SIZE as u8; // 20 — correct header_size
    buf[2..4].copy_from_slice(&fields.flags.to_le_bytes());
    buf[4..8].copy_from_slice(&fields.owner_descriptor.to_le_bytes());
    buf[8..12].copy_from_slice(&fields.chunk_index.to_le_bytes());
    buf[12..16].copy_from_slice(&fields.payload_size.to_le_bytes());
    buf[16..20].copy_from_slice(&fields.next_chunk.to_le_bytes());
    // Block CRC lives in the last 4 bytes.
    write_block_crc(&mut buf);
    buf
}

/// Build a descriptor whose next_chunk=2 (points to block 2) so the chunk in
/// block 2 is actually visited during record verification.
fn build_descriptor_pointing_to(block2: u32, first_payload_cap: u32) -> Vec<u8> {
    let mut buf = vec![0u8; BLOCK_SIZE as usize];
    buf[0] = BLOCK_TYPE_RECORD_DESCRIPTOR;
    buf[1] = RECORD_DESCRIPTOR_HEADER_SIZE as u8;
    // key_size = 1 ("k"), so first_payload_cap = BLOCK_SIZE - 32 - 1 - 4
    let key_size: u16 = 1;
    buf[2..4].copy_from_slice(&key_size.to_le_bytes());
    buf[4..8].copy_from_slice(&1u32.to_le_bytes()); // generation
    let total = first_payload_cap + 1; // needs one extra byte in a chunk
    buf[8..12].copy_from_slice(&total.to_le_bytes());
    buf[12..16].copy_from_slice(&first_payload_cap.to_le_bytes());
    buf[16..20].copy_from_slice(&2u32.to_le_bytes()); // chunk_count = 2
    buf[20..24].copy_from_slice(&block2.to_le_bytes()); // next_chunk
    buf[24..28].copy_from_slice(&0u32.to_le_bytes()); // flags
    buf[28..32].copy_from_slice(&0u32.to_le_bytes()); // user_flags
    buf[RECORD_DESCRIPTOR_HEADER_SIZE as usize] = b'k'; // key byte
    // Block CRC lives in the last 4 bytes (written after key bytes).
    write_block_crc(&mut buf);
    buf
}

fuzz_target!(|fields: ChunkFields| {
    let chunk_buf = build_crc_valid_chunk(&fields);
    let first_payload_cap = BLOCK_SIZE - RECORD_DESCRIPTOR_HEADER_SIZE - 1 - BLOCK_CRC_SIZE; // key_size=1
    let desc_buf = build_descriptor_pointing_to(2, first_payload_cap);

    let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
    if embedkv::format(std::slice::from_mut(&mut dev), &Options::default()).is_err() {
        return;
    }
    let _ = dev.write_block(1, &desc_buf);
    let _ = dev.write_block(2, &chunk_buf);
    // Fill remaining blocks with chunk data too (any chain pointer in range)
    for i in 3..BLOCK_COUNT {
        let _ = dev.write_block(i, &chunk_buf);
    }

    let _ = embedkv::record::verify_and_read_record(&mut dev, 1);

    if let Ok(mut s) = open(vec![dev], Options::default()) {
        let _ = s.build_index();
        let _ = s.get(b"k");
    }
});
