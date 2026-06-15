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

// blockCRCOffset returns the offset of the block_crc32 field, which is always
// the last 4 bytes of the block regardless of block type.
func blockCRCOffset(buf []byte) int { return len(buf) - 4 }

// writeBlockCRC computes the CRC over the whole block (treating the trailing
// CRC field as zero) and stores it at block_size-4.
func writeBlockCRC(buf []byte) {
	off := blockCRCOffset(buf)
	le.PutUint32(buf[off:], computeBlockCRC32(buf, off))
}

// verifyBlockCRC returns true when the stored trailing CRC matches the block.
func verifyBlockCRC(buf []byte) bool {
	off := blockCRCOffset(buf)
	return computeBlockCRC32(buf, off) == le.Uint32(buf[off:])
}
