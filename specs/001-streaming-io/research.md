# Research: Streaming I/O

## Decision 1: Callback Bridge Pattern

**Decision**: Use a global callback registry with integer handles passed
as `user_data` through C trampolines.

**Rationale**: Go function pointers cannot be passed directly to C.
The CGo docs prescribe using exported Go functions called from static C
functions. The integer-handle registry pattern is the standard approach
used across the Go ecosystem (e.g., `mattn/go-sqlite3`, `libgit2/git2go`).
govips already uses a simpler variant of this for logging callbacks.

**Alternatives considered**:
- `cgo.Handle` (Go 1.17+): Built-in handle type for passing Go values
  through C. Simpler API but still requires a C trampoline. The project
  requires Go 1.23+, so this is available. However, `cgo.Handle` does
  not provide per-instance mutex serialization — we need our own wrapper
  anyway for FR-009a.
- Direct `unsafe.Pointer` casting of Go function pointers: Violates CGo
  rules and causes crashes under GC pressure.

**Final choice**: Custom registry (not `cgo.Handle`) because we need
per-instance mutex storage alongside the reader/writer reference.

## Decision 2: Source Loading Strategy

**Decision**: Use `vips_image_new_from_source()` for loading from
a VipsSourceCustom, with format-specific parameters passed as
variadic arguments.

**Rationale**: The existing buffer-based path uses format-specific
loaders (`vips_jpegload_buffer`, etc.) with format detection happening
in Go/C. For sources, `vips_image_new_from_source()` handles format
detection internally by sniffing the stream — this is simpler and is
the intended API. Format-specific params (page, dpi, shrink) can be
passed as variadic key-value pairs.

**Alternatives considered**:
- Format-specific `vips_*load_source()` functions: More control, but
  requires Go-side format detection from stream bytes. Complex and
  error-prone since we'd need to peek at the stream without consuming
  bytes.

## Decision 3: Target Saving Strategy

**Decision**: Use format-specific `vips_*save_target()` functions
(e.g., `vips_jpegsave_target`, `vips_pngsave_target`) matching the
existing `vips_*save_buffer()` pattern.

**Rationale**: The existing save path dispatches by format to specific
functions with format-specific parameters. The target variants have
the same parameter signatures. This maintains consistency with the
existing codebase pattern and gives full control over format-specific
export options.

**Alternatives considered**:
- `vips_image_write_to_target(image, ".jpg", target, ...)`: Simpler
  but uses format extension strings rather than enum types, and the
  variadic parameter passing differs from our existing struct-based
  approach.

## Decision 4: Source Lifetime

**Decision**: Release SourceCustom and deregister the reader
immediately after `vips_image_new_from_source()` returns successfully.

**Rationale**: Once libvips has fully decoded the image into a
VipsImage, the source is no longer needed. Unlike the current buffer
path (where `ImageRef.buf` is retained because some loaders use
random-access transcoding), source-loaded images are fully materialized
in libvips memory. Releasing early frees upstream resources (HTTP
connections, file handles). Confirmed via clarification session.

**Alternatives considered**:
- Hold until `ImageRef.Close()`: Would negate memory savings and block
  upstream resources.

## Decision 5: Signal Connection Mechanism

**Decision**: Use GObject `g_signal_connect()` to register C
trampoline functions as signal handlers on VipsSourceCustom and
VipsTargetCustom objects.

**Rationale**: VipsSourceCustom/VipsTargetCustom use GObject signals
("read", "seek", "write", "end") for callbacks. This is the standard
libvips pattern — not function pointers set on a struct, but GLib
signal handlers.

**Key signals**:
- Source: "read" (required), "seek" (optional)
- Target: "write" (required), "end" (preferred over deprecated "finish")

## Decision 6: End vs Finish Signal

**Decision**: Use the "end" signal (not "finish") for target completion.

**Rationale**: The "end" signal returns `int` (0 success, -1 error),
allowing error propagation during finalization. The "finish" signal is
`void` and deprecated in newer libvips.

## Decision 7: Version Gating

**Decision**: No additional version guard needed for streaming.

**Rationale**: VipsSourceCustom/VipsTargetCustom were introduced in
libvips 8.9. The project requires 8.14+ (README) and checks for 8.10+
at runtime (govips.go). Since 8.14 > 8.9, streaming APIs are always
available when govips runs. FR-012 is satisfied by the existing
minimum version requirement. No compile-time guards needed.

## Decision 8: File Organization

**Decision**: New files `vips/stream.c`, `vips/stream.h`, and
`vips/stream.go` for all streaming code. `vips/stream_test.go` for
tests.

**Rationale**: The streaming feature is a self-contained subsystem
(callback registry, C trampolines, Go wrappers, public API). Keeping
it in dedicated files:
- Avoids bloating existing files (foreign.go is already large)
- Makes the feature easy to review as a single unit
- Follows the pattern of `image_export.go`, `image_transform.go`, etc.
- `SetPipeReadLimit` goes in `govips.go` alongside other global config

## Key Findings: libvips Streaming API

### C API Signatures

**Source callbacks**:
```c
// Read: fill buffer, return bytes read. 0 = EOF, -1 = error
gint64 read_handler(VipsSourceCustom *source, void *buffer,
                     gint64 length, void *user_data);

// Seek: return new absolute position. -1 = error/not supported
gint64 seek_handler(VipsSourceCustom *source, gint64 offset,
                     int whence, void *user_data);
```

**Target callbacks**:
```c
// Write: return bytes written. -1 = error
gint64 write_handler(VipsTargetCustom *target, const void *data,
                      gint64 length, void *user_data);

// End: return 0 success, -1 error
int end_handler(VipsTargetCustom *target, void *user_data);
```

**Loading from source**:
```c
VipsImage *vips_image_new_from_source(VipsSource *source,
                                       const char *option_string, ...);
```

**Saving to target** (format-specific):
```c
int vips_jpegsave_target(VipsImage *in, VipsTarget *target, ...);
int vips_pngsave_target(VipsImage *in, VipsTarget *target, ...);
int vips_webpsave_target(VipsImage *in, VipsTarget *target, ...);
int vips_heifsave_target(VipsImage *in, VipsTarget *target, ...);
int vips_tiffsave_target(VipsImage *in, VipsTarget *target, ...);
int vips_gifsave_target(VipsImage *in, VipsTarget *target, ...);
```

### Elixir Vix Reference

The Vix library validates this pattern from a GC'd language:
- Uses Erlang NIF resources (similar to Go's callback registry) to
  bridge between BEAM processes and C callbacks
- Registers read/write callbacks as NIF resource handles
- C trampolines dispatch to Erlang via enif_send
- Lifecycle managed by NIF resource destructor (similar to
  runtime.SetFinalizer)

The key difference: Vix uses Erlang's message passing (async), while
our Go implementation uses direct synchronous calls from the C
trampoline to exported Go functions (simpler, since CGo supports this
directly).
