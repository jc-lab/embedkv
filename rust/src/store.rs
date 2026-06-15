use std::collections::{HashMap, HashSet};

use crate::block::{
    chunk_payload_capacity, first_payload_capacity, is_free_candidate, make_free_buf,
    marshal_chunk_header, marshal_descriptor_header, marshal_storage_header,
    unmarshal_chunk_header, unmarshal_descriptor_header, unmarshal_storage_header,
    RecordDescriptorHeader, StorageHeader, ValueChunkHeader,
};
use crate::crc::write_block_crc;
use crate::device::BlockDevice;
use crate::error::{Error, Result};
use crate::format::*;
use crate::index::{IndexEntry, MemIndex};
use crate::record::{needed_blocks, verify_and_read_record};

/// Options configuring storage behavior.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// Fill byte when erasing blocks.
    /// Use 0x00 for zero-filled / HDD storage, 0xFF for erased NAND flash.
    pub free_pattern: u8,
    /// Replica identifier written to the first replica's header. Replica i
    /// receives `replica_id + i` (see [`format`]).
    pub replica_id: u32,
    /// Storage format generation counter.
    pub format_seq: u32,
}

impl Options {
    pub fn new() -> Self {
        Self::default()
    }
}

// ── format ─────────────────────────────────────────────────────────────────

/// Write a fresh storage header to block 0 of every replica device.
/// Replica `i` is stamped with `replica_id = opts.replica_id + i`.
/// Call once on freshly created devices before opening them.
pub fn format<D: BlockDevice>(devs: &mut [D], opts: &Options) -> Result<()> {
    if devs.is_empty() {
        return Err(Error::NoReplicas);
    }
    for (i, dev) in devs.iter_mut().enumerate() {
        let block_size = dev.block_size();
        if block_size < STORAGE_HEADER_SIZE + BLOCK_CRC_SIZE {
            return Err(Error::InvalidBlockSize);
        }
        let mut buf = vec![0u8; block_size as usize];
        let hdr = StorageHeader {
            block_type: BLOCK_TYPE_STORAGE_HEADER,
            magic: MAGIC,
            version_major: VERSION_MAJOR,
            version_minor: VERSION_MINOR,
            block_size,
            block_count: dev.block_count(),
            replica_id: opts.replica_id + i as u32,
            format_seq: opts.format_seq,
            flags: 0,
        };
        marshal_storage_header(&hdr, &mut buf);
        write_block_crc(&mut buf);
        dev.write_block(0, &buf)?;
        dev.flush()?;
    }
    Ok(())
}

// ── Replica ──────────────────────────────────────────────────────────────────

/// Per-device state of one storage replica.
struct Replica<D: BlockDevice> {
    dev: D,
    header: StorageHeader,
    index: MemIndex,
}

// ── Store ──────────────────────────────────────────────────────────────────

/// Main handle for an open embedkv storage backed by one or more identical
/// replica devices (§20). After `open`, call `recover()` and/or `build_index()`
/// before reads/writes.
///
/// Writes fan out to every replica (each flushed independently); reads return the
/// value from the replica holding the highest-generation complete record.
pub struct Store<D: BlockDevice> {
    replicas: Vec<Replica<D>>,
    opts: Options,
}

/// Open: validate every replica's storage header and return a Store with empty
/// indexes. All replicas must share the same block_size and block_count.
pub fn open<D: BlockDevice>(devs: Vec<D>, opts: Options) -> Result<Store<D>> {
    if devs.is_empty() {
        return Err(Error::NoReplicas);
    }
    let mut replicas: Vec<Replica<D>> = Vec::with_capacity(devs.len());
    let mut block_size = 0u32;
    let mut block_count = 0u32;
    for (i, mut dev) in devs.into_iter().enumerate() {
        let header = open_replica_header(&mut dev)?;
        if i == 0 {
            block_size = dev.block_size();
            block_count = dev.block_count();
        } else if dev.block_size() != block_size || dev.block_count() != block_count {
            return Err(Error::ReplicaMismatch);
        }
        replicas.push(Replica {
            dev,
            header,
            index: MemIndex::new(),
        });
    }
    Ok(Store { replicas, opts })
}

/// Read and validate a single replica's storage header.
fn open_replica_header<D: BlockDevice>(dev: &mut D) -> Result<StorageHeader> {
    let block_size = dev.block_size();
    if block_size < STORAGE_HEADER_SIZE + BLOCK_CRC_SIZE {
        return Err(Error::InvalidBlockSize);
    }
    let mut buf = vec![0u8; block_size as usize];
    dev.read_block(0, &mut buf)?;

    if buf[0] != BLOCK_TYPE_STORAGE_HEADER {
        return Err(Error::InvalidHeader);
    }
    let mut hdr = StorageHeader::default();
    if !unmarshal_storage_header(&buf, &mut hdr) {
        return Err(Error::CrcMismatch);
    }
    if hdr.magic != MAGIC {
        return Err(Error::InvalidHeader);
    }
    if hdr.block_size != block_size {
        return Err(Error::InvalidHeader);
    }
    if hdr.block_count != dev.block_count() {
        return Err(Error::InvalidHeader);
    }
    if hdr.block_count < 1 {
        return Err(Error::InvalidHeader);
    }
    Ok(hdr)
}

impl<D: BlockDevice> Store<D> {
    /// Scan all data blocks of every replica and populate each replica's index.
    /// Call after `open` (and optionally after `recover`).
    pub fn build_index(&mut self) -> Result<()> {
        for r in &mut self.replicas {
            r.build_index()?;
        }
        Ok(())
    }

    /// Perform a full scan of every replica independently, removing garbage
    /// blocks and flushing affected replicas. Does NOT build the index.
    pub fn recover(&mut self) -> Result<()> {
        let free_pattern = self.opts.free_pattern;
        for r in &mut self.replicas {
            r.recover(free_pattern)?;
        }
        Ok(())
    }

    /// Return the value associated with `key`. Across replicas, the highest
    /// complete generation is returned. Returns `KeyNotFound` if absent everywhere.
    pub fn get(&mut self, key: &[u8]) -> Result<Vec<u8>> {
        let mut best: Option<Vec<u8>> = None;
        let mut best_gen: u32 = 0;
        for r in &mut self.replicas {
            match r.get_with_generation(key) {
                Some(Ok((gen, val))) => {
                    if best.is_none() || gen > best_gen {
                        best = Some(val);
                        best_gen = gen;
                    }
                }
                _ => continue,
            }
        }
        best.ok_or(Error::KeyNotFound)
    }

    /// Write key→value with user_flags=0 to every replica.
    pub fn put(&mut self, key: &[u8], value: &[u8]) -> Result<()> {
        self.put_flagged(key, value, 0)
    }

    /// Write key→value with explicit user_flags to every replica. Each replica
    /// writes and flushes the new record before erasing the old one (§13). The
    /// same generation (max existing across replicas + 1) is written everywhere.
    pub fn put_flagged(&mut self, key: &[u8], value: &[u8], user_flags: u32) -> Result<()> {
        let block_size = self.replicas[0].dev.block_size();
        if key.len() as u32 > block_size - RECORD_DESCRIPTOR_HEADER_SIZE - BLOCK_CRC_SIZE {
            return Err(Error::KeyTooLong);
        }

        let key_str = String::from_utf8_lossy(key).into_owned();
        let mut generation = 0u32;
        for r in &self.replicas {
            if let Some(e) = r.index.get(&key_str) {
                if e.generation > generation {
                    generation = e.generation;
                }
            }
        }
        generation += 1;

        let free_pattern = self.opts.free_pattern;
        for r in &mut self.replicas {
            r.put(key, value, generation, user_flags, free_pattern)?;
        }
        Ok(())
    }

    /// Remove the record for `key` from every replica. Returns `KeyNotFound`
    /// only when the key is absent from all replicas.
    pub fn delete(&mut self, key: &[u8]) -> Result<()> {
        let key_str = String::from_utf8_lossy(key).into_owned();
        let free_pattern = self.opts.free_pattern;
        let mut found = false;
        for r in &mut self.replicas {
            let descriptor_block = match r.index.get(&key_str) {
                Some(e) => e.descriptor_block,
                None => continue,
            };
            found = true;
            r.free_record(descriptor_block, free_pattern)?;
            r.dev.flush()?;
            r.index.delete(&key_str);
        }
        if !found {
            return Err(Error::KeyNotFound);
        }
        Ok(())
    }

    /// Close releases all replica devices.
    pub fn close(self) {
        for r in self.replicas {
            let _ = r.dev.close();
        }
    }

    /// Consume the store and return its replica devices in order. Useful for
    /// reclaiming ownership (e.g. to reopen after external mutation).
    pub fn into_devices(self) -> Vec<D> {
        self.replicas.into_iter().map(|r| r.dev).collect()
    }

    /// Return the first replica's storage header.
    pub fn header(&self) -> &StorageHeader {
        &self.replicas[0].header
    }

    /// Number of replica devices backing this store.
    pub fn replica_count(&self) -> usize {
        self.replicas.len()
    }

    /// Return the union of keys present in any replica's index.
    /// Order is not guaranteed.
    pub fn keys(&self) -> Vec<Vec<u8>> {
        let mut set: HashSet<&str> = HashSet::new();
        for r in &self.replicas {
            for e in r.index.all_entries() {
                set.insert(e.key.as_str());
            }
        }
        set.into_iter().map(|k| k.as_bytes().to_vec()).collect()
    }

    /// Call `f` for every key present in any replica's index.
    /// If `f` returns an error, iteration stops immediately.
    pub fn iterate<F>(&self, mut f: F) -> Result<()>
    where
        F: FnMut(&[u8]) -> Result<()>,
    {
        let mut set: HashSet<&str> = HashSet::new();
        for r in &self.replicas {
            for e in r.index.all_entries() {
                set.insert(e.key.as_str());
            }
        }
        for k in set {
            f(k.as_bytes())?;
        }
        Ok(())
    }
}

// ── per-replica operations ───────────────────────────────────────────────────

impl<D: BlockDevice> Replica<D> {
    fn build_index(&mut self) -> Result<()> {
        self.index = MemIndex::new();
        let block_count = self.dev.block_count();
        let block_size = self.dev.block_size();
        let mut buf = vec![0u8; block_size as usize];

        for i in 1..block_count {
            self.dev.read_block(i, &mut buf)?;
            if buf[0] != BLOCK_TYPE_RECORD_DESCRIPTOR {
                continue;
            }
            if let Some(rec) = verify_and_read_record(&mut self.dev, i)? {
                let key_str = String::from_utf8_lossy(&rec.key).into_owned();
                let should_update = match self.index.get(&key_str) {
                    None => true,
                    Some(ex) => rec.generation > ex.generation,
                };
                if should_update {
                    self.index.put(IndexEntry {
                        key: key_str,
                        generation: rec.generation,
                        descriptor_block: i,
                        total_size: rec.header.total_size,
                    });
                }
            }
        }
        Ok(())
    }

    fn recover(&mut self, free_pattern: u8) -> Result<()> {
        let block_size = self.dev.block_size();
        let block_count = self.dev.block_count();
        let mut buf = vec![0u8; block_size as usize];

        // Collect best complete record per key.
        let mut best: HashMap<String, crate::record::CompleteRecord> = HashMap::new();
        for i in 1..block_count {
            self.dev.read_block(i, &mut buf)?;
            if buf[0] != BLOCK_TYPE_RECORD_DESCRIPTOR {
                continue;
            }
            if let Some(rec) = verify_and_read_record(&mut self.dev, i)? {
                let key_str = String::from_utf8_lossy(&rec.key).into_owned();
                let should_update = match best.get(&key_str) {
                    None => true,
                    Some(ex) => rec.generation > ex.generation,
                };
                if should_update {
                    best.insert(key_str, rec);
                }
            }
        }

        // Mark valid blocks.
        let mut valid: HashSet<u32> = HashSet::new();
        valid.insert(0);
        for rec in best.values() {
            valid.insert(rec.descriptor_block);
            for &cb in &rec.chunk_blocks {
                valid.insert(cb);
            }
        }

        // Erase garbage blocks.
        let free = make_free_buf(block_size, free_pattern);
        let mut garbage_found = false;
        for i in 1..block_count {
            if valid.contains(&i) {
                continue;
            }
            self.dev.read_block(i, &mut buf)?;
            if buf[0] == 0x00 || buf[0] == 0xFF {
                continue; // already free
            }
            self.dev.write_block(i, &free)?;
            garbage_found = true;
        }
        if garbage_found {
            self.dev.flush()?;
        }
        Ok(())
    }

    /// Return `Some(Ok((generation, value)))` if the key is in the index,
    /// `None` if absent, or `Some(Err(...))` on I/O or corruption errors.
    fn get_with_generation(&mut self, key: &[u8]) -> Option<Result<(u32, Vec<u8>)>> {
        let key_str = String::from_utf8_lossy(key).into_owned();
        let (generation, descriptor_block) = match self.index.get(&key_str) {
            Some(e) => (e.generation, e.descriptor_block),
            None => return None,
        };
        let result =
            verify_and_read_record(&mut self.dev, descriptor_block).and_then(|opt| match opt {
                Some(rec) => Ok((generation, rec.value)),
                None => Err(Error::CorruptRecord),
            });
        Some(result)
    }

    fn put(
        &mut self,
        key: &[u8],
        value: &[u8],
        generation: u32,
        user_flags: u32,
        free_pattern: u8,
    ) -> Result<()> {
        let key_str = String::from_utf8_lossy(key).into_owned();
        let existing_block: Option<u32> = self.index.get(&key_str).map(|e| e.descriptor_block);

        let needed = needed_blocks(key, value, self.dev.block_size());
        let free_blocks = self.find_free_blocks(needed)?;

        let descriptor_block = free_blocks[0];
        self.write_record(
            descriptor_block,
            &free_blocks[1..],
            key,
            generation,
            value,
            user_flags,
        )?;
        self.dev.flush()?;

        self.index.put(IndexEntry {
            key: key_str,
            generation,
            descriptor_block,
            total_size: value.len() as u32,
        });

        if let Some(old_block) = existing_block {
            self.free_record(old_block, free_pattern)?;
            self.dev.flush()?;
        }
        Ok(())
    }

    fn find_free_blocks(&mut self, n: u32) -> Result<Vec<u32>> {
        let block_size = self.dev.block_size();
        let block_count = self.dev.block_count();
        let mut buf = vec![0u8; block_size as usize];
        let mut result: Vec<u32> = Vec::with_capacity(n as usize);

        for i in 1..block_count {
            if result.len() >= n as usize {
                break;
            }
            self.dev.read_block(i, &mut buf)?;
            if is_free_candidate(&buf) {
                result.push(i);
            }
        }
        if result.len() < n as usize {
            return Err(Error::StorageFull);
        }
        Ok(result)
    }

    fn write_record(
        &mut self,
        descriptor_block: u32,
        chunk_blocks: &[u32],
        key: &[u8],
        generation: u32,
        value: &[u8],
        user_flags: u32,
    ) -> Result<()> {
        let block_size = self.dev.block_size();
        let key_len = key.len() as u32;
        let first_cap = first_payload_capacity(block_size, key_len);
        let chunk_cap = chunk_payload_capacity(block_size);
        let vlen = value.len() as u32;

        let first_size = vlen.min(first_cap);
        let chunk_count = 1u32 + chunk_blocks.len() as u32;
        let next_chunk = if chunk_blocks.is_empty() {
            NULL_BLOCK_INDEX
        } else {
            chunk_blocks[0]
        };

        // Descriptor block (zero-padded); CRC stored in the last 4 bytes.
        let mut buf = vec![0u8; block_size as usize];
        let hdr = RecordDescriptorHeader {
            block_type: BLOCK_TYPE_RECORD_DESCRIPTOR,
            header_size: RECORD_DESCRIPTOR_HEADER_SIZE as u8,
            key_size: key_len as u16,
            generation,
            total_size: vlen,
            first_payload_size: first_size,
            chunk_count,
            next_chunk,
            flags: 0,
            user_flags,
        };
        marshal_descriptor_header(&hdr, &mut buf);
        let key_off = RECORD_DESCRIPTOR_HEADER_SIZE as usize;
        buf[key_off..key_off + key.len()].copy_from_slice(key);
        let payload_off = key_off + key.len();
        buf[payload_off..payload_off + first_size as usize]
            .copy_from_slice(&value[..first_size as usize]);
        write_block_crc(&mut buf);
        self.dev.write_block(descriptor_block, &buf)?;

        // Value chunk blocks
        let mut remaining = &value[first_size as usize..];
        for (ci, &block_idx) in chunk_blocks.iter().enumerate() {
            let payload_size = (remaining.len() as u32).min(chunk_cap);
            let next_idx = if ci + 1 < chunk_blocks.len() {
                chunk_blocks[ci + 1]
            } else {
                NULL_BLOCK_INDEX
            };

            let mut cbuf = vec![0u8; block_size as usize];
            let ch = ValueChunkHeader {
                block_type: BLOCK_TYPE_VALUE_CHUNK,
                header_size: VALUE_CHUNK_HEADER_SIZE as u8,
                flags: 0,
                owner_descriptor: descriptor_block,
                chunk_index: (ci + 1) as u32,
                payload_size,
                next_chunk: next_idx,
            };
            marshal_chunk_header(&ch, &mut cbuf);
            let hdr_len = VALUE_CHUNK_HEADER_SIZE as usize;
            cbuf[hdr_len..hdr_len + payload_size as usize]
                .copy_from_slice(&remaining[..payload_size as usize]);
            write_block_crc(&mut cbuf);
            self.dev.write_block(block_idx, &cbuf)?;
            remaining = &remaining[payload_size as usize..];
        }
        Ok(())
    }

    fn free_record(&mut self, descriptor_block: u32, free_pattern: u8) -> Result<()> {
        let block_size = self.dev.block_size();
        let block_count = self.dev.block_count();
        let mut buf = vec![0u8; block_size as usize];

        self.dev.read_block(descriptor_block, &mut buf)?;
        let mut hdr = RecordDescriptorHeader::default();
        if !unmarshal_descriptor_header(&buf, &mut hdr) {
            return Err(Error::CrcMismatch);
        }

        let free = make_free_buf(block_size, free_pattern);
        self.dev.write_block(descriptor_block, &free)?;

        let mut next = hdr.next_chunk;
        let mut visited: HashSet<u32> = HashSet::new();
        let mut cbuf = vec![0u8; block_size as usize];

        loop {
            if next == NULL_BLOCK_INDEX || next >= block_count {
                break;
            }
            if visited.contains(&next) {
                break;
            }
            visited.insert(next);
            self.dev.read_block(next, &mut cbuf)?;
            let mut ch = ValueChunkHeader::default();
            if !unmarshal_chunk_header(&cbuf, &mut ch) {
                break;
            }
            let following = ch.next_chunk;
            self.dev.write_block(next, &free)?;
            next = following;
        }
        Ok(())
    }
}
