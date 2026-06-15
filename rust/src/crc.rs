use std::sync::OnceLock;

static CRC_TABLE: OnceLock<[u32; 256]> = OnceLock::new();

fn crc_table() -> &'static [u32; 256] {
    CRC_TABLE.get_or_init(|| {
        let poly: u32 = 0xEDB8_8320; // IEEE reflected polynomial
        let mut table = [0u32; 256];
        for i in 0u32..256 {
            let mut crc = i;
            for _ in 0..8 {
                if crc & 1 != 0 {
                    crc = (crc >> 1) ^ poly;
                } else {
                    crc >>= 1;
                }
            }
            table[i as usize] = crc;
        }
        table
    })
}

fn crc32_update(mut crc: u32, data: &[u8]) -> u32 {
    let table = crc_table();
    for &b in data {
        let idx = ((crc ^ (b as u32)) & 0xFF) as usize;
        crc = (crc >> 8) ^ table[idx];
    }
    crc
}

/// Compute the CRC32 (IEEE) of `buf`, treating the 4 bytes at `crc_offset`
/// as 0x00000000. `buf` must be the full block.
pub fn compute_block_crc32(buf: &[u8], crc_offset: usize) -> u32 {
    let mut crc = 0xFFFF_FFFFu32;
    crc = crc32_update(crc, &buf[..crc_offset]);
    crc = crc32_update(crc, &[0u8; 4]);
    crc = crc32_update(crc, &buf[crc_offset + 4..]);
    crc ^ 0xFFFF_FFFF
}

/// Offset of the block_crc32 field: always the last 4 bytes of the block,
/// regardless of block type.
pub fn block_crc_offset(buf: &[u8]) -> usize {
    buf.len() - 4
}

/// Compute the block CRC (treating the trailing CRC field as zero) and store it
/// in the last 4 bytes of the block.
pub fn write_block_crc(buf: &mut [u8]) {
    let off = block_crc_offset(buf);
    let crc = compute_block_crc32(buf, off);
    buf[off..off + 4].copy_from_slice(&crc.to_le_bytes());
}

/// Return true when the stored trailing CRC matches the block contents.
pub fn verify_block_crc(buf: &[u8]) -> bool {
    let off = block_crc_offset(buf);
    let stored = u32::from_le_bytes([buf[off], buf[off + 1], buf[off + 2], buf[off + 3]]);
    compute_block_crc32(buf, off) == stored
}
