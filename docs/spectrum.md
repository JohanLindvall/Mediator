# The spectrum analyser

- **The player has the spectrum too** (the same `Visualizer`, whose `attach`
  takes media elements, and `SpectrumPanel`), prepended to `.vo-controls` to
  stay above the seek bar however the row wraps, and hidden in fullscreen
  (`syncViz` stops painting). Where the sound must be moved it costs the
  AirPlay route: the button warns first, `showReceiverButton` drops the
  receiver button, and `routed` follows the actual tap (`movedOutput`).
  Dimmed while a DLNA set plays. `dispose()` closes the context with the
  player, as a browser allows few.
- **The spectrum copies the sound and moves it only where it must**
  (`tapChoice` in `visualizer.ts`, tested). `createMediaElementSource` moves
  output irreversibly, taking the AirPlay route with it, and any later
  failure (e.g. a `video.src` swap) mutes the film until reload and falsely
  trips `videoAudioIsReadable`. Copy via `HTMLMediaElement.captureStream`
  (`mozCaptureStream`); **wait**, never move, while the deck has no
  soundtrack (the capture would carry no track); move only with no capture
  (Safari). The
  deck's `loadeddata` re-takes the tap; `release()` returns copies on close
  (a move stays). The analyser is a leaf on a silent gain (to the
  destination a captured deck plays twice; an unpulled graph reads zeros); a
  moved deck plays through its own gain, wired first, so `giveUp` has
  nothing to repair. Build the graph on first open only, fade moved decks in
  over `ROUTE_FADE`, and resume the context before tapping.
- **A quarter-octave analyser**: 36 ratio-spaced bands of 0.2516 octaves
  from 30 Hz to `min(16 kHz, sampleRate * 0.45)` (lossy files are silent
  above), 24 below a 288 px canvas; a power curve would put DC under the
  first bars. `fftSize` 4096 (17 bins under 200 Hz), not 8192 (a 170 ms
  window smears transients). Tilted +3 dB/octave about the geometric centre
  so music reads roughly flat, deliberately not raw magnitude; hence
  `minDecibels` -100, or the lifted top band shows in silence. A band is its
  loudest bin, not its mean (which flattens tonal peaks); edges interpolate,
  but the bottom two octaves are not truly resolved. `smoothingTimeConstant`
  applies per read (frame-rate dependent), so it is only 0.25; the real
  smoothing is `1 - exp(-dt/tau)` on the frame clock, 20 ms up, 150 ms down.
  Caps hold `PEAK_HOLD`, then fall under `PEAK_GRAVITY`.
- **The panel is the picture**: no heading or close button, 6 px padding; its
  button closes it too and is lit while open (`SpectrumPanel.mark`); the label
  is the canvas `aria-label`; the only text, the fault report `.viz-note`,
  hides by `:empty`.
- **Silence is a designed state**: nothing moves without sound; never add an
  idle animation. The floor dims when the context is not running, and every
  showpiece term is zero at silence. Once bars and caps are under
  `STILL_EPS`, energy and kick have decayed and one settled frame is
  painted, painting stops, but the analyser is still read each frame (~0.4%
  of budget) so sound shows on the next frame (a timer throttle would lag
  ~¼ s).
- **A dead audio path says so** (`deafStep` in `playback.ts`, tested;
  `watchForSilence`): phone WebKit accepts routing a `<video>` yet passes no
  sound (an `<audio>` works), so no feature test predicts it, and nothing
  avoids it (no `captureStream()` in Safari; a second element would
  double the bytes). After `DEAF_AFTER` s of real playing (unmuted, volume
  up) with no band ever moving, the header reads "no audio reaches the page"
  — never once sound was heard, counting playing time only, withdrawn if
  sound arrives. `onDeaf` then has `videoAudioIsReadable` drop the button
  for the session (the measuring film keeps its panel); not persisted, as
  WebKit may fix it.
- **The canvas carries no padding** (under `border-box` the bitmap would not
  match its box, resampling every composite); padding is on `.viz-body`.
  Size via `getBoundingClientRect`, not truncating `clientWidth`, checked in
  device pixels each frame rather than by a resize listener (a pixel-ratio
  change fires none; gradients are device-space); pixel ratio capped at 2.
- **Hue runs along the frequency axis**, not up the bars (a vertical ramp is
  only sampled to the tallest bar). The 36 bodies are one path and one fill;
  refilling it (`fill()` keeps the path) washes toward the background up to
  two fifths high; a constant crown at 22% of `--text` edges each bar.
  Colours come from the app's tokens at build time, never literals.
- **The showpiece layer is driven by sound alone**, zero at silence:
  - **Bloom**: the panel redrawn `BLOOM_DOWN` times smaller, multiplied by
    itself (a bright-pass, or dull pixels fog) and added back with `lighter`;
    no `filter()` or shader; radius five, not eight (tinier is blotchy).
  - **Glass floor** (`REFLECT`: the strip above the baseline mirrored below
    it, faded destination-out), **stage light** (alpha = smoothed energy) and
    **sparks** (a crown flashes when its band rose over `SPARK_DELTA` in a
    frame).
  - **Hue flow**: translating between path build and fill slides a periodic
    gradient (accent → accent-2 → accent, twice over double width, so it
    wraps seamlessly) through the geometry, advancing with energy.
  - **Kick** (the bottom `KICK_BANDS` bands; slams light, bloom and flow):
    an onset is a rise of `KICK_DELTA` over the recent **floor**, not the
    average, which dense blast beats lift past the hits; the floor drops at
    once and climbs slowly; a kick above half strength does not re-fire.
  - **`pace`**: spectral flux smoothed over `FLUX_TAU`, mapped to
    `[1, PACE_MAX]`, divides every release (bar fall, cap hold and gravity,
    kick decay, floor climb), never an attack. Reduced motion pins it at 1
    and stills flow, sparks and kick.
  - Deliberately not done: album-art palettes (per-track cost, off the
    accents), per-bar fill styles (break the batched path), frequency
    hairlines (unreadable on a phone).
