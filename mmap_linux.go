package embeddbmmap

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func pageSize() int {
	return unix.Getpagesize()
}

func protToUnix(prot Protection) int {
	p := unix.PROT_NONE
	if prot&ProtRead != 0 {
		p |= unix.PROT_READ
	}
	if prot&ProtWrite != 0 {
		p |= unix.PROT_WRITE
	}
	if prot&ProtExec != 0 {
		p |= unix.PROT_EXEC
	}
	return p
}

func flagsToUnix(flags MapFlag) int {
	f := 0
	if flags&MapShared != 0 {
		f |= unix.MAP_SHARED
	}
	if flags&MapPrivate != 0 {
		f |= unix.MAP_PRIVATE
	}
	if flags&MapAnon != 0 {
		f |= unix.MAP_ANONYMOUS
	}
	if flags&MapFixed != 0 {
		f |= unix.MAP_FIXED
	}
	if flags&MapPopulate != 0 {
		f |= unix.MAP_POPULATE
	}
	if flags&MapHugeTLB != 0 {
		f |= unix.MAP_HUGETLB
	}
	if flags&MapNoReserve != 0 {
		f |= unix.MAP_NORESERVE
	}
	return f
}

func adviceToUnix(advice Advice) int {
	switch advice {
	case AdviceNormal:
		return unix.MADV_NORMAL
	case AdviceRandom:
		return unix.MADV_RANDOM
	case AdviceSequential:
		return unix.MADV_SEQUENTIAL
	case AdviceWillNeed:
		return unix.MADV_WILLNEED
	case AdviceDontNeed:
		return unix.MADV_DONTNEED
	case AdviceRemove:
		return unix.MADV_REMOVE
	case AdviceDontFork:
		return unix.MADV_DOFORK
	case AdviceHugePage:
		return unix.MADV_HUGEPAGE
	case AdviceNoHugePage:
		return unix.MADV_NOHUGEPAGE
	default:
		return unix.MADV_NORMAL
	}
}

func syncFlagsToUnix(flags SyncFlag) int {
	f := 0
	if flags&SyncSync != 0 {
		f |= unix.MS_SYNC
	}
	if flags&SyncAsync != 0 {
		f |= unix.MS_ASYNC
	}
	if flags&SyncInvalidate != 0 {
		f |= unix.MS_INVALIDATE
	}
	return f
}

func mapFile(fd int, offset, length int64, prot Protection, flags MapFlag) (*MappedRegion, error) {
	ps := int64(pageSize())
	if length <= 0 || length%ps != 0 {
		return nil, ErrInvalidSize
	}
	if offset < 0 || offset%ps != 0 {
		return nil, ErrInvalidSize
	}

	fdInt := fd
	if fdInt < 0 {
		fdInt = -1
	}

	addr, _, errno := unix.Syscall6(
		unix.SYS_MMAP,
		0,
		uintptr(length),
		uintptr(protToUnix(prot)),
		uintptr(flagsToUnix(flags)),
		uintptr(fdInt),
		uintptr(offset),
	)
	if errno != 0 {
		return nil, fmt.Errorf("%w: %v", ErrMapFailed, errno)
	}

	mr := &MappedRegion{
		addr:   unsafe.Pointer(addr),
		size:   length,
		prot:   prot,
		flags:  flags,
		fd:     fd,
		offset: offset,
	}
	runtime.SetFinalizer(mr, (*MappedRegion).Unmap)
	return mr, nil
}

func mapAnonymous(length int64, prot Protection) (*MappedRegion, error) {
	ps := int64(pageSize())
	if length <= 0 || length%ps != 0 {
		return nil, ErrInvalidSize
	}

	flags := MapPrivate | MapAnon
	addr, _, errno := unix.Syscall6(
		unix.SYS_MMAP,
		0,
		uintptr(length),
		uintptr(protToUnix(prot)),
		uintptr(flagsToUnix(flags)),
		^uintptr(0),
		0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("%w: %v", ErrMapFailed, errno)
	}

	mr := &MappedRegion{
		addr:  unsafe.Pointer(addr),
		size:  length,
		prot:  prot,
		flags: flags,
		fd:    -1,
	}
	runtime.SetFinalizer(mr, (*MappedRegion).Unmap)
	return mr, nil
}

func (m *MappedRegion) unmap() error {
	_, _, errno := unix.Syscall(unix.SYS_MUNMAP, uintptr(m.addr), uintptr(m.size), 0)
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrUnmapFailed, errno)
	}
	m.addr = nil
	m.size = 0
	runtime.SetFinalizer(m, nil)
	return nil
}

func (m *MappedRegion) resize(newSize int64) (relocated bool, err error) {
	oldAddr := uintptr(m.addr)
	newAddr, _, errno := unix.Syscall6(
		unix.SYS_MREMAP,
		oldAddr,
		uintptr(m.size),
		uintptr(newSize),
		unix.MREMAP_MAYMOVE,
		0,
		0,
	)
	if errno != 0 {
		return false, fmt.Errorf("%w: %v", ErrResizeFailed, errno)
	}

	relocated = newAddr != oldAddr
	m.addr = unsafe.Pointer(newAddr)
	m.size = newSize
	return relocated, nil
}

func (m *MappedRegion) sync(offset, length int64, flags SyncFlag) error {
	addr := uintptr(m.addr) + uintptr(offset)
	_, _, errno := unix.Syscall(unix.SYS_MSYNC, addr, uintptr(length), uintptr(syncFlagsToUnix(flags)))
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrSyncFailed, errno)
	}
	return nil
}

func (m *MappedRegion) advise(offset, length int64, advice Advice) error {
	addr := uintptr(m.addr) + uintptr(offset)
	_, _, errno := unix.Syscall(unix.SYS_MADVISE, addr, uintptr(length), uintptr(adviceToUnix(advice)))
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrAdviseFailed, errno)
	}
	return nil
}

func (m *MappedRegion) protect(offset, length int64, prot Protection) error {
	addr := uintptr(m.addr) + uintptr(offset)
	_, _, errno := unix.Syscall(unix.SYS_MPROTECT, addr, uintptr(length), uintptr(protToUnix(prot)))
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrProtectFailed, errno)
	}
	return nil
}

func (m *MappedRegion) lock(offset, length int64) error {
	addr := uintptr(m.addr) + uintptr(offset)
	_, _, errno := unix.Syscall(unix.SYS_MLOCK, addr, uintptr(length), 0)
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrLockFailed, errno)
	}
	return nil
}

func (m *MappedRegion) unlock(offset, length int64) error {
	addr := uintptr(m.addr) + uintptr(offset)
	_, _, errno := unix.Syscall(unix.SYS_MUNLOCK, addr, uintptr(length), 0)
	if errno != 0 {
		return fmt.Errorf("%w: %v", ErrUnlockFailed, errno)
	}
	return nil
}
