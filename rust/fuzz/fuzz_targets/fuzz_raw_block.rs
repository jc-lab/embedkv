//! fuzz_raw_block — arbitrary bytes as a descriptor block.
//!
//! Exercises classifyBlock + verifyAndReadRecord + full Open/BuildIndex/Recover
//! pipeline. No panic, no OOM, no out-of-bounds access.
//!
//!   cargo +nightly fuzz run fuzz_raw_block -- -max_total_time=30
#![no_main]

use embedkv::{
    format::{BLOCK_TYPE_RECORD_DESCRIPTOR, BLOCK_TYPE_VALUE_CHUNK},
    open, BlockDevice, MemDevice, Options,
};
use libfuzzer_sys::fuzz_target;

const BLOCK_SIZE: u32 = 128;
const BLOCK_COUNT: u32 = 16;

fuzz_target!(|data: &[u8]| {
    // Pad / trim to exact block size.
    let mut block = vec![0u8; BLOCK_SIZE as usize];
    let n = data.len().min(BLOCK_SIZE as usize);
    block[..n].copy_from_slice(&data[..n]);

    // ── Path 1: raw block in a properly formatted storage ──────────────────
    {
        let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
        if embedkv::format(std::slice::from_mut(&mut dev), &Options::default()).is_err() {
            return;
        }
        // Descriptor block
        let _ = dev.write_block(1, &block);
        // Put the same bytes in all remaining blocks so that any next_chunk /
        // owner_descriptor chain pointer that lands in range sees defined data.
        for i in 2..BLOCK_COUNT {
            let _ = dev.write_block(i, &block);
        }

        // verifyAndReadRecord via the record module
        let _ = embedkv::record::verify_and_read_record(&mut dev, 1);

        // High-level pipeline (no recover)
        if let Ok(mut s) = open(vec![dev], Options::default()) {
            let _ = s.build_index();
            let _ = s.get(b"any-key");
        }
    }

    // ── Path 2: same bytes but with Recover ────────────────────────────────
    {
        let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
        if embedkv::format(std::slice::from_mut(&mut dev), &Options::default()).is_err() {
            return;
        }
        let _ = dev.write_block(1, &block);
        for i in 2..BLOCK_COUNT {
            let _ = dev.write_block(i, &block);
        }

        if let Ok(mut s) = open(vec![dev], Options::default()) {
            let _ = s.recover();
            let _ = s.build_index();
            let _ = s.get(b"any-key");
        }
    }

    // ── Path 3: block has type 0x03 (value chunk) ─────────────────────────
    {
        let mut chunk_block = block.clone();
        chunk_block[0] = BLOCK_TYPE_VALUE_CHUNK;

        let mut desc_block = vec![0u8; BLOCK_SIZE as usize];
        desc_block[0] = BLOCK_TYPE_RECORD_DESCRIPTOR;
        // Copy remaining fuzz bytes after byte 0 (guarded against empty input)
        if n > 1 {
            let end = n.min(BLOCK_SIZE as usize);
            desc_block[1..end].copy_from_slice(&data[1..end]);
        }

        let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
        if embedkv::format(std::slice::from_mut(&mut dev), &Options::default()).is_err() {
            return;
        }
        let _ = dev.write_block(1, &desc_block);
        let _ = dev.write_block(2, &chunk_block);

        let _ = embedkv::record::verify_and_read_record(&mut dev, 1);
    }
});
