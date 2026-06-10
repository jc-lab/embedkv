// Package embedkv implements the embedkv storage format defined in ARCH.md.
//
// A storage is a fixed-size array of fixed-size blocks. Block 0 is always the
// storage header. Data blocks are one of: record descriptor, value chunk, or
// free block (0x00 or 0xFF filled).
//
// Key identity is tracked by a 32-bit hash of the key bytes. Two different keys
// that produce the same hash will collide; callers must ensure uniqueness within
// a storage instance.
//
// Example usage:
//
//	dev, _ := embedkv.CreateFileDevice("data.bin", 512, 1024)
//	embedkv.Format(dev, embedkv.DefaultOptions())
//	s, _ := embedkv.Open(dev, embedkv.DefaultOptions())
//	s.Put([]byte("config"), []byte(`{"version":1}`))
//	val, _ := s.Get([]byte("config"))
package embedkv
