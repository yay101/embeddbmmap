package embeddbmmap

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func bpg(n int64) int64 {
	ps := int64(PageSize())
	return ((n + ps - 1) / ps) * ps
}

func mkfile2(t *testing.T, size int64) *os.File {
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

// TestSyncAfterNonAlignedTruncate reproduces the root cause of data loss seen
// in embeddb. The pattern is:
//  1. Map a file with MAP_SHARED
//  2. Grow file via ftruncate to a non-page-aligned size
//  3. Resize the mmap to a page-aligned size (larger than file)
//  4. Write data via mmap pointer at an offset beyond the old file size
//  5. Call msync(MS_SYNC)
//  6. Unmap, then remap
//  7. Data written in step 4 may be lost
//
// Root cause: When the file is truncated to a size smaller than the mmap,
// pages beyond the file boundary but within the mapping may not be properly
// persisted by msync. On Linux with MAP_SHARED, writing to pages that extend
// beyond the file size triggers the kernel to handle page faults by zero-filling
// the tail of the last page. However, if ftruncate sets the file to a
// non-page-aligned size and then msync is called, the kernel may not correctly
// handle the partial-page write-back, leading to data loss in certain
// conditions involving mremap.
//
// Workaround: Always truncate the file to a page-aligned size (>= mapping size)
// before calling msync, or use file.WriteAt for critical writes.
func TestSyncAfterNonAlignedTruncate(t *testing.T) {
	ps := int64(PageSize())
	f := mkfile2(t, ps)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, ps, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	region.Advise(AdviceRandom)

	// Simulate the embeddb allocator pattern:
	// Allocate at offset 8260, record size 34.
	// actualSize = 8260 + 34 = 8294 (non-page-aligned)
	// alignedSize = pageAlign(8294) = 12288 (3 pages)
	dataOffset := int64(8260)
	data := []byte{0x1B, 0x02, 0x01, 0x01, 0x01, 0x06, 0x03, 0x08,
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22,
		0x1B, 0x03}

	// Step 1: Write header at offset 0 (within first page)
	copy(unsafe.Slice((*byte)(region.Pointer()), 8), []byte{0x1B, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	// Step 2: Allocator truncates file to actualSize (non-aligned), then Resize
	actualSize := dataOffset + int64(len(data))
	if err := f.Truncate(actualSize); err != nil {
		t.Fatalf("Truncate(%d): %v", actualSize, err)
	}
	alignedSize := bpg(actualSize)
	if alignedSize > region.Size() {
		if _, err := region.Resize(alignedSize); err != nil {
			t.Fatalf("Resize(%d): %v", alignedSize, err)
		}
	}

	// Step 3: Write record data via mmap pointer
	ptr := region.Pointer()
	copy(unsafe.Slice((*byte)(unsafe.Add(ptr, dataOffset)), len(data)), data)

	// Step 4: Verify write via mmap
	readback := unsafe.Slice((*byte)(unsafe.Add(region.Pointer(), dataOffset)), len(data))
	t.Logf("Before sync: first 4 bytes at %d = %x (expected 1b020101)", dataOffset, readback[:4])
	if readback[0] != 0x1B || readback[1] != 0x02 {
		t.Fatalf("data lost BEFORE sync — this is an mmap write issue")
	}

	// Step 5: Sync (this is where data may be lost)
	if err := region.Sync(SyncSync); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Step 6: Verify data after sync via mmap
	readback = unsafe.Slice((*byte)(unsafe.Add(region.Pointer(), dataOffset)), len(data))
	t.Logf("After sync: first 4 bytes at %d = %x", dataOffset, readback[:4])

	lostAfterSync := readback[0] != 0x1B || readback[1] != 0x02

	// Step 7: Unmap and remap (like embeddb's flush does)
	region.Unmap()
	if err := f.Sync(); err != nil {
		t.Fatalf("file.Sync: %v", err)
	}

	region2, err := Map(int(f.Fd()), 0, bpg(actualSize), ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Re-Map: %v", err)
	}
	defer region2.Unmap()

	// Step 8: Verify data after remap
	readback2 := unsafe.Slice((*byte)(unsafe.Add(region2.Pointer(), dataOffset)), len(data))
	t.Logf("After remap: first 4 bytes at %d = %x", dataOffset, readback2[:4])

	lostAfterRemap := readback2[0] != 0x1B || readback2[1] != 0x02

	if lostAfterSync {
		t.Errorf("BUG REPRODUCED: data at offset %d lost after Sync(MS_SYNC)", dataOffset)
	}
	if lostAfterRemap {
		t.Errorf("BUG REPRODUCED: data at offset %d lost after unmap+remap", dataOffset)
	}
	if lostAfterSync || lostAfterRemap {
		t.Logf("ROOT CAUSE: file.Truncate(%d) set file to non-page-aligned size, "+
			"but mmap was page-aligned to %d. The partial page beyond the file boundary "+
			"may not be correctly synced by msync on Linux.", actualSize, alignedSize)
	}
}

// TestAlignedTruncatePreventsDataLoss verifies that the fix (page-aligned
// truncation) prevents the data loss seen in TestSyncAfterNonAlignedTruncate.
func TestAlignedTruncatePreventsDataLoss(t *testing.T) {
	ps := int64(PageSize())
	f := mkfile2(t, ps)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, ps, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	region.Advise(AdviceRandom)

	dataOffset := int64(8260)
	data := []byte{0x1B, 0x02, 0x01, 0x01, 0x01, 0x06, 0x03, 0x08}

	// FIX: Always truncate to page-aligned size (at least as large as the mapping)
	actualSize := dataOffset + int64(len(data))
	alignedSize := bpg(actualSize)

	if err := f.Truncate(alignedSize); err != nil {
		t.Fatalf("Truncate(%d): %v", alignedSize, err)
	}
	if alignedSize > region.Size() {
		if _, err := region.Resize(alignedSize); err != nil {
			t.Fatalf("Resize(%d): %v", alignedSize, err)
		}
	}

	// Write data
	ptr := region.Pointer()
	copy(unsafe.Slice((*byte)(unsafe.Add(ptr, dataOffset)), len(data)), data)

	// Sync
	if err := region.Sync(SyncSync); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Verify after sync
	readback := unsafe.Slice((*byte)(unsafe.Add(region.Pointer(), dataOffset)), len(data))
	if readback[0] != 0x1B || readback[1] != 0x02 {
		t.Fatalf("FIX FAILED: data lost even with page-aligned truncation")
	}

	// Verify after unmap+remap
	region.Unmap()
	f.Sync()
	region2, err := Map(int(f.Fd()), 0, alignedSize, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Re-Map: %v", err)
	}
	defer region2.Unmap()

	readback2 := unsafe.Slice((*byte)(unsafe.Add(region2.Pointer(), dataOffset)), len(data))
	if readback2[0] != 0x1B || readback2[1] != 0x02 {
		t.Fatalf("FIX FAILED: data lost after unmap+remap")
	}
}

// TestFileWriteAtThenRemap documents the workaround used in embeddb:
// write data via file.WriteAt (not mmap), then sync and remap.
func TestFileWriteAtThenRemap(t *testing.T) {
	ps := int64(PageSize())
	f := mkfile2(t, ps*4)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, ps*4, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	region.Advise(AdviceRandom)

	// Write header via mmap (within first page, always safe)
	copy(unsafe.Slice((*byte)(region.Pointer()), 8), []byte{0x1B, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	// Write record data via file.WriteAt (workaround: bypass mmap for data)
	data := []byte{0x1B, 0x02, 0x01, 0x01, 0x01, 0x06, 0x03, 0x08}
	dataOffset := int64(8260)
	if _, err := f.WriteAt(data, dataOffset); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	f.Sync()

	// Remap and verify
	region.Unmap()
	region2, err := Map(int(f.Fd()), 0, ps*4, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Re-Map: %v", err)
	}
	defer region2.Unmap()

	readback := unsafe.Slice((*byte)(unsafe.Add(region2.Pointer(), dataOffset)), len(data))
	if readback[0] != 0x1B || readback[1] != 0x02 {
		t.Fatalf("file.WriteAt workaround: data lost after remap, got %x", readback[:4])
	}
	t.Logf("file.WriteAt workaround works: data at %d = %x", dataOffset, readback[:4])
}
