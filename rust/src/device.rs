use std::fs::File;
use std::io::{Read, Seek, SeekFrom, Write};

use crate::error::{Error, Result};

/// Abstracts fixed-size block I/O over a storage medium.
pub trait BlockDevice {
    fn block_size(&self) -> u32;
    fn block_count(&self) -> u32;
    fn read_block(&mut self, index: u32, buf: &mut [u8]) -> Result<()>;
    fn write_block(&mut self, index: u32, buf: &[u8]) -> Result<()>;
    /// Flush ensures all prior writes are persisted to the underlying medium.
    fn flush(&mut self) -> Result<()>;
    fn close(self) -> Result<()>;
}

// ── MemDevice ──────────────────────────────────────────────────────────────

/// In-memory block device — useful for testing and embedding.
pub struct MemDevice {
    block_size: u32,
    block_count: u32,
    data: Vec<u8>,
}

impl MemDevice {
    /// Create a zeroed in-memory block device.
    pub fn new(block_size: u32, block_count: u32) -> Self {
        MemDevice {
            block_size,
            block_count,
            data: vec![0u8; block_size as usize * block_count as usize],
        }
    }

    /// Wrap existing bytes as a MemDevice (bytes are used directly, not copied).
    pub fn from_bytes(block_size: u32, data: Vec<u8>) -> Result<Self> {
        if data.len() % block_size as usize != 0 {
            return Err(Error::SizeMismatch);
        }
        let block_count = (data.len() / block_size as usize) as u32;
        Ok(MemDevice { block_size, block_count, data })
    }

    /// Return a reference to the raw storage bytes.
    pub fn bytes(&self) -> &[u8] {
        &self.data
    }
}

impl BlockDevice for MemDevice {
    fn block_size(&self) -> u32 {
        self.block_size
    }

    fn block_count(&self) -> u32 {
        self.block_count
    }

    fn read_block(&mut self, index: u32, buf: &mut [u8]) -> Result<()> {
        if index >= self.block_count {
            return Err(Error::InvalidBlock);
        }
        let off = index as usize * self.block_size as usize;
        buf[..self.block_size as usize].copy_from_slice(&self.data[off..off + self.block_size as usize]);
        Ok(())
    }

    fn write_block(&mut self, index: u32, buf: &[u8]) -> Result<()> {
        if index >= self.block_count {
            return Err(Error::InvalidBlock);
        }
        let off = index as usize * self.block_size as usize;
        self.data[off..off + self.block_size as usize].copy_from_slice(&buf[..self.block_size as usize]);
        Ok(())
    }

    fn flush(&mut self) -> Result<()> {
        Ok(())
    }

    fn close(self) -> Result<()> {
        Ok(())
    }
}

// ── FileDevice ─────────────────────────────────────────────────────────────

/// File-backed block device.
pub struct FileDevice {
    file: File,
    block_size: u32,
    block_count: u32,
}

impl FileDevice {
    /// Open an existing storage file (must be an exact multiple of block_size).
    pub fn open(path: &str, block_size: u32) -> Result<Self> {
        let file = std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .open(path)?;
        let size = file.metadata()?.len();
        if size % block_size as u64 != 0 {
            return Err(Error::SizeMismatch);
        }
        let block_count = (size / block_size as u64) as u32;
        Ok(FileDevice { file, block_size, block_count })
    }

    /// Create (or truncate) a file sized to block_size * block_count.
    pub fn create(path: &str, block_size: u32, block_count: u32) -> Result<Self> {
        let file = std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)?;
        file.set_len(block_size as u64 * block_count as u64)?;
        Ok(FileDevice { file, block_size, block_count })
    }
}

impl BlockDevice for FileDevice {
    fn block_size(&self) -> u32 {
        self.block_size
    }

    fn block_count(&self) -> u32 {
        self.block_count
    }

    fn read_block(&mut self, index: u32, buf: &mut [u8]) -> Result<()> {
        let off = index as u64 * self.block_size as u64;
        self.file.seek(SeekFrom::Start(off))?;
        self.file.read_exact(&mut buf[..self.block_size as usize])?;
        Ok(())
    }

    fn write_block(&mut self, index: u32, buf: &[u8]) -> Result<()> {
        let off = index as u64 * self.block_size as u64;
        self.file.seek(SeekFrom::Start(off))?;
        self.file.write_all(&buf[..self.block_size as usize])?;
        Ok(())
    }

    fn flush(&mut self) -> Result<()> {
        self.file.flush()?;
        self.file.sync_all()?;
        Ok(())
    }

    fn close(self) -> Result<()> {
        Ok(()) // File is closed when dropped
    }
}
