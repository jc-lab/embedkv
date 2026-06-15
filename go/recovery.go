package embedkv

// recover performs a full storage scan of a single replica, removing all garbage
// blocks (incomplete records, orphan chunks, stale generations) and flushing if
// any block was erased. Per ARCH §17.
func (r *replica) recover(freePattern byte) error {
	blockSize := r.dev.BlockSize()
	blockCount := r.dev.BlockCount()
	buf := make([]byte, blockSize)

	// Collect best complete record per key (actual key bytes, not hash).
	best := make(map[string]*completeRecord)

	for i := uint32(1); i < blockCount; i++ {
		if err := r.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] != BlockTypeRecordDescriptor {
			continue
		}
		rec, err := verifyAndReadRecord(r.dev, i)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		keyStr := string(rec.key)
		if ex, ok := best[keyStr]; !ok || rec.generation > ex.generation {
			best[keyStr] = rec
		}
	}

	// Mark all blocks belonging to valid records.
	valid := make(map[uint32]bool)
	valid[0] = true
	for _, rec := range best {
		valid[rec.descriptorBlock] = true
		for _, cb := range rec.chunkBlocks {
			valid[cb] = true
		}
	}

	// Erase garbage blocks.
	free := makeFreeBuf(blockSize, freePattern)
	garbageFound := false
	for i := uint32(1); i < blockCount; i++ {
		if valid[i] {
			continue
		}
		if err := r.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] == 0x00 || buf[0] == 0xFF {
			continue // already free
		}
		if err := r.dev.WriteBlock(i, free); err != nil {
			return err
		}
		garbageFound = true
	}
	if garbageFound {
		if err := r.dev.Flush(); err != nil {
			return err
		}
	}
	return nil
}
