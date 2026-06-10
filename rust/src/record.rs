use std::collections::HashSet;

use crate::block::{
    unmarshal_chunk_header, unmarshal_descriptor_header, ValueChunkHeader,
};

use crate::device::BlockDevice;
use crate::error::Result;
use crate::format::*;

/// A fully validated record ready for returning to callers.
pub struct CompleteRecord {
    pub key: Vec<u8>,
    pub generation: u32,
    pub descriptor_block: u32,
    pub header: crate::block::RecordDescriptorHeader,
    /// Block indices of value chunk blocks (chunk_index >= 1).
    pub chunk_blocks: Vec<u32>,
    pub value: Vec<u8>,
}

/// Read and validate the record whose descriptor is at `descriptor_block`.
/// Returns `Ok(None)` when the record is incomplete or corrupt.
pub fn verify_and_read_record<D: BlockDevice>(
    dev: &mut D,
    descriptor_block: u32,
) -> Result<Option<CompleteRecord>> {
    let block_size = dev.block_size();
    let mut buf = vec![0u8; block_size as usize];

    dev.read_block(descriptor_block, &mut buf)?;

    if buf[0] != BLOCK_TYPE_RECORD_DESCRIPTOR {
        return Ok(None);
    }

    let mut hdr = crate::block::RecordDescriptorHeader::default();
    if !unmarshal_descriptor_header(&buf, &mut hdr) {
        return Ok(None);
    }
    if hdr.header_size as u32 != RECORD_DESCRIPTOR_HEADER_SIZE {
        return Ok(None);
    }
    if hdr.key_size as u32 >= block_size - RECORD_DESCRIPTOR_HEADER_SIZE {
        return Ok(None);
    }
    let first_payload_cap = block_size - RECORD_DESCRIPTOR_HEADER_SIZE - hdr.key_size as u32;
    if hdr.first_payload_size > first_payload_cap {
        return Ok(None);
    }
    if hdr.chunk_count == 0 {
        return Ok(None);
    }

    // Read key bytes
    let key_start = RECORD_DESCRIPTOR_HEADER_SIZE as usize;
    let key_end = key_start + hdr.key_size as usize;
    let key = buf[key_start..key_end].to_vec();

    // Read first value payload — OOM guard
    let max_dev_bytes = block_size as u64 * dev.block_count() as u64;
    let cap_value = (hdr.total_size as u64).min(max_dev_bytes) as usize;
    let cap_chunks = (hdr.chunk_count.saturating_sub(1)).min(dev.block_count()) as usize;

    let payload_start = key_end;
    let payload_end = payload_start + hdr.first_payload_size as usize;
    let mut value = Vec::with_capacity(cap_value);
    value.extend_from_slice(&buf[payload_start..payload_end]);

    let mut rec = CompleteRecord {
        key,
        generation: hdr.generation,
        descriptor_block,
        header: hdr.clone(),
        chunk_blocks: Vec::with_capacity(cap_chunks),
        value,
    };

    if hdr.chunk_count == 1 {
        if hdr.total_size != hdr.first_payload_size || hdr.next_chunk != NULL_BLOCK_INDEX {
            return Ok(None);
        }
        return Ok(Some(rec));
    }

    if hdr.next_chunk == NULL_BLOCK_INDEX {
        return Ok(None);
    }

    let mut chunk_buf = vec![0u8; block_size as usize];
    let mut next = hdr.next_chunk;
    let mut expected_idx: u32 = 1;
    let mut visited: HashSet<u32> = HashSet::new();
    visited.insert(descriptor_block);

    loop {
        if next == NULL_BLOCK_INDEX {
            break;
        }
        if next >= dev.block_count() || visited.contains(&next) {
            return Ok(None);
        }
        visited.insert(next);

        dev.read_block(next, &mut chunk_buf)?;

        if chunk_buf[0] != BLOCK_TYPE_VALUE_CHUNK {
            return Ok(None);
        }

        let mut ch = ValueChunkHeader::default();
        if !unmarshal_chunk_header(&chunk_buf, &mut ch) {
            return Ok(None);
        }
        if ch.header_size as u32 != VALUE_CHUNK_HEADER_SIZE {
            return Ok(None);
        }
        if ch.owner_descriptor != descriptor_block {
            return Ok(None);
        }
        if ch.chunk_index != expected_idx {
            return Ok(None);
        }
        if ch.payload_size > block_size - VALUE_CHUNK_HEADER_SIZE {
            return Ok(None);
        }

        let pstart = VALUE_CHUNK_HEADER_SIZE as usize;
        let pend = pstart + ch.payload_size as usize;
        rec.value.extend_from_slice(&chunk_buf[pstart..pend]);
        rec.chunk_blocks.push(next);

        expected_idx += 1;
        next = ch.next_chunk;
    }

    if expected_idx != hdr.chunk_count {
        return Ok(None);
    }
    if rec.value.len() as u32 != hdr.total_size {
        return Ok(None);
    }

    Ok(Some(rec))
}

/// Calculate the number of blocks needed to store key + value.
pub fn needed_blocks(key: &[u8], value: &[u8], block_size: u32) -> u32 {
    let first_cap = block_size - RECORD_DESCRIPTOR_HEADER_SIZE - key.len() as u32;
    let chunk_cap = block_size - VALUE_CHUNK_HEADER_SIZE;
    let vlen = value.len() as u32;
    if vlen <= first_cap {
        return 1;
    }
    let remaining = vlen - first_cap;
    let extra = (remaining + chunk_cap - 1) / chunk_cap;
    1 + extra
}
