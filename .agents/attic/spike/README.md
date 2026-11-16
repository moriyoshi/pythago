# Archived spikes

The feasibility spikes (S1–S5, sthreads, smn, salloc, saio, goaio, svt, sinline, …) that
PLAN.md and the agent docs (`.agents/docs`) cite, moved here from `internal/spike` on
2026-11-17 before publishing. They are kept for reference and are not part of the module.

They don't build in place. The go command skips directories whose names start with a dot,
and the Go files still import each other as
`github.com/moriyoshi/pythago/internal/spike/...` (an import path element may not start
with a dot). To run one, copy this directory back to `internal/spike`.
`s1/build.sh` works from either place: it reads the C glue relative to itself.

Generated output is not kept. `s1/pyembed` is the bao image that s1, s23, s4, s5, saio,
salloc, smn, statrepro, sthreads and svt import. It is `s1/build.sh`'s `<out-pkg-dir>`, so
run that script into `s1/pyembed` first.
