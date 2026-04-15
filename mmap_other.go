//go:build !linux

package embeddbmmap

import "fmt"

func pageSize() int {
	panic("embeddbmmap: not implemented on this platform")
}

func mapFile(fd int, offset, length int64, prot Protection, flags MapFlag) (*MappedRegion, error) {
	return nil, fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func mapAnonymous(length int64, prot Protection) (*MappedRegion, error) {
	return nil, fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) unmap() error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) resize(newSize int64) (bool, error) {
	return false, fmt.Errorf("embeddbmmap: Resize not implemented on this platform — mremap is Linux-only")
}

func (m *MappedRegion) sync(offset, length int64, flags SyncFlag) error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) advise(offset, length int64, advice Advice) error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) protect(offset, length int64, prot Protection) error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) lock(offset, length int64) error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}

func (m *MappedRegion) unlock(offset, length int64) error {
	return fmt.Errorf("embeddbmmap: not implemented on this platform — mmap is Linux-only")
}
