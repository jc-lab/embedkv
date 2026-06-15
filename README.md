# embedkv

A compact, block-based embedded key-value store designed for constrained storage
environments such as microcontrollers, UEFI, and small flash devices.

embedkv is implemented in both **Go** and **Rust** sharing an identical binary format,
so a storage written by one implementation can be read by the other.

---

## Features

- Fixed-size block I/O — every read and write is exactly one block
- Per-block CRC32 integrity (IEEE polynomial), stored in the last 4 bytes of every non-free block
- UTF-8 string keys stored inside each record descriptor
- Copy-on-write updates: new record is fully flushed before the old one is erased
- Power-loss safe: recovery selects the highest-generation complete record
- Persistent free-list-free design — garbage is collected lazily at recovery time
- Storage-level replication: multiple independent replicas, best generation wins
- Minimal metadata overhead per block

---

## Storage format at a glance

```
Storage (block array)
┌─────────────┬─────────────┬─────────────┬─────────────┐
│  Block 0    │  Block 1    │  Block 2    │  Block N    │
│ StorageHdr  │ Descriptor  │ ValueChunk  │  Free/...   │
└─────────────┴─────────────┴─────────────┴─────────────┘
```

| Block type        | First byte | Description                          |
|-------------------|-----------|--------------------------------------|
| Storage header    | `0x01`          | Block 0; holds format metadata |
| Record descriptor | `0x02`          | Key + first value payload      |
| Value chunk       | `0x03`          | Continuation of value data     |
| Free block        | `0x00` / `0xFF` | Available for allocation       |

See [docs/ARCH.md](docs/ARCH.md) for the full binary layout specification.

---

## Getting started

### Go

```go
import "github.com/jc-lab/embedkv/go"

// Create a new storage file (256-byte blocks, 1024 blocks = 256 KiB)
dev, err := embedkv.CreateFileDevice("data.bin", 256, 1024)

// Format / Open take a list of replica devices; a single device is a 1-element slice
devs := []embedkv.BlockDevice{dev}
embedkv.Format(devs, embedkv.DefaultOptions())

// Open and build the in-memory index
s, err := embedkv.Open(devs, embedkv.DefaultOptions())
s.Recover()
s.BuildIndex()

// Read / write / delete
s.Put([]byte("config"), []byte(`{"version":1}`))
val, err := s.Get([]byte("config"))
s.Delete([]byte("config"))

// After an unclean shutdown, run recovery first
s, _ = embedkv.Open(devs, embedkv.DefaultOptions())
s.Recover()     // scan + garbage-collect (each replica independently)
s.BuildIndex()  // rebuild in-memory index
```

**Import path**: `github.com/jc-lab/embedkv/go` (package `embedkv`)

### Rust

```rust
use embedkv::{format, open, MemDevice, Options};

// In-memory devices (useful for testing / embedded RAM buffers).
// Format / open take a list of replica devices; a single device is a 1-element Vec.
let mut devs = vec![MemDevice::new(256, 1024)];
format(&mut devs, &Options::default()).unwrap();

let mut s = open(devs, Options::default()).unwrap();
s.build_index().unwrap();

s.put(b"config", b"{\"version\":1}").unwrap();
let val = s.get(b"config").unwrap();
s.delete(b"config").unwrap();

// Recovery after power loss: reclaim the devices, then reopen
let devs = s.into_devices();
let mut s = open(devs, Options::default()).unwrap();
s.recover().unwrap();
s.build_index().unwrap();
```

**Cargo dependency**:
```toml
embedkv = { git = "https://github.com/jc-lab/embedkv", package = "embedkv" }
```

---

## API reference

### Open sequence

Both implementations follow the same three-step open sequence:

```
Open(devices, options)   →   Recover() [optional]   →   BuildIndex()
```

| Step | Purpose | When to call |
|------|---------|--------------|
| `Open` / `open` | Validate every replica's storage header | Always |
| `Recover` / `recover` | Scan all blocks, erase garbage, flush | After unclean shutdown |
| `BuildIndex` / `build_index` | Populate in-memory key index | Always (after Recover if used) |

### Core operations

| Go | Rust | Description |
|----|------|-------------|
| `Format(devs, opts)` | `format(&mut devs, &opts)` | Initialise new storage on each replica |
| `Open(devs, opts)` | `open(devs, opts)` | Open one or more replica devices |
| `s.Get(key)` | `s.get(key)` | Read value (highest complete generation across replicas) |
| `s.Put(key, value)` | `s.put(key, value)` | Write or update a key on every replica |
| `s.Delete(key)` | `s.delete(key)` | Remove a key from every replica |
| `s.Recover()` | `s.recover()` | Garbage-collect each replica after crash |
| `s.BuildIndex()` | `s.build_index()` | Build in-memory index |

### Devices

| Go | Rust | Description |
|----|------|-------------|
| `NewMemDevice(bs, n)` | `MemDevice::new(bs, n)` | In-memory (testing/RAM) |
| `CreateFileDevice(path, bs, n)` | — | Create new file-backed storage |
| `OpenFileDevice(path, bs)` | `FileDevice::open(path, bs)` | Open existing file |

### Replicas

A store is opened over one or more replica devices. Writes fan out to every
replica; reads return the highest complete generation across replicas, so a read
survives corruption or loss of any single replica.

```go
// Go — three replicas
devs := []embedkv.BlockDevice{dev0, dev1, dev2}
embedkv.Format(devs, opts)
s, _ := embedkv.Open(devs, opts)
s.Recover()      // each replica is recovered independently
s.BuildIndex()
s.Put([]byte("k"), []byte("v"))   // written to all three replicas
val, _ := s.Get([]byte("k"))      // best generation across replicas
```

```rust
// Rust — three replicas
let mut devs = vec![dev0, dev1, dev2];
embedkv::format(&mut devs, &opts).unwrap();
let mut s = embedkv::open(devs, opts).unwrap();
s.recover().unwrap();
s.build_index().unwrap();
s.put(b"k", b"v").unwrap();
let val = s.get(b"k").unwrap();
```

---

## Testing

### Go

```bash
# Unit + scenario + compatibility tests
go test ./go/...

# Fuzz tests (run each for desired duration)
go test ./go/... -fuzz=FuzzCRCValidDescriptor  -fuzztime=60s
go test ./go/... -fuzz=FuzzStoragePipeline     -fuzztime=60s
go test ./go/... -fuzz=FuzzPutGetDelete        -fuzztime=60s
go test ./go/... -fuzz=FuzzRawBlock            -fuzztime=60s
go test ./go/... -fuzz=FuzzCRCValidChunk       -fuzztime=60s
```

### Rust

```bash
# Unit + integration tests (reads testdata/ fixtures)
cargo test

# Fuzz tests (requires nightly + cargo-fuzz)
rustup toolchain install nightly
cargo install cargo-fuzz

cd rust
cargo +nightly fuzz run fuzz_crc_valid_descriptor -- -max_total_time=60
cargo +nightly fuzz run fuzz_raw_block            -- -max_total_time=60
cargo +nightly fuzz run fuzz_crc_valid_chunk      -- -max_total_time=60
cargo +nightly fuzz run fuzz_storage_pipeline     -- -max_total_time=60
cargo +nightly fuzz run fuzz_put_get_delete       -- -max_total_time=60
```

### Regenerate testdata fixtures

```bash
go test -run TestGenerateFixtures ./go/...
```

---

## Cross-language compatibility

Binary fixtures written by Go are read and verified by Rust, and vice versa. The
shared test fixtures live in `testdata/`:

| File | Contents |
|------|----------|
| `testdata/small_value.bin` | Single-block record: `"hello"→"world"` |
| `testdata/large_value.bin` | Multi-chunk record: `"bigkey"→500 bytes` |
| `testdata/multi_key.bin` | Three independent records |
| `testdata/recovery/partial_write.bin` | Power-loss during gen-2 write |
| `testdata/recovery/partial_erase.bin` | Power-loss during gen-1 erasure |

The Go test suite reads these fixtures in `TestReadFixtures`, and the Rust integration
test `tests/compat.rs` reads the same files.

---

## Repository layout

```
embedkv/
├── go.mod                 # Go module root (import path: github.com/jc-lab/embedkv)
├── Cargo.toml             # Rust workspace root
├── go/                    # Go implementation (package embedkv)
│   ├── *.go
│   └── *_test.go
├── rust/                  # Rust implementation (crate embedkv)
│   ├── src/
│   ├── tests/compat.rs
│   └── fuzz/
├── testdata/              # Shared binary fixtures for cross-language tests
│   └── recovery/
└── docs/
    └── ARCH.md            # Binary format specification
```

---

## License

[Apache 2.0 License](LICENSE).
