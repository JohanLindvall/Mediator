/**
 * Spectrum view: what is sounding right now, drawn from the audio itself.
 *
 * The whole thing is optional and defensive, because the cost of getting it
 * wrong is silence rather than a missing picture. Routing an element through
 * Web Audio takes its output off the speakers and puts it in the graph, and
 * that cannot be undone — an element can only ever be handed to
 * createMediaElementSource once. So the graph is built the first time the
 * view is opened, never before; every step is guarded; and if any of it
 * throws, the source that was already made is wired straight to the output so
 * the music keeps playing and the view reports itself unavailable for good.
 *
 * What it draws is a quarter-octave analyser: 36 bands, each a minor third
 * wide, spaced geometrically from 30 Hz to 16 kHz. Three decisions carry it.
 *
 * **The bands are musical, not arithmetic.** Frequency bins are linear in Hz
 * and hearing is not, so bars have to be spaced by ratio. The power curve this
 * replaced gave its first three bars bin 0 — DC, which carries no sound at
 * all — and put the first bin with music in it under a single bar. That is
 * why the bass end moved as one rigid block: a kick and a bass note were
 * literally the same reading.
 *
 * **The display is tilted.** Music has far more energy at the bottom than the
 * top, so an honest plot of magnitude is a slope into the floor with a dead
 * right-hand third. `TILT` adds 3 dB per octave — the slope of pink noise — so
 * well-mastered material draws roughly flat and a hi-hat actually moves
 * something. That is a display convention and it is a chosen lie: what is on
 * screen is no longer raw spectral magnitude.
 *
 * **Movement is measured in seconds, not frames.** The AnalyserNode's own
 * smoothing is applied per read, so at 120 Hz it smooths twice as fast as at
 * 60 Hz — the same music drawn differently by a better screen. Nearly all of
 * it is turned off here and the smoothing is done against the clock instead.
 */

import { deafStep, tapChoice, type DeafState } from './playback';

/**
 * The panel a spectrum is shown in, and the button that opens and closes it.
 *
 * The player and the music bar used to each build this markup and each carry
 * the same open, close and mark-the-button code; what differs between them —
 * where the panel sits, what the button says, what else has to happen when
 * the sound is tapped — stays with them. The panel is the picture: no
 * heading, no close button, six pixels of padding. What a heading would
 * have said is on the canvas's own label, and the note is empty unless
 * there is something wrong to report.
 */
export class SpectrumPanel {
  readonly el: HTMLElement;
  readonly viz: Visualizer;

  constructor(
    private readonly button: HTMLElement,
    extraClass = '',
  ) {
    this.el = document.createElement('div');
    this.el.className = extraClass ? `viz-panel ${extraClass}` : 'viz-panel';
    this.el.hidden = true;
    this.el.innerHTML = `
      <div class="viz-body">
        <canvas class="viz-canvas" role="img" aria-label="Spectrum analyser"></canvas>
        <p class="viz-note" role="status"></p>
      </div>`;
    this.viz = new Visualizer(
      this.el.querySelector('canvas') as HTMLCanvasElement,
      this.el.querySelector<HTMLElement>('.viz-note'),
    );
  }

  get isOpen(): boolean {
    return !this.el.hidden;
  }

  /**
   * Tap the decks and show the panel. False when the browser cannot manage
   * the graph at all, in which case the button goes rather than staying on
   * screen as a dead control.
   *
   * The context is resumed inside the click that opened the panel, which is
   * what allows it: a context starts suspended, and a suspended one silences
   * everything routed through it.
   */
  open(decks: readonly HTMLMediaElement[]): boolean {
    if (!this.viz.attach(decks)) {
      this.button.hidden = true;
      return false;
    }
    this.viz.resume();
    this.el.hidden = false;
    requestAnimationFrame(() => this.el.classList.add('open'));
    this.mark(true);
    this.viz.start();
    return true;
  }

  /** Put the panel away and give the copied decks back. */
  close(): void {
    this.viz.stop();
    this.viz.release();
    this.el.classList.remove('open');
    this.el.hidden = true;
    this.mark(false);
  }

  /**
   * Light the button while the panel is up. With no close button on the
   * panel, this is what says the button is the way out of it as well as the
   * way in — and it is the state every other toggle already shows.
   */
  private mark(open: boolean): void {
    this.button.classList.toggle('active', open);
    this.button.setAttribute('aria-expanded', String(open));
  }
}

/** Where the display starts. Below this there is nothing to trust: at 11.7 Hz
 * per bin it is one bin of rumble, and including bin 0 (DC) is why the
 * leftmost bar used to be permanently lit. */
const MIN_F = 30;

/** Where it stops — deliberately not Nyquist. A sixth of the old panel drew
 * 16-24 kHz, which is silent in every lossy file a library is full of. */
const MAX_F = 16000;

/**
 * Bands, and the width at which we drop to the coarser rung.
 *
 * 36 bands over this range is 0.2516 octaves apiece — a quarter octave, a
 * minor third. The coarse rung is not another named interval and is not
 * pretending to be one: it is the count that still reads on a canvas under
 * 288 CSS px, where 36 bars would be four pixels wide. Two rungs and no
 * more — a phone at 320 px genuinely needs the coarse one, and anything
 * below 24 stops being a spectrum.
 */
const BANDS = 36;
const BANDS_NARROW = 24;
const NARROW_BELOW = 288;

/** Decibels per octave added to flatten music's own slope. 3.0 is pink. */
const TILT = 3.0;

/**
 * The window of loudness the height represents, and the analyser's own range
 * underneath it.
 *
 * AMIN has to sit far enough below the floor that the tilt cannot lift
 * digital silence off the ground: the top band is tilted up by 13.2 dB, so an
 * AMIN of -92 would leave it standing at a tenth of the panel's height with
 * every byte reading zero. AMAX is above the -13.5 dB a full-scale sine
 * actually reaches in these units, so nothing pins at the top.
 */
const FLOOR_DB = -85;
const SPAN_DB = 60;
const AMIN = -100;
const AMAX = -8;

/** The whole amplitude path, precomputed: byte to normalised height. */
const SCALE = (AMAX - AMIN) / (255 * SPAN_DB);

/** How fast a bar rises and falls, as time constants. Rise is nearly
 * instant — a transient should land — and the fall is slow enough to read. */
const TAU_UP = 0.02;
const TAU_DOWN = 0.15;

/** The same, for a viewer who has asked for less movement: slower, but still
 * moving. A spectrum that does not move would be lying about the audio. */
const TAU_UP_CALM = 0.12;
const TAU_DOWN_CALM = 0.4;

/**
 * The peak caps: how long one hangs before it falls, and how hard it then
 * accelerates (in heights per second squared).
 *
 * This is the only thing on screen reporting dynamics rather than
 * instantaneous level — the gap that opens between a fallen bar and its cap
 * is how hard that hit was. Full fall is sqrt(2 / gravity) seconds.
 */
const PEAK_HOLD = 0.45;
const PEAK_GRAVITY = 3.6;
const PEAK_HOLD_CALM = 1.2;
const PEAK_GRAVITY_CALM = 0.8;

/** Below this a cap is not worth drawing: it would be a mark hovering over a
 * band that reads as silent. */
const PEAK_FLOOR = 0.035;

/** A level under this counts as nothing happening, for the still-frame skip. */
const STILL_EPS = 0.002;

/**
 * The showpiece layer. Everything below is driven by the audio and only by
 * the audio — at silence every one of these terms is zero, the panel is
 * perfectly still, and the still-frame skip ends painting exactly as before.
 * That is the line this file has always drawn (no idle animation, ever), and
 * the spectacle lives inside it rather than instead of it.
 */

/** The glass floor: how much of the panel's height mirrors the bars back,
 * fading with depth. The one piece that costs bar travel, and the piece that
 * makes the block read as an object standing on a stage rather than a chart. */
const REFLECT = 0.16;

/**
 * The bloom: the panel redrawn into a canvas this many times smaller and
 * composited back over itself with `lighter`. Bilinear filtering does the
 * blur for free on the way back up — a few drawImage calls, no filter, no
 * shader — and the result is the neon halo every hardware analyser has.
 * Its strength breathes with the music (BLOOM_BASE at rest, more as the
 * energy rises), which is what makes a drop land visually.
 *
 * **It blooms the highlights, not the panel.** Compositing the whole picture
 * back over itself adds light in proportion to what was already there, so
 * every dull pixel contributes: measured on the transfer, a mid-tone of 0.30
 * was adding 0.20 at a peak, which is not a glow but a fog — the background
 * stopped being background and the whole thing read as smudged. The small
 * canvas is therefore multiplied by itself before it is composited, which
 * squares every channel: the background falls by four to fourteen times
 * while a bright bar keeps two thirds of its halo. That is a bright-pass,
 * the step every real bloom starts with, and here it is one drawImage on an
 * image a twenty-fifth of the size.
 */
const BLOOM_DOWN = 5;
const BLOOM_BASE = 0.18;
const BLOOM_ENERGY = 0.9;

/** The stage light: a radial wash rising from the baseline, scaled by the
 * smoothed overall energy and by nothing else. Zero sound, zero light. */
const GLOW_GAIN = 1.5;

/** How the overall energy is smoothed before it drives anything, so the
 * light and the bloom swell and settle rather than flickering per frame. */
const ENERGY_TAU = 0.22;
const ENERGY_TAU_CALM = 0.6;

/**
 * The hue flows along the bars, at a rate set by the energy: a quiet passage
 * drifts, a loud one streams, silence freezes. Measured in panel-widths per
 * second at full scale, so the motion reads the same at every size. The
 * gradient is periodic (accent → accent-2 → accent, twice over a double
 * width), which is what lets the phase wrap without a visible seam.
 */
const FLOW_SPEED = 0.22;

/** A rise this large in one frame is a transient, and its crown flashes —
 * the attack made visible. Decayed fast enough to read as a spark. */
const SPARK_DELTA = 0.16;
const SPARK_TAU = 0.1;
const SPARK_SHOW = 0.35;

/**
 * The kick: the beat made visible, which is the difference between a panel
 * that describes the music and one that moves with it. The bottom KICK_BANDS
 * bands are watched as one signal, and an onset — a kick drum, a bass note
 * landing — slams the stage light and the bloom. Level alone cannot say
 * this: a sustained bass line is loud without hitting, and it is the hit
 * that a head nods to.
 *
 * What counts as an onset is a rise of KICK_DELTA above the signal's own
 * **recent floor**, not above its average. The difference is blast beats: at
 * seven hits a second in a wall-of-sound mix, an average climbs to the
 * middle of the oscillation and the hits stop clearing it — the detector
 * went quiet on exactly the music with the most beat. A floor follows dips
 * instantly and rises only slowly (KICK_FLOOR_TAU), so however dense the
 * mix, the brief dip between two hits re-arms it. The rise must also be a
 * rise (bass above the previous frame), and a kick still burning past half
 * strength refuses to re-fire, which is what keeps one hit one flash.
 */
const KICK_BANDS = 6;
const KICK_DELTA = 0.08;
const KICK_TAU = 0.14;
const KICK_FLOOR_TAU = 0.5;

/**
 * The pace: the panel keeps time with the music. The spectral flux — how
 * much the bands rose this frame, per second — is a tempo the analyser can
 * read without ever finding a beat: blast beats churn it, a ballad barely
 * stirs it. It is smoothed over FLUX_TAU and mapped onto [1, PACE_MAX], and
 * the pace then divides every release time — the bars' fall, the caps' hold,
 * the kick's decay — so fast drums get a panel that falls fast enough to
 * show every hit, while a slow song keeps the slow read that suits it.
 * Attack times are left alone: a transient should land at any tempo.
 */
const FLUX_LO = 0.3;
const FLUX_HI = 1.2;
const PACE_MAX = 2.5;
const FLUX_TAU = 0.8;


/** The frame budget. Sixty is enough for this and leaves the rest of the
 * machine to the playback that matters more. */
const MIN_FRAME = 1 / 60;
const MIN_FRAME_CALM = 1 / 30;

/** Longer than this between frames means the tab was away: snap rather than
 * sweep, or everything crawls up from zero on return. */
const AWAY_GAP = 0.5;

/**
 * The largest pixel ratio worth drawing at. A three-times display would have
 * us clearing 420,000 pixels a frame for a decoration, on exactly the device
 * where playback is tightest. At two the snapping still lands on whole device
 * pixels and nothing looks softer.
 */
const MAX_DPR = 2;

/**
 * How long a deck takes to fade in as it is routed into the graph.
 *
 * Handing a *playing* element to Web Audio moves its output from the
 * speakers into the graph between one sample and the next, and a step in a
 * waveform is a click — which is what opening this view on a playing track
 * sounded like. Long enough to turn that step into a slope, short enough
 * that nobody hears it as a fade. Paid only where the sound has to be moved
 * rather than copied — see `captureAudio`.
 */
const ROUTE_FADE = 0.03;

type CapturingElement = HTMLMediaElement & {
  captureStream?: () => MediaStream;
  mozCaptureStream?: () => MediaStream;
};

/**
 * Whether tapping this deck will cost it its own output.
 *
 * Exported because the owner has to say so *before* the view is opened: a
 * button that ends AirPlay must warn, and one that does not must not.
 */
export function spectrumTakesOutput(deck: HTMLMediaElement): boolean {
  const el = deck as CapturingElement;
  return typeof (el.captureStream ?? el.mozCaptureStream) !== 'function';
}

/**
 * Take a copy of what a deck is playing, without taking it off the speakers.
 *
 * This is the whole difference between a decoration that can fail and one
 * that can take the film with it — the reasoning is with `tapChoice`, which
 * is where it can be tested. A stream with no audio track in it is returned
 * as it is rather than as nothing: "not ready" and "cannot" are different
 * answers and only one of them is worth the element's output.
 */
function captureAudio(deck: HTMLMediaElement): MediaStream | null {
  const el = deck as CapturingElement;
  const capture = el.captureStream ?? el.mozCaptureStream;
  if (typeof capture !== 'function') return null;
  try {
    return capture.call(el) ?? null;
  } catch {
    // A browser that has the call and refuses it — a tainted element, an
    // element it will not capture — has answered "no copy", not "no track".
    return null;
  }
}

/**
 * How one deck's sound reaches the analyser.
 *
 * `stream` is null where the element had to be routed, which is what says
 * the tap cannot be taken again.
 */
type Tap = {
  stream: MediaStream | null;
  node: AudioNode;
};

type RGB = [number, number, number];

/** #rgb or #rrggbb, or nothing — a token this cannot read falls back to the
 * literal rather than throwing, so a future palette in another colour space
 * degrades to the shipped one. */
function parseHex(s: string): RGB | null {
  const t = s.trim();
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(t);
  if (!m) return null;
  const h = m[1]!;
  const full = h.length === 3 ? h[0]! + h[0]! + h[1]! + h[1]! + h[2]! + h[2]! : h;
  return [
    parseInt(full.slice(0, 2), 16),
    parseInt(full.slice(2, 4), 16),
    parseInt(full.slice(4, 6), 16),
  ];
}

function rgba(c: RGB, a: number): string {
  return `rgba(${c[0]}, ${c[1]}, ${c[2]}, ${a})`;
}

/**
 * One rectangle, rounded where the browser can round it.
 *
 * A module function rather than a closure built inside the frame: the paint
 * loop's whole claim is that it allocates nothing, and a closure per frame is
 * an allocation per frame. roundRect is Safari 16.4 and later — without it
 * the bars are square, which is this app's register: draw something honest
 * rather than nothing.
 */
function box(
  g: CanvasRenderingContext2D,
  rounded: boolean,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number[],
): void {
  if (rounded) g.roundRect(x, y, w, h, r);
  else g.rect(x, y, w, h);
}

export class Visualizer {
  /** The two drawing contexts, taken once: asking the canvas per frame is a lookup nobody needs. */
  private g: CanvasRenderingContext2D | null = null;
  private bg: CanvasRenderingContext2D | null = null;
  private ctx: AudioContext | null = null;
  private analyser: AnalyserNode | null = null;
  private data = new Uint8Array(new ArrayBuffer(0));
  private raf = 0;
  /** Where the analyser's readings are pulled from, so nothing is run twice. */
  private mute: GainNode | null = null;
  /** How each deck is tapped, and whether that tap can be taken again. */
  private taps = new Map<HTMLMediaElement, Tap>();
  /** The listener each deck carries, so it can be taken off again. */
  private watching = new Map<HTMLMediaElement, () => void>();
  // The same decks again, walkable: whether anything is actually playing is
  // the question that tells silence apart from an audio path that never
  // arrived, and a map's keys are the decks that have a tap, which is not
  // the same set.
  private decks: HTMLMediaElement[] = [];

  /** False once the browser has shown it cannot do this. */
  available = true;

  /**
   * True once this browser has been *measured* to accept the routing and pass
   * nothing — see `deafStep`. Distinct from `available`, which is about the
   * graph failing to build: here everything built and works, and the audio
   * simply never arrives.
   */
  get deaf(): boolean {
    return this.deafState.deaf;
  }

  /**
   * Called the first time that is measured, so an owner can stop offering a
   * control this browser cannot honour. Not called again if sound arrives
   * later and the report is withdrawn — the owner has already acted, and a
   * button that came back would read as a fault of its own.
   */
  onDeaf?: () => void;

  // ---- the band table, rebuilt only when the canvas or the ratio changes --
  private n = 0;
  private edgeBin = new Int32Array(0);
  private edgeRat = new Float32Array(0);
  private off = new Float32Array(0);
  private x0 = new Int32Array(0);
  private bw = new Int32Array(0);

  // ---- per-band state, all preallocated ---------------------------------
  // The frame allocates nothing of its own. The one exception is the DOMRect
  // that getBoundingClientRect hands back, which is the price of measuring a
  // fractional canvas honestly — clientWidth truncates to an integer.
  private level = new Float32Array(0);
  private peak = new Float32Array(0);
  private hold = new Float32Array(0);
  private vel = new Float32Array(0);

  // ---- geometry and paint resources, rebuilt with the table ---------------
  private dpr = 1;
  private restH = 2;
  private capH = 2;
  private rad = 3;
  private rTop: number[] = [3, 3, 0, 0];
  private rPill: number[] = [1, 1, 1, 1];
  private rCap: number[] = [1, 1, 1, 1];
  private gradHue: CanvasGradient | null = null;
  private gradShade: CanvasGradient | null = null;
  private restCol = '';
  private restDim = '';
  private tipCol = '';
  private capCol = '';

  // ---- the showpiece layer (see the constants above) ----------------------
  private base = 0; // the baseline the bars stand on; below it is the mirror
  private reflH = 0;
  private energy = 0;
  private phase = 0;
  private kick = 0;
  private bassPrev = 0;
  // The floor starts high so the first frames cannot fire a kick off nothing:
  // it falls to the real signal instantly on the first reading.
  private bassFloor = 1;
  private flux = 0;
  private spark = new Float32Array(0);
  private bloomCv: HTMLCanvasElement | null = null;
  private gradGlow: CanvasGradient | null = null;
  private gradFade: CanvasGradient | null = null;
  private sparkCol = '';

  // ---- frame bookkeeping --------------------------------------------------
  private last = 0;
  private stillPainted = false;
  private wasRunning = false;
  /**
   * Whether the audio path ever arrived. The rule is in `playback.ts`, where
   * it can be tested; this only holds its state and writes the caption.
   * Measured rather than assumed of any browser: one band moving ends it.
   */
  private deafState: DeafState = { heard: false, silent: 0, deaf: false };
  private calm = false;

  /** roundRect is Safari 16.4 and later. Without it the bars are square, which
   * is the app's register: draw something honest rather than nothing. */
  private readonly rounded =
    typeof CanvasRenderingContext2D !== 'undefined' &&
    typeof CanvasRenderingContext2D.prototype.roundRect === 'function';

  constructor(
    private canvas: HTMLCanvasElement,
    // Where something wrong is reported. Empty the rest of the time: the
    // panel is the picture, and a line of chrome saying "spectrum" over a
    // spectrum is a line of bars nobody gets to see.
    private note?: HTMLElement | null,
  ) {
    const mq = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null;
    this.calm = mq?.matches ?? false;
    // Subscribed rather than read once: the panel outlives a preference
    // change, and a viewer who asks for less movement while it is open
    // should not have to reopen it.
    mq?.addEventListener?.('change', (ev) => {
      this.calm = ev.matches;
    });
  }

  /**
   * Build the graph if it is not built, and tap these decks into it.
   * Safe to call again for one that is already tapped.
   *
   * Media elements, not audio elements: a film has a soundtrack like
   * anything else, and the player taps its `<video>` into the same graph
   * the music bar taps its decks into.
   */
  attach(decks: readonly HTMLMediaElement[]): boolean {
    if (!this.available) return false;
    try {
      const Ctor = window.AudioContext ?? (window as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
      if (!Ctor) throw new Error('no AudioContext');
      if (!this.ctx) {
        this.ctx = new Ctor();
        this.analyser = this.ctx.createAnalyser();
        // Minimum before maximum: the two are checked against each other on
        // assignment, and writing them the other way round is only safe by
        // accident of what the defaults happen to be.
        this.analyser.minDecibels = AMIN;
        this.analyser.maxDecibels = AMAX;
        // 2048 bins: 11.7 Hz apiece at 48 kHz, which puts seventeen bins
        // under 200 Hz where the old 1024 had four. That is what makes a
        // bass end possible at all. Twice this would be an 170 ms window —
        // slower than the attack envelope, so transients would smear.
        this.analyser.fftSize = 4096;
        // Nearly off, and time-based smoothing does the work instead: this
        // one is applied per read rather than per second, so it smooths at
        // whatever rate the display happens to refresh at. What is left
        // damps bin noise before the per-band maximum goes looking for it.
        this.analyser.smoothingTimeConstant = 0.25;
        // The analyser is a **leaf**: what it reads has already reached the
        // speakers by its own route, and connecting it to the output would
        // play a captured deck a second time. The silent gain is there
        // because a node with nothing downstream is not guaranteed to be
        // pulled at all, and a graph nobody pulls reads zeros.
        this.mute = this.ctx.createGain();
        this.mute.gain.value = 0;
        this.analyser.connect(this.mute);
        this.mute.connect(this.ctx.destination);
        this.data = new Uint8Array(new ArrayBuffer(this.analyser.frequencyBinCount));
      }
      // A suspended context passes nothing, and anything routed into one is
      // silent until it resumes. Ask before routing rather than after, so
      // that gap never opens: this is called from inside the click that
      // opened the view, which is what allows the request.
      if (this.ctx.state === 'suspended') void this.ctx.resume().catch(() => {});
      for (const deck of decks) {
        if (!this.watching.has(deck)) {
          this.decks.push(deck);
          // A new source is a new capture: the stream belongs to the file
          // that was playing when it was taken and its track ends with that
          // file. The deck is asked rather than the several places that set
          // a source, since a fact spelled out in several places is one that
          // goes wrong quietly — and this one went wrong silently.
          const again = (): void => this.retap(deck);
          deck.addEventListener('loadeddata', again);
          this.watching.set(deck, again);
        }
        // Not `else`: a deck let go when the panel closed is watched and
        // untapped, and this is what takes it up again.
        if (!this.taps.has(deck)) this.open(deck);
      }
      return true;
    } catch {
      // Whatever went wrong, the elements must still reach the speakers.
      this.giveUp();
      return false;
    }
  }

  /**
   * Tap one deck, preferring the copy over the move.
   *
   * A capture that is not ready yet leaves the deck untapped rather than
   * falling back to routing it: the panel is a moment of emptiness either
   * way, and the fallback would spend the element's output to save it.
   */
  private open(deck: HTMLMediaElement): void {
    const ctx = this.ctx;
    const analyser = this.analyser;
    if (!ctx || !analyser) return;
    const stream = captureAudio(deck);
    const choice = tapChoice(stream != null, stream?.getAudioTracks().length ?? 0);
    if (choice === 'wait') return; // asked again when the deck reports data
    if (choice === 'copy' && stream) {
      const node = ctx.createMediaStreamSource(stream);
      node.connect(analyser);
      this.taps.set(deck, { stream, node });
      return;
    }
    // No copy to be had, so the element's own output is moved into the
    // graph. It reaches the speakers through its **own** gain rather than
    // through the analyser, so nothing about the reading — a disconnect, a
    // failure half way through building the rest — can silence the film.
    const src = ctx.createMediaElementSource(deck);
    const gain = ctx.createGain();
    const now = ctx.currentTime;
    gain.gain.setValueAtTime(0, now);
    gain.gain.linearRampToValueAtTime(1, now + ROUTE_FADE);
    src.connect(gain);
    gain.connect(ctx.destination);
    gain.connect(analyser);
    this.taps.set(deck, { stream: null, node: src });
  }

  /**
   * Whether any deck's output had to be moved into the graph to read it.
   *
   * Asked *after* attaching rather than predicted before it: a browser that
   * has the call and refuses it for this element falls back to the move, and
   * the owner of the AirPlay button needs what happened, not what was
   * likely. `spectrumTakesOutput` is the prediction, and it is only good
   * enough for wording a button nobody has pressed yet.
   */
  get movedOutput(): boolean {
    for (const tap of this.taps.values()) if (!tap.stream) return true;
    return false;
  }

  /**
   * Let the decks go, the panel having closed.
   *
   * Only the copies: a moved output cannot be given back, which is the
   * asymmetry this whole arrangement exists to exploit. The tracks are
   * dropped rather than stopped — ending a track the browser handed us is
   * poking at the element that is playing the film, and the failure being
   * guarded against here is precisely a film going quiet.
   */
  release(): void {
    for (const [deck, tap] of [...this.taps]) {
      if (!tap.stream) continue;
      try {
        tap.node.disconnect();
      } catch {
        // Already gone with the source it belonged to.
      }
      this.taps.delete(deck);
      // Let go of the deck itself as well: the next attach takes it up
      // again from scratch, and one kept here between openings is a stale
      // element for sounding() to consult on a visualiser reused across
      // films. A moved deck stays, there being nothing to give back.
      const again = this.watching.get(deck);
      if (again) deck.removeEventListener('loadeddata', again);
      this.watching.delete(deck);
      this.decks = this.decks.filter((d) => d !== deck);
    }
  }

  /**
   * Take the tap again, the deck having changed what it is playing.
   *
   * Only a captured tap can be: a routed element cannot be routed twice —
   * `createMediaElementSource` throws for one it has already taken — which
   * is the whole reason the capture is preferred. There the panel simply
   * stays as it is; the sound is the element's own and was never at stake.
   */
  private retap(deck: HTMLMediaElement): void {
    const tap = this.taps.get(deck);
    if (tap && !tap.stream) return;
    if (tap) {
      try {
        tap.node.disconnect();
      } catch {
        // Already gone with the source it belonged to.
      }
      this.taps.delete(deck);
    }
    try {
      this.open(deck);
    } catch {
      // The panel goes quiet. The sound does not.
    }
  }

  /**
   * A context starts suspended until something the user did resumes it, and
   * a suspended one produces silence for anything routed through it.
   */
  resume(): void {
    if (this.ctx?.state === 'suspended') void this.ctx.resume().catch(() => {});
  }

  start(): void {
    if (!this.analyser || this.raf) return;
    this.last = 0;
    const draw = (ts: number): void => {
      this.raf = requestAnimationFrame(draw);
      this.paint(ts);
    };
    this.raf = requestAnimationFrame(draw);
  }

  stop(): void {
    if (!this.raf) return;
    cancelAnimationFrame(this.raf);
    this.raf = 0;
  }

  /**
   * Give the audio context back.
   *
   * For the music bar there is nothing to give back — it is a singleton and
   * lives as long as the page. The player is not: a viewer who opens the
   * spectrum on six films in a row would leave six contexts behind, and a
   * browser allows about that many before it refuses to make another. The
   * elements routed through this one are being discarded with it.
   */
  dispose(): void {
    this.stop();
    for (const [deck, again] of this.watching) deck.removeEventListener('loadeddata', again);
    this.watching.clear();
    for (const tap of this.taps.values()) {
      try {
        tap.node.disconnect();
      } catch {
        // Nothing to give back.
      }
    }
    this.taps.clear();
    this.decks = [];
    this.mute = null;
    const ctx = this.ctx;
    this.ctx = null;
    this.analyser = null;
    void ctx?.close().catch(() => {});
  }

  /**
   * Report an audio path that never arrived, and take it back if it does.
   *
   * The caption is the whole report, because an empty panel with nothing said
   * about it is what makes this look like a fault in the page rather than a
   * limit of the browser. The button stays: it is what opens the panel the
   * explanation is written in.
   */
  private watchForSilence(heard: boolean, dt: number): void {
    const was = this.deafState.deaf;
    this.deafState = deafStep(this.deafState, { heard, sounding: this.sounding(), dt });
    if (this.deafState.deaf === was) return;
    if (this.deafState.deaf) this.onDeaf?.();
    if (!this.note) return;
    if (this.deafState.deaf) {
      this.note.textContent =
        'This browser plays the sound but will not pass it to the page, ' +
        'so there is nothing to draw.';
    } else {
      this.note.textContent = '';
    }
  }

  /** Whether any routed deck is actually making a sound to be read. */
  private sounding(): boolean {
    for (const deck of this.decks) {
      if (!deck.paused && !deck.muted && deck.volume > 0) return true;
    }
    return false;
  }

  /**
   * Put the sources straight on the output and never try again. Called when
   * the graph could not be finished — leaving a half-built one in place is
   * what would turn a decorative failure into a silent player.
   */
  private giveUp(): void {
    this.available = false;
    this.stop();
    // Nothing to repair: a captured deck never left the speakers, and a
    // routed one is put on the destination through its own gain before the
    // analyser is so much as mentioned. That ordering is what makes this
    // method able to do nothing.
  }

  /**
   * Work out the bands, the geometry and the paint resources for a canvas of
   * this size.
   *
   * Everything here is arithmetic the frame must never do: the logarithms
   * that place the band edges, the tilt in decibels, the gradients, and the
   * radius arrays — a literal `[r, r, 0, 0]` inside the loop allocated
   * nearly three thousand throwaway arrays a second.
   */
  private build(w: number, h: number, dpr: number, cssW: number): void {
    const an = this.analyser;
    const ctx = this.ctx;
    if (!an || !ctx) return;

    this.dpr = dpr;
    const n = cssW >= NARROW_BELOW ? BANDS : BANDS_NARROW;
    this.n = n;

    // The glass floor takes its strip off the bottom; the bars stand on what
    // is left. Skipped entirely on a panel too short to afford it.
    let reflH = Math.round(h * REFLECT);
    if (reflH < 3) reflH = 0;
    const base = h - reflH;
    this.base = base;
    this.reflH = reflH;

    // Never assume 48 kHz, and never map a band past what the rate can carry.
    const maxF = Math.min(MAX_F, ctx.sampleRate * 0.45);
    const binHz = ctx.sampleRate / an.fftSize;
    const bins = an.frequencyBinCount;
    const ratio = maxF / MIN_F;
    const tiltRef = Math.sqrt(MIN_F * maxF);

    this.edgeBin = new Int32Array(n + 1);
    this.edgeRat = new Float32Array(n + 1);
    this.off = new Float32Array(n);
    this.x0 = new Int32Array(n);
    this.bw = new Int32Array(n);
    if (this.level.length !== n) {
      this.level = new Float32Array(n);
      this.peak = new Float32Array(n);
      this.hold = new Float32Array(n);
      this.vel = new Float32Array(n);
      this.spark = new Float32Array(n);
    }

    const edge = (i: number): number => MIN_F * Math.pow(ratio, i / n);
    for (let i = 0; i <= n; i++) {
      const pos = edge(i) / binHz;
      // Two short of the end, so reading the bin after an edge is always safe.
      const b = Math.min(bins - 2, Math.max(0, Math.floor(pos)));
      this.edgeBin[i] = b;
      this.edgeRat[i] = Math.min(1, Math.max(0, pos - b));
    }
    for (let i = 0; i < n; i++) {
      // A band's own centre, geometrically — the tilt is symmetric about the
      // middle of the range rather than lifting the whole display.
      const fc = Math.sqrt(edge(i) * edge(i + 1));
      const tilt = TILT * Math.log2(fc / tiltRef);
      this.off[i] = (tilt - (FLOOR_DB - AMIN)) / SPAN_DB;
    }

    const gap = Math.max(1, Math.round(1.6 * dpr));
    const pitch = w / n;
    let minBw = w;
    for (let i = 0; i < n; i++) {
      // Half a gap is taken off each side of every bar rather than a whole
      // one off the right: the same spacing between bars, but the block ends
      // up centred in a panel whose padding is symmetric.
      const x = Math.round(i * pitch + gap / 2);
      const width = Math.max(1, Math.round((i + 1) * pitch - gap / 2) - x);
      this.x0[i] = x;
      this.bw[i] = width;
      if (width < minBw) minBw = width;
    }

    this.restH = Math.max(2, Math.round(2 * dpr));
    this.capH = this.restH;
    this.rad = Math.max(1, Math.round(Math.min(minBw * 0.35, 4 * dpr)));
    const pill = Math.min(this.restH / 2, minBw / 2);
    this.rTop = [this.rad, this.rad, 0, 0];
    this.rPill = [pill, pill, pill, pill];
    this.rCap = [this.capH / 2, this.capH / 2, this.capH / 2, this.capH / 2];

    // The palette is the app's own. This module used to be the one place in
    // the frontend that restated a design token as a literal.
    const cs = getComputedStyle(this.canvas);
    const tok = (name: string, fallback: string): RGB =>
      parseHex(cs.getPropertyValue(name)) ?? parseHex(fallback)!;
    const accent = tok('--accent', '#7c8cff');
    const accent2 = tok('--accent-2', '#38d0ff');
    const text = tok('--text', '#e9ecf4');
    const surface = tok('--bg-elev', '#10131b');

    const g = (this.g ??= this.canvas.getContext('2d'));
    if (g) {
      // **Hue runs along the frequency axis, not up the bars.** A vertical
      // ramp in canvas space is only ever sampled up to the height of the
      // tallest bar, so the cyan half of the app's accent pair was
      // unreachable at ordinary listening levels and every quiet passage was
      // a flat indigo wall. Horizontal, it is all on screen at every level —
      // and one gradient object still fills all 36 bars in a single path.
      // Periodic — accent to accent-2 and back, twice over a double width —
      // so the flow can slide it forever without a seam: the visible window
      // always holds one full cycle whatever the phase.
      this.gradHue = g.createLinearGradient(0, 0, 2 * w, 0);
      this.gradHue.addColorStop(0, rgba(accent, 1));
      this.gradHue.addColorStop(0.25, rgba(accent2, 1));
      this.gradHue.addColorStop(0.5, rgba(accent, 1));
      this.gradHue.addColorStop(0.75, rgba(accent2, 1));
      this.gradHue.addColorStop(1, rgba(accent, 1));
      // What replaces the level channel the hue gave up: a wash toward the
      // panel's own background, full strength at the baseline and gone by
      // two fifths of the height. A short bar recedes into the surface, a
      // tall one stands clear of it — and it is theme-correct by
      // construction, since it shades toward whatever the panel is.
      this.gradShade = g.createLinearGradient(0, base, 0, base * 0.58);
      this.gradShade.addColorStop(0, rgba(surface, 0.38));
      this.gradShade.addColorStop(1, rgba(surface, 0));
      // The stage light: brightest at the middle of the baseline, gone well
      // before the panel's edge. Built once; per-frame only its alpha moves.
      this.gradGlow = g.createRadialGradient(w / 2, base, 0, w / 2, base, Math.max(w * 0.55, base));
      this.gradGlow.addColorStop(0, rgba(accent2, 0.3));
      this.gradGlow.addColorStop(1, rgba(accent2, 0));
      // What fades the mirror with depth: painted destination-out, so only
      // its alpha matters and the panel's own background shows through.
      this.gradFade = null;
      if (reflH > 0) {
        this.gradFade = g.createLinearGradient(0, base, 0, h);
        this.gradFade.addColorStop(0, 'rgba(0,0,0,0.15)');
        this.gradFade.addColorStop(1, 'rgba(0,0,0,1)');
      }
    }
    this.restCol = rgba(accent, 0.24);
    this.restDim = rgba(accent, 0.14);
    // Constant alpha, deliberately not scaled by level: on a bright tall bar
    // it adds almost nothing, and on a short shaded one it is the lit edge
    // that keeps a six-pixel mark reading as an object rather than a smudge.
    this.tipCol = rgba(text, 0.22);
    // Neutral rather than accent, so a cap reads as a separate object and
    // not as more of the bar it is standing over.
    this.capCol = rgba(text, 0.5);
    // The spark is the text colour at nearly full strength: the ink flash,
    // correct in either theme, and unmistakably not more bar.
    this.sparkCol = rgba(text, 0.95);

    // What a heading would have said, on the canvas's own label instead: a
    // screen reader still finds it, and it costs nothing on screen.
    const hi = maxF >= 1000 ? `${Math.round(maxF / 100) / 10} kHz` : `${Math.round(maxF)} Hz`;
    this.canvas.setAttribute('aria-label', `Spectrum analyser, ${MIN_F} Hz to ${hi}`);
  }

  private paint(ts: number): void {
    const an = this.analyser;
    const c = this.canvas;
    if (!an) return;

    // Time first, and the frame budget before any work at all.
    const calm = this.calm;
    const minFrame = calm ? MIN_FRAME_CALM : MIN_FRAME;
    const now = ts || performance.now();
    const gapT = this.last ? (now - this.last) / 1000 : AWAY_GAP + 1;
    if (gapT < minFrame - 0.002) return;
    const away = gapT > AWAY_GAP;
    const dt = Math.min(gapT, 0.05);
    this.last = now;

    // Measured, not read off clientWidth: that truncates to an integer, and
    // the canvas is routinely a fractional width.
    const rect = c.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return;
    const dpr = Math.min(MAX_DPR, window.devicePixelRatio || 1);
    const w = Math.round(rect.width * dpr);
    const h = Math.round(rect.height * dpr);
    if (w === 0 || h === 0) return;
    // The check is on device pixels, inside the frame, and not on a resize
    // listener: dragging a window to another display changes the ratio with
    // no resize event at all, and the gradients are built in device space.
    // Moving this onto an event reintroduces a stale gradient stretched
    // across the wrong width, which is easy to miss and hard to explain.
    if (c.width !== w || c.height !== h || this.dpr !== dpr || this.n === 0) {
      c.width = w;
      c.height = h;
      this.build(w, h, dpr, rect.width);
      this.stillPainted = false;
    }

    const g = (this.g ??= c.getContext('2d'));
    if (!g) return;
    an.getByteFrequencyData(this.data);

    const n = this.n;
    const data = this.data;
    const edgeBin = this.edgeBin;
    const edgeRat = this.edgeRat;
    const off = this.off;
    const level = this.level;
    const peak = this.peak;
    const hold = this.hold;
    const vel = this.vel;
    const spark = this.spark;
    const sparkK = Math.exp(-dt / SPARK_TAU);

    // Two exponentials for the whole frame, not two per band — and every
    // release divided by the pace, so the panel falls fast enough for the
    // music it is drawing. The pace reads last frame's smoothed flux, which
    // is a frame stale and could not matter less at a 0.8 s smoothing.
    const pace = calm
      ? 1
      : Math.min(PACE_MAX, Math.max(1, 1 + ((this.flux - FLUX_LO) * (PACE_MAX - 1)) / (FLUX_HI - FLUX_LO)));
    const aUp = 1 - Math.exp(-dt / (calm ? TAU_UP_CALM : TAU_UP));
    const aDn = 1 - Math.exp((-dt * pace) / (calm ? TAU_DOWN_CALM : TAU_DOWN));
    const peakHold = (calm ? PEAK_HOLD_CALM : PEAK_HOLD) / pace;
    const gravity = (calm ? PEAK_GRAVITY_CALM : PEAK_GRAVITY) * pace * pace;

    let active = false;
    let heard = false;
    let sum = 0;
    let bass = 0;
    let flux = 0;
    for (let i = 0; i < n; i++) {
      const lo = edgeBin[i]!;
      const hi = edgeBin[i + 1]!;
      // The band's value is the loudest bin in it, not the average: a mean
      // drags a real tonal peak down toward its quiet neighbours, so a clean
      // sustained note comes out as a low broad hump. The two edges are
      // interpolated between bins — which makes the staircase invisible at
      // the bass end without pretending to resolve what is not there.
      let b = data[lo]! + (data[lo + 1]! - data[lo]!) * edgeRat[i]!;
      const other = data[hi]! + (data[hi + 1]! - data[hi]!) * edgeRat[i + 1]!;
      if (other > b) b = other;
      for (let k = lo + 1; k <= hi; k++) {
        const s = data[k]!;
        if (s > b) b = s;
      }

      let v = b * SCALE + off[i]!;
      if (v < 0) v = 0;
      else if (v > 1) v = 1;

      const prev = level[i]!;
      if (away) {
        // Back from a backgrounded tab: take the reading as it is rather
        // than sweeping up to it from wherever it was left.
        level[i] = v;
        peak[i] = v;
        hold[i] = peakHold;
        vel[i] = 0;
      } else {
        level[i] += (v - level[i]!) * (v > level[i]! ? aUp : aDn);
        if (level[i]! >= peak[i]!) {
          peak[i] = level[i]!;
          hold[i] = peakHold;
          vel[i] = 0;
        } else if (hold[i]! > 0) {
          hold[i]! -= dt;
        } else {
          vel[i]! += gravity * dt;
          peak[i] = Math.max(0, peak[i]! - vel[i]! * dt);
        }
      }
      // The attack, made visible: a band that leapt gets its crown flashed,
      // and the bottom of the range feeds the kick.
      spark[i] = !away && !calm && v - prev > SPARK_DELTA ? 1 : spark[i]! * sparkK;
      sum += level[i]!;
      if (v > prev) flux += v - prev;
      if (i < KICK_BANDS) bass += v;
      if (b > 0) heard = true;
      if (level[i]! > STILL_EPS || peak[i]! > STILL_EPS) active = true;
    }
    this.watchForSilence(heard, dt);

    // The slow answers everything breathes with: overall energy, and the
    // beat. The kick is an onset — bass rising sharply above its own recent
    // average — which level alone cannot say: a sustained bass line is loud
    // without hitting, and the hit is what a head nods to. All of it decays
    // to exactly zero in silence, so nothing here outlives the sound.
    bass /= KICK_BANDS;
    const eK = 1 - Math.exp(-dt / (calm ? ENERGY_TAU_CALM : ENERGY_TAU));
    this.energy += (sum / n - this.energy) * eK;
    if (!away) this.flux += (flux / n / dt - this.flux) * (1 - Math.exp(-dt / FLUX_TAU));

    // The onset: a real rise, clear of the recent floor, with the last flash
    // burnt out. The floor follows dips instantly and climbs slowly, which is
    // what keeps this firing through a blast beat — the brief dip between
    // two hits re-arms it where an average would have climbed out of reach.
    const rising = bass > this.bassPrev + 0.004;
    if (!away && !calm && rising && bass - this.bassFloor > KICK_DELTA && this.kick < 0.5) {
      this.kick = 1;
    } else {
      this.kick *= Math.exp((-dt * pace) / KICK_TAU);
    }
    if (bass < this.bassFloor) this.bassFloor = bass;
    else this.bassFloor += (bass - this.bassFloor) * (1 - Math.exp((-dt * pace) / KICK_FLOOR_TAU));
    this.bassPrev = bass;

    if (!calm) this.phase = (this.phase + FLOW_SPEED * (this.energy + this.kick * 0.6) * w * dt) % w;
    if (this.energy > STILL_EPS || this.kick > 0.01) active = true;

    const running = this.ctx?.state === 'running';
    // Nothing is sounding and nothing is left moving: the last frame already
    // put the resting floor on screen, so there is nothing to draw. One
    // frame is still painted after everything settles, and the reading above
    // still happens — which is what makes the first sound after silence
    // appear in the very next frame rather than a quarter of a second later.
    if (!active && this.stillPainted && running === this.wasRunning) return;
    this.stillPainted = !active;
    this.wasRunning = running;

    const rTop = this.rTop;
    const rounded = this.rounded;
    const base = this.base;
    const reflH = this.reflH;

    g.clearRect(0, 0, w, h);

    // The stage light, breathing with the energy and slammed by the kick.
    // Exactly zero in silence: no sound, no light, and the last still frame
    // is as clean as it ever was.
    const glowA = Math.min(1, this.energy * GLOW_GAIN + this.kick * 0.5);
    if (this.gradGlow && glowA > 0.004) {
      g.globalAlpha = glowA;
      g.fillStyle = this.gradGlow;
      g.fillRect(0, 0, w, base);
      g.globalAlpha = 1;
    }

    // The floor. Always there, dimmed when the graph is not running, and
    // drawn under the bodies so a bar sitting at rest shows no seam.
    const restH = this.restH;
    g.fillStyle = running ? this.restCol : this.restDim;
    g.beginPath();
    for (let i = 0; i < n; i++) box(g, rounded, this.x0[i]!, base - restH, this.bw[i]!, restH, this.rPill);
    g.fill();

    // The bodies. One path — and the colour *flows through it*: a canvas
    // path is baked in device space the moment it is built, while a gradient
    // is mapped through the transform at fill time, so sliding the transform
    // slides the hue through the bars without touching a single coordinate.
    // The phase advances with the music and freezes with it.
    g.beginPath();
    let any = false;
    for (let i = 0; i < n; i++) {
      const barH = Math.round(level[i]! * base);
      if (barH <= restH) continue;
      box(g, rounded, this.x0[i]!, base - barH, this.bw[i]!, barH, rTop);
      any = true;
    }
    if (any) {
      g.save();
      g.translate(-this.phase, 0);
      g.fillStyle = this.gradHue ?? this.restCol;
      g.fill();
      g.restore();
      if (this.gradShade) {
        g.fillStyle = this.gradShade;
        g.fill();
      }
    }

    // The lit crown, after the shade so it is never dimmed by it.
    const rad = this.rad;
    g.fillStyle = this.tipCol;
    g.beginPath();
    for (let i = 0; i < n; i++) {
      const barH = Math.round(level[i]! * base);
      if (barH < rad + restH) continue;
      box(g, rounded, this.x0[i]!, base - barH, this.bw[i]!, rad, rTop);
    }
    g.fill();

    // The sparks: crowns that leapt this instant, flashed in ink. One
    // batched path like everything else; a transient lights a handful of
    // bands and the decay puts them out inside a few frames.
    g.fillStyle = this.sparkCol;
    g.beginPath();
    let lit = false;
    for (let i = 0; i < n; i++) {
      if (spark[i]! <= SPARK_SHOW) continue;
      const barH = Math.round(level[i]! * base);
      if (barH < rad + restH) continue;
      box(g, rounded, this.x0[i]!, base - barH, this.bw[i]!, rad, rTop);
      lit = true;
    }
    if (lit) g.fill();

    // The bloom: the panel so far, redrawn small and composited back with
    // `lighter`. Bilinear filtering is the blur — two drawImage calls buy
    // the halo a hardware analyser has, and its strength rides the energy
    // and the kick so a drop lands visually. Before the caps, which stay
    // needle-sharp over the glow.
    if (!this.bloomCv) this.bloomCv = document.createElement('canvas');
    const bw2 = Math.max(1, Math.round(w / BLOOM_DOWN));
    const bh2 = Math.max(1, Math.round(base / BLOOM_DOWN));
    const bc = this.bloomCv;
    if (bc.width !== bw2 || bc.height !== bh2) {
      bc.width = bw2;
      bc.height = bh2;
    }
    const bg = (this.bg ??= bc.getContext('2d'));
    if (bg) {
      bg.globalCompositeOperation = 'source-over';
      bg.clearRect(0, 0, bw2, bh2);
      bg.drawImage(c, 0, 0, w, base, 0, 0, bw2, bh2);
      // The bright-pass. Multiplying the image by itself squares every
      // channel, so what was dim becomes negligible and what was bright
      // barely moves — the haze goes and the halo stays. A canvas may be
      // drawn into itself; the source is snapshotted first.
      bg.globalCompositeOperation = 'multiply';
      bg.drawImage(bc, 0, 0);
      bg.globalCompositeOperation = 'source-over';
      g.save();
      g.globalCompositeOperation = 'lighter';
      g.globalAlpha = BLOOM_BASE + Math.min(0.5, this.energy * BLOOM_ENERGY + this.kick * 0.35);
      g.drawImage(bc, 0, 0, bw2, bh2, 0, 0, w, base);
      g.restore();
    }

    // The glass floor: the strip above the baseline mirrored below it,
    // bloom and all, then faded out with depth. destination-out, so what
    // shows through the fade is the panel's own background.
    if (reflH > 0) {
      g.save();
      g.globalAlpha = 0.3;
      g.scale(1, -1);
      g.drawImage(c, 0, base - reflH, w, reflH, 0, -(base + reflH), w, reflH);
      g.restore();
      if (this.gradFade) {
        g.save();
        g.globalCompositeOperation = 'destination-out';
        g.fillStyle = this.gradFade;
        g.fillRect(0, base, w, reflH);
        g.restore();
      }
    }

    // The caps last: one must never be hidden by the bar it belongs to, and
    // the gap between the two is the only thing here reporting dynamics
    // rather than instantaneous level.
    const capH = this.capH;
    g.fillStyle = this.capCol;
    g.beginPath();
    for (let i = 0; i < n; i++) {
      if (peak[i]! <= PEAK_FLOOR) continue;
      // Clamped into the panel, and clamped *before* the guard below so that
      // guard tests the position actually painted. The cap is the only thing
      // here drawn upward from the value it marks: at full scale it was
      // placed at y = -capH and vanished — precisely on the loudest hit,
      // which is the one it exists to report.
      const capY = Math.min(Math.round(peak[i]! * base), base - capH);
      if (capY < Math.round(level[i]! * base) + capH + dpr) continue;
      box(g, rounded, this.x0[i]!, base - capY - capH, this.bw[i]!, capH, this.rCap);
    }
    g.fill();
  }
}
