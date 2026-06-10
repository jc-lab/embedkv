package embedkv

import "hash/crc32"

var crcTable = crc32.MakeTable(crc32.IEEE)

// computeBlockCRC32 computes the CRC32 of buf, treating bytes [crcOffset, crcOffset+4)
// as 0x00000000. buf must be the full block (all blockSize bytes).
func computeBlockCRC32(buf []byte, crcOffset int) uint32 {
	h := crc32.New(crcTable)
	h.Write(buf[:crcOffset])
	h.Write([]byte{0, 0, 0, 0})
	h.Write(buf[crcOffset+4:])
	return h.Sum32()
}
