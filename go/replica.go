package embedkv

// ReplicaSet wraps multiple independent storage replicas and provides a unified
// key-value interface. Writes go to all replicas; reads select the highest
// complete generation across replicas.
type ReplicaSet struct {
	stores []*Store
}

// OpenReplicas opens all given devices as replicas (Open → BuildIndex only; no recovery).
// On error all previously opened stores are closed before returning.
func OpenReplicas(devices []BlockDevice, opts Options) (*ReplicaSet, error) {
	stores := make([]*Store, 0, len(devices))
	for _, dev := range devices {
		s, err := Open(dev, opts)
		if err != nil {
			closeAll(stores)
			return nil, err
		}
		if err := s.BuildIndex(); err != nil {
			s.Close()
			closeAll(stores)
			return nil, err
		}
		stores = append(stores, s)
	}
	return &ReplicaSet{stores: stores}, nil
}

// RecoverReplicas runs Open → Recover → BuildIndex on every device.
func RecoverReplicas(devices []BlockDevice, opts Options) (*ReplicaSet, error) {
	stores := make([]*Store, 0, len(devices))
	for _, dev := range devices {
		s, err := Open(dev, opts)
		if err != nil {
			closeAll(stores)
			return nil, err
		}
		if err := s.Recover(); err != nil {
			s.Close()
			closeAll(stores)
			return nil, err
		}
		if err := s.BuildIndex(); err != nil {
			s.Close()
			closeAll(stores)
			return nil, err
		}
		stores = append(stores, s)
	}
	return &ReplicaSet{stores: stores}, nil
}

func closeAll(stores []*Store) {
	for _, s := range stores {
		s.Close()
	}
}

// Get returns the value from the replica holding the highest complete generation.
func (r *ReplicaSet) Get(key []byte) ([]byte, error) {
	keyStr := string(key)
	var best []byte
	bestGen := uint32(0)
	found := false

	for _, s := range r.stores {
		s.mu.RLock()
		entry, ok := s.index.get(keyStr)
		s.mu.RUnlock()
		if !ok {
			continue
		}
		if found && entry.Generation <= bestGen {
			continue
		}
		val, err := s.Get(key)
		if err != nil {
			continue
		}
		best = val
		bestGen = entry.Generation
		found = true
	}

	if !found {
		return nil, ErrKeyNotFound
	}
	return best, nil
}

// Put writes key→value to all replicas sequentially, each with its own flush.
func (r *ReplicaSet) Put(key, value []byte) error {
	for _, s := range r.stores {
		if err := s.Put(key, value); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the record from all replicas. Tolerates ErrKeyNotFound on
// individual replicas (may already be absent after a prior partial failure).
func (r *ReplicaSet) Delete(key []byte) error {
	for _, s := range r.stores {
		if err := s.Delete(key); err != nil && err != ErrKeyNotFound {
			return err
		}
	}
	return nil
}

// Close closes all replica stores.
func (r *ReplicaSet) Close() error {
	var last error
	for _, s := range r.stores {
		if err := s.Close(); err != nil {
			last = err
		}
	}
	return last
}
