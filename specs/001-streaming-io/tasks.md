# Tasks: Streaming I/O via VipsSourceCustom/VipsTargetCustom

**Input**: Design documents from `/specs/001-streaming-io/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/public-api.md, quickstart.md

**Tests**: Included — the spec mandates test coverage (Constitution Principle III, SC-001..SC-006): per-format roundtrips, error paths, concurrency, and leak detection.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- All paths are relative to the repository root

## Path Conventions

Go library with CGo: all source lives in `vips/`. New streaming code goes in dedicated `vips/stream.h`, `vips/stream.c`, `vips/stream.go`, `vips/stream_test.go` files per plan.md. `SetPipeReadLimit` goes in `vips/govips.go` alongside `Startup`/`Shutdown`.

---

## Phase 1: Setup (File Scaffolding)

**Purpose**: Create the new `stream.*` files so the C/Go bridge compiles end-to-end before any logic lands.

- [ ] T001 Create `vips/stream.h` with all C declarations from plan.md: extern Go callback declarations (`goSourceReadCb`, `goSourceSeekCb`, `goTargetWriteCb`, `goTargetEndCb`), helper prototypes (`create_source_custom(int handle, int has_seek)`, `create_target_custom(int handle)`, `load_from_source(VipsSourceCustom*, LoadParams*)`, `save_jpeg_to_target`, `save_png_to_target`, `save_webp_to_target`, `save_heif_to_target`, `save_tiff_to_target`, `save_gif_to_target`), guarded with include guards and including `foreign.h` for `LoadParams`/`SaveParams`; every declaration MUST carry an ownership-semantics comment (who unrefs the returned source/target, who owns callback buffers) per Constitution Principle I
- [ ] T002 [P] Create `vips/stream.c` skeleton: include `stream.h` and vips headers, stub helper functions returning error so the file compiles
- [ ] T003 [P] Create `vips/stream.go` skeleton: `package vips`, CGo preamble (`// #include "stream.h"`), file-level doc comment describing the streaming subsystem
- [ ] T004 Verify scaffolding compiles: `go build ./...` succeeds with the stub files (fix any CGo declaration mismatches between `stream.h` and `stream.go`)

---

## Phase 2: Foundational (Callback Bridge — Blocking Prerequisites)

**Purpose**: The callback registry, exported Go callbacks, and C trampolines that BOTH load and save paths depend on (FR-007, FR-008, FR-009, FR-009a).

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T005 Implement the callback registry in `vips/stream.go` per data-model.md: `sourceEntry` struct (`reader io.Reader`, `seeker io.Seeker`, `mu sync.Mutex`, `lastErr error`), `targetEntry` struct (`writer io.Writer`, `mu sync.Mutex`, `lastErr error`), global registry (`sources map[int]*sourceEntry`, `targets map[int]*targetEntry`, `mu sync.Mutex`, `nextHandle int`), and functions `registerSource`, `deregisterSource`, `registerTarget`, `deregisterTarget`, `lookupSource`, `lookupTarget` — registry mutex protects only map access, never held during I/O
- [ ] T006 Implement exported Go callbacks in `vips/stream.go` (`//export` directives): `goSourceReadCb(handle, buf, len)` — lookup entry, lock per-instance `mu`, call `reader.Read` into the C buffer, return bytes read / 0 on `io.EOF` / -1 on error (store in `lastErr`); `goSourceSeekCb(handle, offset, whence)` — same pattern via `seeker.Seek`, return -1 if no seeker; `goTargetWriteCb(handle, data, len)` — write full chunk via `writer.Write`; `goTargetEndCb(handle)` — return 0, store any flush error; stale handles (not in registry) return -1
- [ ] T007 Implement C trampolines in `vips/stream.c`: `source_read_handler`, `source_seek_handler`, `target_write_handler`, `target_end_handler` — each extracts the integer handle from `user_data` via `GPOINTER_TO_INT` and forwards to the corresponding exported Go callback, returning its result to libvips
- [ ] T008 [P] Add registry unit tests in `vips/stream_test.go`: concurrent `registerSource`/`deregisterSource` from multiple goroutines (run with `-race`), callbacks on a deregistered (stale) handle return -1, handles are unique and monotonically increasing

**Checkpoint**: Callback bridge compiles, registry is race-safe — user story implementation can begin.

---

## Phase 3: User Story 1 - Stream-Load an Image from io.Reader (Priority: P1) 🎯 MVP

**Goal**: `LoadImageFromReader(r io.Reader, params *ImportParams) (*ImageRef, error)` loads an image by streaming bytes through `VipsSourceCustom`, with seek support when the reader implements `io.Seeker` (FR-001, FR-003, FR-004, FR-005, FR-008, FR-011).

**Independent Test**: Open a JPEG as `os.File`, pass it to `LoadImageFromReader`, verify the resulting `ImageRef` matches the dimensions/format of `LoadImageFromBuffer` on the same file, and confirm `ImageRef.buf` is nil.

### Implementation for User Story 1

- [ ] T009 [US1] Implement `create_source_custom(int handle, int has_seek)` in `vips/stream.c`: create `vips_source_custom_new()`, `g_signal_connect` the `"read"` trampoline with `GINT_TO_POINTER(handle)` as user_data, conditionally connect the `"seek"` trampoline only when `has_seek` is non-zero (FR-003/FR-004)
- [ ] T010 [US1] Implement `load_from_source(VipsSourceCustom *source, LoadParams *params)` in `vips/stream.c` using `vips_image_new_from_source()` with format auto-detection, applying the same `LoadParams` option handling as the existing buffer load path in `vips/foreign.c`
- [ ] T011 [US1] Implement `LoadImageFromReader(r io.Reader, params *ImportParams)` in `vips/stream.go`: type-assert `io.Seeker`, `registerSource`, `C.create_source_custom(handle, hasSeek)`, call `load_from_source`, build the `*ImageRef` with nil `buf` (same construction path as `LoadImageFromBuffer` in `vips/image.go`), then on ALL paths (success and error) immediately `g_object_unref` the source and `deregisterSource` (FR-008 — release after decode, not at `Close()`); on error, wrap the stored `entry.lastErr` together with the libvips error from `handleVipsError()` (FR-011); add `incOpCounter("load_source")` for observability parity
- [ ] T012 [US1] Per-format streaming load roundtrip tests in `vips/stream_test.go`: for each of JPEG, PNG, WebP, HEIF/HEIC, TIFF, GIF (FR-005), load a test image from `resources/` via `os.File` reader and assert width/height/format match `LoadImageFromBuffer` of the same file; assert returned `ImageRef` works for a subsequent operation (e.g., `Resize`)
- [ ] T013 [US1] Seekable vs non-seekable load tests in `vips/stream_test.go`: load via `os.File` (seek path, acceptance scenario 2) and via an `io.Pipe`/`io.Reader`-only wrapper that strips `Seek` (sequential fallback, acceptance scenario 3) — both must succeed for at least JPEG and HEIF
- [ ] T014 [US1] Load error-path tests in `vips/stream_test.go`: reader that errors mid-stream propagates a Go error containing the reader's error (acceptance scenario 4), empty reader returns a clear error, truncated input returns an error (not a corrupt image); each test asserts the leak detector reports zero live allocations afterward

**Checkpoint**: `LoadImageFromReader` is fully functional and independently testable — MVP complete.

---

## Phase 4: User Story 2 - Stream-Save an Image to io.Writer (Priority: P2)

**Goal**: `(*ImageRef).SaveToWriter(w io.Writer, format ImageType, params *ExportParams) error` encodes directly into a writer via `VipsTargetCustom` for all six formats (FR-002, FR-006, FR-008, FR-011).

**Independent Test**: Load an image via the existing buffer path, call `SaveToWriter` into a `bytes.Buffer` for `ImageTypeJPEG`, and verify the output matches `ExportJpeg` with equivalent params.

### Implementation for User Story 2

- [ ] T015 [US2] Implement `create_target_custom(int handle)` in `vips/stream.c`: create `vips_target_custom_new()`, `g_signal_connect` the `"write"` and `"end"` trampolines with `GINT_TO_POINTER(handle)` as user_data (research decision: use "end", not deprecated "finish")
- [ ] T016 [US2] Implement `save_jpeg_to_target(SaveParams *params, VipsTargetCustom *target)` in `vips/stream.c` using `vips_jpegsave_target()`, mirroring the parameter handling of the existing `save_jpeg_buffer` in `vips/foreign.c`
- [ ] T017 [US2] Implement the remaining five save functions in `vips/stream.c` following the T016 pattern: `save_png_to_target` (`vips_pngsave_target`), `save_webp_to_target` (`vips_webpsave_target`), `save_heif_to_target` (`vips_heifsave_target`), `save_tiff_to_target` (`vips_tiffsave_target`), `save_gif_to_target` (`vips_gifsave_target`)
- [ ] T018 [US2] Implement `(*ImageRef).SaveToWriter(w io.Writer, format ImageType, params *ExportParams)` in `vips/stream.go`: error (not panic) on closed `ImageRef` and on unsupported format, build `SaveParams` from `ExportParams` reusing the existing mapping in `vips/foreign.go`, `registerTarget` + `C.create_target_custom(handle)`, dispatch by format to the six `save_*_to_target` functions per plan.md, then on ALL paths unref the target and `deregisterTarget` (FR-008), combining stored `entry.lastErr` with the libvips error (FR-011); serialize via the ImageRef's existing lock; add `incOpCounter("save_" + format + "_target")`
- [ ] T019 [US2] Per-format streaming save tests in `vips/stream_test.go`: for each of JPEG, PNG, WebP, HEIF, TIFF, GIF (FR-006), `SaveToWriter` into a `bytes.Buffer` and assert output is byte-identical (`bytes.Equal`) to the corresponding `Export*` method with the same params (acceptance scenarios 1–2, SC-001); verify each output re-loads as a valid image of the expected format
- [ ] T020 [US2] Save error-path tests in `vips/stream_test.go`: writer that returns an error propagates as a Go error containing the writer's error (acceptance scenario 3), `SaveToWriter` on a closed `ImageRef` returns an error without panicking, unsupported `ImageType` returns an error; each test asserts zero leaked allocations

**Checkpoint**: User Stories 1 AND 2 both work independently — full streaming load and save available.

---

## Phase 5: User Story 3 - End-to-End Streaming Pipeline (Priority: P3)

**Goal**: Validate that streaming load + process + save compose correctly with bounded memory, identical pixels, no races, and no leaks (SC-001, SC-002, SC-004, SC-005, SC-006).

**Independent Test**: Open a large test image as `io.Reader`, load via `LoadImageFromReader`, resize, save via `SaveToWriter` to an output file, and verify the output is valid.

### Implementation for User Story 3

- [ ] T021 [US3] End-to-end HEIC→JPEG pipeline test in `vips/stream_test.go`: open a HEIC resource as `os.File` (`io.ReadSeeker`), `LoadImageFromReader`, apply `AutoRotate` + resize, `SaveToWriter(w, ImageTypeJPEG, nil)`, assert output is a valid JPEG with correct dimensions (acceptance scenario 1, SC-006)
- [ ] T022 [US3] Output-equivalence test in `vips/stream_test.go`: run the same transform through the streaming pipeline and the buffer-based pipeline (`LoadImageFromBuffer` + `ExportJpeg`), assert the encoded outputs are byte-identical (acceptance scenario 2, SC-001); additionally compare the streaming result against a committed golden file in `resources/` using the helpers from `vips/image_golden_helpers_test.go` — reuse an existing golden if one matches the chosen transform, otherwise commit a new golden in the same PR per the constitution's Development Workflow (Constitution Principle III)
- [ ] T023 [US3] Concurrency stress test in `vips/stream_test.go`: N goroutines each running a full streaming load→resize→save pipeline with independent readers/writers concurrently; must pass under `go test -race` with no deadlocks (FR-009, FR-009a, SC-004); include one case where multiple goroutines call `LoadImageFromReader` simultaneously (edge case from spec)
- [ ] T024 [US3] Leak detection test in `vips/stream_test.go`: after all streaming operations (success AND error paths) complete and `ImageRef`s are closed, assert the leak detector reports zero live C allocations and the callback registry maps are empty (acceptance scenario 3, SC-005)
- [ ] T025 [US3] Memory benchmark in `vips/stream_test.go`: `BenchmarkStreamLoad` vs `BenchmarkBufferLoad` on the same large test image with `b.ReportAllocs()`, asserting (via a companion test using `testing.AllocsPerRun` or `runtime.MemStats`) that the streaming path's total Go-side allocated bytes are less than 10% of the compressed input file size (the buffer path allocates ≥100% by holding the full input), proving allocations are bounded by callback chunk size (SC-002); record before/after numbers for the PR description (Constitution Principle IV)

**Checkpoint**: All user stories independently functional and validated end-to-end.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Remaining functional requirement (FR-013), documentation, and final validation.

- [ ] T026 [P] Implement `SetPipeReadLimit(bytes int64)` in `vips/govips.go` calling `vips_pipe_read_limit_set()`, with godoc matching contracts/public-api.md (default ~1 GB, affects non-seekable sources, call before streaming loads) (FR-013)
- [ ] T027 Add `SetPipeReadLimit` test in `vips/stream_test.go`: set a small limit, load via a non-seekable reader, verify behavior (small image loads; restore default limit after the test to avoid cross-test interference)
- [ ] T028 [P] Audit godoc on all new exported symbols (`LoadImageFromReader`, `SaveToWriter`, `SetPipeReadLimit`) in `vips/stream.go` and `vips/govips.go` against the behavior contracts in `specs/001-streaming-io/contracts/public-api.md`
- [ ] T029 [P] Add a streaming I/O section to `README.md` with the load/save/pipeline examples from `specs/001-streaming-io/quickstart.md`
- [ ] T030 Run the full validation checklist from `specs/001-streaming-io/quickstart.md` and confirm `make test` is green with all existing tests unchanged (FR-010, SC-003)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately. T002/T003 parallel after T001; T004 last.
- **Foundational (Phase 2)**: Depends on Phase 1. T005 → T006 (same file, callbacks use registry) → T007 (trampolines call exported callbacks); T008 parallel with T007.
- **User Story 1 (Phase 3)**: Depends on Phase 2. T009 → T010 → T011 → T012 → T013 → T014 (C source creation → C load → Go API → tests; tests share `stream_test.go` so run sequentially).
- **User Story 2 (Phase 4)**: Depends on Phase 2 only — does NOT depend on US1 (its tests load via the existing buffer path). T015 → T016 → T017 → T018 → T019 → T020.
- **User Story 3 (Phase 5)**: Depends on US1 AND US2 (integration of both). T021–T025 share `stream_test.go`, run sequentially.
- **Polish (Phase 6)**: T026/T028/T029 parallel; T027 after T026; T030 last.

### User Story Dependencies

- **US1 (P1)**: Foundational only — independently testable against `LoadImageFromBuffer`.
- **US2 (P2)**: Foundational only — independently testable using buffer-loaded images. Shares `stream.c`/`stream.go` files with US1, so parallel work by one person requires care; with two developers, coordinate or sequence file edits.
- **US3 (P3)**: Requires US1 + US2 complete (it is the integration story by design).

### Parallel Opportunities

- Phase 1: T002 ∥ T003 (different files)
- Phase 2: T008 ∥ T007 (`stream_test.go` vs `stream.c`)
- Phase 3/4: US1 and US2 are logically independent after Phase 2; C-side tasks (T009–T010 vs T015–T017) touch `stream.c` and Go-side tasks touch `stream.go`, so true parallelism requires separate branches or careful file coordination
- Phase 6: T026 ∥ T028 ∥ T029 (different files)

---

## Parallel Example: Phase 2 → Phase 3

```bash
# After T006 completes, run in parallel:
Task: "T007 Implement C trampolines in vips/stream.c"
Task: "T008 Registry unit tests in vips/stream_test.go"

# Phase 6 parallel batch:
Task: "T026 SetPipeReadLimit in vips/govips.go"
Task: "T028 Godoc audit in vips/stream.go"
Task: "T029 README streaming section"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T004)
2. Complete Phase 2: Foundational (T005–T008) — CRITICAL, blocks all stories
3. Complete Phase 3: User Story 1 (T009–T014)
4. **STOP and VALIDATE**: `make test` green; stream-load works for all six formats with seekable and non-seekable readers
5. This alone delivers the largest memory win (compressed input no longer buffered in Go)

### Incremental Delivery

1. Setup + Foundational → callback bridge ready
2. Add US1 → validate independently → MVP (streaming load)
3. Add US2 → validate independently → full streaming save
4. Add US3 → end-to-end, concurrency, and leak validation
5. Polish → `SetPipeReadLimit`, docs, final `make test`

---

## Notes

- Tests live in `vips/stream_test.go` (single file per plan.md) — test tasks within a story are sequential, not [P]
- `ImageRef.buf` is nil for stream-loaded images; only `Close()` touches it, so no other changes needed (plan.md Notes)
- Version gating (FR-012) needs no runtime check: project requires libvips 8.14+, streaming APIs exist since 8.9 (research.md decision)
- Use `incOpCounter` for streaming ops (`"load_source"`, `"save_*_target"`) — folded into T011/T018
- Commit after each task or logical group; stop at any checkpoint to validate the story independently
