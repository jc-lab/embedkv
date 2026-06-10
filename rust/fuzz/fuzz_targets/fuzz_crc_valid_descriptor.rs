//! fuzz_crc_valid_descriptor — descriptor with a valid CRC but arbitrary field values.
//!
//! This is the highest-risk path: after the CRC gate passes, extreme values in
//! fields such as TotalSize=MAX or ChunkCount=MAX must not cause OOM or panic.
//!
//!   cargo +nightly fuzz run fuzz_crc_valid_descriptor -- -max_total_time=30
#![no_main]

use arbitrary::Arbitrary;
use embedkv::{
    crc::compute_block_crc32,
    format::{BLOCK_TYPE_RECORD_DESCRIPTOR, RECORD_DESCRIPTOR_HEADER_SIZE},
    open, BlockDevice, MemDevice, Options,
};
use libfuzzer_sys::fuzz_target;

const BLOCK_SIZE: u32 = 128;
const BLOCK_COUNT: u32 = 16;

#[derive(Arbitrary, Debug)]
struct DescriptorFields {
    key_size: u16,
    generation: u32,
    total_size: u32,
    first_payload_size: u32,
    chunk_count: u32,
    next_chunk: u32,
    flags: u32,
}

/// Construct a block with the given descriptor fields and a valid CRC at offset 28.
/// header_size is always set to the correct value (32) so validation passes that gate.
fn build_crc_valid_descriptor(fields: &DescriptorFields) -> Vec<u8> {
    let mut buf = vec![0u8; BLOCK_SIZE as usize];
    buf[0] = BLOCK_TYPE_RECORD_DESCRIPTOR;
    buf[1] = RECORD_DESCRIPTOR_HEADER_SIZE as u8; // always 32 — passes header_size check
    buf[2..4].copy_from_slice(&fields.key_size.to_le_bytes());
    buf[4..8].copy_from_slice(&fields.generation.to_le_bytes());
    buf[8..12].copy_from_slice(&fields.total_size.to_le_bytes());
    buf[12..16].copy_from_slice(&fields.first_payload_size.to_le_bytes());
    buf[16..20].copy_from_slice(&fields.chunk_count.to_le_bytes());
    buf[20..24].copy_from_slice(&fields.next_chunk.to_le_bytes());
    buf[24..28].copy_from_slice(&fields.flags.to_le_bytes());
    // Patch CRC (field at offset 28, length 4)
    let crc = compute_block_crc32(&buf, 28);
    buf[28..32].copy_from_slice(&crc.to_le_bytes());
    buf
}

fuzz_target!(|fields: DescriptorFields| {
    let desc_buf = build_crc_valid_descriptor(&fields);

    let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
    if embedkv::format(&mut dev, &Options::default()).is_err() {
        return;
    }
    // Fill every data block with the same fuzzed descriptor so that any
    // next_chunk pointer that falls in range encounters defined data.
    for i in 1..BLOCK_COUNT {
        let _ = dev.write_block(i, &desc_buf);
    }

    // Direct record verification — the critical OOM/panic path.
    let _ = embedkv::record::verify_and_read_record(&mut dev, 1);

    // High-level pipeline.
    if let Ok(mut s) = open(dev, Options::default()) {
        let _ = s.build_index();
        let _ = s.get(b"key");
    }
});
