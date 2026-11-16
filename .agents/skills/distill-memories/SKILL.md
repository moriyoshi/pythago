---
name: distill-memories
description: Read `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` documents, find durable knowledge that belongs in the canonical docs, and update those documents with concise synthesized findings. Targets are the root `README.md` (user-facing), the root `PLAN.md` (product plan, architecture and status), and the Go package documentation of `py`, `aio` and `asgi`. Use when journal or LTM notes have accumulated implementation, system, or user-facing knowledge that should be promoted into the project's canonical docs.
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Grep, Glob
---

# Distill Memories

## Overview

Promote durable facts from `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` into the project's canonical documentation, keeping these up to date:

a. `README.md` (repository root) ... what pythago is and how a user uses it: building an image with `pythago-dist`, the `py`/`aio`/`asgi` APIs, supported targets and Python versions, extensions, CI and benchmarks.
b. `PLAN.md` (repository root) ... the product plan: goals and non-goals, architecture, milestones and their status, and the decisions behind them.
c. The Go package documentation (package and exported-identifier doc comments in `py/`, `aio/`, `asgi/`, and the `cmd/` tools' usage text) ... the API contract a user reads on pkg.go.dev.

## Sources: JOURNAL and LTM

- `.agents/docs/JOURNAL.md` is the append-only log of findings, measurements and decisions. It holds the **freshest** durable facts, including changes not yet consolidated into LTM. The `good-sleep` / `reconcile-journal-ltm` skills consolidate JOURNAL entries into `.agents/docs/LTM/`; distill runs against whatever is present.
- `.agents/docs/LTM/` holds the consolidated, topic-organized reference material (`INDEX.md` is its table of contents).

Read both. When JOURNAL and LTM overlap, treat LTM as the settled synthesis and JOURNAL as the source of anything newer than the last consolidation. If JOURNAL records a substantial code change that the target docs do not yet reflect, that is exactly the gap this skill closes.

## Read in This Order

1. Read `README.md` and `PLAN.md` first.
2. Read `.agents/docs/JOURNAL.md` (at least the entries since the last consolidation record) and `.agents/docs/LTM/INDEX.md`.
3. Open only the LTM documents that look relevant to the gaps, stale sections, or missing detail you identified in the target docs.
4. For any candidate fact about an API, open the package source it would land in to check what its doc comments already say.

The source of truth for user-facing detail is always the code (exported identifiers and their doc comments, `cmd/pythago-dist` flags, `ci/*.sh`). Verify a fact against the code before writing it into README or a doc comment; JOURNAL/LTM point you at what changed, not the exact current flag or field name.

## Classify Findings Before Editing

For each candidate fact, decide whether it belongs in:

- `README.md`: user-observable behavior: how to build and embed, API usage, options and their defaults, supported platforms, limits, and measured performance a user would weigh. Frame it for a user doing a task, not for a maintainer reading the code.
- `PLAN.md`: architecture, subsystem boundaries, milestone status, decisions and the alternatives rejected, risks and dependencies (bao, python-build-standalone).
- Package docs: the contract of a package or exported identifier: what it does, its defaults, its threading and ownership rules, and its costs where they affect the choice.
- More than one: a single change often lands in two places (e.g. a new `asgi.Options` field belongs in its doc comment and in README). Update each in its own register.
- None of them: narrow bug history, one-off migrations, temporary workarounds, or details too fine-grained for canonical docs. Those stay in LTM.

Prefer durable knowledge over incident history. Convert timelines into timeless guidance.

## Update Strategy

- Synthesize; do not copy JOURNAL/LTM prose verbatim.
- Merge into existing sections when possible instead of appending new ones.
- Add a new section only when the information is a stable topic the document truly lacks.
- Keep summaries compact. Canonical docs should stay easier to scan than the notes beneath them.
- Preserve exact file paths, identifiers, flags and env var names when they help precision.
- Link agent docs from README only where a reader needs the recorded evidence (`.agents/docs/JOURNAL.md`).
- If multiple sources disagree, or JOURNAL and the current code disagree, call out the ambiguity or stop and ask the user before cementing one interpretation.

## Validation

Before finishing:

1. Re-read the edited sections of every document you changed.
2. Check that each added fact is supported by a source note (JOURNAL/LTM) and, for user-facing detail, verified against the code.
3. Check that plan-level rationale did not leak into README usage sections, and that usage detail did not bloat PLAN.
4. For changed Go doc comments, run `gofmt -l` on the files (must print nothing) and `go vet` on the packages.
5. Keep the documentation style rules in `AGENTS.md`.
