# Implementation Plan: Streaming I/O

**Branch**: `001-streaming-io` | **Date**: 2026-04-14 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/001-streaming-io/spec.md`

## Summary

Add `io.Reader`-based loading and `io.Writer`-based saving to govips
using libvips VipsSourceCustom/VipsTargetCustom APIs. This enables
image processing without buffering the full compressed input/output
in Go memory. The implementation uses a global callback registry with
C trampolines and per-instance mutex serialization.

## Technical Context

**Language/Version**: Go 1.23+ with CGo (C11)
**Primary Dependencies**: libvips 8.14+ (system), testify (test-only)
**Storage**: N/A
**Testing**: `go test` via `make test`, golden file comparison, testify
**Target Platform**: Linux (CI), macOS (development)
**Project Type**: Library (Go bindings for native C library)
**Performance Goals**: No regression vs. buffer path; streaming should
reduce peak memory proportional to input size
**Constraints**: CGo callback bridge required; per-instance
serialization; no new Go dependencies
**Scale/Scope**: 3 new Go files, 2 new C files, ~600-800 lines total

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Evidence |
|-----------|--------|----------|
| I. CGo Safety | PASS | SourceCustom/TargetCustom are g_object_unref'd after use. Callback registry deregisters on completion. Leak detector validates zero live allocations. Per-instance mutex prevents concurrent access. |
| II. API Stability | PASS | Purely additive: new functions `LoadImageFromReader`, `SaveToWriter`, `SetPipeReadLimit`. All existing API unchanged (FR-010). |
| III. Test Coverage with Golden Files | PASS | Streaming roundtrip tests compare output against buffer path for each format. Error path tests via testify. |
| IV. Performance-First | PASS | Delegates to libvips C functions for all I/O. No Go-side buffer copies. Per-callback CGo overhead is negligible vs. I/O latency. |
| V. Minimal Dependencies | PASS | Zero new Go dependencies. Uses only libvips C API (VipsSourceCustom/VipsTargetCustom, available since 8.9, project requires 8.14+). |

**Gate result**: All principles satisfied. No violations to justify.

## Project Structure

### Documentation (this feature)

```text
specs/001-streaming-io/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── public-api.md    # Phase 1 output
└── tasks.md             # Phase 2 output (/speckit.tasks)
```

### Source Code (repository root)

```text
vips/
├── stream.h             # C declarations: trampolines, helper functions
├── stream.c             # C implementations: source/target creation,
│                        #   signal connection, trampolines
├── stream.go            # Go: callback registry, exported Go callbacks,
│                        #   public API (LoadImageFromReader, SaveToWriter)
├── stream_test.go       # Tests: per-format roundtrip, error paths,
│                        #   concurrency, leak detection
└── govips.go            # Modified: add SetPipeReadLimit
```

**Structure Decision**: All streaming code in dedicated `stream.*` files.
This keeps the feature self-contained, easy to review, and avoids
bloating existing files. `SetPipeReadLimit` goes in `govips.go` because
it's a global config function alongside `Startup`/`Shutdown`.

## Complexity Tracking

No constitution violations to justify.

---

## Phase 0: Research (Complete)

See [research.md](research.md). All technical decisions resolved:
- Callback bridge: custom registry (not `cgo.Handle`)
- Source loading: `vips_image_new_from_source()` (auto-detection)
- Target saving: format-specific `vips_*save_target()` functions
- Source lifetime: release immediately after decode
- End vs finish: use "end" signal
- Version gating: not needed (8.14 > 8.9)
- File organization: dedicated `stream.*` files

## Phase 1: Design (Complete)

See [data-model.md](data-model.md), [contracts/public-api.md](contracts/public-api.md),
[quickstart.md](quickstart.md).

### Key Design Decisions

**C Bridge Layer** (`stream.h` / `stream.c`):

```c
// Exported Go functions (called by C trampolines)
extern gint64 goSourceReadCb(int handle, void *buf, gint64 len);
extern gint64 goSourceSeekCb(int handle, gint64 offset, int whence);
extern gint64 goTargetWriteCb(int handle, const void *data, gint64 len);
extern int    goTargetEndCb(int handle);

// C helper functions (called by Go)
VipsSourceCustom *create_source_custom(int handle, int has_seek);
VipsTargetCustom *create_target_custom(int handle);
int load_from_source(VipsSourceCustom *source, LoadParams *params);
int save_jpeg_to_target(SaveParams *params, VipsTargetCustom *target);
int save_png_to_target(SaveParams *params, VipsTargetCustom *target);
int save_webp_to_target(SaveParams *params, VipsTargetCustom *target);
int save_heif_to_target(SaveParams *params, VipsTargetCustom *target);
int save_tiff_to_target(SaveParams *params, VipsTargetCustom *target);
int save_gif_to_target(SaveParams *params, VipsTargetCustom *target);
```

**Callback Flow** (read example):

```
libvips worker thread
  → C trampoline source_read_handler(source, buf, len, user_data)
    → extract handle from user_data (GPOINTER_TO_INT)
    → call exported Go function goSourceReadCb(handle, buf, len)
      → registry lookup: handle → *sourceEntry
      → entry.mu.Lock() (per-instance serialization)
      → entry.reader.Read(buf[:len])
      → entry.mu.Unlock()
      → return bytes read (or 0 for EOF, -1 for error)
```

**Error Propagation**:

```
Go reader returns error
  → stored in entry.lastErr
  → trampoline returns -1
  → libvips sets error buffer, aborts operation
  → Go receives libvips error via handleVipsError()
  → Go wraps both errors: "vips error: ... (caused by: reader error)"
```

**SaveToWriter Format Dispatch** (mirrors existing Export pattern):

```go
func (r *ImageRef) SaveToWriter(w io.Writer, format ImageType,
    params *ExportParams) error {
    // 1. Create target, register writer
    // 2. Dispatch by format:
    switch format {
    case ImageTypeJPEG:  C.save_jpeg_to_target(&saveParams, target)
    case ImageTypePNG:   C.save_png_to_target(&saveParams, target)
    case ImageTypeWEBP:  C.save_webp_to_target(&saveParams, target)
    case ImageTypeHEIF:  C.save_heif_to_target(&saveParams, target)
    case ImageTypeTIFF:  C.save_tiff_to_target(&saveParams, target)
    case ImageTypeGIF:   C.save_gif_to_target(&saveParams, target)
    }
    // 3. Cleanup: unref target, deregister writer
    // 4. Check entry.lastErr, combine with vips error
}
```

### Constitution Re-Check (Post-Design)

| Principle | Status | Notes |
|-----------|--------|-------|
| I. CGo Safety | PASS | C objects unref'd in defer. Registry cleanup on all paths (success + error). Per-instance mutex prevents data races. |
| II. API Stability | PASS | Three new exports, zero changes to existing. |
| III. Test Coverage | PASS | Plan includes per-format roundtrip, error propagation, concurrency, and leak detection tests. |
| IV. Performance-First | PASS | No buffer copies. One CGo call per read/write chunk. libvips manages its own I/O scheduling. |
| V. Minimal Dependencies | PASS | Zero new dependencies. |

## Phase 2: Task Decomposition

Task decomposition will be generated by `/speckit.tasks` based on this
plan and the spec's user stories.

**Anticipated phases**:

1. **Setup**: Create `stream.h`, `stream.c`, `stream.go` file
   scaffolding with callback registry infrastructure.
2. **Foundation**: Implement C trampolines + Go exported callbacks +
   callback registry with tests.
3. **User Story 1 (P1)**: `LoadImageFromReader` — source creation,
   signal connection, load-from-source C wrapper, Go public API,
   per-format roundtrip tests.
4. **User Story 2 (P2)**: `SaveToWriter` — target creation, signal
   connection, format-specific save-to-target C wrappers, Go public
   API, per-format roundtrip tests.
5. **User Story 3 (P3)**: End-to-end tests — streaming pipeline,
   HEIC→JPEG conversion, concurrency, leak detection.
6. **Polish**: `SetPipeReadLimit`, observability (govipsLog integration),
   documentation.

## Notes

- The `SaveToWriter` implementation reuses `SaveParams` from foreign.go.
  The C `save_*_to_target` functions follow the same parameter pattern
  as existing `save_*_buffer` functions but write to a VipsTargetCustom
  instead of returning a buffer.
- `ImageRef.buf` will be nil for stream-loaded images. Code that
  accesses `ImageRef.buf` (currently only in `Close()` where it's set
  to nil) is unaffected.
- The `incOpCounter` pattern should be used for streaming operations
  (e.g., `"load_source"`, `"save_jpeg_target"`) to maintain
  observability parity with the buffer path.
