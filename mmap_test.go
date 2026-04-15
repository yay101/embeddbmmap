package embeddbmmap

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func pageSizeAligned(n int64) int64 {
	ps := int64(PageSize())
	return ((n + ps - 1) / ps) * ps
}

func createTempFile(t *testing.T, size int64) *os.File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mmaptest")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate temp file: %v", err)
	}
	return f
}

func TestMapAnonymous(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	if region.Size() != size {
		t.Errorf("expected size %d, got %d", size, region.Size())
	}

	b := region.Bytes()
	for i := range b {
		b[i] = byte(i)
	}
	for i := range b {
		if b[i] != byte(i) {
			t.Errorf("byte at %d: expected %d, got %d", i, byte(i), b[i])
		}
	}

	p := region.Pointer()
	if p == nil {
		t.Error("Pointer() returned nil")
	}

	got := *(*byte)(p)
	if got != 0 {
		t.Errorf("Pointer byte: expected 0, got %d", got)
	}
}

func TestMapFile(t *testing.T) {
	size := pageSizeAligned(8192)
	f := createTempFile(t, size)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, size, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	defer region.Unmap()

	b := region.Bytes()
	for i := 0; i < 4096; i++ {
		b[i] = byte(i & 0xff)
	}

	if err := region.Sync(SyncSync); err != nil {
		t.Errorf("Sync: %v", err)
	}

	buf := make([]byte, 4096)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Errorf("ReadAt: %v", err)
	}
	for i := range buf {
		if buf[i] != byte(i&0xff) {
			t.Errorf("file byte at %d: expected %d, got %d", i, byte(i&0xff), buf[i])
			break
		}
	}
}

func TestResizeGrow(t *testing.T) {
	size := pageSizeAligned(4096)
	newSize := pageSizeAligned(8192)

	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	originalPtr := region.Pointer()
	b := region.Bytes()
	b[0] = 0xAB

	relocated, err := region.Resize(newSize)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if region.Size() != newSize {
		t.Errorf("expected size %d, got %d", newSize, region.Size())
	}

	newBytes := region.Bytes()
	if newBytes[0] != 0xAB {
		t.Error("data not preserved after resize")
	}

	newBytes[size] = 0xCD
	if newBytes[size] != 0xCD {
		t.Error("cannot write to grown region")
	}

	if relocated && region.Pointer() == originalPtr {
		t.Error("reported relocation but pointer unchanged")
	}

	_ = relocated
}

func TestResizeShrink(t *testing.T) {
	size := pageSizeAligned(8192)
	newSize := pageSizeAligned(4096)

	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	b := region.Bytes()
	b[0] = 0xEF

	relocated, err := region.Resize(newSize)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if region.Size() != newSize {
		t.Errorf("expected size %d, got %d", newSize, region.Size())
	}

	b2 := region.Bytes()
	if b2[0] != 0xEF {
		t.Error("data not preserved after shrink")
	}

	_ = relocated
}

func TestAdvise(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	tests := []Advice{
		AdviceRandom,
		AdviceSequential,
		AdviceWillNeed,
		AdviceNormal,
	}
	for _, advice := range tests {
		if err := region.Advise(advice); err != nil {
			t.Errorf("Advise(%v): %v", advice, err)
		}
	}

	if err := region.AdviseRange(0, size, AdviceRandom); err != nil {
		t.Errorf("AdviseRange: %v", err)
	}
}

func TestProtect(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	b := region.Bytes()
	b[0] = 0x42

	if err := region.Protect(ProtRead); err != nil {
		t.Errorf("Protect(ProtRead): %v", err)
	}

	if err := region.Protect(ProtRead | ProtWrite); err != nil {
		t.Errorf("Protect(ProtRead|ProtWrite): %v", err)
	}

	if err := region.ProtectRange(0, size, ProtRead|ProtWrite); err != nil {
		t.Errorf("ProtectRange: %v", err)
	}
}

func TestLockUnlock(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	if err := region.Lock(); err != nil {
		t.Errorf("Lock: %v", err)
	}
	if err := region.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}

	if err := region.LockRange(0, size); err != nil {
		t.Errorf("LockRange: %v", err)
	}
	if err := region.UnlockRange(0, size); err != nil {
		t.Errorf("UnlockRange: %v", err)
	}
}

func TestSyncRange(t *testing.T) {
	size := pageSizeAligned(8192)
	f := createTempFile(t, size)
	defer f.Close()

	region, err := Map(int(f.Fd()), 0, size, ProtRead|ProtWrite, MapShared)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	defer region.Unmap()

	region.Bytes()[0] = 0xFF

	if err := region.SyncRange(0, pageSizeAligned(4096), SyncSync); err != nil {
		t.Errorf("SyncRange: %v", err)
	}
}

func TestDoubleUnmap(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}

	if err := region.Unmap(); err != nil {
		t.Errorf("first Unmap: %v", err)
	}

	if err := region.Unmap(); err != ErrNotMapped {
		t.Errorf("second Unmap: expected ErrNotMapped, got %v", err)
	}
}

func TestPointerAfterUnmap(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}

	if p := region.Pointer(); p == nil {
		t.Error("Pointer should not be nil before unmap")
	}
	if b := region.Bytes(); b == nil {
		t.Error("Bytes() should not be nil before unmap")
	}

	region.Unmap()

	if p := region.Pointer(); p != nil {
		t.Error("Pointer should be nil after unmap")
	}
	if b := region.Bytes(); b != nil {
		t.Error("Bytes() should be nil after unmap")
	}
}

func TestInvalidSize(t *testing.T) {
	_, err := MapAnonymous(100, ProtRead|ProtWrite)
	if err == nil {
		t.Error("expected error for non-page-aligned size")
	}

	region, _ := MapAnonymous(pageSizeAligned(4096), ProtRead|ProtWrite)
	if region != nil {
		defer region.Unmap()
	}

	_, err = region.Resize(100)
	if err == nil {
		t.Error("expected error for non-page-aligned resize")
	}
}

func TestMapPrivate(t *testing.T) {
	size := pageSizeAligned(4096)
	f := createTempFile(t, size)
	defer f.Close()

	buf := make([]byte, size)
	for i := range buf {
		buf[i] = 0xAA
	}
	f.WriteAt(buf, 0)

	region, err := Map(int(f.Fd()), 0, size, ProtRead|ProtWrite, MapPrivate)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	defer region.Unmap()

	b := region.Bytes()
	b[0] = 0xFF

	readBuf := make([]byte, 1)
	f.ReadAt(readBuf, 0)
	if readBuf[0] != 0xAA {
		t.Error("MAP_PRIVATE should not modify the file")
	}
}

func TestMapPopulateFlag(t *testing.T) {
	size := pageSizeAligned(4096)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	region.Bytes()[0] = 0x42

	if region.Bytes()[0] != 0x42 {
		t.Error("basic read/write failed")
	}
}

func TestResizeRelocateDetection(t *testing.T) {
	region, err := MapAnonymous(pageSizeAligned(4096), ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	origAddr := uintptr(region.Pointer())

	relocated, err := region.Resize(pageSizeAligned(8192))
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	newAddr := uintptr(region.Pointer())
	if !relocated && newAddr != origAddr {
		t.Error("relocated=false but address changed")
	}
	if relocated && newAddr == origAddr {
		t.Error("relocated=true but address unchanged")
	}
}

func TestUnsafePointerArithmetic(t *testing.T) {
	size := pageSizeAligned(8192)
	region, err := MapAnonymous(size, ProtRead|ProtWrite)
	if err != nil {
		t.Fatalf("MapAnonymous: %v", err)
	}
	defer region.Unmap()

	b := region.Bytes()
	b[100] = 0x42

	p := region.Pointer()
	byteAt100 := *(*byte)(unsafe.Pointer(uintptr(p) + 100))
	if byteAt100 != 0x42 {
		t.Errorf("pointer arithmetic: expected 0x42, got 0x%02x", byteAt100)
	}
}
