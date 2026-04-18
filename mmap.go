package embeddbmmap

import (
	"errors"
	"sync"
	"unsafe"
)

var (
	ErrNotMapped     = errors.New("region not mapped")
	ErrInvalidSize   = errors.New("invalid size: must be positive and page-aligned")
	ErrResizeFailed  = errors.New("failed to resize mapping")
	ErrMapFailed     = errors.New("failed to create mapping")
	ErrUnmapFailed   = errors.New("failed to unmap region")
	ErrSyncFailed    = errors.New("failed to sync mapping")
	ErrAdviseFailed  = errors.New("failed to set advisory")
	ErrProtectFailed = errors.New("failed to change protection")
	ErrLockFailed    = errors.New("failed to lock memory")
	ErrUnlockFailed  = errors.New("failed to unlock memory")
)

type Protection int

const (
	ProtNone Protection = 0
	ProtRead Protection = 1 << iota
	ProtWrite
	ProtExec
)

type MapFlag int

const (
	MapShared MapFlag = 1 << iota
	MapPrivate
	MapAnon
	MapFixed
	MapPopulate
	MapHugeTLB
	MapNoReserve
)

type Advice int

const (
	AdviceNormal Advice = iota
	AdviceRandom
	AdviceSequential
	AdviceWillNeed
	AdviceDontNeed
	AdviceRemove
	AdviceDontFork
	AdviceHugePage
	AdviceNoHugePage
)

type SyncFlag int

const (
	SyncSync SyncFlag = 1 << iota
	SyncAsync
	SyncInvalidate
)

type MappedRegion struct {
	mu     sync.RWMutex
	addr   unsafe.Pointer
	size   int64
	prot   Protection
	flags  MapFlag
	fd     int
	offset int64
}

// Map creates a file-backed memory mapping.
// fd is an open file descriptor, offset must be page-aligned.
func Map(fd int, offset, length int64, prot Protection, flags MapFlag) (*MappedRegion, error) {
	return mapFile(fd, offset, length, prot, flags)
}

// MapAnonymous creates an anonymous memory mapping (no file backing).
func MapAnonymous(length int64, prot Protection) (*MappedRegion, error) {
	return mapAnonymous(length, prot)
}

// Unmap removes the memory mapping.
func (m *MappedRegion) Unmap() error {
	if m.addr == nil {
		return ErrNotMapped
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr == nil {
		return ErrNotMapped
	}
	return m.unmap()
}

// Resize grows or shrinks the mapping to newSize bytes.
// Returns relocated=true if the mapping was moved to a different address.
// The caller must re-acquire any derived pointers (Pointer, Bytes) after resize.
// Resize acquires the write lock internally; callers must not hold RLock.
func (m *MappedRegion) Resize(newSize int64) (relocated bool, err error) {
	if newSize <= 0 || newSize%int64(PageSize()) != 0 {
		return false, ErrInvalidSize
	}
	if m.addr == nil {
		return false, ErrNotMapped
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resize(newSize)
}

// Sync flushes the entire mapping to the underlying file.
func (m *MappedRegion) Sync(flags SyncFlag) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	return m.sync(0, m.size, flags)
}

// SyncRange flushes a portion of the mapping to the underlying file.
// offset and length must be page-aligned.
func (m *MappedRegion) SyncRange(offset, length int64, flags SyncFlag) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if offset < 0 || length <= 0 || offset+length > m.size {
		return ErrInvalidSize
	}
	return m.sync(offset, length, flags)
}

// Advise sets kernel memory access pattern hints for the entire mapping.
func (m *MappedRegion) Advise(advice Advice) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	return m.advise(0, m.size, advice)
}

// AdviseRange sets kernel memory access pattern hints for a portion of the mapping.
func (m *MappedRegion) AdviseRange(offset, length int64, advice Advice) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if offset < 0 || length <= 0 || offset+length > m.size {
		return ErrInvalidSize
	}
	return m.advise(offset, length, advice)
}

// Protect changes the memory protection for the entire mapping.
func (m *MappedRegion) Protect(prot Protection) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if err := m.protect(0, m.size, prot); err != nil {
		return err
	}
	m.prot = prot
	return nil
}

// ProtectRange changes the memory protection for a portion of the mapping.
func (m *MappedRegion) ProtectRange(offset, length int64, prot Protection) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if offset < 0 || length <= 0 || offset+length > m.size {
		return ErrInvalidSize
	}
	return m.protect(offset, length, prot)
}

// Lock locks the entire mapping in RAM, preventing it from being paged out.
func (m *MappedRegion) Lock() error {
	if m.addr == nil {
		return ErrNotMapped
	}
	return m.lock(0, m.size)
}

// LockRange locks a portion of the mapping in RAM.
func (m *MappedRegion) LockRange(offset, length int64) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if offset < 0 || length <= 0 || offset+length > m.size {
		return ErrInvalidSize
	}
	return m.lock(offset, length)
}

// Unlock unlocks the entire mapping, allowing it to be paged out again.
func (m *MappedRegion) Unlock() error {
	if m.addr == nil {
		return ErrNotMapped
	}
	return m.unlock(0, m.size)
}

// UnlockRange unlocks a portion of the mapping.
func (m *MappedRegion) UnlockRange(offset, length int64) error {
	if m.addr == nil {
		return ErrNotMapped
	}
	if offset < 0 || length <= 0 || offset+length > m.size {
		return ErrInvalidSize
	}
	return m.unlock(offset, length)
}

func (m *MappedRegion) RLock() {
	m.mu.RLock()
}

func (m *MappedRegion) RUnlock() {
	m.mu.RUnlock()
}

func (m *MappedRegion) WriteLock() {
	m.mu.Lock()
}

func (m *MappedRegion) WriteUnlock() {
	m.mu.Unlock()
}

// Bytes returns a []byte slice covering the entire mapping.
// The caller must not use the returned slice after Resize or Unmap.
// For large or long-lived mappings, prefer Pointer() to avoid GC pressure.
func (m *MappedRegion) Bytes() []byte {
	if m.addr == nil {
		return nil
	}
	return unsafe.Slice((*byte)(m.addr), int(m.size))
}

// Pointer returns an unsafe.Pointer to the start of the mapping.
// Preferred for DB use: no GC overhead, caller manages offsets.
func (m *MappedRegion) Pointer() unsafe.Pointer {
	if m.addr == nil {
		return nil
	}
	return m.addr
}

// Size returns the current size of the mapping in bytes.
func (m *MappedRegion) Size() int64 {
	return m.size
}

// PageSize returns the system's memory page size.
func PageSize() int {
	return pageSize()
}
