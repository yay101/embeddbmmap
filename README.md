# embeddbmmap

A Go memory-mapped file library for Linux, built for embedded database workloads. Provides direct access to `mmap`, `mremap`, `madvise`, `mprotect`, `msync`, and `mlock`/`munlock` through a clean Go API — zero cgo dependency, Linux-only.

Designed as a drop-in replacement for generic mmap libraries in database engines where in-place resizing (`mremap`) and relocation-awareness are critical.

## Features

- **`mremap` support** — resize mappings without unmap/remap cycles, with relocation detection
- **`Pointer()` and `Bytes()`** — raw `unsafe.Pointer` for zero-GC-overhead DB access, or `[]byte` for convenience
- **Partial sync** — `SyncRange` to flush only dirty pages
- **Memory advice** — `Advise`/`AdviseRange` with all `madvise` hints (random, sequential, willneed, dontneed, hugepage, etc.)
- **Protection changes** — `Protect`/`ProtectRange` via `mprotect`
- **Locking** — `Lock`/`Unlock` to pin pages in RAM via `mlock`/`munlock`
- **Anonymous mappings** — `MapAnonymous` for shared memory or allocator use
- **DB-friendly flags** — `MapPopulate`, `MapHugeTLB`, `MapNoReserve` exposed as first-class options
- **Zero cgo** — all syscalls via `golang.org/x/sys/unix`

## Installation

```bash
go get github.com/yay101/embeddbmmap
```

Requires Go 1.21+ and Linux.

## Quick Start

### File-backed mapping

```go
f, _ := os.OpenFile("data.db", os.O_RDWR|os.O_CREATE, 0644)
f.Truncate(4096)
ps := embeddbmmap.PageSize()
size := int64(ps) // must be page-aligned

region, err := embeddbmmap.Map(int(f.Fd()), 0, size, embeddbmmap.ProtRead|embeddbmmap.ProtWrite, embeddbmmap.MapShared)
if err != nil {
    log.Fatal(err)
}
defer region.Unmap()
```

### Anonymous mapping

```go
ps := int64(embeddbmmap.PageSize())
size := ((8192 + ps - 1) / ps) * ps // round up to page size

region, err := embeddbmmap.MapAnonymous(size, embeddbmmap.ProtRead|embeddbmmap.ProtWrite)
if err != nil {
    log.Fatal(err)
}
defer region.Unmap()
```

### Resizing a mapping (mremap)

```go
// Grow from 4KB to 16KB
newSize := int64(embeddbmmap.PageSize()) * 4
relocated, err := region.Resize(newSize)
if err != nil {
    log.Fatal(err)
}

if relocated {
    // The mapping was moved to a new address.
    // Any previously derived pointers/slices are invalid — re-acquire them.
    base = region.Pointer()
}

// Shrink back down
relocated, err = region.Resize(int64(embeddbmmap.PageSize()))
```

### Reading and writing

```go
// Via []byte (convenience):
b := region.Bytes()
b[0] = 0x42
_ = b[100]

// Via unsafe.Pointer (zero GC overhead, DB-preferred):
base := region.Pointer()
*(*byte)(unsafe.Pointer(uintptr(base) + 100)) = 0xFF
val := *(*byte)(unsafe.Pointer(uintptr(base) + 100))
```

### Syncing to disk

```go
// Flush entire mapping synchronously
region.Sync(embeddbmmap.SyncSync)

// Flush only a range (partial sync)
region.SyncRange(0, 4096, embeddbmmap.SyncSync)

// Async flush
region.Sync(embeddbmmap.SyncAsync)
```

### Memory advice

```go
// Tell the kernel we'll be doing random access (good for B-tree pages)
region.Advise(embeddbmmap.AdviceRandom)

// Release pages we no longer need
region.AdviseRange(oldOffset, oldLength, embeddbmmap.AdviceDontNeed)

// Prefault pages at mmap time (embeddbmmap.MapPopulate flag handles this too)
region.Advise(embeddbmmap.AdviceWillNeed)
```

### Protection changes

```go
// Make a page read-only
region.ProtectRange(0, 4096, embeddbmmap.ProtRead)

// Make it read-write again
region.ProtectRange(0, 4096, embeddbmmap.ProtRead|embeddbmmap.ProtWrite)
```

### Locking pages in RAM

```go
// Prevent swapping (useful for latency-sensitive DB pages)
region.Lock()
// ...
region.Unlock()

// Lock only a range
region.LockRange(0, 4096)
region.UnlockRange(0, 4096)
```

## API Reference

### Constructor Functions

| Function | Description |
|---|---|
| `Map(fd, offset, length, prot, flags)` | Create a file-backed mapping |
| `MapAnonymous(length, prot)` | Create an anonymous mapping (`MAP_PRIVATE | MAP_ANONYMOUS`) |
| `PageSize() int` | Return system page size |

### MappedRegion Methods

| Method | Description |
|---|---|
| `Unmap() error` | Remove the mapping |
| `Resize(newSize) (relocated bool, err error)` | Resize via `mremap(MREMAP_MAYMOVE)`. Returns `true` if the base address changed. |
| `Sync(flags) error` | Flush entire mapping to disk |
| `SyncRange(offset, length, flags) error` | Flush a portion of the mapping |
| `Advise(advice) error` | Set access pattern hint for entire mapping |
| `AdviseRange(offset, length, advice) error` | Set access pattern hint for a portion |
| `Protect(prot) error` | Change protection for entire mapping |
| `ProtectRange(offset, length, prot) error` | Change protection for a portion |
| `Lock() error` | Lock entire mapping in RAM |
| `LockRange(offset, length) error` | Lock a portion in RAM |
| `Unlock() error` | Unlock entire mapping |
| `UnlockRange(offset, length) error` | Unlock a portion |
| `Bytes() []byte` | Slice over the entire mapping (do not use after Resize/Unmap) |
| `Pointer() unsafe.Pointer` | Raw pointer to mapping start (preferred for DB use) |
| `Size() int64` | Current mapping size in bytes |

### Protection Flags

| Constant | Value |
|---|---|
| `ProtNone` | No access |
| `ProtRead` | Read access |
| `ProtWrite` | Write access |
| `ProtExec` | Execute access |

Combine with bitwise OR: `ProtRead | ProtWrite`

### Map Flags

| Constant | Corresponds to | Description |
|---|---|---|
| `MapShared` | `MAP_SHARED` | Changes are visible to other processes and written to file |
| `MapPrivate` | `MAP_PRIVATE` | Copy-on-write; changes are private to this mapping |
| `MapAnon` | `MAP_ANONYMOUS` | Anonymous mapping (no file backing) |
| `MapFixed` | `MAP_FIXED` | Force exact address |
| `MapPopulate` | `MAP_POPULATE` | Prefault page tables (reduces page faults on first access) |
| `MapHugeTLB` | `MAP_HUGETLB` | Use transparent huge pages |
| `MapNoReserve` | `MAP_NORESERVE` | Don't reserve swap space |

### Advice Constants

| Constant | `madvise` behavior |
|---|---|
| `AdviceNormal` | No special treatment |
| `AdviceRandom` | Expect random access (disables readahead) |
| `AdviceSequential` | Expect sequential access (aggressive readahead) |
| `AdviceWillNeed` | Prefault pages into memory |
| `AdviceDontNeed` | Free pages, data will be re-read from file |
| `AdviceRemove` | Free page table entries |
| `AdviceDontFork` | Mapping won't be inherited across fork |
| `AdviceHugePage` | Prefer huge pages |
| `AdviceNoHugePage` | Prefer regular pages |

### Sync Flags

| Constant | Description |
|---|---|
| `SyncSync` | Synchronous flush (`MS_SYNC`) |
| `SyncAsync` | Asynchronous flush (`MS_ASYNC`) |
| `SyncInvalidate` | Invalidate cached data (`MS_INVALIDATE`) |

### Error Variables

| Variable | Meaning |
|---|---|
| `ErrNotMapped` | Operation on an unmapped region |
| `ErrInvalidSize` | Size must be positive and page-aligned |
| `ErrResizeFailed` | `mremap` returned an error |
| `ErrMapFailed` | `mmap` returned an error |
| `ErrUnmapFailed` | `munmap` returned an error |
| `ErrSyncFailed` | `msync` returned an error |
| `ErrAdviseFailed` | `madvise` returned an error |
| `ErrProtectFailed` | `mprotect` returned an error |
| `ErrLockFailed` | `mlock` returned an error |
| `ErrUnlockFailed` | `munlock` returned an error |

## Why mremap?

Most Go mmap libraries handle growth by calling `munmap` + `mmap`, which:

1. Tears down the entire virtual address range
2. May assign a completely new address
3. Causes `SIGBUS` if any goroutine accesses the old mapping between the unmap and remap

`mremap` solves this by letting the kernel resize in-place (or relocate atomically). The `relocated` return value tells you whether the base address changed, so you can invalidate cached pointers.

### Typical DB grow pattern

```go
// Old way (munmap + mmap, SIGBUS-prone):
func ensureSize(old mmap.MMap, size int64) (mmap.MMap, error) {
    old.Unmap()           // window where any access = SIGBUS
    return mmap.Map(...)  // may get new address
}

// New way (mremap, atomic):
func ensureSize(region *embeddbmmap.MappedRegion, size int64) error {
    relocated, err := region.Resize(size)
    if err != nil {
        return err
    }
    if relocated {
        // Invalidate cached base pointers
        db.base = region.Pointer()
    }
    return nil
}
```

## Why two accessors?

- **`Pointer()`** returns an `unsafe.Pointer` with zero allocation overhead. Use this for database hot paths where you compute offsets into the mapping via pointer arithmetic. The GC doesn't track `unsafe.Pointer`, so large mappings don't inflate the GC's scan workload.

- **`Bytes()`** returns a `[]byte` slice. More ergonomic, but the slice header is visible to the GC. For a 1GB mapping, the GC just sees a 3-word slice header, but if you hold a reference to the returned slice across Resize/Unmap, you get a use-after-free. Prefer `Pointer()` for long-lived mappings.

## Safety

- `MappedRegion` has a finalizer that calls `Unmap()` — but you should always call `Unmap()` explicitly. The finalizer is a safety net, not a primary cleanup mechanism.
- After `Resize()`, any previously obtained `Pointer()` or `Bytes()` values are invalid. Always re-acquire them.
- After `Unmap()`, `Pointer()` returns `nil` and `Bytes()` returns `nil`.
- Sizes must be page-aligned. Use `PageSize()` to align: `aligned := ((size + PageSize() - 1) / PageSize()) * PageSize()`

## Platform Support

| Platform | Status | Resize Strategy | Notes |
|---|---|---|---|
| Linux (amd64) | **Full support** | `mremap` — atomic, no SIGBUS window | All features available |
| macOS | **Not supported** | Would need `munmap`+`mmap` fallback | `mremap` unavailable; `Resize` always relocates |
| Windows | **Not supported** | Would need `UnmapViewOfFile`+`MapViewOfFile` | Completely different API (`CreateFileMapping`, etc.) |

Building on macOS or Windows will succeed, but calling any mmap function will return a descriptive runtime error. `PageSize()` will panic.

### Adding macOS or Windows support

To add support for another platform, create a `mmap_{platform}.go` file with a `//go:build {platform}` tag. The file must implement these functions (see `mmap_linux.go` as reference):

| Function | Linux syscall | macOS equivalent | Windows equivalent |
|---|---|---|---|
| `pageSize()` | `unix.Getpagesize()` | Same | `os.Getpagesize()` |
| `mapFile()` | `SYS_MMAP` | Same POSIX | `CreateFileMapping` + `MapViewOfFile` |
| `mapAnonymous()` | `SYS_MMAP` MAP_ANON | Same | `CreateFileMapping(INVALID_HANDLE_VALUE)` |
| `unmap()` | `SYS_MUNMAP` | Same | `UnmapViewOfFile` + `CloseHandle` |
| `resize()` | `SYS_MREMAP` | `munmap`+`mmap` (always relocates) | `UnmapViewOfFile`+`MapViewOfFile` (always relocates) |
| `sync()` | `SYS_MSYNC` | Same | `FlushViewOfFile` |
| `advise()` | `SYS_MADVISE` | Subset | No equivalent — no-op |
| `protect()` | `SYS_MPROTECT` | Same | `VirtualProtect` |
| `lock()` | `SYS_MLOCK` | Same | `VirtualLock` |
| `unlock()` | `SYS_MUNLOCK` | Same | `VirtualUnlock` |

Key differences for non-Linux platforms:

**macOS**: Has the same POSIX mmap API but **no `mremap`**. The `resize()` implementation must:
1. `mmap` a new region of `newSize` bytes
2. `memcpy` the old data
3. `munmap` the old region
4. Return `relocated=true` (always, since the address changes)

The `Advise` constants `AdviceDontFork`, `AdviceHugePage`, `AdviceNoHugePage`, `AdviceRemove` don't exist on macOS — return `ErrAdviseFailed` or no-op.

**Windows**: Completely different API. `CreateFileMapping` creates a mapping object, `MapViewOfFile` maps it. No `madvise` equivalent. `Protect` uses `VirtualProtect`. File descriptors become `HANDLE`s. The `Map(fd int, ...)` API may need adjustment since `os.File.Fd()` returns a `HANDLE` on Windows.

## License

MIT