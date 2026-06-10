# testdata fixtures

Binary storage fixtures for cross-language compatibility testing between the Go and Rust
implementations of embedkv. Both implementations must be able to read every fixture
that the other writes.

## Regenerating

```
go test -run TestGenerateFixtures ./go/...
```

## Common parameters

All fixtures use **block_size = 256 bytes** and little-endian byte order.

## Files

### small_value.bin (256 × 8 = 2 KiB)

| Block | Content |
|-------|---------|
| 0     | StorageHeader (replica_id=0, format_seq=0) |
| 1     | RecordDescriptor — key `"hello"`, value `"world"` (single-block record) |
| 2–7   | Free (0x00) |

Expected: `Get("hello") == "world"`

---

### large_value.bin (256 × 16 = 4 KiB)

500-byte value split across descriptor + 2 value chunks.

| Block | Content |
|-------|---------|
| 0     | StorageHeader |
| 1     | RecordDescriptor — key `"bigkey"`, first 218 B of value |
| 2     | ValueChunk 1 — next 232 B |
| 3     | ValueChunk 2 — remaining 50 B |
| 4–15  | Free |

Value bytes: `[0x00, 0x01, 0x02, ..., 0xF3]` (byte i = i % 256 for i in 0..500).

Expected: `Get("bigkey") == value[0..500]`

---

### multi_key.bin (256 × 16 = 4 KiB)

Three independent single-block records.

| Block | Content |
|-------|---------|
| 0     | StorageHeader |
| 1     | RecordDescriptor — key `"alpha"`, value `"value-alpha"` |
| 2     | RecordDescriptor — key `"beta"`, value `"value-beta"` |
| 3     | RecordDescriptor — key `"gamma"`, value `"value-gamma"` |
| 4–15  | Free |

---

### recovery/partial_write.bin (256 × 16 = 4 KiB)

Simulates power loss after gen-2 descriptor was written but before its chunk was flushed.
Gen-2 chunk block has an invalid CRC.

| Block | Content |
|-------|---------|
| 0     | StorageHeader |
| 1     | RecordDescriptor — key `"power"`, value `"gen1-data"` (gen 1, complete) |
| 2     | RecordDescriptor — key `"power"`, gen 2, valid CRC, next_chunk=3 |
| 3     | Chunk block — `block_type=0x03` but CRC = 0 (invalid) |
| 4–15  | Free |

After `Recover()` + `BuildIndex()`: `Get("power") == "gen1-data"`. Blocks 2 and 3 become free.

---

### recovery/partial_erase.bin (256 × 16 = 4 KiB)

Simulates power loss during erasure of the old record after gen-2 update completed.
Gen-1 descriptor was erased but an orphan chunk (valid CRC, points to non-existent
descriptor block 99) remained.

| Block | Content |
|-------|---------|
| 0     | StorageHeader |
| 1     | Free (gen-1 descriptor already erased) |
| 2     | RecordDescriptor — key `"power"`, value `"gen2-data"` (gen 2, complete) |
| 3     | Orphan ValueChunk — valid CRC, `owner_descriptor = 99` |
| 4–15  | Free |

After `Recover()` + `BuildIndex()`: `Get("power") == "gen2-data"`. Block 3 becomes free.
