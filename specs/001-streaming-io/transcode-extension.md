# Extension: End-to-End Streaming Transcode

**Extends**: spec.md / plan.md (001-streaming-io)
**Date**: 2026-06-12
**Consumer**: alkem-io/file-service spec 020-stream-uploads (parallel
workstream; 020 ships with its pixel-budget guard FR-010 and does not
block on this).

## Problem

The base feature forced `vips_image_copy_memory()` after every
streaming load, so a transcode still materialized the full decoded
frame in RAM. This extension removes that requirement with two decode
strategies and a one-shot pipeline API.

## Requirements → implementation map

| # | Requirement | Implementation | Tests |
|---|-------------|----------------|-------|
| 1 | Sequential fast path | `ImportParams.Access = AccessSequential` keeps the source connected (lazy decode); released on `ImageRef.Close`. `TranscodeStream` uses it by default. | `TestTranscodeStream_SequentialMemoryBounded_PixelBomb`, `TestLoadImageFromReader_SequentialIsLazy` |
| 2 | Disk-backed random access | Default load materializes: memory at or below threshold, unlinked `.v` scratch file above. Knobs: `SetStreamDiscThreshold` (default `VIPS_DISC_THRESHOLD` env or 100 MB), `SetStreamScratchDir` (default `os.TempDir()`). Source still released right after materialization (early-release semantics preserved on this path; only the sequential path keeps the source connected). | `TestDiscBackedLoad_MemoryBounded`, `TestDiscBackedLoad_ByteIdentityAndScratchCleanup` |
| 3 | Caller contract: io.Reader/io.Writer only | `TranscodeStream(r io.Reader, w io.Writer, opts)`; `io.Writer` IS the chunk callback. No bespoke callback API; composes with `io.MultiWriter`/`io.Pipe`/`io.Copy`. `SaveToReader` deliberately not added yet (thin `io.Pipe` wrapper if ever needed). | all transcode tests |
| 4 | Byte identity | Both paths reuse the exact `Export*` parameter mapping; sequential vs materialized vs buffer pixels are identical. | `TestTranscodeStream_SequentialByteIdentity` (6 formats), `TestDiscBackedLoad_ByteIdentityAndScratchCleanup`, `TestTranscodeStream_AutoRotateMaterializes` |
| 5a | RSS budget | libvips tracked-memory sampling at every output chunk; hard ceiling = decoded-size/4 for a 144 MB frame. Codec-internal allocations (libheif, libjpeg progressive) are outside this proxy — documented. | `..._PixelBomb`, `TestDiscBackedLoad_MemoryBounded` |
| 5b | Incremental read | Counting reader: sequential load consumes <20% of input at return (header only); first output chunk arrives with <50% of input consumed. | `TestLoadImageFromReader_SequentialIsLazy`, `..._PixelBomb` |
| 5c | Chunked write | Instrumented writer: ≥4 chunks during encode. | `..._PixelBomb`, `TestDiscBackedLoad_MemoryBounded` |
| 5d | Pixel bomb | 1.7 MB compressed → 144 MB decoded fixture; streams within budget. | `..._PixelBomb` |
| 5e | Truncated stream | Sequential: error surfaces at the first full pixel pass (SaveToWriter), wrapped with reader error. Materialized: error surfaces at load. | `TestTranscodeStream_TruncatedSequential`, `TestDiscBackedLoad_Truncated`, `TestLoadImageFromReader_TruncatedInput` |
| 5f | Slow/blocking writer | 2 s stall mid-encode: bounded memory growth (<32 MB observed budget), no deadlock against registry/per-instance mutexes, byte-identical output. | `TestTranscodeStream_SlowWriterBackpressure` |
| 6 | Docs | README §16: sequential-safe op table, codec caveats, error-surfacing semantics. | — |

## Key design decisions

- **Lazy C loader, Go-side policy.** `load_from_source` now returns the
  lazy image; Go decides materialization (memory / disc / none). The
  decoded size is computed from the header
  (`image_decoded_size`), so the threshold decision needs no pixel
  decode.
- **Scratch files are unlinked immediately** after the random-access
  reopen — the open file descriptor keeps the data alive (POSIX), so
  scratch never leaks even on crash, and the scratch dir is fully
  controllable from Go without env tricks.
- **Streaming operations build uncached** (`vips_object_build` instead
  of `vips_cache_operation_buildp`): cache keys include the unique
  source/target object so hits are impossible; caching would only pin
  sources and lazy images past their natural lifetime and evict useful
  cache entries.
- **Orientation-driven path selection.** EXIF orientation is header
  metadata; `TranscodeStream` reads it before any pixel decode and
  materializes only for orientations ≥3 (1 = no-op, 2 = horizontal
  flip, both sequential-safe).
- **Progressive JPEG is the documented streaming killer**: progressive
  decode buffers all input before the first row; progressive encode
  (govips' default `Interlace: true`!) buffers the whole image before
  the first output byte. `TranscodeStream` keeps byte-identity with
  `Export*` defaults rather than silently switching to baseline;
  callers that want true streaming pass `Interlaced: false`.

## Semantics changes vs base spec

- FR-008 (early source release) now applies to the default
  (materialized) path only. With `AccessSequential` the source and the
  caller's reader stay connected until `ImageRef.Close()` — that is the
  point of the mode. Truncation errors correspondingly move from load
  time to the first full pass over the pixels (documented in godoc and
  README).
