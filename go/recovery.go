package embedkv

// Recover performs a full storage scan, removes all garbage blocks (incomplete
// records, orphan chunks, stale generations), and flushes if any block was erased.
//
// Recover does NOT build the in-memory index; call BuildIndex() afterwards.
// Per ARCH §17.
func (s *Store) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	blockSize := s.dev.BlockSize()
	blockCount := s.dev.BlockCount()
	buf := make([]byte, blockSize)

	// Collect best complete record per key (actual key bytes, not hash)
	type keyGen struct {
		key        string
		generation uint32
	}
	best := make(map[string]*completeRecord) // key string → best record

	for i := uint32(1); i < blockCount; i++ {
		if err := s.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] != BlockTypeRecordDescriptor {
			continue
		}
		rec, err := verifyAndReadRecord(s.dev, i)
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

	// Mark all blocks belonging to valid records
	valid := make(map[uint32]bool)
	valid[0] = true
	for _, rec := range best {
		valid[rec.descriptorBlock] = true
		for _, cb := range rec.chunkBlocks {
			valid[cb] = true
		}
	}

	// Erase garbage blocks
	free := makeFreeBuf(blockSize, s.opts.FreePattern)
	garbageFound := false
	for i := uint32(1); i < blockCount; i++ {
		if valid[i] {
			continue
		}
		if err := s.dev.ReadBlock(i, buf); err != nil {
			return err
		}
		if buf[0] == 0x00 || buf[0] == 0xFF {
			continue // already free
		}
		if err := s.dev.WriteBlock(i, free); err != nil {
			return err
		}
		garbageFound = true
	}
	if garbageFound {
		if err := s.dev.Flush(); err != nil {
			return err
		}
	}
	return nil
}
