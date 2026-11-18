# Pythago presentation

This worktree contains the five-minute Pythago explainer. Open `animation/index.html` for the browser player. Read `animation/README.md` for editing, narration, and export instructions. Record presentation decisions in `JOURNAL.md`.

## Animation

- Keep diagrams, character rigs, and gestures driven by the absolute playhead time so scrubbing and frame-by-frame export render the same poses.
- Update narration cues in `animation/story.js` and regenerate `animation/narration.srt` from it. Keep pronunciation overrides in `animation/narrate.py` so caption spelling stays intact.
- Reuse the supplied art in `assets/` and `animation/art/`. Keep references inside this worktree.

## Review and export

- Preview the affected scenes in Chromium or Chrome. Verify exported audio and video duration with FFprobe when producing an MP4. The Go runtime suites are outside this worktree's scope.
- Keep generated audio, video, and temporary files in `.agents-workspace/tmp/`. Preserve attribution when using the documented Piper voice.
- Commit or push only when explicitly requested.
