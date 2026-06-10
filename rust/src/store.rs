use std::collections::{HashMap, HashSet};

use crate::block::{
    is_free_candidate, make_free_buf, marshal_chunk_header, marshal_descriptor_header,
    marshal_storage_header, unmarshal_chunk_header, unmarshal_descriptor_header,
    unmarshal_storage_header, RecordDescriptorHeader, StorageHeader, ValueChunkHeader,
};
use crate::crc::compute_block_crc32;
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
    /// Identifies this storage replica (stored in the header).
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

/// Write a new storage header to block 0.
/// Call once on a freshly created device before opening it.
pub fn format<D: BlockDevice>(dev: &mut D, opts: &Options) -> Result<()> {
    let block_size = dev.block_size();
    if block_size < STORAGE_HEADER_SIZE {
        return Err(Error::InvalidBlockSize);
    }
    let mut buf = vec![0u8; block_size as usize];
    let mut hdr = StorageHeader {
        block_type: BLOCK_TYPE_STORAGE_HEADER,
        magic: MAGIC,
        version_major: VERSION_MAJOR,
        version_minor: VERSION_MINOR,
        block_size,
        block_count: dev.block_count(),
        replica_id: opts.replica_id,
        format_seq: opts.format_seq,
        flags: 0,
        block_crc32: 0,
    };
    marshal_storage_header(&hdr, &mut buf);
    hdr.block_crc32 = compute_block_crc32(&buf, 28);
    marshal_storage_header(&hdr, &mut buf);
    dev.write_block(0, &buf)
}

// ── Store ──────────────────────────────────────────────────────────────────

/// Main handle for an open embedkv storage.
/// After `open`, call `recover()` and/or `build_index()` before reads/writes.
pub struct Store<D: BlockDevice> {
    dev: D,
    opts: Options,
    header: StorageHeader,
    index: MemIndex,
}

/// Open: validate the storage header and return a Store with an empty index.
pub fn open<D: BlockDevice>(mut dev: D, opts: Options) -> Result<Store<D>> {
    let block_size = dev.block_size();
    if block_size < STORAGE_HEADER_SIZE {
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

    Ok(Store {
        dev,
        opts,
        header: hdr,
        index: MemIndex::new(),
    })
}

impl<D: BlockDevice> Store<D> {
    /// Scan all data blocks and populate the in-memory key index.
    /// Call after `open` (and optionally after `recover`).
    pub fn build_index(&mut self) -> Result<()> {
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

    /// Perform a full storage scan: remove garbage blocks, flush if anything changed.
    pub fn recover(&mut self) -> Result<()> {
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
        let free = make_free_buf(block_size, self.opts.free_pattern);
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

    /// Return the value associated with `key`. Returns `ErrKeyNotFound` if absent.
    pub fn get(&mut self, key: &[u8]) -> Result<Vec<u8>> {
        let key_str = String::from_utf8_lossy(key);
        let descriptor_block = match self.index.get(&key_str) {
            Some(e) => e.descriptor_block,
            None => return Err(Error::KeyNotFound),
        };
        match verify_and_read_record(&mut self.dev, descriptor_block)? {
            Some(rec) => Ok(rec.value),
            None => Err(Error::CorruptRecord),
        }
    }

    /// Return `Some(Ok((generation, value)))` if the key is in the index,
    /// `None` if absent, or `Some(Err(...))` on I/O or corruption errors.
    /// Used by `ReplicaSet::get` to pick the highest-generation replica.
    pub fn get_with_generation(&mut self, key: &[u8]) -> Option<Result<(u32, Vec<u8>)>> {
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

    /// Write key→value, creating or copy-on-write updating the record.
    pub fn put(&mut self, key: &[u8], value: &[u8]) -> Result<()> {
        if key.len() as u32 >= self.dev.block_size() - RECORD_DESCRIPTOR_HEADER_SIZE {
            return Err(Error::KeyTooLong);
        }

        let key_str = String::from_utf8_lossy(key).into_owned();
        let existing_block: Option<u32> = self.index.get(&key_str).map(|e| e.descriptor_block);
        let generation = match self.index.get(&key_str) {
            Some(e) => e.generation + 1,
            None => 1,
        };

        let needed = needed_blocks(key, value, self.dev.block_size());
        let free_blocks = self.find_free_blocks(needed)?;

        let descriptor_block = free_blocks[0];
        self.write_record(descriptor_block, &free_blocks[1..], key, generation, value)?;
        self.dev.flush()?;

        self.index.put(IndexEntry {
            key: key_str.clone(),
            generation,
            descriptor_block,
            total_size: value.len() as u32,
        });

        if let Some(old_block) = existing_block {
            self.free_record(old_block)?;
            self.dev.flush()?;
        }

        Ok(())
    }

    /// Remove the record for `key`. Returns `ErrKeyNotFound` if absent.
    pub fn delete(&mut self, key: &[u8]) -> Result<()> {
        let key_str = String::from_utf8_lossy(key).into_owned();
        let descriptor_block = match self.index.get(&key_str) {
            Some(e) => e.descriptor_block,
            None => return Err(Error::KeyNotFound),
        };
        self.free_record(descriptor_block)?;
        self.dev.flush()?;
        self.index.delete(&key_str);
        Ok(())
    }

    /// Close releases the underlying device.
    pub fn close(self) {
        let _ = self.dev.close();
    }

    /// Return a copy of the storage header.
    pub fn header(&self) -> &StorageHeader {
        &self.header
    }

    // ── internal helpers ──────────────────────────────────────────────────

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
    ) -> Result<()> {
        let block_size = self.dev.block_size();
        let key_len = key.len() as u32;
        let first_cap = block_size - RECORD_DESCRIPTOR_HEADER_SIZE - key_len;
        let chunk_cap = block_size - VALUE_CHUNK_HEADER_SIZE;
        let vlen = value.len() as u32;

        let first_size = vlen.min(first_cap);
        let chunk_count = 1u32 + chunk_blocks.len() as u32;
        let next_chunk = if chunk_blocks.is_empty() {
            NULL_BLOCK_INDEX
        } else {
            chunk_blocks[0]
        };

        // Write descriptor block (zero-padded)
        let mut buf = vec![0u8; block_size as usize];
        let mut hdr = RecordDescriptorHeader {
            block_type: BLOCK_TYPE_RECORD_DESCRIPTOR,
            header_size: RECORD_DESCRIPTOR_HEADER_SIZE as u8,
            key_size: key_len as u16,
            generation,
            total_size: vlen,
            first_payload_size: first_size,
            chunk_count,
            next_chunk,
            flags: 0,
            block_crc32: 0,
        };
        marshal_descriptor_header(&hdr, &mut buf);
        let key_off = RECORD_DESCRIPTOR_HEADER_SIZE as usize;
        buf[key_off..key_off + key.len()].copy_from_slice(key);
        let payload_off = key_off + key.len();
        buf[payload_off..payload_off + first_size as usize]
            .copy_from_slice(&value[..first_size as usize]);
        hdr.block_crc32 = compute_block_crc32(&buf, 28);
        marshal_descriptor_header(&hdr, &mut buf);
        self.dev.write_block(descriptor_block, &buf)?;

        // Write value chunk blocks
        let mut remaining = &value[first_size as usize..];
        for (ci, &block_idx) in chunk_blocks.iter().enumerate() {
            let payload_size = (remaining.len() as u32).min(chunk_cap);
            let next_idx = if ci + 1 < chunk_blocks.len() {
                chunk_blocks[ci + 1]
            } else {
                NULL_BLOCK_INDEX
            };

            let mut cbuf = vec![0u8; block_size as usize];
            let mut ch = ValueChunkHeader {
                block_type: BLOCK_TYPE_VALUE_CHUNK,
                header_size: VALUE_CHUNK_HEADER_SIZE as u8,
                flags: 0,
                owner_descriptor: descriptor_block,
                chunk_index: (ci + 1) as u32,
                payload_size,
                next_chunk: next_idx,
                block_crc32: 0,
            };
            marshal_chunk_header(&ch, &mut cbuf);
            let hdr_len = VALUE_CHUNK_HEADER_SIZE as usize;
            cbuf[hdr_len..hdr_len + payload_size as usize]
                .copy_from_slice(&remaining[..payload_size as usize]);
            ch.block_crc32 = compute_block_crc32(&cbuf, 20);
            marshal_chunk_header(&ch, &mut cbuf);
            self.dev.write_block(block_idx, &cbuf)?;
            remaining = &remaining[payload_size as usize..];
        }

        Ok(())
    }

    fn free_record(&mut self, descriptor_block: u32) -> Result<()> {
        let block_size = self.dev.block_size();
        let block_count = self.dev.block_count();
        let mut buf = vec![0u8; block_size as usize];

        self.dev.read_block(descriptor_block, &mut buf)?;
        let mut hdr = RecordDescriptorHeader::default();
        if !unmarshal_descriptor_header(&buf, &mut hdr) {
            return Err(crate::error::Error::CrcMismatch);
        }

        let free = make_free_buf(block_size, self.opts.free_pattern);
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
            let mut ch = crate::block::ValueChunkHeader::default();
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
