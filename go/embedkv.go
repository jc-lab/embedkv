// Package embedkv implements the embedkv storage format defined in ARCH.md.
//
// A storage is a fixed-size array of fixed-size blocks. Block 0 is always the
// storage header. Data blocks are one of: record descriptor, value chunk, or
// free block (0x00 or 0xFF filled). Every non-free block stores its CRC32 in
// the last 4 bytes of the block (offset block_size-4), regardless of block type.
//
// A Store is backed by one or more identical replica devices (§20). Writes fan
// out to every replica; reads return the highest-generation complete record
// across replicas. Single-device storage is simply a slice of one device.
//
// Example usage:
//
//	dev, _ := embedkv.CreateFileDevice("data.bin", 512, 1024)
//	devs := []embedkv.BlockDevice{dev}
//	embedkv.Format(devs, embedkv.DefaultOptions())
//	s, _ := embedkv.Open(devs, embedkv.DefaultOptions())
//	s.BuildIndex()
//	s.Put([]byte("config"), []byte(`{"version":1}`))
//	val, _ := s.Get([]byte("config"))
package embedkv
