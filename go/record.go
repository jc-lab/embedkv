package embedkv

// neededBlocks returns the number of blocks required to store key+value.
func neededBlocks(key, value []byte, blockSize uint32) uint32 {
	firstCap := firstPayloadCapacity(blockSize, uint32(len(key)))
	chunkCap := chunkPayloadCapacity(blockSize)
	vlen := uint32(len(value))
	if vlen <= firstCap {
		return 1
	}
	remaining := vlen - firstCap
	extra := (remaining + chunkCap - 1) / chunkCap
	return 1 + extra
}

// completeRecord holds the fully validated content of a readable record.
type completeRecord struct {
	key             []byte
	generation      uint32
	descriptorBlock uint32
	header          RecordDescriptorHeader
	chunkBlocks     []uint32 // block indices of value chunk blocks (chunk_index >= 1)
	value           []byte
}

// verifyAndReadRecord reads and validates the record whose descriptor is at
// descriptorBlock. Returns nil (no error) when the record is incomplete or corrupt.
func verifyAndReadRecord(dev BlockDevice, descriptorBlock uint32) (*completeRecord, error) {
	blockSize := dev.BlockSize()
	buf := make([]byte, blockSize)

	if err := dev.ReadBlock(descriptorBlock, buf); err != nil {
		return nil, err
	}
	if buf[0] != BlockTypeRecordDescriptor {
		return nil, nil
	}

	var hdr RecordDescriptorHeader
	if !unmarshalDescriptorHeader(buf, &hdr) {
		return nil, nil
	}
	if uint32(hdr.HeaderSize) != RecordDescriptorHeaderSize {
		return nil, nil
	}
	if uint32(hdr.KeySize) > blockSize-RecordDescriptorHeaderSize-BlockCRCSize {
		return nil, nil
	}
	firstPayloadCap := firstPayloadCapacity(blockSize, uint32(hdr.KeySize))
	if hdr.FirstPayloadSize > firstPayloadCap {
		return nil, nil
	}
	if hdr.ChunkCount == 0 {
		return nil, nil
	}

	// Read key bytes
	keyStart := RecordDescriptorHeaderSize
	keyEnd := keyStart + uint32(hdr.KeySize)
	key := make([]byte, hdr.KeySize)
	copy(key, buf[keyStart:keyEnd])

	// Read first value payload
	payloadStart := keyEnd
	payloadEnd := payloadStart + hdr.FirstPayloadSize
	// Cap initial slice capacities to the physical device size to prevent OOM
	// when headers contain extreme values but a valid CRC (e.g., fuzz inputs).
	// Actual bytes appended via append are bounded by block content, not header fields.
	maxDevBytes := uint64(blockSize) * uint64(dev.BlockCount())
	capValue := uint64(hdr.TotalSize)
	if capValue > maxDevBytes {
		capValue = maxDevBytes
	}
	// ChunkCount >= 1 already verified above; cap at block count (max reachable chain length)
	capChunks := uint64(hdr.ChunkCount - 1)
	if capChunks > uint64(dev.BlockCount()) {
		capChunks = uint64(dev.BlockCount())
	}

	value := make([]byte, 0, capValue)
	value = append(value, buf[payloadStart:payloadEnd]...)

	rec := &completeRecord{
		key:             key,
		generation:      hdr.Generation,
		descriptorBlock: descriptorBlock,
		header:          hdr,
		chunkBlocks:     make([]uint32, 0, capChunks),
		value:           value,
	}

	if hdr.ChunkCount == 1 {
		if hdr.TotalSize != hdr.FirstPayloadSize || hdr.NextChunk != NullBlockIndex {
			return nil, nil
		}
		return rec, nil
	}

	if hdr.NextChunk == NullBlockIndex {
		return nil, nil
	}

	chunkBuf := make([]byte, blockSize)
	next := hdr.NextChunk
	expectedIdx := uint32(1)
	visited := make(map[uint32]bool)
	visited[descriptorBlock] = true

	for next != NullBlockIndex {
		if next >= dev.BlockCount() || visited[next] {
			return nil, nil
		}
		visited[next] = true

		if err := dev.ReadBlock(next, chunkBuf); err != nil {
			return nil, err
		}
		if chunkBuf[0] != BlockTypeValueChunk {
			return nil, nil
		}

		var ch ValueChunkHeader
		if !unmarshalChunkHeader(chunkBuf, &ch) {
			return nil, nil
		}
		if uint32(ch.HeaderSize) != ValueChunkHeaderSize {
			return nil, nil
		}
		if ch.OwnerDescriptor != descriptorBlock {
			return nil, nil
		}
		if ch.ChunkIndex != expectedIdx {
			return nil, nil
		}
		if ch.PayloadSize > chunkPayloadCapacity(blockSize) {
			return nil, nil
		}

		rec.value = append(rec.value, chunkBuf[ValueChunkHeaderSize:ValueChunkHeaderSize+ch.PayloadSize]...)
		rec.chunkBlocks = append(rec.chunkBlocks, next)

		expectedIdx++
		next = ch.NextChunk
	}

	if expectedIdx != hdr.ChunkCount {
		return nil, nil
	}
	if uint32(len(rec.value)) != hdr.TotalSize {
		return nil, nil
	}

	return rec, nil
}
