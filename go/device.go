package embedkv

import (
	"fmt"
	"os"
)

// BlockDevice abstracts fixed-size block I/O over a storage medium.
type BlockDevice interface {
	BlockSize() uint32
	BlockCount() uint32
	ReadBlock(index uint32, buf []byte) error
	WriteBlock(index uint32, buf []byte) error
	// Flush ensures all prior writes are persisted to the underlying medium.
	Flush() error
	Close() error
}

// MemDevice is an in-memory BlockDevice used for testing and embedding.
type MemDevice struct {
	blockSize  uint32
	blockCount uint32
	data       []byte
}

// NewMemDevice creates a zeroed in-memory block device.
func NewMemDevice(blockSize, blockCount uint32) *MemDevice {
	return &MemDevice{
		blockSize:  blockSize,
		blockCount: blockCount,
		data:       make([]byte, uint64(blockSize)*uint64(blockCount)),
	}
}

// NewMemDeviceFromBytes wraps existing bytes as a MemDevice.
// The slice is used directly (not copied), so mutations are visible to callers.
func NewMemDeviceFromBytes(blockSize uint32, data []byte) (*MemDevice, error) {
	if uint64(len(data))%uint64(blockSize) != 0 {
		return nil, ErrSizeMismatch
	}
	return &MemDevice{
		blockSize:  blockSize,
		blockCount: uint32(uint64(len(data)) / uint64(blockSize)),
		data:       data,
	}, nil
}

func (d *MemDevice) BlockSize() uint32  { return d.blockSize }
func (d *MemDevice) BlockCount() uint32 { return d.blockCount }

func (d *MemDevice) ReadBlock(index uint32, buf []byte) error {
	if index >= d.blockCount {
		return fmt.Errorf("embedkv: block index %d out of range (count=%d)", index, d.blockCount)
	}
	off := uint64(index) * uint64(d.blockSize)
	copy(buf, d.data[off:off+uint64(d.blockSize)])
	return nil
}

func (d *MemDevice) WriteBlock(index uint32, buf []byte) error {
	if index >= d.blockCount {
		return fmt.Errorf("embedkv: block index %d out of range (count=%d)", index, d.blockCount)
	}
	off := uint64(index) * uint64(d.blockSize)
	copy(d.data[off:], buf[:d.blockSize])
	return nil
}

func (d *MemDevice) Flush() error { return nil }
func (d *MemDevice) Close() error { return nil }

// Bytes returns the raw storage bytes (not copied).
func (d *MemDevice) Bytes() []byte { return d.data }

// FileDevice is a file-backed BlockDevice.
type FileDevice struct {
	f          *os.File
	blockSize  uint32
	blockCount uint32
}

// OpenFileDevice opens an existing storage file. The file size must be an exact
// multiple of blockSize.
func OpenFileDevice(path string, blockSize uint32) (*FileDevice, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := info.Size()
	if uint64(size)%uint64(blockSize) != 0 {
		f.Close()
		return nil, ErrSizeMismatch
	}
	return &FileDevice{
		f:          f,
		blockSize:  blockSize,
		blockCount: uint32(uint64(size) / uint64(blockSize)),
	}, nil
}

// CreateFileDevice creates (or truncates) a file and sizes it to blockSize*blockCount.
func CreateFileDevice(path string, blockSize, blockCount uint32) (*FileDevice, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(int64(blockSize) * int64(blockCount)); err != nil {
		f.Close()
		return nil, err
	}
	return &FileDevice{f: f, blockSize: blockSize, blockCount: blockCount}, nil
}

func (d *FileDevice) BlockSize() uint32  { return d.blockSize }
func (d *FileDevice) BlockCount() uint32 { return d.blockCount }

func (d *FileDevice) ReadBlock(index uint32, buf []byte) error {
	_, err := d.f.ReadAt(buf[:d.blockSize], int64(index)*int64(d.blockSize))
	return err
}

func (d *FileDevice) WriteBlock(index uint32, buf []byte) error {
	_, err := d.f.WriteAt(buf[:d.blockSize], int64(index)*int64(d.blockSize))
	return err
}

func (d *FileDevice) Flush() error { return d.f.Sync() }
func (d *FileDevice) Close() error { return d.f.Close() }
