package embedkv

import (
	"bytes"
	"errors"
	"testing"
)

// newTestStore creates a formatted, opened, and index-built store backed by MemDevice.
func newTestStore(t *testing.T, blockSize, blockCount uint32) *Store {
	t.Helper()
	dev := NewMemDevice(blockSize, blockCount)
	if err := Format([]BlockDevice{dev}, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s, err := Open([]BlockDevice{dev}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMultiReplicaWriteAndFaultTolerance(t *testing.T) {
	const blockSize = 256
	dev0 := NewMemDevice(blockSize, 16)
	dev1 := NewMemDevice(blockSize, 16)
	devs := []BlockDevice{dev0, dev1}

	if err := Format(devs, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	// replica_id is stamped per device (base + index).
	if got := le.Uint32(dev1.Bytes()[16:]); got != 1 {
		t.Fatalf("replica 1 replica_id = %d, want 1", got)
	}

	s, err := Open(devs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	if s.ReplicaCount() != 2 {
		t.Fatalf("ReplicaCount = %d, want 2", s.ReplicaCount())
	}
	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Both replicas must independently hold the record.
	for i, dev := range []*MemDevice{dev0, dev1} {
		one, err := Open([]BlockDevice{dev}, DefaultOptions())
		if err != nil {
			t.Fatalf("replica %d open: %v", i, err)
		}
		if err := one.BuildIndex(); err != nil {
			t.Fatal(err)
		}
		got, err := one.Get([]byte("k"))
		if err != nil || string(got) != "v" {
			t.Fatalf("replica %d: got %q err %v", i, got, err)
		}
		one.Close()
	}

	// Corrupt replica 0's data block entirely; the store must still read from replica 1.
	garbage := make([]byte, blockSize)
	for j := range garbage {
		garbage[j] = 0x5A
	}
	dev0.WriteBlock(1, garbage)

	s2, err := Open(devs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get([]byte("k"))
	if err != nil {
		t.Fatalf("expected read to survive replica-0 corruption: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q, want v", got)
	}
}

func TestFormatOpen(t *testing.T) {
	dev := NewMemDevice(256, 16)
	if err := Format([]BlockDevice{dev}, DefaultOptions()); err != nil {
		t.Fatal("Format:", err)
	}
	s, err := Open([]BlockDevice{dev}, DefaultOptions())
	if err != nil {
		t.Fatal("Open:", err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal("BuildIndex:", err)
	}
	s.Close()
}

func TestOpenInvalidMagic(t *testing.T) {
	dev := NewMemDevice(256, 8)
	if _, err := Open([]BlockDevice{dev}, DefaultOptions()); err != ErrInvalidHeader {
		t.Fatalf("expected ErrInvalidHeader, got %v", err)
	}
}

func TestPutGetSmallValue(t *testing.T) {
	s := newTestStore(t, 256, 16)
	defer s.Close()

	key := []byte("hello")
	val := []byte("world")
	if err := s.Put(key, val); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("got %q, want %q", got, val)
	}
}

func TestGetKeyNotFound(t *testing.T) {
	s := newTestStore(t, 256, 8)
	defer s.Close()
	if _, err := s.Get([]byte("missing")); err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestPutGetLargeValue(t *testing.T) {
	// blockSize=64: descriptor header=36, key="bigkey"(6) → firstCap=64-36-6=22
	// chunkCap=64-24=40; value=200 → extra=ceil((200-22)/40)=5 → 6 blocks total
	s := newTestStore(t, 64, 32)
	defer s.Close()

	key := []byte("bigkey")
	val := make([]byte, 200)
	for i := range val {
		val[i] = byte(i)
	}
	if err := s.Put(key, val); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val) {
		t.Fatal("large value mismatch")
	}
}

func TestPutUpdate(t *testing.T) {
	s := newTestStore(t, 256, 16)
	defer s.Close()

	key := []byte("key")
	if err := s.Put(key, []byte("value1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(key, []byte("value2-updated")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "value2-updated" {
		t.Fatalf("got %q", got)
	}
}

func TestUpdateFreesOldBlocks(t *testing.T) {
	const blockSize = 256
	dev := NewMemDevice(blockSize, 6) // block 0=header, blocks 1-5 data
	if err := Format([]BlockDevice{dev}, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s, err := Open([]BlockDevice{dev}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Fill all 5 data blocks (one block per single-byte record)
	for i := 0; i < 5; i++ {
		if err := s.Put([]byte{byte(i)}, []byte{byte(i)}); err != nil {
			t.Fatalf("initial put %d: %v", i, err)
		}
	}
	// Storage is now full
	if err := s.Put([]byte("overflow"), []byte("x")); err != ErrStorageFull {
		t.Fatalf("expected ErrStorageFull, got %v", err)
	}
	// Free one block then put should succeed
	if err := s.Delete([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("fits"), []byte("y")); err != nil {
		t.Fatalf("put after delete: %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t, 256, 8)
	defer s.Close()

	key := []byte("todelete")
	if err := s.Put(key, []byte("val")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(key); err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := newTestStore(t, 256, 8)
	defer s.Close()
	if err := s.Delete([]byte("ghost")); err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestMultipleKeys(t *testing.T) {
	s := newTestStore(t, 256, 32)
	defer s.Close()

	pairs := [][2]string{
		{"alpha", "value-alpha"},
		{"beta", "value-beta"},
		{"gamma", "value-gamma"},
	}
	for _, kv := range pairs {
		if err := s.Put([]byte(kv[0]), []byte(kv[1])); err != nil {
			t.Fatalf("put %q: %v", kv[0], err)
		}
	}
	for _, kv := range pairs {
		got, err := s.Get([]byte(kv[0]))
		if err != nil {
			t.Fatalf("get %q: %v", kv[0], err)
		}
		if string(got) != kv[1] {
			t.Fatalf("key %q: got %q want %q", kv[0], got, kv[1])
		}
	}
}

func TestReopenPreservesData(t *testing.T) {
	dev := NewMemDevice(256, 16)
	if err := Format([]BlockDevice{dev}, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	s, err := Open([]BlockDevice{dev}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("persistent"), []byte("data")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open([]BlockDevice{dev}, DefaultOptions())
	if err != nil {
		t.Fatal("reopen:", err)
	}
	if err := s2.BuildIndex(); err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get([]byte("persistent"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("got %q", got)
	}
}

func TestEmptyValue(t *testing.T) {
	s := newTestStore(t, 256, 8)
	defer s.Close()

	if err := s.Put([]byte("empty"), []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get([]byte("empty"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty value, got %q", got)
	}
}

// TestValueExactlyFillsDescriptor: value fills exactly first_payload_capacity.
func TestValueExactlyFillsDescriptor(t *testing.T) {
	const blockSize = 256
	k := []byte("exact")
	capacity := int(blockSize - RecordDescriptorHeaderSize - uint32(len(k)) - BlockCRCSize)
	val := make([]byte, capacity)
	for i := range val {
		val[i] = byte(i % 256)
	}

	s := newTestStore(t, blockSize, 8)
	defer s.Close()

	if err := s.Put(k, val); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(k)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val) {
		t.Fatal("boundary value mismatch")
	}
}

func TestKeyTooLong(t *testing.T) {
	const blockSize = 64
	s := newTestStore(t, blockSize, 8)
	defer s.Close()

	// key of blockSize-RecordDescriptorHeaderSize bytes is exactly at the limit
	tooLong := make([]byte, blockSize-RecordDescriptorHeaderSize)
	if err := s.Put(tooLong, []byte("v")); err != ErrKeyTooLong {
		t.Fatalf("expected ErrKeyTooLong, got %v", err)
	}
}

func TestNeededBlocks(t *testing.T) {
	const bs = 256
	k := []byte("k")                                                          // 1 byte key
	firstCap := bs - RecordDescriptorHeaderSize - uint32(len(k)) - BlockCRCSize // 219
	chunkCap := bs - ValueChunkHeaderSize - BlockCRCSize                       // 232

	cases := []struct {
		vlen uint32
		want uint32
	}{
		{0, 1},
		{1, 1},
		{firstCap, 1},
		{firstCap + 1, 2},
		{firstCap + chunkCap, 2},
		{firstCap + chunkCap + 1, 3},
	}
	for _, tc := range cases {
		got := neededBlocks(k, make([]byte, tc.vlen), bs)
		if got != tc.want {
			t.Errorf("neededBlocks(vlen=%d, bs=%d) = %d, want %d", tc.vlen, bs, got, tc.want)
		}
	}
}

func TestPutWithUserFlags(t *testing.T) {
	s := newTestStore(t, 256, 16)
	defer s.Close()

	if err := s.Put([]byte("flagged"), []byte("val"), 0xCAFEBABE); err != nil {
		t.Fatal(err)
	}
	// Get still returns value
	got, err := s.Get([]byte("flagged"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "val" {
		t.Fatalf("got %q", got)
	}
}

func TestKeys(t *testing.T) {
	s := newTestStore(t, 256, 32)
	defer s.Close()

	want := []string{"alpha", "beta", "gamma"}
	for _, k := range want {
		if err := s.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	got := s.Keys()
	if len(got) != len(want) {
		t.Fatalf("Keys() returned %d keys, want %d", len(got), len(want))
	}
	set := make(map[string]bool)
	for _, k := range got {
		set[string(k)] = true
	}
	for _, k := range want {
		if !set[k] {
			t.Errorf("key %q missing from Keys()", k)
		}
	}
}

func TestIterate(t *testing.T) {
	s := newTestStore(t, 256, 32)
	defer s.Close()

	wantKeys := []string{"key1", "key2", "key3"}
	for _, k := range wantKeys {
		if err := s.Put([]byte(k), []byte("v")); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}

	seen := make(map[string]bool)
	err := s.Iterate(func(key []byte) error {
		seen[string(key)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range wantKeys {
		if !seen[k] {
			t.Errorf("key %q not visited by Iterate", k)
		}
	}
	if len(seen) != len(wantKeys) {
		t.Errorf("Iterate visited %d keys, want %d", len(seen), len(wantKeys))
	}
}

func TestIterateEarlyExit(t *testing.T) {
	s := newTestStore(t, 256, 32)
	defer s.Close()

	for i := range [5]int{} {
		s.Put([]byte{byte(i)}, []byte{byte(i)})
	}
	count := 0
	sentinel := errors.New("stop")
	err := s.Iterate(func(key []byte) error {
		count++
		if count == 2 {
			return sentinel
		}
		return nil
	})
	if err != sentinel {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if count != 2 {
		t.Fatalf("expected fn called 2 times, got %d", count)
	}
}
