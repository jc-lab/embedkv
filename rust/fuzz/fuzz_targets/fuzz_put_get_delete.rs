//! fuzz_put_get_delete — arbitrary key and value through Put / Get / Delete.
//!
//! Invariants:
//!   • Put succeeds or returns KeyTooLong / StorageFull (no other errors)
//!   • After a successful Put, Get must return the identical value
//!   • After Delete, Get must return KeyNotFound
//!
//!   cargo +nightly fuzz run fuzz_put_get_delete -- -max_total_time=30
#![no_main]

use arbitrary::Arbitrary;
use embedkv::{format, open, MemDevice, Options};
use embedkv::Error;
use libfuzzer_sys::fuzz_target;

const BLOCK_SIZE: u32 = 128;
const BLOCK_COUNT: u32 = 64;

#[derive(Arbitrary, Debug)]
struct PutGetInput {
    key: Vec<u8>,
    value: Vec<u8>,
}

fuzz_target!(|input: PutGetInput| {
    let mut dev = MemDevice::new(BLOCK_SIZE, BLOCK_COUNT);
    if format(std::slice::from_mut(&mut dev), &Options::default()).is_err() {
        return;
    }
    let Ok(mut s) = open(vec![dev], Options::default()) else { return };
    if s.build_index().is_err() {
        return;
    }

    // ── First Put ──────────────────────────────────────────────────────────
    match s.put(&input.key, &input.value) {
        Err(Error::KeyTooLong | Error::StorageFull) => return,
        Err(e) => panic!("Put returned unexpected error: {e}"),
        Ok(()) => {}
    }

    // Get must return the same value
    let got = match s.get(&input.key) {
        Ok(v) => v,
        Err(e) => panic!("Get failed after successful Put: {e}"),
    };
    assert_eq!(
        got, input.value,
        "value mismatch after Put (key={:?})",
        input.key
    );

    // ── Update (second Put, same key) ──────────────────────────────────────
    match s.put(&input.key, &input.value) {
        Ok(()) => {
            let got2 = match s.get(&input.key) {
                Ok(v) => v,
                Err(e) => panic!("Get failed after update: {e}"),
            };
            assert_eq!(got2, input.value, "value mismatch after update");
        }
        Err(Error::StorageFull) => {
            // Update needs space for both old and new record simultaneously;
            // running out is acceptable.
        }
        Err(e) => panic!("Update (second Put) returned unexpected error: {e}"),
    }

    // ── Delete ─────────────────────────────────────────────────────────────
    match s.delete(&input.key) {
        Ok(()) => {}
        Err(e) => panic!("Delete after Put failed: {e}"),
    }

    match s.get(&input.key) {
        Err(Error::KeyNotFound) => {}
        Ok(_) => panic!("Get after Delete returned a value"),
        Err(e) => panic!("Get after Delete returned unexpected error: {e}"),
    }
});
