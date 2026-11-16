# Project Status and Agent Process

## Summary
Where pythago stands (milestones M1–M4 done, M5 partly done as of 2026-11-17), how the
journal is written, and how the repository is laid out for publishing and for coding agents.
The agent harness (`AGENTS.md`, `.agents/docs` with JOURNAL, LTM and TODO, and the memory
skills) was adopted from the sibling project cornus on 2026-11-17. Read this before changing
documentation or the `.agents` tree.

## Key Facts
- **Status:** M1 runtime hardening, M2 product layout and stdlib from `fs.FS`, M3
  environments and M4 CI tiers are done (2026-11-15). M5: `aio` is done (2026-11-16);
  darwin and Windows are not started. `asgi` was added beyond the original plan.
- **Consumers need only Go:** `pythago-dist -mode shared` (default) runs bao in process
  (pinned `github.com/moriyoshi/bao v0.0.8` in `go.mod`); no LLVM, no separate bao. `-mode
  lto` needs LLVM.
- **Speed:** equal to stock CPython on uvicorn (asyncio and uvloop) after bao #32/#33.
- **glibc floor:** 2.18 for shared mode, proven at runtime by `ci/floor.sh`; LTO images need
  2.30+.
- **Journal is append-only** (from 2026-11-16): new entries at the end under
  `### YYYY-MM-DD — <the finding, as a sentence>`; corrections are new entries.
- **Durable knowledge lives in `.agents/docs/LTM/`** (index `INDEX.md`); open work in
  `.agents/docs/TODO.md`; the product plan in `PLAN.md`.
- **Spikes are archived in `.agents/attic/spike/`** and published, but do not build in place.
- `CLAUDE.md` is a symlink to `AGENTS.md`; `.claude/skills` is a symlink to
  `.agents/skills`.

## Details

### Milestone status
Full detail in `PLAN.md` section 13; the plan came from the spike verdict
([feasibility-and-threading-decision.md](feasibility-and-threading-decision.md)).

| Milestone | Status | Result |
|---|---|---|
| M1 runtime hardening (`py`) | done 2026-11-15 | `Run`, `OnMain`, `RegisterImage`, `Config.Modules`/`Environment`/`PythonSignals`/`MultiprocessingStart`, extshim in `Init`; torch and vLLM through the public API. See [py-runtime-and-threads.md](py-runtime-and-threads.md). |
| M2 product layout, stdlib from `fs.FS` | done 2026-11-15 | glue in `glue/`, `cmd/pythago-dist`, importer inside `py` (`py/fsimport.go`); `examples/hello` (49 MB) runs from an empty directory under `env -i` on arm64 and amd64 (qemu). |
| M3 environments | done 2026-11-15, amended | environments provisioned by a stock PBS Python of the same release and variant (`env`, `cmd/pythago-env`); embedded wheels deferred. |
| M4 CI and verification tiers | done 2026-11-15 | `ci/unit.sh`, `ci/floor.sh`, `ci/gpu.sh`, `.github/workflows/ci.yml` and `gpu-nightly.yml` (workflows never run: no remote). |
| M5 `aio`, darwin, Windows | `aio` done 2026-11-16; rest open | `GoEventLoop`, `GoSelectorEventLoop`, `goaio.run`. |

### State at the end of 2026-11-16
- **Build: Go-only for consumers.**
  - `pythago-dist -mode shared` translates PBS's `install_only_stripped` libpython with bao
    (`-linux-shared-object`, `-wrap`) and links prebuilt, byte-reproducible glue objects
    (`glue/prebuilt`, checked by `ci/glue.sh`).
  - bao runs in process as a Go module (`build.Run`). `go generate` takes under a second
    with the PBS archive cached.
  - `-mode lto` remains and needs LLVM. Opt-in `-static-tls` puts the image's TLS in glibc
    static TLS.
- **Speed.** Allocation-heavy Python had been 2–3x slower than stock; callgrind put 50% of
  instructions in bao's TLS descriptor thunk. bao #32 (fast path) and #33 (glibc static TLS)
  fixed it; pythago equals stock on uvicorn (asyncio and uvloop), even on one core with
  `-static-tls`. On the way a latent `py` bug was found: out-parameters lost to a stack move
  (`//go:uintptrescapes`; see [embedding-cpython.md](embedding-cpython.md)).
- **glibc floor proven at runtime.** `ci/floor.sh` runs a program through Debian jessie's
  (2.19) and buster's (2.28) loaders on both arches. That exposed three bao bugs, fixed in
  bao (#34 moved symbols, #35 weak imports, #36 DT_NEEDED order). LTO images still need
  glibc 2.30+ (weak code references in archives).
- **`aio`** (M5): `GoEventLoop` (goroutines plus `net`) and `GoSelectorEventLoop`.
  BufferedProtocol reads are zero-copy and tried inline on the loop thread first; a
  256 KiB buffer matches the selector loop; 4 KiB reads went from 300 to about 840 MB/s.
  On two cores, uvicorn on `GoEventLoop` matches stock uvloop.
- **`py` API growth:** zero-copy arrays both ways through the buffer protocol
  (`Object.Buffer`, `BufferInto`, `View[T]`, `WithView[T]`, `NewBuffer[T]` with Go memory
  pinned for Python); `Object.UnsafeBytes`; conversions that skip GC-cleanup registration
  for nested values; `RegisterExports`.
- **`asgi`:** an ASGI server as an `http.Handler`. Lifespan, streaming with backpressure,
  disconnects, response trailers, WebSockets over HTTP/1.1 and HTTP/2, pathsend (sendfile).
  FastAPI and Starlette work. Every security check is mutation-tested
  ([hardening-network-facing-code.md](hardening-network-facing-code.md)). Throughput was
  about 4% short of uvicorn + uvloop on two cores with `GOMAXPROCS=4`; net/http costs about
  9 µs per request, so it cannot win on one core with a loop-thread handoff. (Later, on
  2026-11-17, inline requests became the default and `asgi.Options.LoopThread` the
  opt-out.)
- **CI:** the unit tier covers `py`, `aio` and `asgi` on 3.13 and 3.14t, natively and under
  qemu, for shared and LTO modes, plus glue-reproducibility and old-glibc floor checks.

### Journal conventions (`.agents/docs/JOURNAL.md`)
- **Append-only from 2026-11-16.** New entries go at the end, under
  `### YYYY-MM-DD — <the finding, as a sentence>`. Existing entries are not edited; a
  correction is a new entry that names the one it corrects. The only exception is the
  `reconcile-journal-ltm` skill, which may remove entries already consolidated into LTM.
- **An entry records:** the context (who asked, the constraint); the decision and rejected
  alternatives with reasons; each bug with the exact error text in a code block; why the
  existing tests missed it and which layer caught it; the fix and the control (a sabotage or
  mutation run showing the test fails without it); measurements before and after with the
  setup (versions, cores, commands); commits, versions and digests (bao commits, PBS
  release, Go toolchain, library versions, glue `SHA256SUMS`); and the rule to carry.
- **Not recorded:** routine "the gate passed" notes. Scratch artifacts stay in
  `.agents-workspace/tmp`; their names in the journal identify past runs, not permanent
  files.
- **Peer work:** findings from the bao agent (bao-77) are recorded with its PR numbers and
  commits. pythago never edits bao ([process-lessons.md](process-lessons.md)).
- **Style origin:** on 2026-11-16 the user asked for detail like bao's and yesno's journals.
  The 2026-11-16 entries from the shared-object switch onward were rewritten from
  compressed bullets into that form; earlier entries were left as they were.
- **Curated sections moved out (2026-11-17), verbatim:** "Durable lessons" became the ten
  original LTM documents, one per subsection; "Open items" became `TODO.md` checkboxes, plus
  two items recorded only inside entries (removing the experimental `pythago_comb_*` glue
  before a release, and the per-core asgi gap). The pre-split journal was kept as
  `.agents-workspace/tmp/JOURNAL.before-split.md` (scratch).

### Repository layout for publishing
- **Spikes:** moved from `internal/spike` to `.agents/attic/spike` on 2026-11-17. They are
  no longer git-ignored, so they are published. The generated bao image `s1/pyembed`
  (35 MB, written by `s1/build.sh`) was removed and is not kept.
  - Every path reference in PLAN, the journal, the glue and `py/spike_combiner.go` was
    rewritten, as were references inside the spikes.
  - `s1/build.sh` reads its glue relative to itself, so it works in the attic and copied
    back.
  - The Go import paths inside the spikes still say
    `github.com/moriyoshi/pythago/internal/spike/...`. A dot-leading element is not a valid
    import path, and the go command skips dot directories, so the spikes build only when
    copied back to `internal/spike`. `.agents/attic/spike/README.md` says so.
  - `s1/pyembed` is the image that s1, s23, s4, s5, saio, salloc, smn, statrepro, sthreads
    and svt import; run `s1/build.sh` into it first.
- **Generated, never checked in:** the test image in `.work/testimage` with
  `.work/go.work` (root `.gitignore` ignores `/.work/`), and `examples/hello/pydist/`.
  `glue/prebuilt` is checked in and reproducible (`ci/glue.sh`).

### The agent harness
Adopted from `../cornus` before publishing, written from this project's own standing rules
rather than copied.
- **`AGENTS.md`** (`CLAUDE.md` is a symlink to it): a document index (README, PLAN, JOURNAL,
  LTM index, TODO, the attic README), then rules: general (bao is never edited locally;
  another agent may share the tree), file management, building with the test image, testing
  with controls, git (no `git checkout`/`git restore` against the working tree, no
  discretionary commits), documentation (half-width punctuation only), skills, and shell
  pitfalls.
- **`.agents/docs/`:** `JOURNAL.md` (moved from the repository root), `LTM/` with
  `INDEX.md`, and `TODO.md`.
- **`.agents/skills/`** (`.claude/skills` links to it):

  | Skill | Purpose |
  |---|---|
  | `good-sleep` | consolidate journal entries into `.agents/docs/LTM/`; extract to-dos into `TODO.md` |
  | `deep-sleep` | merge overlapping LTM documents into synthesis documents |
  | `reconcile-journal-ltm` | audit journal coverage in LTM and TODO, then collapse consolidation records and remove consolidated entries |
  | `distill-memories` | promote durable knowledge into `README.md`, `PLAN.md` and the `py`, `aio`, `asgi` package docs (cornus targeted its docs site) |
  | `tackle-todos` | sweep `TODO.md` and TODO/FIXME comments with parallel agents; scans Go, C, Python and shell and runs this project's gate |

  Not adopted from cornus: `translate-documents`, `web-screenshot`, `assess-doc-quality`
  (cornus's docs site and web UI) and `audit-licenses` (cornus's license policy).
- **Workspace:** `.agents-workspace/tmp` for temporary files and build outputs;
  `.agents-workspace/.gitignore` and `.agents/.gitignore` ignore `tmp/` and `worktrees/`.

## Files
- `AGENTS.md` (and symlink `CLAUDE.md`) — rules and document index for agents.
- `PLAN.md` — product plan, architecture, milestones (section 13).
- `.agents/docs/JOURNAL.md` — chronological log; its "Conventions" section is canonical.
- `.agents/docs/LTM/INDEX.md` — LTM index.
- `.agents/docs/TODO.md` — open items.
- `.agents/skills/*/SKILL.md` — the five memory and maintenance skills.
- `.agents/attic/spike/README.md` — how to run the archived spikes.
- `ci/unit.sh`, `ci/floor.sh`, `ci/glue.sh`, `ci/gpu.sh`, `ci/lib.sh` — the CI tiers.

## Test Coverage
Not code. The repository gate is in `AGENTS.md`: `gofmt -l`, `go vet`,
`GOWORK=$PWD/.work/go.work go test <pkgs>`, then `ci/unit.sh gil && ci/unit.sh ft`.

## Pitfalls
- Do not edit existing JOURNAL entries; append. Only `reconcile-journal-ltm` removes
  consolidated entries.
- Do not use full-width parentheses or colons in repo documentation.
- Do not try to `go build` inside `.agents/attic/spike`; copy it back to `internal/spike`.
- Journal names of scratch files (`.agents-workspace/tmp/...`, or the session scratchpad
  before 2026-11-17) identify past runs; the files may be gone.
- The GitHub workflows have never run; local CI scripts are the only verified tiers.
- Another agent may own files in the same tree; say so rather than editing them.
