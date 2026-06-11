# Feature Specification: Streaming I/O via VipsSourceCustom/VipsTargetCustom

**Feature Branch**: `001-streaming-io`
**Created**: 2026-04-14
**Status**: Draft
**Input**: User description: "Add streaming support via libvips VipsSourceCustom/VipsTargetCustom APIs"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Stream-Load an Image from io.Reader (Priority: P1)

A service receives image data from an HTTP request body, S3 download
stream, or file handle. The caller passes the `io.Reader` directly to
govips without first buffering the entire image into a `[]byte`. govips
wraps the reader as a `VipsSourceCustom`, and libvips pulls bytes on
demand through C callbacks.

**Why this priority**: This is the foundational capability. Without
stream-based loading, stream-based saving has no source image to
operate on. It also delivers the largest memory savings since compressed
input is typically the largest allocation.

**Independent Test**: Open a JPEG file as an `os.File` (which
implements `io.Reader`), pass it to `LoadImageFromReader`, verify the
resulting `ImageRef` has correct dimensions and format, and confirm no
full-file buffer exists in Go memory.

**Acceptance Scenarios**:

1. **Given** a valid JPEG file opened as `io.Reader`, **When**
   `LoadImageFromReader(r, nil)` is called, **Then** an `ImageRef` is
   returned with correct width, height, and format matching the
   buffer-based `LoadImageFromBuffer` result for the same file.
2. **Given** an `io.ReadSeeker` (e.g., `os.File`), **When**
   `LoadImageFromReader(r, nil)` is called, **Then** libvips receives
   both read and seek callbacks, enabling efficient random-access
   loading for formats like HEIF.
3. **Given** a plain `io.Reader` (no seek), **When**
   `LoadImageFromReader(r, nil)` is called, **Then** libvips falls
   back to sequential access (buffering headers internally), and the
   image loads successfully.
4. **Given** a reader that returns an error mid-stream, **When**
   `LoadImageFromReader(r, nil)` is called, **Then** the error
   propagates as a Go error and no resources are leaked.

---

### User Story 2 - Stream-Save an Image to io.Writer (Priority: P2)

After processing an image (loaded via any method), the caller wants to
encode the result directly into an `io.Writer` — an HTTP response body,
an S3 upload stream, or a file handle — without materializing the
entire encoded output as a `[]byte` first.

**Why this priority**: Completes the streaming pipeline. Without this,
callers still need to buffer the full encoded output even if they
streamed the input.

**Independent Test**: Load an image (buffer-based is fine), call
`SaveToWriter(w, ImageTypeJPEG, nil)` where `w` is a `bytes.Buffer`,
verify the buffer contains a valid JPEG identical to what `ExportJpeg`
produces.

**Acceptance Scenarios**:

1. **Given** a loaded `ImageRef` and a `bytes.Buffer` as `io.Writer`,
   **When** `SaveToWriter(w, ImageTypeJPEG, params)` is called,
   **Then** the buffer contains a valid JPEG that is byte-identical
   to the `ExportJpeg` output with the same params.
2. **Given** a loaded `ImageRef`, **When** `SaveToWriter` is called
   for each supported format (JPEG, PNG, WebP, HEIF, TIFF, GIF),
   **Then** each produces valid output byte-identical to the
   corresponding `Export*` method.
3. **Given** a writer that returns an error, **When** `SaveToWriter`
   is called, **Then** the error propagates as a Go error and no
   resources are leaked.

---

### User Story 3 - End-to-End Streaming Pipeline (Priority: P3)

A service streams an image from an input source, processes it
(resize, format conversion), and streams the result to an output
destination — all without holding the full input or output in memory.
This is the primary use case for file-service-go handling large files.

**Why this priority**: This is the integration scenario that validates
the P1 and P2 capabilities work together. It demonstrates the actual
memory savings that motivate this feature.

**Independent Test**: Open a large test image as `io.Reader`, load via
`LoadImageFromReader`, apply a resize, save via `SaveToWriter` to an
output file, verify the output is valid and that peak memory stayed
bounded.

**Acceptance Scenarios**:

1. **Given** a HEIC file opened as `io.ReadSeeker`, **When** loaded
   via `LoadImageFromReader`, converted to JPEG, and saved via
   `SaveToWriter`, **Then** the output is a valid JPEG with correct
   dimensions.
2. **Given** a streaming pipeline processing a test image, **When**
   compared to the equivalent buffer-based pipeline (same operations,
   same parameters), **Then** the encoded output is byte-identical.
3. **Given** a streaming pipeline, **When** the `ImageRef` is closed
   and all writers/readers are released, **Then** the leak detector
   reports zero live allocations.

---

### Edge Cases

- What happens when a reader returns `io.EOF` prematurely (truncated
  input)? libvips MUST return an error, not produce a corrupt image.
- What happens when `LoadImageFromReader` is called with an
  `io.Reader` wrapping an empty stream? MUST return a clear error.
- What happens when `SaveToWriter` is called on a closed `ImageRef`?
  MUST return an error, not panic.
- What happens when multiple goroutines call `LoadImageFromReader`
  concurrently with different readers? The callback registry MUST
  handle concurrent registrations safely.
- What happens when libvips calls the read callback after the Go
  reader has been garbage collected? The callback registry MUST
  prevent this by preventing GC of registered readers.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Library MUST provide `LoadImageFromReader(r io.Reader,
  params *ImportParams) (*ImageRef, error)` that loads an image by
  streaming bytes from `r` via libvips `VipsSourceCustom`.
- **FR-002**: Library MUST provide `(*ImageRef).SaveToWriter(w
  io.Writer, format ImageType, params *ExportParams) error` that
  encodes the image and streams bytes to `w` via libvips
  `VipsTargetCustom`.
- **FR-003**: If the provided `io.Reader` also implements `io.Seeker`,
  the library MUST expose the seek callback to libvips for
  random-access loading.
- **FR-004**: If the provided `io.Reader` does not implement
  `io.Seeker`, the library MUST omit the seek callback, letting
  libvips fall back to sequential buffering.
- **FR-005**: Supported formats for streaming load MUST include: JPEG,
  PNG, WebP, HEIF/HEIC, TIFF, GIF.
- **FR-006**: Supported formats for streaming save MUST include: JPEG,
  PNG, WebP, HEIF/HEIC, TIFF, GIF.
- **FR-007**: The CGo callback bridge MUST use a global callback
  registry (synchronized map of integer handles to Go functions) with
  C trampoline functions for the read, seek, write, and end callbacks
  (libvips "end" signal; the deprecated "finish" signal is not used).
- **FR-008**: Registered Go readers/writers MUST be kept alive
  (preventing GC) while the corresponding C source/target is in use.
  The SourceCustom and its registered reader MUST be released
  (unref'd and deregistered) immediately after the image is fully
  decoded — not held until `ImageRef.Close()`. This frees upstream
  resources (HTTP connections, file handles) as early as possible.
  TargetCustom and its writer MUST be released after the save
  operation completes.
- **FR-009**: The callback registry MUST be safe for concurrent access
  from multiple goroutines and libvips worker threads.
- **FR-009a**: Each SourceCustom/TargetCustom instance MUST serialize
  its callback invocations with a per-instance mutex so that the
  underlying Go `io.Reader`/`io.Writer` is never accessed concurrently
  by libvips worker threads. Callers MUST NOT be required to provide
  thread-safe readers or writers.
- **FR-010**: Existing public API (`LoadImageFromBuffer`,
  `NewImageFromReader`, `NewImageFromBuffer`, `NewImageFromFile`,
  `Export*` methods) MUST remain unchanged and backward-compatible.
- **FR-011**: Errors from Go readers/writers MUST propagate through
  the C callback layer and surface as Go errors from the
  `LoadImageFromReader`/`SaveToWriter` calls.
- **FR-012**: Streaming features rely on VipsSourceCustom/
  VipsTargetCustom, introduced in libvips 8.9. The project's minimum
  supported libvips is 8.14+, so no runtime version gating is
  required or implemented (see Assumptions). Builds against older
  libvips fail at compile time via the existing version floor.
- **FR-013**: Library MUST expose `SetPipeReadLimit(bytes int64)` as
  a public function that calls `vips_pipe_read_limit_set()`. This
  controls how much data libvips buffers when loading from a
  non-seekable source (no seek callback). Callers processing large
  non-seekable streams can lower this to bound memory usage.

### Key Entities

- **sourceEntry**: Registry entry for a streaming source. Holds the
  Go `io.Reader` (and optional `io.Seeker`), a per-instance mutex
  serializing callback invocations, and the last reader error for
  propagation. Registered before load; deregistered (with the
  `VipsSourceCustom` unref'd) immediately after decode completes.
- **targetEntry**: Registry entry for a streaming target. Holds the
  Go `io.Writer`, a per-instance mutex, and the last writer error.
  Registered before save; deregistered (with the `VipsTargetCustom`
  unref'd) after the save operation completes.
- **Callback Registry**: Global, mutex-protected map that assigns
  integer handles to source/target entries. Enables C trampolines
  (read/seek/write/end) to dispatch to the correct Go reader/writer.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Streaming save produces output byte-identical to the
  corresponding `Export*` method with the same parameters, and the
  streaming load + save roundtrip produces output byte-identical to
  the equivalent buffer-based pipeline, for all supported formats.
- **SC-002**: Go-side heap allocation during streaming load is
  bounded by the callback chunk size, not the input file size: the
  full compressed input is never held in Go memory. Verified by a
  memory benchmark comparing allocated bytes for the streaming vs.
  buffer-based path on the same test image.
- **SC-003**: All existing tests continue to pass unchanged (`make
  test` green).
- **SC-004**: Streaming load and save work correctly under concurrent
  access from multiple goroutines (no races, no deadlocks).
- **SC-005**: Leak detector reports zero live C allocations after
  streaming operations complete and resources are released.
- **SC-006**: HEIC-to-JPEG conversion via the streaming path produces
  a valid JPEG output.

## Assumptions

- libvips >= 8.9 is available in the build environment (streaming APIs
  were introduced in 8.9; the project already requires 8.14+).
- The Elixir Vix library's VipsSourceCustom/VipsTargetCustom
  implementation validates that this callback pattern works reliably
  from a garbage-collected language.
- The `ImportParams` and `ExportParams` structs from the existing API
  are reusable for the streaming variants without modification.
- Format-specific export params (e.g., `JpegExportParams`) are not
  needed for the initial `SaveToWriter` API; the generic
  `ExportParams` is sufficient. Format-specific streaming variants can
  be added later if needed.
- The CGo callback overhead per read/write call is negligible compared
  to the I/O and image processing time.
- The project's existing test infrastructure (golden files, testify,
  `make test`) is sufficient for validating streaming correctness.

## Clarifications

### Session 2026-04-14

- Q: Should the C trampoline serialize callback invocations to protect the caller's io.Reader/io.Writer from concurrent access by libvips worker threads? → A: Yes — serialize per-instance with a mutex on each SourceCustom/TargetCustom. Callers must not be required to provide thread-safe readers/writers.
- Q: Should SourceCustom (and the registered reader) be released immediately after decode or held until ImageRef.Close()? → A: Release immediately after successful decode. This frees upstream resources early and is the primary memory benefit of streaming.
- Q: Should govips expose `vips_pipe_read_limit_set()` for controlling how much libvips buffers when loading from a non-seekable source? → A: Yes — expose as `SetPipeReadLimit(bytes int64)` public function.
