<!--
  Sync Impact Report
  ===================
  Version change: 0.0.0 → 1.0.0 (initial ratification)
  Modified principles: N/A (initial version)
  Added sections:
    - Principle I: CGo Safety
    - Principle II: API Stability
    - Principle III: Test Coverage with Golden Files
    - Principle IV: Performance-First
    - Principle V: Minimal Dependencies
    - Technical Constraints section
    - Development Workflow section
  Removed sections: N/A
  Templates requiring updates:
    - .specify/templates/plan-template.md — ✅ no updates needed
      (Constitution Check section is generic; principles applied at fill time)
    - .specify/templates/spec-template.md — ✅ no updates needed
      (template is generic; principles enforced at spec creation)
    - .specify/templates/tasks-template.md — ✅ no updates needed
      (task categorization is generic; principles enforced at task creation)
    - .specify/templates/checklist-template.md — ✅ no updates needed
    - .specify/templates/agent-file-template.md — ✅ no updates needed
  Follow-up TODOs: none
-->

# govips Constitution

## Core Principles

### I. CGo Safety

Every change that touches C interop MUST be memory-safe and
leak-free. Concrete rules:

- All C-allocated resources (VipsImage, VipsOperation, buffers)
  MUST have a deterministic Go-side release path (destructor,
  Close method, or explicit free call).
- Callers MUST NOT retain pointers to C memory beyond the
  lifetime of the owning Go object.
- New CGo bridge functions MUST document ownership semantics
  in a comment on the C declaration.
- The leak detector (`stats.go`) MUST report zero live
  allocations at test-suite exit; a non-zero count is a test
  failure.

**Rationale**: govips wraps a native C library. A single
dangling pointer or leaked VipsImage causes silent corruption
or OOM in long-running services.

### II. API Stability

The public Go API (`vips/` package exports) MUST remain
backward-compatible within a major version. Concrete rules:

- Exported function signatures, struct fields, and constants
  MUST NOT change in a breaking way without a major version
  bump.
- New functionality SHOULD be added through new functions or
  option-struct fields rather than altering existing
  signatures.
- Auto-generated bindings (`generated.go`, `generated.c`,
  `generated.h`) MUST be regenerated via `cmd/vipsgen/` and
  MUST NOT be hand-edited.
- Deprecations MUST be communicated via Go `// Deprecated:`
  comments for at least one minor release before removal.

**Rationale**: Downstream consumers depend on a stable import
path. Breaking changes without a version bump break builds
silently across the ecosystem.

### III. Test Coverage with Golden Files

Every user-facing image operation MUST have test coverage,
and visual correctness MUST be validated through golden-file
comparison. Concrete rules:

- New image operations MUST include at least one test that
  round-trips an image through the operation and compares
  the result against a committed golden file in `resources/`.
- Golden files MUST be committed alongside the code that
  produces them; PRs that add operations without golden tests
  are incomplete.
- Non-image logic (error paths, option parsing, metadata)
  MUST have standard unit tests using `testify/assert`.
- `make test` MUST pass on every commit; broken tests block
  merge.

**Rationale**: Image processing correctness cannot be
verified by assertion alone. Golden files catch regressions
in pixel output that unit assertions would miss.

### IV. Performance-First

govips exists because libvips is 4-8x faster than
alternatives. Changes MUST NOT regress that advantage.
Concrete rules:

- Hot-path changes (image load, transform, export) MUST NOT
  add unnecessary memory copies or CGo call overhead.
- New operations SHOULD delegate to libvips C functions
  rather than reimplementing in Go.
- Large buffer allocations MUST reuse existing patterns
  (e.g., letting libvips manage its own buffer pool) rather
  than introducing Go-side allocations.
- If a change is suspected to affect throughput, before/after
  benchmarks MUST be included in the PR description.

**Rationale**: Users choose govips specifically for speed.
A convenience feature that costs 2x memory or 50% throughput
undermines the library's core value proposition.

### V. Minimal Dependencies

The dependency tree MUST stay small and auditable. Concrete
rules:

- New third-party Go dependencies MUST be justified in the
  PR description; the bar is "cannot reasonably be done
  without it."
- The only required system dependency is libvips (8.14+) and
  a C compiler. Additional system libraries MUST NOT be
  introduced.
- Test-only dependencies (e.g., `testify`) are acceptable
  but MUST be confined to `_test.go` files.
- `go.sum` changes MUST be reviewed for unexpected transitive
  additions.

**Rationale**: As a low-level library, govips is embedded in
many dependency trees. Every added dependency is a supply-chain
risk and a build-time cost for every downstream consumer.

## Technical Constraints

- **Language**: Go 1.23+ with CGo (C11)
- **System dependency**: libvips 8.14+ (no vendored copy)
- **Build**: `go build ./...` and `make test` MUST succeed
  on Linux (CI) and macOS (development)
- **Code generation**: `cmd/vipsgen/` produces `generated.*`
  files via libvips introspection. These files MUST NOT be
  hand-edited; regenerate and commit.
- **Platforms**: Linux and macOS are first-class; Windows via
  WSL is best-effort.
- **License**: MIT — all contributions MUST be compatible.

## Development Workflow

- All code changes happen in git worktrees, never directly
  on `master`.
- Use `/dev` to start work (creates worktree automatically).
- Use `/stage` to wrap up (prepares a clean commit for
  landing).
- Review and land via `wtr` (fast-forward-only merge).
- Every PR MUST pass `make test` before merge.
- Golden file updates MUST be included in the same PR as the
  code that changes output.
- Issues are tracked on GitHub:
  https://github.com/davidbyttow/govips/issues

## Governance

This constitution is the authoritative source of project
standards. It supersedes informal conventions, chat
discussions, and ad-hoc PR comments.

- **Amendments** require a PR that updates this file, with a
  clear rationale in the PR description and approval from a
  maintainer.
- **Versioning** follows semantic versioning:
  - MAJOR: backward-incompatible governance/principle changes
  - MINOR: new principles or materially expanded guidance
  - PATCH: clarifications, typo fixes, non-semantic edits
- **Compliance review**: every PR and code review SHOULD
  verify alignment with these principles. Deviations MUST be
  explicitly justified in the PR description.
- **Complexity justification**: any addition that increases
  build complexity, dependency count, or CGo surface area
  MUST document why simpler alternatives were rejected.

**Version**: 1.0.0 | **Ratified**: 2026-04-14 | **Last Amended**: 2026-04-14
