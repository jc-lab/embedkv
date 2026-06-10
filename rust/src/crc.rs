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
