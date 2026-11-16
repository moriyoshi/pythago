# Documents for both humans and coding agents

* [README.md](./README.md) ... what pythago is and how to use it.
* [PLAN.md](./PLAN.md) ... the product plan: goals, architecture, milestones and their status.

# Documents for coding agents

* [./.agents/docs/JOURNAL.md](./.agents/docs/JOURNAL.md) ... findings, measurements, decisions and review history, in order. Its "Conventions" section says what an entry must contain.
* [./.agents/docs/LTM/INDEX.md](./.agents/docs/LTM/INDEX.md) ... long-term memory index for durable project knowledge under `./.agents/docs/LTM/`.
* [./.agents/docs/TODO.md](./.agents/docs/TODO.md) ... open to-do items. Check and update this file when picking up or finishing work. Re-verify an entry against the tree before acting on it.
* [./.agents/attic/spike/README.md](./.agents/attic/spike/README.md) ... the archived feasibility spikes (virtual threads, Go-driven asyncio loops, allocator and threading experiments) that PLAN and the journal cite. They don't build in place.

# Rules and protocols

## General

* pythago embeds CPython in `CGO_ENABLED=0` Go programs. `cmd/pythago-dist` turns python-build-standalone archives into a native image with bao; `py` is the Go↔Python API, `aio` the Go-backed asyncio loop and channels, `asgi` the ASGI server. Read `PLAN.md` before changing subsystem boundaries.
* **bao** (`github.com/moriyoshi/bao`) is maintained by its own agent. ❌ Never edit bao locally. Send missing features or bugs to the bao agent, don't test a bao PR while it is being rebased, and ask the user before adopting new bao flags as defaults (e.g. `-static-tls`).
* Another agent may be working in the same tree. ❌ Don't edit files it owns; if a change needs one, say so instead.
* The minimum toolchain is Go 1.26.11. No prebuilt `.syso` is distributed, and glibc older than 2.18 is unsupported.

## File Management

* When you'd make summary documents for your work, be sure to write them under `./.agents/docs`, not under `/tmp`.
* Temporary files should be created under `./.agents-workspace/tmp`, not under `/tmp`.
* ❌ Do not build binaries into the version-controlled tree. Output to `./.agents-workspace/tmp` (e.g. `go build -o ./.agents-workspace/tmp/asgiserve ./internal/tools/asgiserve`).
* ❌ Never delete user files without permission. Only safe to delete: files YOU created in THIS session that are in `./.agents-workspace/tmp/`. Always ask first if unsure. Assume all pre-existing files belong to the user.
* Generated, never checked in: the test image in `.work/testimage` with its workspace `.work/go.work` (written by `ci/lib.sh`), and `examples/hello/pydist/`. `glue/prebuilt` is checked in and reproducible: `ci/glue.sh` rebuilds it and its `SHA256SUMS`.

## Building

* Environment for every `go` command: `GOTOOLCHAIN=go1.26.11 CGO_ENABLED=0`. Tests and the tools under `internal/tools` also need the test image: `GOWORK=$PWD/.work/go.work`.
* The test image is a module of its own (path `github.com/moriyoshi/pythago/internal/testimage`) in `.work/testimage`. It holds whichever variant the last `ci/unit.sh` run generated (`gil`: CPython 3.13, `ft`: 3.14t). Regenerate the variant you need before building a tool against it; a 3.13 environment with a 3.14t image fails at Init.
* Run `gofmt -w` on every Go file you change before running `go vet` or `go test`, and before reporting a change as done.
* The standard local gate for any change you make — this applies to subagents too:
  ```
  gofmt -l <changed files>                      # must print nothing
  go vet <changed packages>
  GOWORK=$PWD/.work/go.work go test <changed packages>
  ci/unit.sh gil && ci/unit.sh ft               # before declaring a change complete
  ```
  The CI tiers regenerate the test image, run every package natively and under qemu for the other architecture, and run `asgi` twice (inline and `PYTHAGO_ASGI_LOOP_THREAD=1`). Fix violations and re-run until clean. Do not declare a change complete with a failing tier.
* Changing a structure shared between Go and the C glue (`glue/pythago_py.c`) means bumping `PYTHAGO_PY_ABI` and `glueABI` (`py/capi.go`) together and rebuilding `glue/prebuilt` with `ci/glue.sh`.

## Testing

* Make sure that regression tests are ready for your fix.
* **Audit the code before you write the test for it.** Read what the code actually does and state it, then write the test against that. A test written from your model of the fix inherits that model's blind spots.
* Before trusting any check — a new test, a `grep`, a benchmark, a built artifact — ask **what would a PASS look like if this claim were false?** Then neutralize it: deliberately break the fix and confirm the test fails with the diagnostic you intended, and record that control in the journal entry. ❌ A COMPILE error is not a valid neutralization; it proves the test names a symbol, not that it observes a behavior.
* Compare against stock CPython where a behavior has a stock counterpart; it catches test bugs as well as product bugs.
* Benchmarks: the host is shared and busy. Compare configurations interleaved, not one after another, and pin the server and the load generator to separate cores (the fast cores are 5–9 and 15–19). Record versions, cores and commands with every number.

## Git Workflow

* ❌ Do not run `git checkout` or `git restore` against the working tree — another agent may be working concurrently in the same directory.
* ❌ Never make discretionary commits. Commit or push only when the user explicitly asks.

## Documentation

* Try to write your work summary to one of the existing documents under `./.agents/docs`.
* ❌ Avoid editing any existing sections of `JOURNAL.md`. Append new entries to the end. (The sole exception is the `reconcile-journal-ltm` skill, which may remove entries already consolidated into `.agents/docs/LTM/` per the canonical `## LTM Consolidation Record`.)
* Keep `README.md` and `PLAN.md` current when a change affects users or the plan; the `distill-memories` skill does this in bulk.
* ❌ For repo-authored documentation, never use full-width parentheses (`（` `）`) or full-width colons (`：`). Use half-width ones.

## Skills

`.agents/skills` (also reachable as `.claude/skills`):

* `good-sleep` ... consolidate journal entries into `.agents/docs/LTM/` and extract to-dos into `TODO.md`.
* `deep-sleep` ... merge overlapping LTM documents into synthesis documents.
* `reconcile-journal-ltm` ... audit journal coverage in LTM, then collapse consolidation records and remove consolidated entries.
* `distill-memories` ... promote durable knowledge into `README.md`, `PLAN.md` and the package docs.
* `tackle-todos` ... sweep `TODO.md` and TODO/FIXME comments with parallel agents.
* `bench-http` ... benchmark `asgi` and `wsgi` against uvicorn and gunicorn (plain and TLS) and refresh README.md's tables.

## Shell Pitfalls

* ❌ `pkill -f PATTERN` and `pgrep -f PATTERN` match the invoking shell when the pattern appears in its command line, and kill it (exit 144). Use PIDs or `pgrep -x NAME`.
* ❌ Under `set -euo pipefail`, `grep -c` exits 1 when it counts zero and ends the script. Append `|| true` where zero is a valid count.
* Under qemu-user, binaries that read `/proc/self/maps` crash; `ci/lib.sh`'s `run_arch` preloads the `ci/qemu/nomaps.c` shim. Use it rather than invoking qemu directly.
