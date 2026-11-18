# Presentation journal

### 2026-11-17 — A deterministic browser timeline explains Pythago's architecture

- **Context:** The user requested a five-minute HTML and JavaScript animation for later movie capture, using the existing assets. The self-contained player and capture instructions are in `animation/`; the timed narration text is the source for both captions and SRT. A recorded voice track remains optional.
- **Decision:** Render each frame at an explicit timeline time and pipe browser screenshots to FFmpeg, rather than relying on real-time playback. The example uses the ASGI request path while distinguishing embedded resources from disk environments and recording platform limits.
- **Verification and control:** Playwright checked scene and cue continuity, all chapter visuals, play, seek, and the caption toggle; short silent and audio MP4 exports were inspected with ffprobe. As a control, shifting a cue start by one second in an in-memory copy made the continuity check report `cue gap`. This checks that the continuity check observes timing gaps rather than just parsing the script. The GIL and free-threaded unit tiers passed on native arm64 and qemu amd64.
- **Carry:** Keep the cue text and timestamps in `story.js` as the single source for captions and recorded narration, and recheck platform statements before publishing a later version.

### 2026-11-17 — Timeline-aligned speech and diagonal box connectors for the architecture animation

- **Context:** The user requested free-to-use TTS and noticed the fourth slide's near-vertical connector. The old connector selected horizontal box edges based on center separation, placing the endpoints only 20 pixels apart horizontally.
- **Change:** `animation/narrate.py` synthesizes each subtitle cue using Piper and pads or time-adjusts it to its window. The narration uses the scratch-trained `en_US-hifi_tts_92-medium` model at revision `cf910fb59f1d79ab3b625a63993a108751c6c25d`, with dataset and reader attribution in `animation/README.md`. Box connectors now intersect each rectangle along the line joining their centers.
- **Verification and control:** All 31 speech cues have nonzero audio and silent margins inside their caption windows. A browser check measured the corrected fourth-slide connector at dx 168.2, dy 125.0. Its mutation control served the original connector code to the browser and failed with `Connector is not diagonal: M1450 402.5 L1470 670`, demonstrating that the check observes the reported defect.
- **Carry:** Voice models have individual data provenance and licensing; preserve the model revision and attribution, and regenerate the narration whenever cue text or timings change.

### 2026-11-17 — The animation's free-threaded test tier passed on retry after rendering

- **Context:** During simultaneous video rendering, `ci/unit.sh ft` failed under qemu amd64 in `TestIOProxyConcurrent`: `/proxy?via=streams: 7 of 16 requests failed to meet at the upstream: 500 Internal Server Error`. The prior GIL tier passed.
- **Verification:** After the render and final encoding completed, a fresh `ci/unit.sh ft` passed on arm64 and qemu amd64. The first failure occurred with the renderer active; this correlation does not establish its cause.

### 2026-11-17 — The gopher and gophersnake present the architecture animation

- **Context:** The user requested a plain white background and the mascots as presenters. The supplied SVG already depicts the two characters together, so the animation reuses that artwork instead of inventing separate characters.
- **Change:** Each chapter displays the pair with timeline-driven movement and a chapter-specific presenter cue. The canvas is white; diagram, caption, and voice-credit colors use dark ink for contrast. Narration and timings remain unchanged.
- **Verification and control:** Browser checks verified the white canvas, presenter visibility and movement in all eight chapters, and the existing diagram-connector check. A mutation replacing the mascot image URL made the presenter check fail with `white background or presenter missing`.

### 2026-11-17 — Presenter animation follows the captured timeline smoothly

- **Context:** The user asked for smooth mascot motion as the animation progresses. The rendered movie now uses 30 frames per second, while the mascots' translation and rotation depend on the absolute timeline rather than wall-clock timers.
- **Verification and control:** A browser check compared presenter positions at one-frame intervals around all seven chapter boundaries and verified deterministic seeking. As a control, replacing both motion expressions with zero caused `Motion does not follow the timeline deterministically`.

### 2026-11-17 — The animation's two mascots are separate independently animated vectors

- **Context and change:** The user asked to split the mascots and move them independently. The original `assets/pythago.svg` has a complete gopher group and a snake foreground group; `animation/art/` contains separate SVGs derived from these, with the snake's cut-off tail closed. Their independent translation and rotation are functions of the same absolute timeline, preserving smooth seeking and chapter transitions.
- **Verification and control:** A browser check inspected both assets in every chapter and verified movement, independent transforms, deterministic seeking and continuity at chapter boundaries. Replacing the snake's motion with a constant caused `Error: independent timeline motion broken`, showing the check observes movement rather than merely the presence of two images.


### 2026-11-18 — The gopher gestures and the gophersnake bends to narration cues

- The user asked for articulated movement and a bone rig for the snake. The inline SVG preserves the mascots' faces and gives the gopher independently hinged arms and legs. The snake's twelve fixed-length bones propagate local rotations from head to tail. The outline is drawn as a continuous curve, and the ears and face follow the first bone.
- Each existing narration cue has a lead character. Gestures ease into and out of the original cue timing. The gopher waves for the opening and closing and gestures toward the diagrams in between.
- A Chromium check observed changes in both arms, both legs, the torso, and the snake's silhouette. Across cue boundaries sampled one frame before and after at 30 fps, the largest sampled snake-outline movement was 2.96 local SVG units. Seeking to an earlier frame reproduced its pose. Disabling local bends caused `Snake outline did not bend independently of head position`. Freezing the left leg caused `Some joints never moved`.
- The exported H.264 and AAC streams each run for 300 seconds. The video contains 9,000 frames at 30 fps, and the exported SRT matches the original. The GIL tier passed on arm64 and amd64. The free-threaded tier repeatedly failed in the existing `TestIOProxyConcurrent` test with `/proxy?via=streams: 2 of 16 requests failed to meet at the upstream: 500 Internal Server Error` on the last isolated retry.
- Chrome's headless export process exposed `speechSynthesis` but returned zero voices, including with `--enable-speech-dispatcher`. The Web Speech API plays utterances through the browser and exposes no audio file or stream for the movie encoder. The existing Piper WAV supplies the movie audio.

### 2026-11-18 — The snake slithers and both presenters shift their gaze

- The user asked for a more snake-like posture and moving eyes. A travelling bend now runs down twelve fixed-length bones while the body stays low. The head lifts as the snake takes the lead and the tail sometimes curls in while it listens. Separate pupils shift with the narration cues.
- Chromium sampled 1,579 poses. The snake kept its tail below the head in 1,320 samples, curled the tail back in 202, and showed a travelling curve peak at seven different spine positions. All four pupils changed position. The existing checks also verified smooth cue transitions and deterministic seeking.
- Serving the previous arched pose failed with `Tail stays raised instead of following the low slither`. Disabling the pupil animation failed with `A mascot keeps a fixed gaze`.

### 2026-11-18 — The narration says See-Go and Whisky

- The user corrected the pronunciations of CGo and WSGI. Case-insensitive whole-word overrides apply only to text sent to Piper, and the subtitles retain the written names.
- Piper's phonemizer produced `sˈiː ɡˌoʊ` and `wˈɪski` in the two affected cues. The updated MP4 reuses the video stream and replaces only those cue windows in the narration source. Both streams are five minutes long.

### 2026-11-18 — The narration spells out GIL

- The user requested Gee-Ai-Ell. The pronunciation override sends `gee eye ell` to Piper, whose phonemizer produced `dʒˈiːʲ ˈaɪ ˈɛl`. The revised MP4 replaces the cue starting at 2:50 and retains the CGo and WSGI corrections.

### 2026-11-18 — The mascots cuddle and trade a wave

- The user asked for more playful interaction and freer arm motion. Both gopher arms now bend at the shoulder and elbow. During the opening and closing cuddles, the snake leans into the gopher, the right paw crosses in front below the snake's face, and two hearts appear. In the middle, the gopher waves a paw and the snake responds with a tail flick.
- Chromium verified arm overlap at both cuddles, paw and tail movement during the wave, and reproducible poses on seek. Freezing the cuddle timing caused `Both cuddles must bring the front arm over the snake`. The Go runtime checks were omitted per the user's earlier direction.

### 2026-11-18 — The explainer moves to its own orphan worktree

- The user requested an orphan branch and independent instructions. The `presentation` worktree contains `animation/`, this journal, and its own `AGENTS.md`. It also carries a copy of the logo SVG referenced by the player. The move preserved the animation files' staging state, and the branch has no commits.
- Chromium opened the moved HTML directly, loaded the copied logo, and reproduced both cuddle poses. Generated media belongs in the worktree's ignored `.agents-workspace/tmp/` directory.
- The final movie and subtitle sidecar are in the worktree scratch directory. FFprobe confirmed both streams run for five minutes, and the video has 9,000 frames at 30 fps.

### 2026-11-18 — The gopher cradles the snake with both arms

- The user spotted the gopher's arm cutting through the snake in the cuddle. The gopher now opens both arms, supports the snake as it rises across its lap, and folds the near paw over the belly after the lift. The paw opens before the snake descends.
- The SVG draws the supporting arms behind the snake and the near paw above the belly. The foreground copy is clipped below the head and hidden during the lift and release. Chromium verified the layer order and paw visibility at all three stages of both cuddles. Preview frames showed the head and face clear.

### 2026-11-18 — The cradles lose the floating hearts

- The user asked to remove the hearts. The player no longer draws them. The movie replaces the two affected video segments and retains the original audio. Both cuddles were visually checked in the export, and all 9,000 frame timestamps match the 30 fps timeline.

### 2026-11-18 — The cradle arms join the shoulders cleanly

- The foreground arm layer exposed the rounded stroke caps at both shoulders. The torso now covers those caps as it does for the normal arm poses. The foreground mask reveals only the lower part of the stroke as the arm crosses the torso into the cradle.
- Before-and-after Chromium frames confirmed the change in the approach, hold, and release at both ends of the presentation. The movie replaces the affected segments, and all 9,000 video timestamps match the 30 fps timeline.

### 2026-11-18 — Both paws close over the cradle

- The user asked to make the two-arm hold visible and have the gopher look down at the snake. The far paw now curls up from underneath while the near paw closes over the body. Both paws stay clipped below the snake's face and open before it descends. The gopher's pupils track down and to its left during the hold.
- Chromium previews confirmed both paws and the downward gaze in both scenes. The reassembled movie has 9,000 frames at 30 fps with continuous timestamps and five minutes of video and audio.

### 2026-11-18 — The end credits name the Go gopher designer

- The closing scene credits Renée French for the original Go gopher design from 4:49 through the end. The browser credit links to the [Go project attribution](https://go.dev/blog/gopher) and the CC BY 4.0 license.
- Checked the credit at its start, at the end, and after seeking backward. Reviewed the closing frame in the updated MP4 and verified all 9,000 frame timestamps.

### 2026-11-18 — The mascots step forward for the closing credits

- The user asked for the summary and diagrams to remain behind the mascots in the final shot. From 4:49, the summary and diagrams recede while the mascot group grows and moves to center. Both credits now share a block below the mascots, above the narration caption.
- The browser and movie share the credit layout. The `--voice-credit` option still controls the voice credit and MP4 metadata. Chromium verified the transition, seek behavior, and space between the mascots and credits. The export has 9,000 evenly spaced frames and five minutes of audio and video.

### 2026-11-18 — The final cut renders at native 1080p

- The user asked for better video quality. All 9,000 frames were rendered from the browser at 1920 × 1080 and encoded at H.264 CRF 15. The exporter now accepts `--crf` for reproducible quality settings.
- FFprobe verified 30 fps and five minutes in both streams. The audio stream hash matches the prior cut. At 4:54.5, SSIM against the native Chromium reference was 0.998 in the new encode and 0.956 in a Lanczos-upscaled frame from the old 540p cut.

### 2026-11-18 — The diagrams use consistent box spacing

- All eight chapters use a 56-pixel gap between settled boxes in each row and between rows at 1920 × 1080. The rows share a center, with the asyncio box aligned to the right edge under the ASGI app. Connector endpoints follow the boxes as they move into place.
- Chrome previews covered every chapter and the closing shot. Browser checks measured the gaps, checked that labels fit, and reproduced diagram poses when seeking back to the same time.
- The updated 1080p cut has 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. The encoded narration matches the prior cut, and the subtitle sidecar matches the narration cues.

### 2026-11-18 — Presentation labels use sentence case

- The masthead, chapter labels, diagrams, and footer use normal capitalization. Technical acronyms, code identifiers, and attribution retain their conventional spelling.
- Chrome previews verified label casing and fit in all eight chapters and the closing shot. Opening a chapter from the player menu displays the updated label. Regenerating the subtitles produced an identical file.

### 2026-11-18 — Spicy Rice sets the headings and diagram titles

- Spicy Rice is bundled locally with its Open Font License. Chapter headings and box titles use the font, and the movie renderer waits for fonts before capturing the first frame. [Google Fonts source](https://github.com/google/fonts/tree/main/ofl/spicyrice)
- Chrome verified the actual font used while offline. All eight chapter headings retained their two-line layout, and each diagram label fit inside its box.
- The final 1080p export contains 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. Its narration stream and subtitles match the previous cut.

### 2026-11-18 — The mascots move their snouts and noses as they speak

- Both faces use the narration's short-window loudness, sampled by absolute playhead time. The cue's lead moves its snout, nose, and mouth, and both faces settle during pauses. The browser uses levels sampled from the prior movie's narration, and exports with `--audio` sample the supplied track.
- Chrome checked all 31 cues, pauses, speaker assignments, and repeatable seeking. Substituting a silent track held both faces at rest. A narrated preview starting seven seconds into the timeline yielded four seconds of video and audio.
- The updated 1080p movie contains 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. The encoded narration and subtitles match the prior cut. The closing shot shows both mascots speaking as they hold the cradle pose.

### 2026-11-18 — The gopher narrates while the gophersnake listens

- The gopher speaks every cue. Its teeth and snout share an upper-jaw group, so they lift and settle together. The gophersnake's two original wrinkle curves stay fixed relative to its head, with its existing gaze and body gestures.
- Chrome checked all 31 cues and their pauses, confirmed that the two wrinkle paths match the original art, and verified repeatable poses when seeking. The opening and closing cradle previews show the gopher speaking with the upper teeth attached to the snout.
- The gopher's peak vertical mouth opening is 22 SVG units, down from 34. The mouth ellipse sits 3.5 units higher. In 1,478 sampled speaking poses, its upper edge overlapped the snout. Chrome also compared closing cradles at the same speech peak.
- The revised 1080p movie has 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. Its encoded narration and subtitles match the previous cut.

### 2026-11-18 — The near arm keeps a continuous outline during the cradle

- The elbow bends out so the foreground mask starts outside the snake's yellow fill. The support strokes reach full opacity at the mask boundary. The foreground mask applies to the composed arm strokes as one layer, removing the horizontal seam across the fill.
- Chrome previews covered the lift, hold, and release in both cuddles. The shoulder stayed anchored in the torso, and all 118 frames with the foreground paws visible kept the mask boundary outside the snake's fill.

### 2026-11-18 — Diagrams and their text are larger

- The diagram boxes, labels, and notes grew by 18%. Settled gaps remain 56 pixels at 1080p. The densest rows shift left to fit within the existing right margin.
- Chrome sampled 601 frames and confirmed the diagram labels fit. The diagrams stay clear of the chapter copy and the mascots through the presentation and recede behind the mascots in the closing shot.
- The combined 1080p export contains 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. The encoded narration and subtitles match the previous cut.

### 2026-11-18 — The diagrams move slightly left

- Boxes, connectors, and notes move left together by 24 pixels at 1080p. Chrome checked 601 poses across the eight chapters. The closest chapter text clearance in those poses was about 85 pixels.
- The export has 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. Its encoded narration and subtitles match the prior cut. The current closing mascot pose overlaps the first credit line.

### 2026-11-18 — The mascot pair moves 100 pixels down

- The shared stage moves 100 pixels down at 1080p. The offset is applied after the closing scale so the gopher, gophersnake, and both layers of the cradle travel together by the same amount throughout.
- The closing credits move below the lowered pair and use tighter type spacing. The closing caption moves down as the shot starts. Chrome checked 909 poses, including all closing frames, with at least 9 pixels between the mascots and credits and 10 pixels between the credits and caption.
- The 1080p cut has 9,000 evenly spaced frames at 30 fps and five minutes of video and audio. The encoded narration and subtitles match the prior cut.

### 2026-11-18 — The player width drives CSS scaling

- The 16:9 player width defines the shared CSS length used for film typography and closing movement. Capture mode sets that width to the viewport width.
- Chrome compared the layout at six viewport sizes. All 11 sampled 1080p frames matched pixel for pixel, so the verified mascot export also represents the revised CSS.

### 2026-11-18 — A companion movie traces asyncio, threads, and trampolines

- The companion follows one DNS lookup through the ready queue, Go's completion path, CPython thread-state attachment, polling with `py.Release`, and the `py.NewFunc` callback trampoline. It includes a task timeline, a completion and readiness comparison, thread-state figures, and a three-lane sequence diagram. The details were checked against the Pythago working tree based on `00210df`. [Asyncio event loop](https://docs.python.org/3/library/asyncio-eventloop.html) · [CPython thread states](https://docs.python.org/3/c-api/threads.html) · [Go thread pinning](https://pkg.go.dev/runtime#LockOSThread)
- The companion has 31 cues and uses the same credited Piper voice and mascot rig. The original narration and subtitle file still match. Chrome checked label bounds and deterministic seeking in every cue and confirmed that the original story remains the default player entry.
- The H.264 CRF 15 export has 9,000 evenly spaced 1080p frames at 30 fps and 300 seconds in both streams. FFprobe confirmed the mono AAC narration and voice attribution, and the subtitle sidecar matches the companion story.

### 2026-11-18 — The player selects a movie and follows its running time

- `?movie=introduction` and `?movie=internals` select the story, figures, and speech timing. The bare URL opens the introduction, and the former `?internals=1` link still opens the companion. The exporter uses the same selector.
- The final scene supplies the playhead maximum and export duration. The closing shot, credits, and closing cradle are timed from that end. Narration synthesis and speech timing also cover the selected story. A temporary 10:42 edition verified browser seeking, speech after 10:30, and a one-second narrated export at 10:41.

### 2026-11-18 — The introduction covers macOS and Windows

- Six updated cues reflect the current development tree's Linux, macOS, and Windows support. The build chapter distinguishes the translated image for Linux and macOS from Windows's bundled DLLs. The thread chapter distinguishes eligible pthread wrapping from Windows's CPython-managed threads. The deployment chapter explains matching a native extension to its target and Python ABI.
- The revised subtitles and speech timing come from the updated cues. All other cues keep their original PCM narration samples. Chrome verified the new narration captions, build labels, and deployment platform labels.
- The updated introduction has 9,000 evenly spaced 1080p frames at 30 fps and five minutes of video and AAC narration. FFprobe confirmed the durations and voice attribution, and the exported subtitles match the story.

### 2026-11-18 — The runtime companion walks through source, threads, and stacks

- The 12:40 cut adds 23 line-numbered excerpts for the asyncio adapter, Go queue, thread attachment, trampoline, and dispatcher. The relevant lines light up for each narration cue. The thread layout compares Go entries, pthread-backed Python threads, and Windows threads. The stack layout follows a nested Go-to-C-to-Go call and calls out the Windows path. [Go runtime stack transitions](https://go.dev/src/runtime/cgocall.go)
- The same credited Piper speaker supplies the narration. The installed model has one speaker and no pitch input. The audio treatment lowers pitch by two semitones with formants preserved and retains the 760-second timing. The mascot speech track uses that adjusted audio.
- The excerpt lines were compared with the runtime source. Chrome checked all 76 cues for layout and repeatable seeking. The introduction still opens at its original five-minute duration.
- Long lines wrap within the excerpt panel at 20–23 pixels at 1080p. Audio analysis estimated a drop of 1.96 semitones across 2,829 voiced windows and found zero lag in the speech envelope at 20-millisecond resolution.

### 2026-11-18 — Both movies open on the mascots and have YouTube stills

- The shared opening uses the same centered camera scale as the closing shot. It plays over the first narration cue and transitions to the chapter layout by 0:06.3. The title and chapter copy fade in sequence.
- Each splash uses the supplied mascots in their cradle pose, laid out beside the movie title. The two PNGs are 3840 × 2160 and under 2 MB. [YouTube thumbnail guidance](https://support.google.com/youtube/answer/72431?hl=en)
- FFprobe verified 9,000 contiguous frames for the introduction and 22,800 for the companion at 1080p and 30 fps. Audio and video run for 5:00 and 12:40 respectively, with the original voice attribution. Both subtitle sidecars match the stories. Encoded opening frames and the wrapped `py.Release` excerpt match their Chrome previews visually.

### 2026-11-18 — The splash images use distinct drawings and larger type

- The introduction splash uses the animated cradle pose. The internals splash uses `assets/pythago.svg` directly. The titles grew by 12.5% and the subtitles by about 13%, with balanced subtitle lines. Chrome checked clearance between the text and artwork and exported both 3840 × 2160 PNGs under 2 MB.

### 2026-11-18 — A higher-pitched Ryan voice audition

- The audition uses Piper `en_US-ryan-high` for 10:07–10:29 of the internals movie. It raises pitch by two semitones with formants and tempo preserved. The measured change was +1.99 semitones across 143 voiced windows with zero envelope lag at 20-millisecond resolution.
- The Ryan model card lists the RyanSpeech training data under CC BY-NC-SA 4.0. The audition credits the dataset and model in its metadata. [Ryan model card](https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/ryan/high/MODEL_CARD) · [RyanSpeech authors](https://arxiv.org/abs/2106.08468)
- A second audition raises the same original take by six semitones. The measured change was +5.98 semitones across 143 voiced windows with zero envelope lag at 20-millisecond resolution.

### 2026-11-18 — Both movies use Ryan at +6 semitones

- Piper `en_US-ryan-high` supplies the narration for both movies. The pitch shift preserves formants and tempo. All 107 cues fit their existing subtitle windows, and the mascot speech tracks use the shifted audio. The closing credits and movie metadata credit Piper, Ryan, and RyanSpeech. [Ryan model card](https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/ryan/high/MODEL_CARD)
- Chrome and the encoded closing shots show the new voice credit clear of the captions. FFprobe verified matching audio and video durations of 5:00 and 12:40 and checked every frame timestamp at 1080p and 30 fps. The exported subtitle sidecars match the stories.

### 2026-11-18 — Native internals companion on object editing, hooks, and thunks

- The 12:20 movie uses `index.html?movie=native`. It follows `pthread_create` through symbol wrapping, image generation, runtime fallback, and the thread start and release handshake. It contrasts the contracts of the outbound C call thunk, the inbound callback trampoline, and the native TLS resolver, then compares Linux, macOS, and Windows.
- The 15 chapters include 17 line-numbered excerpts from Pythago `00210df` and Bao `v0.0.8`. Diagram steps and source highlights follow absolute cue times. Chrome verified all 59 cues at 1920 × 1080 and 960 × 540, including repeatable seeking, caption placement, and the existing movie selectors.
- All 59 Ryan narration cues fit at the model's natural tempo. The six-semitone shift preserves formants and timing. The bundled mascot speech levels come from the shifted WAV.
- FFprobe verified 22,200 contiguous 1080p frames at 30 fps and 12:20 of both audio and video. The export includes RyanSpeech attribution and a matching subtitle sidecar. The encoded closing shot preserves the voice credit and caption clearance.

### 2026-11-19 — Native internals YouTube splash

- `index.html?movie=native&splash=1` pairs the original `assets/pythago.svg` drawing with a lavender background and a two-line Spicy Rice title. Chrome checked text clearance at 480 and 3840 pixels and exported a 3840 × 2160 PNG at 1.47 MB. [YouTube thumbnail guidance](https://support.google.com/youtube/answer/72431?hl=en)
