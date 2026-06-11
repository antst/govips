# Data Model: Streaming I/O

## Entities

### sourceEntry (internal)

Registry entry for a streaming source. Not exported.

| Field    | Type         | Description |
|----------|--------------|-------------|
| reader   | `io.Reader`  | The Go reader providing image data |
| seeker   | `io.Seeker`  | Non-nil if reader implements `io.Seeker` |
| mu       | `sync.Mutex` | Per-instance lock serializing callback invocations |
| lastErr  | `error`      | Last error from reader, stored for propagation to Go caller |

**Lifecycle**: Created by `registerSource()` → used during
`vips_image_new_from_source()` → removed by `deregisterSource()`
immediately after load completes (success or failure).

### targetEntry (internal)

Registry entry for a streaming target. Not exported.

| Field    | Type         | Description |
|----------|--------------|-------------|
| writer   | `io.Writer`  | The Go writer receiving encoded image data |
| mu       | `sync.Mutex` | Per-instance lock serializing callback invocations |
| lastErr  | `error`      | Last error from writer, stored for propagation to Go caller |

**Lifecycle**: Created by `registerTarget()` → used during
`vips_*save_target()` → removed by `deregisterTarget()` after save
completes (success or failure), including after "end" signal fires.

### Callback Registry (internal)

Global registry mapping integer handles to source/target entries.

| Field           | Type                    | Description |
|-----------------|-------------------------|-------------|
| sources         | `map[int]*sourceEntry`  | Active source entries |
| targets         | `map[int]*targetEntry`  | Active target entries |
| mu              | `sync.Mutex`            | Protects map access |
| nextHandle      | `int`                   | Monotonically increasing handle counter |

**Thread safety**: The registry mutex (`mu`) protects map reads/writes.
Per-entry mutexes protect the actual reader/writer invocations. These
are separate locks to avoid holding the global lock during I/O.

## State Transitions

### Source Entry

```
(none) → Registered → Active (callbacks being invoked) → Deregistered
```

- `Registered`: Entry exists in registry, callbacks not yet invoked.
  Transition: `vips_image_new_from_source()` begins processing.
- `Active`: C trampolines are calling read/seek via exported Go
  functions. Per-instance mutex serializes calls.
  Transition: Load completes (success or error).
- `Deregistered`: Entry removed from registry. Handle is stale.
  Any subsequent callback with this handle returns -1 (error).

### Target Entry

```
(none) → Registered → Active → Ending → Deregistered
```

- `Registered`: Entry exists in registry.
  Transition: `vips_*save_target()` begins encoding.
- `Active`: C trampolines are calling write.
  Transition: Encoding completes, libvips fires "end" signal.
- `Ending`: "end" callback fires. Go side can flush/finalize.
  Transition: "end" handler returns.
- `Deregistered`: Entry removed from registry.

## Error Propagation Model

1. Go reader/writer returns an error during callback.
2. Error is stored in entry's `lastErr` field.
3. C trampoline returns -1 to libvips.
4. libvips aborts the operation and sets its error buffer.
5. Go caller receives the libvips error via `handleVipsError()`.
6. Additionally, the stored `lastErr` is checked and wrapped into
   the returned error for full context (both libvips message and
   original Go error).
