/**
 * The transport of a television that is playing something for this page.
 *
 * A set driven over DLNA is asked, not told: every request is a SOAP round
 * trip made by the server, and none of them is fast enough to drive a
 * control that expects an answer. So whoever is playing to one carries a
 * clock of its own — moved forward a second at a time, corrected whenever
 * the set is actually asked — and reads what the set says through one rule
 * (`castStep`, in playback.ts, tested): a set that has not played yet says
 * STOPPED while it opens the file, one that has says STOPPED at the end of
 * it, and STOPPED half way is somebody with the remote.
 *
 * The music bar and the video player both did all of that, each in its own
 * words, and the two had begun to disagree — the bar read every stop as the
 * track finishing, so a listener could not stop the music from the set's
 * own remote. This is the one copy. What differs between the two is passed
 * in: how often to ask, whether to ask every second near the end of a track
 * (a gap between songs is heard, where a few seconds at the end of a film is
 * not), and how long a stop with something queued is given to become the
 * handover it probably is. Everything about what is on screen stays with
 * the caller, through the hooks — this module never touches the DOM, which
 * is what lets a test drive it with a fake set and a fake clock.
 *
 * Hooks fire only from the tick and from the set's answers, never from a
 * caller's own call: a caller that seeks knows it did, and being told again
 * from inside its own call is the kind of re-entrancy that goes wrong
 * quietly.
 */
import { castStep, type CastPoll } from './playback.ts';

/** What the set says when asked, with the URI it is holding where it says one. */
export interface CastAnswer extends CastPoll {
  uri?: string;
}

/**
 * The part of a receiver the transport drives. `Cast` (cast.ts) is one; a
 * test hands in a plain object.
 */
export interface CastLike {
  status(): Promise<CastAnswer | null>;
  queueNext(item: string, audio?: number): Promise<string | null>;
  play(): void;
  pause(): void;
  seek(seconds: number): void;
}

export interface CastHooks {
  /** The finger is on the bar: the clock must not move under it. */
  seeking(): boolean;
  /** Every tick and after every answer: the clock as it now stands. */
  onClock(pos: number, dur: number): void;
  /** The set has been seen playing what it was given. */
  onPlaying?(): void;
  /** Stopped at the end of what it was given: the queue or the season moves on. */
  onEnded(): void;
  /** Stopped short of the end, or gone: somebody with the remote, or another page. */
  onStopped(): void;
  /** It moved on by itself to what it was handed in advance. */
  onAdvanced?(uri: string): void;
  /** After every answer that was about the file, once the clock and the state are taken. */
  onPoll?(): void;
}

/** A clock to tick by: the window's, or a test's. */
export interface CastTimers {
  setInterval(fn: () => void, ms: number): unknown;
  clearInterval(id: unknown): void;
}

export interface CastOptions {
  /** How many ticks between questions to the set. */
  pollEvery: number;
  /**
   * How close to the end, in seconds, the set is asked every tick instead.
   * Left out, it is never asked more often than `pollEvery`.
   */
  endWindow?: number;
  /**
   * How many stopped answers in a row a set that was handed the next item
   * is given to move on to it before it is taken to have finished. Left
   * out, a stop is taken at its word at once.
   */
  handover?: number;
  timers?: CastTimers;
}

const windowTimers: CastTimers = {
  setInterval: (fn, ms) => setInterval(fn, ms),
  clearInterval: (id) => clearInterval(id as number),
};

export class CastTransport<C extends CastLike = CastLike> {
  readonly cast: C;
  /** Where the set is, as far as this page knows: carried forward, corrected by the poll. */
  pos = 0;
  /** How long the file is: what the library measured until the set has said. */
  dur = 0;
  /** Whether the clock is running. Written by play/pause, corrected by the poll. */
  playing = false;
  private hasPlayed = false;
  private ticks = 0;
  private stops = 0;
  private next: string | null = null;
  /**
   * Which file the answers in flight are about. Bumped whenever the file
   * changes or the poll is stopped, so an answer that was asked about the
   * last file cannot land on this one — the set reports the last film
   * stopped until it has the next, and that must not read as a second
   * ending.
   */
  private gen = 0;
  private timer: unknown = null;
  private readonly hooks: CastHooks;
  private readonly opts: CastOptions;
  private readonly timers: CastTimers;

  constructor(cast: C, hooks: CastHooks, opts: CastOptions) {
    this.cast = cast;
    this.hooks = hooks;
    this.opts = opts;
    this.timers = opts.timers ?? windowTimers;
  }

  /** Whether the set has been seen playing the file now in hand. */
  get seen(): boolean {
    return this.hasPlayed;
  }

  /** What the set was handed to play after this, if it accepted one. */
  get nextUri(): string | null {
    return this.next;
  }

  /**
   * A file: where it starts and how long it is. Nothing has yet been seen
   * of it playing, so a STOPPED from a set still opening it is not an
   * ending; and whatever was queued behind the last file is forgotten,
   * since it is this one's turn to have something queued behind it.
   */
  begin(at: number, dur: number): void {
    this.gen++;
    this.pos = at;
    this.dur = dur;
    this.playing = true;
    this.hasPlayed = false;
    this.ticks = 0;
    this.stops = 0;
    this.next = null;
  }

  /** Start the clock; started already, it is left running. */
  run(): void {
    if (this.timer != null) return;
    this.timer = this.timers.setInterval(() => this.tick(), 1000);
  }

  /**
   * Stop the clock and drop any answer still in flight. The set is told
   * nothing: that is the caller's decision, and the caller may be leaving it
   * holding the last track deliberately.
   */
  stop(): void {
    this.gen++;
    if (this.timer == null) return;
    this.timers.clearInterval(this.timer);
    this.timer = null;
  }

  play(): void {
    if (this.playing) return;
    this.playing = true;
    this.cast.play();
  }

  pause(): void {
    if (!this.playing) return;
    this.playing = false;
    this.cast.pause();
  }

  toggle(): void {
    if (this.playing) this.pause();
    else this.play();
  }

  /** The readout moves at once; the set follows a round trip later. */
  seek(t: number): void {
    this.pos = t;
    this.cast.seek(t);
  }

  /**
   * Hand the set what follows, so it can open it before it needs it, and
   * remember what it said it would play — the poll then recognises the
   * moment it moves on. A set that will not take one answers null, and the
   * caller goes on sending each item as it sees the last one end.
   */
  async queueNext(item: string, audio?: number): Promise<void> {
    this.next = null;
    const gen = this.gen;
    const uri = await this.cast.queueNext(item, audio);
    // The file changed while the set was being asked: what it accepted was
    // queued behind the last one, and claiming it here would make the poll
    // see the set "move on" to what it is already playing.
    if (gen !== this.gen) return;
    this.next = uri;
  }

  private tick(): void {
    if (this.playing && !this.hooks.seeking()) this.pos += 1;
    this.hooks.onClock(this.pos, this.dur);
    // Counted in ticks, not in the position: that comes back from the set
    // with fractions in it, and a modulo on it would ask again by accident
    // or never again at all.
    this.ticks++;
    const w = this.opts.endWindow;
    const nearEnd = w != null && this.dur > 0 && this.pos >= this.dur - w;
    if (!nearEnd && this.ticks % this.opts.pollEvery !== 0) return;
    const gen = this.gen;
    void this.cast.status().then((st) => {
      if (gen === this.gen) this.answer(st);
    });
  }

  private answer(st: CastAnswer | null): void {
    // The set moved on to what it was given in advance: nothing to send,
    // and nothing was missed — the caller simply has to catch up with it.
    if (st?.uri && this.next != null && st.uri === this.next) {
      this.hooks.onAdvanced?.(st.uri);
      return;
    }
    // A set that has measured the file outranks the library on its length,
    // and the rule below judges an ending against it — so it is taken
    // first, and the caller sees the same number the verdict was reached by.
    if (st?.duration) this.dur = st.duration;
    const step = castStep(this.hasPlayed, st, this.pos, this.dur);
    this.hasPlayed = step.seen;
    switch (step.action) {
      case 'nothing':
        return;
      case 'playing':
        this.stops = 0;
        this.hooks.onPlaying?.();
        break;
      case 'ended':
      case 'stopped':
        // Measured at a boundary, a set about to move on by itself says
        // STOPPED for a second or two first — which is also exactly how it
        // says a track has ended. Read as the second, the next track would
        // be sent into a set already starting it and the two would race.
        // So with something queued, a stop is given a few polls to become
        // the handover it probably is, and only then taken at its word.
        if (this.next != null && ++this.stops < (this.opts.handover ?? 0)) break;
        if (step.action === 'ended') this.hooks.onEnded();
        else this.hooks.onStopped();
        return;
      default:
        break;
    }
    this.playing = step.action === 'playing';
    // A set that has not opened the file will not say where it is, and a
    // zero there would drag the clock back to the beginning.
    if (st?.position && !this.hooks.seeking()) this.pos = st.position;
    this.hooks.onClock(this.pos, this.dur);
    this.hooks.onPoll?.();
  }
}
