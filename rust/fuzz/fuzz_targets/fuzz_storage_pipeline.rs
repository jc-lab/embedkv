//! fuzz_storage_pipeline — arbitrary bytes as the entire storage content.
//!
//! Runs the full Open → Recover → BuildIndex pipeline on arbitrary byte content.
//! All code paths that read blocks must handle any content without panic.
//!
//!   cargo +nightly fuzz run fuzz_storage_pipeline -- -max_total_time=30
#![no_main]

use embedkv::{open, MemDevice, Options};
use libfuzzer_sys::fuzz_target;

// Small block/count to keep allocations bounded during fuzzing.
const BLOCK_SIZE: u32 = 64;
const BLOCK_COUNT: u32 = 8;
const STORAGE_SIZE: usize = (BLOCK_SIZE * BLOCK_COUNT) as usize;

fuzz_target!(|data: &[u8]| {
    // Pad / trim fuzz input to exactly the storage size.
    let mut storage = vec![0u8; STORAGE_SIZE];
    let n = data.len().min(STORAGE_SIZE);
    storage[..n].copy_from_slice(&data[..n]);

    let Ok(dev) = MemDevice::from_bytes(BLOCK_SIZE, storage) else {
        return;
    };

    // Open may fail if the header is invalid — that's fine.
    let Ok(mut s) = open(vec![dev], Options::default()) else {
        return;
    };

    // All three of these must not panic regardless of block content.
    let _ = s.recover();
    let _ = s.build_index();

    let _ = s.get(b"hello");
    let _ = s.get(b"bigkey");
    let _ = s.get(b"power");
});
