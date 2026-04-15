package embeddbmmap

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func rpg(n int64) int64 {
	ps := int64(PageSize())
	return ((n + ps - 1) / ps) * ps
}

func mkf(t *testing.T, size int64) *os.File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	return f
}

// TestNonAlignedTruncateWithMultipleResizes reproduces the exact embeddb pattern:
// Multiple allocate cycles (truncate+resize) followed by write+sync, where
// the file is always truncated to a non-page-aligned size.
func TestNonAlignedTruncateWithMultipleResizes(t *testing.T) {
	ps := int64(PageSize())
	f := mkf(t, ps)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, ps, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	defer region.Unmap()
	region.Advise(AdviceRandom)

	// Simulate many allocations like the B-tree does
	type write struct {
		offset int64
		data   []byte
	}
	var writes []write
	actualSize := int64(64) // skip header
	alignedSize := ps

	for i := 0; i < 50; i++ {
		dataLen := int64(24 + i*4) // varying sizes
		offset := actualSize
		actualSize += dataLen

		// allocator.Allocate pattern: truncate to exact size, resize to aligned
		if err := f.Truncate(actualSize); err != nil {
			t.Fatalf("Truncate(%d): %v", actualSize, err)
		}
		newAligned := rpg(actualSize)
		if newAligned > alignedSize {
			alignedSize = newAligned
			if alignedSize > region.Size() {
				if _, err := region.Resize(alignedSize); err != nil {
					t.Fatalf("Resize(%d): %v", alignedSize, err)
				}
			}
		}

		data := make([]byte, dataLen)
		for j := range data {
			data[j] = byte(i + j)
		}
		ptr := region.Pointer()
		copy(unsafe.Slice((*byte)(unsafe.Add(ptr, offset)), len(data)), data)
		writes = append(writes, write{offset: offset, data: data})
	}

	// Verify all writes before sync
	for _, w := range writes {
		ptr := region.Pointer()
		got := unsafe.Slice((*byte)(unsafe.Add(ptr, w.offset)), len(w.data))
		for j := range w.data {
			if got[j] != w.data[j] {
				t.Fatalf("BEFORE sync: data at %d mismatch", w.offset)
			}
		}
	}

	// Now sync (this is what flush() does)
	if err := region.Sync(SyncSync); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Verify all writes after sync
	lostCount := 0
	for _, w := range writes {
		ptr := region.Pointer()
		got := unsafe.Slice((*byte)(unsafe.Add(ptr, w.offset)), 4)
		if got[0] != w.data[0] {
			lostCount++
			t.Errorf("AFTER sync: data at offset %d lost: expected 0x%02x got 0x%02x", w.offset, w.data[0], got[0])
		}
	}
	if lostCount > 0 {
		t.Fatalf("BUG: %d/%d writes lost after Sync(SyncSync)", lostCount, len(writes))
	}
	t.Logf("All %d writes preserved after Sync", len(writes))
}

// TestNonAlignedTruncateSyncRemap does the full flush pattern:
// write → allocate+resize → write → Sync → Truncate smaller → Unmap → Remap
func TestNonAlignedTruncateSyncRemap(t *testing.T) {
	ps := int64(PageSize())
	f := mkf(t, ps*2)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, ps*2, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	region.Advise(AdviceRandom)

	// Write header
	copy(unsafe.Slice((*byte)(region.Pointer()), 8), []byte{0x1B, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	// Allocate some data: grow file to non-aligned size, resize region
	dataOffset := int64(64)
	data := []byte{0x1B, 0x02, 0x01, 0x01, 0x01, 0x06, 0x03, 0x08, 0xDD, 0xEE}
	actualSize := dataOffset + int64(len(data))

	if err := f.Truncate(actualSize); err != nil {
		t.Fatalf("Truncate(%d): %v", actualSize, err)
	}
	alignedSize := rpg(actualSize)
	if alignedSize > region.Size() {
		if _, err := region.Resize(alignedSize); err != nil {
			t.Fatalf("Resize(%d): %v", alignedSize, err)
		}
	}

	// Write data via mmap
	ptr := region.Pointer()
	copy(unsafe.Slice((*byte)(unsafe.Add(ptr, dataOffset)), len(data)), data)

	// Verify before operations
	readback := unsafe.Slice((*byte)(unsafe.Add(region.Pointer(), dataOffset)), len(data))
	if readback[0] != 0x1B || readback[1] != 0x02 {
		t.Fatalf("data not visible before sync")
	}

	// This is the flush pattern: Sync, then Truncate smaller, then Unmap+Remap
	if err := region.Sync(SyncSync); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Verify after sync
	readback = unsafe.Slice((*byte)(unsafe.Add(region.Pointer(), dataOffset)), len(data))
	if readback[0] != 0x1B || readback[1] != 0x02 {
		t.Fatalf("BUG: data at %d lost after Sync: got %x expected %x", dataOffset, readback[:4], data[:4])
	}

	// Truncate and remap
	newSize := ps * 2 // smaller than actualSize? No, just remap cleanly
	region.Unmap()
	if err := f.Truncate(newSize); err != nil {
		t.Fatalf("Truncate(%d): %v", newSize, err)
	}
	f.Sync()

	region2, err := Map(int(f.Fd()), 0, newSize, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Re-Map: %v", err)
	}
	defer region2.Unmap()

	readback2 := unsafe.Slice((*byte)(unsafe.Add(region2.Pointer(), dataOffset)), len(data))
	if readback2[0] != 0x1B || readback2[1] != 0x02 {
		t.Fatalf("BUG: data at %d lost after truncate+remap: got %x expected %x", dataOffset, readback2[:4], data[:4])
	}
	t.Logf("DATA PRESERVED: offset %d, data=%x", dataOffset, readback2[:4])
}
