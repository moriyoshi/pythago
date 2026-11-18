# Pythago architecture animation

The 16:9 movies are built from HTML, CSS, SVG and JavaScript. They animate the gopher and gophersnake as independent presenters on a white background. The separate vector assets in `art/` come from `../assets/pythago.svg`, with the snake tail completed where the original artwork overlapped the gopher. Open [`index.html?movie=introduction`](index.html?movie=introduction), [`index.html?movie=internals`](index.html?movie=internals), or [`index.html?movie=native`](index.html?movie=native) to choose a movie. Plain `index.html` opens the introduction. The browser player displays the captions and animates the presenters from bundled speech timing. The exported movies carry the voice track.

The stories live in `story.js`, `internals/story.js`, and `native/story.js`. Playback and exported subtitles use the same cues on an absolute timeline. Edit the cue text and timing in the story, then regenerate the voice track. Each story's final scene sets its running time. Keep its last narration cue aligned to the same end time when regenerating the WAV. The closing shot and credits are timed from the end of the selected movie.

In `presenters.js`, `leads` assigns the emphasis in each cue's body gestures to the gopher, the snake, or both. The player uses the same timestamp for gestures, gazes, speech poses, captions, and diagrams. The gopher narrates every cue. Its snout, nose, and mouth follow the narration's short-window loudness and settle during pauses. If you add or reorder cues, update the gesture assignments.

The browser player uses the speech levels bundled in `speech-track.js`. After updating the aligned narration, regenerate them with `node speech.mjs narration.wav > speech-track.js`. An export with `--audio` samples its supplied track directly, including when rendering a clip from the middle of the presentation.

## Capture a movie

Requirements: Node.js, FFmpeg with `libx264`, and Playwright's Chromium. From this directory:

```sh
npm ci
npx playwright install chromium
node render.mjs --output pythago-architecture.mp4 --audio narration.wav
```

The default export is 1920 × 1080 at 30 fps in H.264 with burned-in captions. It covers the full selected story. Pass `--movie internals` or `--movie native` to select a companion. The default compression is CRF 18. Use `--crf 15` to retain more detail in a larger file. The audio starts at the beginning of the timeline, and short tracks are padded with silence. Omit `--audio` to produce a silent movie. A full-length export also writes an `.srt` file alongside the movie. Use `--no-captions` for clean picture, and use the SRT for selectable captions. To produce subtitles without rendering:

```sh
node subtitles.mjs > pythago-architecture.srt
```

For a quick sample, use `node render.mjs --output preview.mp4 --start 180 --duration 10 --width 960 --fps 15`. Run `node render.mjs --help` for options. To use an installed browser instead of downloading Playwright Chromium, omit the `npx playwright install chromium` step and set `PYTHAGO_CHROMIUM` to its executable path. For example: `PYTHAGO_CHROMIUM=/usr/bin/google-chrome node render.mjs --output pythago-architecture.mp4`. Rendering seeks the animation to each frame's exact timestamp; it does not depend on real-time screen recording. Output paths are relative to the working directory and existing outputs are overwritten.

## Opening shots and splash images

Each movie opens on a title and the centered mascots, then transitions to the chapter layout at 0:06.3. The opening uses the first narration cue. To capture a separate splash, open the [introduction](index.html?movie=introduction&splash=1), [runtime internals](index.html?movie=internals&splash=1), or [native internals](index.html?movie=native&splash=1) splash in a 3840 × 2160 Chromium viewport. The introduction uses the cradle pose. Both internals splashes use `assets/pythago.svg`, with a lavender background for native internals. The 16:9 PNG exports use larger titles and subtitles and are below 2 MB. [YouTube thumbnail guidance](https://support.google.com/youtube/answer/72431?hl=en)

## Chapters

| Time | Topic |
| --- | --- |
| 0:00 | Go and Python in one process |
| 0:25 | Build a native image with pythago-dist and bao |
| 1:05 | Bidirectional calls through the py API and glue |
| 1:45 | Embedded files and disk environments |
| 2:20 | Threading and CPython variants |
| 3:00 | An HTTP request through Go, ASGI and Python |
| 4:05 | Deployment choices and platform limitations |
| 4:40 | Recap |

Technical claims reflect the project documentation at the time of production. Recheck platform support before publishing a later revision.

## Runtime companion

The 12:40 companion traces a DNS lookup through asyncio scheduling, Go-backed completion and readiness, CPython thread attachment, a blocking poll, and the Python-to-Go trampoline. The second half walks through line-numbered excerpts in `internals/excerpts.js`. A gap row marks omitted source lines. The figures in `internals/figures.js` include dedicated thread and stack layouts. The implementation was checked against the Pythago working tree based on `00210df`, including `aio/goaio.py`, `aio/aio.go`, `py/thread.go`, `py/ccall.go`, `py/ccall_amd64.s`, `py/ccall_windows.go`, `py/func.go`, and `glue/pythago_py.c`. [Asyncio event loop reference](https://docs.python.org/3/library/asyncio-eventloop.html) · [CPython thread-state reference](https://docs.python.org/3/c-api/threads.html) · [Go runtime stack transitions](https://go.dev/src/runtime/cgocall.go)

| Time | Walkthrough |
| --- | --- |
| 4:40 | Python loop and token dispatch |
| 5:42 | Go completion queue and wakeups |
| 6:44 | CPython thread entry and release |
| 7:46 | Thread layout |
| 8:42 | Goroutine and system stack layouts |
| 9:46 | C callback trampoline |
| 10:50 | Go dispatch and reference ownership |
| 11:52 | Convert and return the completion |

## Native internals companion

The 12:20 movie follows `pthread_create` from a binary reference to a real operating-system thread. It covers object and archive edits, shared-image translation, the Linux LTO path, pthread lifecycle and fallbacks, Go-to-C argument passing, C-to-Go dispatch, and native TLS descriptors. The platform comparison shows how Linux and macOS translate native images and how Windows loads the bundled DLLs.

| Time | Walkthrough |
| --- | --- |
| 0:31 | Sections, symbols, and relocations |
| 1:20 | Wrapping, renaming, prefixing, and symbol binding |
| 2:10 | Bao’s wrap implementation |
| 2:48 | Shared-image generation and LTO source |
| 4:29 | Pthread hook decisions and source |
| 6:09 | Thread start, join, detach, and exit |
| 7:13 | C call thunk, register layout, and callback source |
| 8:55 | Per-thread storage and TLS descriptor source |
| 10:52 | Linux, macOS, and Windows |
| 11:43 | End-to-end recap |

The 17 line-numbered excerpts in `native/excerpts.js` come from Pythago `00210df` (MIT) and its pinned Bao `v0.0.8` dependency (Apache 2.0). The active source lines and diagram steps follow the absolute playhead. [Pythago source](https://github.com/moriyoshi/pythago/tree/00210df) · [Bao v0.0.8 source](https://github.com/moriyoshi/bao/tree/v0.0.8) · [ELF specification](https://gabi.xinuos.com/elf.pdf) · [Mach-O fixup definitions](https://github.com/apple-oss-distributions/dyld/blob/main/include/mach-o/fixup-chains.h) · [Windows PE format](https://learn.microsoft.com/en-us/windows/win32/debug/pe-format)

## Generate the narration

The movies use Piper `en_US-ryan-high` raised by six semitones with formants and timing preserved. The voice was trained on RyanSpeech by Zandie, Mahoor, Madsen, and Emamian. The Piper model card lists the dataset under CC BY-NC-SA 4.0. Install `piper-tts`, download the model and its `.onnx.json` configuration, and use a build of FFmpeg with the `rubberband` filter. [Ryan model card](https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/ryan/high/MODEL_CARD) · [RyanSpeech paper](https://arxiv.org/abs/2106.08468)

Run from `animation/`:

```bash
scratch=../.agents-workspace/tmp/ryan-movies
model=/path/to/en_US-ryan-high.onnx
mkdir -p "$scratch"
for movie in introduction internals native; do
  pronunciation=()
  assets=.
  if [[ "$movie" != introduction ]]; then pronunciation=("--$movie"); assets="$movie"; fi
  node subtitles.mjs --movie "$movie" > "$assets/narration.srt"
  python narrate.py "${pronunciation[@]}" --srt "$assets/narration.srt" --model "$model" --output "$scratch/$movie-natural.wav"
  duration=$(ffprobe -v error -show_entries stream=duration -of csv=p=0 "$scratch/$movie-natural.wav")
  ffmpeg -y -i "$scratch/$movie-natural.wav" -af "rubberband=pitch=1.41421356237:formant=preserved:pitchq=quality,apad,atrim=duration=$duration" -c:a pcm_s16le "$scratch/$movie-plus-six.wav"
  node speech.mjs "$scratch/$movie-plus-six.wav" --movie "$movie" > "$assets/speech-track.js"
  node render.mjs --movie "$movie" --audio "$scratch/$movie-plus-six.wav" --width 1920 --fps 30 --crf 15 --voice-credit=ryan --output "$scratch/pythago-$movie-ryan-1080p.mp4"
done
```

The synthesis script places each cue in its subtitle interval and uses FFmpeg to speed up a cue if it would run into the next one. The mono WAV runs through the end of the final cue. Regenerate it after changing the story.

Earlier exports used Piper `en_US-hifi_tts_92-medium`, trained on the Hi-Fi Multi-Speaker English TTS Dataset from NVIDIA featuring reader Cori Samuel. The dataset is available under CC BY 4.0. For those tracks, render with `--voice-credit` to display their attribution. [Hi-Fi TTS dataset](https://www.openslr.org/109/) · [Original Piper model card](https://huggingface.co/rhasspy/piper-voices/blob/cf910fb59f1d79ab3b625a63993a108751c6c25d/en/en_US/hifi_tts_92/medium/MODEL_CARD) · [Piper license](https://github.com/OHF-Voice/piper1-gpl)
