# embedkv — Binary Format Specification

This document is the single source of truth for the embedkv on-disk format.
Both the Go and Rust implementations must conform to every detail described here.

---

## 1. Design Goals

- Fixed-size block I/O over a fixed-size storage region
- Minimal per-block metadata
- Minimise write count — a block should reach its final form in one write
- Sequential-write friendly layout
- Per-block CRC32 integrity (IEEE polynomial)
- Copy-on-write updates; no in-place overwrite
- Power-loss safe recovery
- Storage-level replica support

---

## 2. Conventions

### Byte order

All multi-byte integers are **little-endian**.

```
u32 value 0x12345678  →  78 56 34 12
```

### Struct layout

All structs use a **packed** (no padding) layout. Implementations must
serialize and deserialize at the exact offsets listed.

### Block index

Block indices are `u32`. Special values:

| Value | Meaning |
|-------|---------|
| `0` | Storage header block |
| `1 .. block_count-1` | Data block |
| `0xFFFFFFFF` | Null / no next chunk |

### CRC32

IEEE CRC-32 polynomial (reflected: `0xEDB88320`). When computing the CRC of a
block, the four bytes of the `block_crc32` field are treated as `0x00000000`.
The CRC covers the **entire block** (all `block_size` bytes).

Free blocks have no CRC.

---

## 3. Storage Layout

```
Storage
┌──────────┬──────────┬──────────┬──────────┬──────────┐
│ Block 0  │ Block 1  │ Block 2  │  ...     │ Block N  │
│  Header  │  data    │  data    │          │  data    │
└──────────┴──────────┴──────────┴──────────┴──────────┘
```

Block 0 is always the storage header. All other blocks hold one of:

| Block type        | First byte      | Description |
|-------------------|-----------------|-------------|
| Free block        | `0x00` or `0xFF`| Empty, available for allocation |
| Storage header    | `0x01`          | Storage-level metadata (block 0 only) |
| Record descriptor | `0x02`          | Key + first value payload |
| Value chunk       | `0x03`          | Continuation of value data |

`0x00` (zero-filled) and `0xFF` (erased NAND) are both treated as free. This
allows zero-initialised files and erased flash to work without reformatting.

---

## 4. Block Classification

To determine the type of a block:

1. If `buf[0]` is `0x01`, `0x02`, or `0x03` — compute the CRC.
2. If the CRC is valid → the block is a non-free block of that type.
3. Otherwise, if `buf[0]` is `0x00` or `0xFF` → free block.
4. Otherwise → garbage; treat as free during recovery.

---

## 5. Storage Header Block (block 0)

### Layout

```
┌──────────────────────────┬──────────────────────┐
│  StorageHeader (32 B)    │  Padding / Reserved  │
└──────────────────────────┴──────────────────────┘
```

### StorageHeader — 32 bytes, CRC at offset 28

| Offset | Size | Type   | Field           | Notes |
|--------|------|--------|-----------------|-------|
| 0      | 1    | `u8`   | `block_type`    | `0x01` |
| 1      | 3    | `u8[3]`| `magic`         | ASCII `"EKV"` |
| 4      | 2    | `u16`  | `version_major` | Current: `1` |
| 6      | 2    | `u16`  | `version_minor` | Current: `0` |
| 8      | 4    | `u32`  | `block_size`    | Block size in bytes |
| 12     | 4    | `u32`  | `block_count`   | Total blocks in storage |
| 16     | 4    | `u32`  | `replica_id`    | Replica identifier |
| 20     | 4    | `u32`  | `format_seq`    | Format generation counter |
| 24     | 4    | `u32`  | `flags`         | Reserved, set to `0` |
| 28     | 4    | `u32`  | `block_crc32`   | CRC over entire block |

### Validation

A storage header is valid if and only if:

- `block_type == 0x01`
- `magic == "EKV"`
- `block_size` is a supported value
- `block_count >= 1`
- `block_size * block_count == actual storage size`
- CRC32 passes

---

## 6. Record Descriptor Block

A record descriptor represents one key-value record. It contains the key and the
first part of the value. If the value is too large to fit, additional value chunk
blocks are chained via `next_chunk`.

### Block layout

```
┌─────────────────────────────┬──────────────────┬────────────────────┬──────────┐
│  RecordDescriptorHeader     │  Key bytes       │  First value       │ Padding  │
│  (32 bytes)                 │  (key_size bytes)│  payload           │ (zeros)  │
└─────────────────────────────┴──────────────────┴────────────────────┴──────────┘
  offset 0                      offset 32          offset 32+key_size
```

### RecordDescriptorHeader — 32 bytes, CRC at offset 28

| Offset | Size | Type  | Field               | Notes |
|--------|------|-------|---------------------|-------|
| 0      | 1    | `u8`  | `block_type`        | `0x02` |
| 1      | 1    | `u8`  | `header_size`       | `32` |
| 2      | 2    | `u16` | `key_size`          | Key length in bytes (UTF-8) |
| 4      | 4    | `u32` | `generation`        | Monotonically increasing per key |
| 8      | 4    | `u32` | `total_size`        | Total value size in bytes |
| 12     | 4    | `u32` | `first_payload_size`| Value bytes in this block |
| 16     | 4    | `u32` | `chunk_count`       | Total chunk count (descriptor = chunk 0) |
| 20     | 4    | `u32` | `next_chunk`        | Block index of chunk 1, or `0xFFFFFFFF` |
| 24     | 4    | `u32` | `flags`             | Reserved, set to `0` |
| 28     | 4    | `u32` | `block_crc32`       | CRC over entire block |

### Key and payload offsets

```
key_offset             = 32
first_payload_offset   = 32 + key_size
first_payload_capacity = block_size - 32 - key_size
```

Constraints:
- `key_size < block_size - 32` (key must fit alongside the header)
- `first_payload_size <= first_payload_capacity`

### Key format

The key is a UTF-8 string stored verbatim starting at offset `32`. Key equality
is determined by direct byte comparison of the stored key against the requested key.

### Single-block record (value fits in descriptor)

```
total_size == first_payload_size
chunk_count == 1
next_chunk == 0xFFFFFFFF
```

### Multi-block record (value spans additional chunks)

```
total_size > first_payload_size
chunk_count > 1
next_chunk != 0xFFFFFFFF
```

### CRC scope

`block_crc32` covers the full block (`block_size` bytes). Bytes 28–31 are treated
as `0x00000000` during computation. Padding bytes after the value payload must be
deterministic — the recommended value is `0x00`.

---

## 7. Value Chunk Block

Value chunks hold the portions of a value that did not fit in the descriptor.

### Block layout

```
┌───────────────────────┬──────────────────────┬──────────┐
│  ValueChunkHeader     │  Payload             │ Padding  │
│  (24 bytes)           │  (payload_size bytes)│ (zeros)  │
└───────────────────────┴──────────────────────┴──────────┘
  offset 0                offset 24
```

### ValueChunkHeader — 24 bytes, CRC at offset 20

| Offset | Size | Type  | Field              | Notes |
|--------|------|-------|--------------------|-------|
| 0      | 1    | `u8`  | `block_type`       | `0x03` |
| 1      | 1    | `u8`  | `header_size`      | `24` |
| 2      | 2    | `u16` | `flags`            | Reserved, set to `0` |
| 4      | 4    | `u32` | `owner_descriptor` | Block index of the owning descriptor |
| 8      | 4    | `u32` | `chunk_index`      | `1` for the first extra chunk, `2` for the second, … |
| 12     | 4    | `u32` | `payload_size`     | Payload bytes in this block |
| 16     | 4    | `u32` | `next_chunk`       | Block index of next chunk, or `0xFFFFFFFF` |
| 20     | 4    | `u32` | `block_crc32`      | CRC over entire block |

Constraints:
- `payload_size <= block_size - 24`
- `chunk_index >= 1` (the descriptor counts as chunk index 0)

---

## 8. Free Block

A block is free when `buf[0] == 0x00` or `buf[0] == 0xFF`. Free blocks have no
structure — no header, no CRC.

```
Free Block
┌──────────────────────────────────┐
│  0x00...  or  0xFF...            │
└──────────────────────────────────┘
```

When erasing a block, fill the entire block with either `0x00` (for HDD / file
storage) or `0xFF` (for erased NAND flash).

---

## 9. Write Procedures

### New record

1. Compute the number of blocks required.
2. Scan from block 1 upward to find that many free blocks.
3. Write the descriptor block (key + first value payload).
4. Write value chunk blocks in order.
5. Flush.

The descriptor does not need to be written last; implementations may choose any
order that is convenient for sequential writes.

A record is considered **complete** only when all of the following hold:

- Descriptor CRC is valid.
- All value chunk CRCs are valid.
- The chunk chain is unbroken (no missing indices, no cycles).
- `chunk_count` matches the actual number of chunks in the chain.
- Sum of all payload sizes equals `total_size`.

### Update (copy-on-write)

1. Find the existing complete record for the key.
2. Find enough free blocks for the new record.
3. Write the new descriptor and chunks.
4. **Flush** (first flush — new record is now durable).
5. Erase all blocks of the old record.
6. **Flush** (second flush — erasure is now durable).

The old record must remain on storage until after the first flush. This guarantees
that at least one complete record exists at any point in time.

### Delete

1. Find the latest complete record for the key.
2. Erase the descriptor and all its value chunk blocks.
3. Flush.

---

## 10. Recovery

Recovery performs a full storage scan, removes garbage, and rebuilds the usable
record set.

### Procedure

1. Read and validate the storage header.
2. Scan all blocks. Collect every record descriptor with a valid CRC.
3. For each descriptor, read and validate its key and full chunk chain.
4. For each key (by actual key bytes), select the highest-generation **complete**
   record.
5. Mark all blocks belonging to selected records as valid.
6. Erase every block that is neither valid nor already free.
7. Flush (only if any block was erased).

### Power-loss outcomes

| Scenario | Outcome |
|----------|---------|
| Power lost during new record write | Incomplete new record → garbage-collected; previous record (if any) retained |
| Power lost between first and second flush of an update | New record survives (gen N+1); old record blocks may remain until next recovery |
| Power lost during old-record erasure | Old orphan chunks/descriptor garbage-collected; new record (gen N+1) remains |

---

## 11. Incomplete Record Criteria

A record is **incomplete** (and must not be returned to callers) if any of the
following are true:

- Descriptor CRC mismatch
- `block_type != 0x02`
- `header_size != 32`
- `key_size >= block_size - 32`
- `first_payload_size > block_size - 32 - key_size`
- `chunk_count == 0`
- Sum of payload sizes ≠ `total_size`
- Actual chunk count in chain ≠ `chunk_count`
- Any `next_chunk` points outside `[0, block_count)`
- Chunk chain contains a cycle
- Any value chunk CRC mismatch
- Any value chunk `block_type != 0x03`
- Any value chunk `header_size != 24`
- Any value chunk `owner_descriptor` ≠ the descriptor's block index
- Any value chunk `chunk_index` out of expected sequence
- Any value chunk `payload_size > block_size - 24`

---

## 12. In-Memory Index

Implementations may maintain an optional in-memory index to avoid scanning the
full storage on every read:

```
key (UTF-8 string bytes) → latest complete record descriptor
```

Index entry fields:

| Field | Description |
|-------|-------------|
| `key` | Actual key bytes |
| `generation` | Generation of the selected record |
| `descriptor_block` | Block index of the record descriptor |
| `total_size` | Total value size |

The index is ephemeral — it is rebuilt by scanning the storage at open time (via
`BuildIndex`). It is not part of the persistent format.

---

## 13. Replica Support

A replica is a complete, independent copy of the entire storage — not a block-level
mirror.

### Write order (per replica, sequential)

For each replica: write new record → flush → erase old record → flush.

### Recovery

Each replica is recovered independently. Across replicas, for each key the
highest-generation **complete** record wins:

```
Replica 0: key A  gen 5  complete    ← selected
Replica 1: key A  gen 6  incomplete
Replica 2: key A  gen 4  complete
```

A higher generation that is incomplete is ignored; the next-highest complete
generation is used instead.

---

## 14. Format Version

| Field | Value |
|-------|-------|
| `version_major` | `1` |
| `version_minor` | `0` |

Minor version bumps are backward-compatible additions. Major version bumps indicate
breaking changes. Implementations should reject storage headers whose `version_major`
they do not support.
