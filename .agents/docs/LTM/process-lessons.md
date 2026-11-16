# Process

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- Missing bao features go to the bao agent (tmux pane %12, session bao-77). Never edit bao
  locally, and don't test a bao PR while it is being rebased. Acknowledge its PR notices
  briefly and record them in the plan. Switching build.sh to bao flags waits for the user.
- **When an experiment fails, revert to the last verified design and re-run its matrix**
  before reporting. Don't leave half-working variants in the tree.
- Use subagent reviews for security self-assessment; they found bugs that the stress tests
  never triggered.
- **`pkill -f PATTERN` and `pgrep -f` match the invoking shell** when the pattern is in its
  command line, and kill it (exit 144). Find processes by name (`pidof`).
- **The test image (`.work/testimage`) holds whichever variant ran last.** The CI tiers
  regenerate it, so regenerate the needed variant before building a tool against it. A
  3.13 venv against a 3.14t image fails at Init.
- **Pin to unmerged dependency branches only to verify them,** say so, and repin to the
  release once merged (bao v0.0.7, then v0.0.8 for `-darwin-dylib`).
- **New dependencies are a visible decision:** coder/websocket (runtime) and
  golang.org/x/net (tests only) were added with the reason recorded.
