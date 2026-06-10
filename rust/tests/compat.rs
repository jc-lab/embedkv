use embedkv::{open, MemDevice, Options};
use std::path::PathBuf;

fn testdata_path(name: &str) -> PathBuf {
    let manifest = std::env::var("CARGO_MANIFEST_DIR").unwrap();
    PathBuf::from(manifest)
        .join("..")
        .join("testdata")
        .join(name)
}

fn load_fixture(name: &str) -> MemDevice {
    let path = testdata_path(name);
    let data = std::fs::read(&path).expect(&format!("cannot read fixture {:?}", path));
    MemDevice::from_bytes(256, data).expect("MemDevice::from_bytes")
}

// ── small_value.bin ────────────────────────────────────────────────────────

#[test]
fn small_value_get() {
    let dev = load_fixture("small_value.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");
    let val = store.get(b"hello").expect("get hello");
    assert_eq!(val, b"world");
}

#[test]
fn small_value_recover_get() {
    let dev = load_fixture("small_value.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.recover().expect("recover");
    store.build_index().expect("build_index");
    let val = store.get(b"hello").expect("get hello");
    assert_eq!(val, b"world");
}

// ── large_value.bin ────────────────────────────────────────────────────────

#[test]
fn large_value_get() {
    let dev = load_fixture("large_value.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");
    let val = store.get(b"bigkey").expect("get bigkey");
    let expected: Vec<u8> = (0u16..500).map(|i| (i % 256) as u8).collect();
    assert_eq!(val, expected);
}

#[test]
fn large_value_recover_get() {
    let dev = load_fixture("large_value.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.recover().expect("recover");
    store.build_index().expect("build_index");
    let val = store.get(b"bigkey").expect("get bigkey");
    let expected: Vec<u8> = (0u16..500).map(|i| (i % 256) as u8).collect();
    assert_eq!(val, expected);
}

// ── multi_key.bin ─────────────────────────────────────────────────────────

#[test]
fn multi_key_get() {
    let dev = load_fixture("multi_key.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");
    // All three keys must be readable; the fixture defines alpha/beta/gamma.
    let _alpha = store.get(b"alpha").expect("get alpha");
    let _beta = store.get(b"beta").expect("get beta");
    let _gamma = store.get(b"gamma").expect("get gamma");
}

#[test]
fn multi_key_recover_get() {
    let dev = load_fixture("multi_key.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.recover().expect("recover");
    store.build_index().expect("build_index");
    let _alpha = store.get(b"alpha").expect("get alpha");
    let _beta = store.get(b"beta").expect("get beta");
    let _gamma = store.get(b"gamma").expect("get gamma");
}

// ── recovery/partial_write.bin ─────────────────────────────────────────────

#[test]
fn partial_write_after_recover() {
    let dev = load_fixture("recovery/partial_write.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.recover().expect("recover");
    store.build_index().expect("build_index");
    let val = store.get(b"power").expect("get power after recover");
    assert_eq!(val, b"gen1-data");
}

// ── recovery/partial_erase.bin ─────────────────────────────────────────────

#[test]
fn partial_erase_get() {
    let dev = load_fixture("recovery/partial_erase.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");
    let val = store.get(b"power").expect("get power");
    assert_eq!(val, b"gen2-data");
}

#[test]
fn partial_erase_recover_get() {
    let dev = load_fixture("recovery/partial_erase.bin");
    let mut store = open(dev, Options::default()).expect("open");
    store.recover().expect("recover");
    store.build_index().expect("build_index");
    let val = store.get(b"power").expect("get power after recover");
    assert_eq!(val, b"gen2-data");
}

// ── round-trip: format → open → put → get ─────────────────────────────────

#[test]
fn round_trip_mem() {
    use embedkv::format;

    let mut dev = MemDevice::new(256, 64);
    format(&mut dev, &Options::default()).expect("format");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");

    store.put(b"key1", b"value1").expect("put key1");
    store.put(b"key2", b"value2").expect("put key2");

    assert_eq!(store.get(b"key1").expect("get key1"), b"value1");
    assert_eq!(store.get(b"key2").expect("get key2"), b"value2");
}

#[test]
fn round_trip_update() {
    use embedkv::format;

    let mut dev = MemDevice::new(256, 64);
    format(&mut dev, &Options::default()).expect("format");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");

    store.put(b"k", b"v1").expect("put v1");
    store.put(b"k", b"v2").expect("put v2");
    assert_eq!(store.get(b"k").expect("get k"), b"v2");
}

#[test]
fn round_trip_delete() {
    use embedkv::format;

    let mut dev = MemDevice::new(256, 64);
    format(&mut dev, &Options::default()).expect("format");
    let mut store = open(dev, Options::default()).expect("open");
    store.build_index().expect("build_index");

    store.put(b"k", b"v").expect("put");
    store.delete(b"k").expect("delete");
    assert!(store.get(b"k").is_err());
}
