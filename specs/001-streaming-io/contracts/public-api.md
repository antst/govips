# Public API Contract: Streaming I/O

## New Exported Functions

### LoadImageFromReader

```go
// LoadImageFromReader loads an image from the given io.Reader using
// libvips streaming (VipsSourceCustom). If r also implements
// io.Seeker, seek is exposed to libvips for efficient random-access
// loading. Otherwise libvips uses sequential mode with automatic
// header buffering.
//
// The reader is consumed during this call and is not retained after
// LoadImageFromReader returns. Callers may close their reader
// immediately after this function returns.
//
// params may be nil for default import settings.
func LoadImageFromReader(r io.Reader, params *ImportParams) (*ImageRef, error)
```

**Behavior contract**:
- Returns `(*ImageRef, nil)` on success.
- Returns `(nil, error)` on failure. Error includes both the libvips
  error message and the original Go reader error if one occurred.
- The returned `ImageRef` has the same behavior as one created by
  `LoadImageFromBuffer` — all operations work identically.
- The reader is NOT retained on the `ImageRef`. The `ImageRef.buf`
  field will be nil (no buffer backing).
- Thread-safe: multiple goroutines can call this concurrently with
  different readers.
- Requires govips to be initialized (`Startup` called).

### SaveToWriter

```go
// SaveToWriter encodes the image in the specified format and writes
// the encoded bytes directly to w using libvips streaming
// (VipsTargetCustom). The writer receives chunks of encoded data
// as they are produced.
//
// format specifies the output image type (e.g., ImageTypeJPEG).
// params may be nil for default export settings.
//
// The writer is consumed during this call and is not retained after
// SaveToWriter returns.
func (r *ImageRef) SaveToWriter(w io.Writer, format ImageType, params *ExportParams) error
```

**Behavior contract**:
- Returns `nil` on success.
- Returns `error` on failure. Error includes both the libvips error
  message and the original Go writer error if one occurred.
- Supported formats: ImageTypeJPEG, ImageTypePNG, ImageTypeWEBP,
  ImageTypeHEIF, ImageTypeTIFF, ImageTypeGIF.
- Unsupported format returns an error (not a panic).
- Output is byte-identical to the corresponding `Export*` method
  with the same parameters.
- The ImageRef must not be closed before calling this method.
- Thread-safe: serialized by the ImageRef's existing lock.

### SetPipeReadLimit

```go
// SetPipeReadLimit sets the maximum number of bytes libvips will
// buffer when loading from a non-seekable source. This controls
// memory usage for sequential (pipe) mode sources. The default is
// approximately 1 GB.
//
// Must be called before any streaming load operations.
func SetPipeReadLimit(bytes int64)
```

**Behavior contract**:
- Calls `vips_pipe_read_limit_set()` in libvips.
- Affects all subsequent `LoadImageFromReader` calls with
  non-seekable readers.
- No return value (libvips function is void).

## Unchanged Existing Functions

The following functions remain unchanged and backward-compatible:

- `NewImageFromReader(r io.Reader) (*ImageRef, error)` — still
  calls `io.ReadAll` internally.
- `LoadImageFromBuffer(buf []byte, params *ImportParams) (*ImageRef, error)`
- `NewImageFromBuffer(buf []byte) (*ImageRef, error)`
- `NewImageFromFile(file string) (*ImageRef, error)`
- `LoadImageFromFile(file string, params *ImportParams) (*ImageRef, error)`
- All `Export*` methods on `ImageRef`.

## Type Reuse

- `ImportParams` — reused as-is for `LoadImageFromReader`.
- `ExportParams` — reused as-is for `SaveToWriter`.
- `ImageType` — reused for format selection in `SaveToWriter`.
- `ImageRef` — returned from `LoadImageFromReader`, used as
  receiver for `SaveToWriter`.
