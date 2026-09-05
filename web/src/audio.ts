/**
 * Persistent bottom-bar music player: play queue with shuffle/repeat,
 * seek, volume, a queue panel, and OS media-key integration (Media Session).
 */
import { setLike, tracksOf, PLAY_AFTER, recordPlay, streamUrl, thumbUrl, type Item } from './api';
import {
  airPlaySupported,
  remotePlaybackSupported,
  showAirPlayPicker,
  watchAirPlay,
} from './airplay';
import { Cast, fillReceiverMenu, knownRenderers, renderers } from './cast';
import { CastTransport, type CastHooks } from './casting';
import type { RendererInfo } from './types.gen';
import { claimMediaKeys, setPlaybackState } from './mediakeys';
import { playingAudio } from './nowplaying';
import { clamp, esc, formatDuration } from './format';
import { playButtonIcon } from './playback';
import { recall, remember } from './remember';
import { icons } from './icons';
import { showToast } from './toast';
import { appendToOrder, nextPosition, placeFirst, resumable, shuffleInPlace, windowRows } from './queue';
import { sameRelease } from './cover';
import { shareItem } from './links';
import { SpectrumPanel } from './visualizer';

/**
 * How close to the end of a track the next one starts buffering. Long enough
 * that it is ready when it is wanted, short enough that only one boundary's
 * worth of the library is ever being fetched twice over.
 */
const PRELOAD_LEAD_SEC = 15;
/**
 * The most a queue may hold, however it is filled. Not a limit anybody
 * meets: the whole library goes in and is shuffled there, and the panel
 * draws a window of it rather than every row.
 */
const QUEUE_CAP = 1 << 20;
/**
 * How many tracks radio keeps ahead of the one playing. A radio's worth is
 * fetched whenever the queue runs shorter than this, so it never runs dry
 * and never fills with a hundred tracks nobody asked for.
 */
const RADIO_AHEAD = 5;
/** A queue panel row's height, pinned in the stylesheet: the window is laid out from it. */
const Q_ROW = 38;
/** Rows drawn beyond the visible window on either side, so a scroll never shows blank. */
const Q_MARGIN = 8;
/** The overflow button's glyph; a glyph rather than an icon, since it is text. */
const MORE_GLYPH = '⋯';

/**
 * How long the music takes to fall silent when it is paused, and to come
 * back when it is resumed.
 *
 * Stopping an element outright cuts the waveform wherever it happens to be,
 * and a step in a waveform is a click — the same fault the spectrum's
 * `ROUTE_FADE` exists for, heard every time anybody pressed pause. A tenth
 * of a second or so turns the step into a slope: long enough that nothing
 * clicks, short enough that the button still feels immediate.
 *
 * The ramp is on the element's own volume rather than a Web Audio gain,
 * because the graph is only ever built if somebody opens the spectrum —
 * routing a deck into it cannot be undone, and pausing is not a good enough
 * reason to spend that.
 */
const FADE_MS = 150;

/**
 * How often a television is actually asked where it has got to, in seconds
 * of the clock the bar carries forward between answers. It is also how long
 * a finished track can go unnoticed before the next one is sent, which is
 * why it is not longer: a gap between songs is heard.
 */
const CAST_POLL = 3;

/**
 * How close to the end of a track the set is asked every second instead.
 *
 * The track that follows is not sent until the set says the last one has
 * ended, so however often it is asked is how long the silence between two
 * songs is. Asking constantly for the whole of a track to save two seconds
 * at the end of it is the wrong trade; asking constantly for the last few
 * seconds is the right one, and the clock carried forward here already
 * knows when those are.
 */
const CAST_END_WINDOW = 6;

/**
 * How many stopped answers in a row are allowed before a set that was given
 * the next track is taken to have declined to play it after all. Each is a
 * second, since a stop near the end of a track is inside the window above.
 */
const CAST_HANDOVER = 5;

export class AudioPlayer {
  /**
   * Two elements, alternated. Loading a new source into the element that is
   * playing is what the silence between tracks was: teardown, a fresh
   * request, and a decode, all after the last sample of the previous track
   * had already gone quiet. So the track that is coming buffers in the other
   * element while this one plays, and the boundary is a change of deck.
   */
  private decks: [HTMLAudioElement, HTMLAudioElement] = [new Audio(), new Audio()];
  private deckUrl: [string, string] = ['', ''];
  private deck = 0;
  private queue: Item[] = [];
  private order: number[] = [];
  private orderPos = -1;
  private shuffle = false;
  private repeat = false;
  private seeking = false;
  private errStreak = 0;
  /**
   * Who filled the queue: an opaque token handed to `playItems`, `null` for
   * queues nobody claimed (the grid builds one from the whole library). Views
   * compare it with their own token to tell "this is mine" from "this is
   * someone else's", instead of guessing from what the queue contains.
   */
  private queueCtx: string | null = null;
  /**
   * The television the queue is playing on, if it is not playing here.
   *
   * The target outlives any one track: a queue handed to a set keeps going
   * there, so each track in turn is started on it as the one before ends —
   * which is the poll's job, the set having no idea there is a queue at all.
   */
  private castTarget: RendererInfo | null = null;
  /**
   * The set's transport while a track is on it (casting.ts, the same one
   * the player uses): the clock the bar carries forward, the poll that
   * corrects it, and what the set has been handed to play next — the queue
   * then advances on the television itself, with no silence at the
   * boundary, and this page finds out by seeing the URI it is playing
   * become that one. One per track, a receiver being opened on one item.
   */
  private tv: CastTransport<Cast> | null = null;
  /** What the bar does with what the set says; the transport decides what it means. */
  private readonly castHooks: CastHooks = {
    seeking: () => this.seeking,
    onClock: (pos, dur) => this.paintTime(pos, dur),
    onPoll: () => this.updatePlayState(),
    onPlaying: () => {
      this.errStreak = 0;
    },
    onAdvanced: () => this.advanceWithSet(),
    onEnded: () => this.next(true),
    onStopped: () => {
      // Stopped short of the end is somebody with the set's own remote. The
      // bar used to read every stop as the track finishing and send the next
      // one, so the music could not be stopped from the television at all;
      // now the casting ends. Except where the set gave no length: an ending
      // cannot then be told from a stop, and the old reading — the queue
      // goes on — is the one that keeps the music playing.
      if (this.tv && this.tv.dur > 0) this.endCast(false);
      else this.next(true);
    },
  };
  /** Whether the track now loaded has been counted as played. */
  private played = false;
  /**
   * What the volume control says, which is the ceiling every fade climbs
   * back to. Held here because during a fade the element's own volume is
   * somewhere in between and is no longer the answer to "how loud is this".
   */
  private volume = 1;
  /** The fade in flight: its generation, its frame, and its endpoint. */
  private fadeGen = 0;
  private fadeRaf = 0;
  private fadeEnd = 0;
  private browserReceiver = new Set<HTMLMediaElement>();
  private netReceivers = false;
  /**
   * Set when the queue ran out — `next(true)` pauses without advancing, so the
   * element is left parked on a track that has already finished. The element's
   * own `ended` flag cannot stand in for this: the flag is also true for the
   * instant between the `pause` and `ended` events at every track boundary,
   * and it stays true after the queue is exhausted even if `repeat` is
   * switched on afterwards, when nothing is going to wrap it.
   */
  private exhausted = false;
  /** The track whose sleeve the bar has up — see showTrack. */
  private shown: Item | null = null;
  /** The sleeve fetched ahead of the boundary, so it is fetched once. */
  private artAhead = '';
  /**
   * Radio: when on, the queue is kept going with tracks that sound like the
   * one playing (RADIO_AHEAD). Remembered, since it is a way of listening
   * rather than a choice about one track.
   */
  private radio = recall('media.radio') === '1';
  private radioBusy = false;
  /** The verdict request in flight: only the latest answer is applied. */
  private rateGen = 0;
  /** Whether a media session has ever been set for this page — see prefetchArt. */
  private sessionSet = false;
  /** The queue panel's pending repaint, and the window it last drew. */
  private queueRaf = 0;
  private queuePainted = { first: -1, last: -1, cur: -1 };

  private root: HTMLElement;
  private queuePanel: HTMLElement;
  private spectrum: SpectrumPanel;
  private els!: {
    cover: HTMLImageElement;
    title: HTMLElement;
    artist: HTMLElement;
    play: HTMLButtonElement;
    shuffle: HTMLButtonElement;
    repeat: HTMLButtonElement;
    seek: HTMLInputElement;
    cur: HTMLElement;
    dur: HTMLElement;
    vol: HTMLInputElement;
    queueBtn: HTMLButtonElement;
    airplay: HTMLButtonElement;
    vizBtn: HTMLButtonElement;
    like: HTMLButtonElement;
    dislike: HTMLButtonElement;
    radio: HTMLButtonElement;
    share: HTMLButtonElement;
    more: HTMLButtonElement;
  };
  private castMenu!: HTMLElement;
  private moreMenu!: HTMLElement;

  /** Fired whenever the current track changes (or playback stops). */
  onTrackChange: (item: Item | null) => void = () => {};

  /** Observers of the play state — see watch(). */
  private watchers = new Set<() => void>();

  constructor(container: HTMLElement) {
    this.root = container;
    this.root.innerHTML = `
      <div class="ab-seekrow">
        <span class="ab-time" data-cur>0:00</span>
        <input class="ab-seek" data-seek type="range" min="0" max="1000" value="0" aria-label="Seek">
        <span class="ab-time" data-dur>0:00</span>
      </div>
      <div class="ab-main">
        <img class="ab-cover" alt="" hidden>
        <div class="ab-cover-fallback">${icons.music}</div>
        <div class="ab-info">
          <div class="ab-title" data-title></div>
          <div class="ab-artist" data-artist></div>
        </div>
        <div class="ab-controls">
          <button class="icon-btn sm ab-shuffle" data-shuffle aria-label="Shuffle" aria-pressed="false">${icons.shuffle}</button>
          <button class="icon-btn" data-prev aria-label="Previous">${icons.prev}</button>
          <button class="icon-btn ab-play" data-play aria-label="Play">${icons.play}</button>
          <button class="icon-btn" data-next aria-label="Next">${icons.next}</button>
          <button class="icon-btn sm ab-repeat" data-repeat aria-label="Repeat" aria-pressed="false">${icons.repeat}</button>
        </div>
        <div class="ab-side">
          <input class="ab-vol" data-vol type="range" min="0" max="1" step="0.02" aria-label="Volume">
          <div class="ab-menu-wrap">
            <button class="icon-btn sm" data-airplay aria-label="Play on another device" aria-haspopup="menu" aria-expanded="false" hidden>${icons.airplay}</button>
            <div class="ab-menu" data-castmenu role="menu" hidden></div>
          </div>
          <button class="icon-btn sm ab-viz" data-viz aria-label="Spectrum">${icons.grid}</button>
          <button class="icon-btn sm ab-like" data-like="1" aria-label="Like" aria-pressed="false" title="Like: outranks any number of plays in the popular orders">${icons.thumbUp}</button>
          <button class="icon-btn sm ab-like" data-like="-1" aria-label="Dislike" aria-pressed="false" title="Dislike: sinks below anything untouched">${icons.thumbDown}</button>
          <button class="icon-btn sm ab-radio" data-radio aria-label="Radio" aria-pressed="false" title="Radio: keep the queue going with tracks that sound like this one">${icons.radio}</button>
          <button class="icon-btn sm" data-share aria-label="Copy a link to this track">${icons.link}</button>
          <div class="ab-menu-wrap ab-more-wrap">
            <button class="icon-btn sm ab-more" data-more aria-label="More" aria-haspopup="menu" aria-expanded="false">${MORE_GLYPH}</button>
            <div class="ab-menu" data-moremenu role="menu" hidden></div>
          </div>
          <button class="icon-btn sm" data-queue aria-label="Queue">${icons.queue}</button>
          <button class="icon-btn sm" data-close aria-label="Stop and close">${icons.close}</button>
        </div>
      </div>`;

    this.spectrum = new SpectrumPanel(this.root.querySelector('[data-viz]') as HTMLElement);
    document.body.appendChild(this.spectrum.el);

    this.queuePanel = document.createElement('div');
    this.queuePanel.className = 'queue-panel';
    this.queuePanel.hidden = true;
    document.body.appendChild(this.queuePanel);

    this.els = {
      cover: this.q<HTMLImageElement>('.ab-cover'),
      title: this.q('[data-title]'),
      artist: this.q('[data-artist]'),
      play: this.q<HTMLButtonElement>('[data-play]'),
      shuffle: this.q<HTMLButtonElement>('[data-shuffle]'),
      repeat: this.q<HTMLButtonElement>('[data-repeat]'),
      seek: this.q<HTMLInputElement>('[data-seek]'),
      cur: this.q('[data-cur]'),
      dur: this.q('[data-dur]'),
      vol: this.q<HTMLInputElement>('[data-vol]'),
      queueBtn: this.q<HTMLButtonElement>('[data-queue]'),
      airplay: this.q<HTMLButtonElement>('[data-airplay]'),
      vizBtn: this.q<HTMLButtonElement>('[data-viz]'),
      like: this.q<HTMLButtonElement>('[data-like="1"]'),
      dislike: this.q<HTMLButtonElement>('[data-like="-1"]'),
      radio: this.q<HTMLButtonElement>('[data-radio]'),
      share: this.q<HTMLButtonElement>('[data-share]'),
      more: this.q<HTMLButtonElement>('[data-more]'),
    };
    this.castMenu = this.q('[data-castmenu]');
    this.moreMenu = this.q('[data-moremenu]');
    this.press(this.els.radio, this.radio);
    this.bind();

    const vol = Number(recall('media.volume') ?? '1');
    this.setVolume(Number.isFinite(vol) ? vol : 1);
    this.els.vol.value = String(this.volume);
  }

  /** The element that is sounding. */
  private get audio(): HTMLAudioElement {
    return this.decks[this.deck];
  }

  /** The other one: free, or holding the track that comes next. */
  private get idle(): HTMLAudioElement {
    return this.decks[1 - this.deck];
  }

  // ---- public API ------------------------------------------------------

  get current(): Item | null {
    const i = this.order[this.orderPos];
    return i === undefined ? null : (this.queue[i] ?? null);
  }

  get isPlaying(): boolean {
    return !this.audio.paused;
  }

  /**
   * True when pressing play would *continue* rather than start something over.
   * A view whose button offers "Resume" has to know, because the word is a
   * promise: `play()` on an ended element seeks back to zero per the HTML play
   * algorithm, so resuming a played-out queue replays its last track, and
   * `play()` on a failed one does nothing at all. Both must fall back to
   * "Play", which starts the collection from the top — what that word
   * promises in turn.
   *
   * Being at the end of the last track is not by itself disqualifying. The
   * spec fires `pause` with `ended` already true immediately before the
   * `ended` event, so between the two events every boundary looks finished;
   * only the boundary with nothing after it is, and with `repeat` on not even
   * that one, because `next()` is about to wrap. What settles it is
   * `exhausted`: the player parked here and no longer intends to move.
   */
  canResume(): boolean {
    // `exhausted` disqualifies on its own, without asking where in the order
    // the position sits. Tying it to "the position is last" put the answer at
    // the mercy of anything that moves the position without loading: shuffling
    // from the bar rebuilds the order and resets the position to zero, leaving
    // the element parked on the finished track while the arithmetic said there
    // was more to come — and "Resume" then replayed that track from the start.
    // The final boundary is the one case `exhausted` has not caught yet: the
    // spec fires `pause` with `ended` already true just before the `ended`
    // event, so for that instant every boundary looks finished. Only the one
    // with nothing after it is, and with `repeat` on not even that, because
    // `next()` is about to wrap. The rule itself is `resumable`, tested.
    return resumable({
      loaded: this.current != null,
      failed: this.audio.error != null,
      exhausted: this.exhausted,
      ended: this.audio.ended,
      atLast: this.orderPos + 1 >= this.order.length,
      repeat: this.repeat,
    });
  }

  /**
   * The token the current queue was started with, `null` when the queue was
   * built without one. See `queueCtx`.
   */
  queueContext(): string | null {
    return this.queueCtx;
  }

  /**
   * Queue index of the current track, or -1 when nothing is loaded. This is an
   * index into the queue, not into the play order, so it can be compared with
   * the position of a track in the list a caller handed to `playItems`.
   */
  get queuePos(): number {
    return this.order[this.orderPos] ?? -1;
  }

  /**
   * Observe playback state: the current track and whether it is sounding.
   * Unlike `onTrackChange` (a single slot, and silent on pause/resume) this
   * is a set, and it fires for every state change wherever it comes from —
   * the bar's own buttons, the OS media keys, a track ending, another view
   * taking over the queue. Returns an unsubscribe so an overlay can drop its
   * subscription when it closes.
   */
  watch(fn: () => void): () => void {
    this.watchers.add(fn);
    return () => {
      this.watchers.delete(fn);
    };
  }

  /** Copy the set first: a watcher may unsubscribe from inside its callback. */
  private emit(): void {
    for (const fn of [...this.watchers]) fn();
  }

  /**
   * Replace the queue and start playback at queue index `start`. `context` is
   * the caller's own identity — an album sheet passes its album id, the grid
   * passes nothing — and it is the *only* thing that later answers "is this
   * queue mine?". Every path that replaces the queue therefore has to pass its
   * token or explicitly pass none: an unlabelled queue must not be mistaken
   * for a labelled one that has been superseded.
   */
  playItems(items: Item[], start = 0, shuffleAll = false, context: string | null = null): void {
    if (items.length === 0) {
      showToast('Nothing to play');
      return;
    }
    this.queueCtx = context;
    this.queue = items.slice(0, QUEUE_CAP);
    this.shuffle = shuffleAll;
    this.rebuildOrder(shuffleAll ? -1 : start);
    this.orderPos = 0;
    // Both ways: a sequential start after a shuffled one must put the lamp
    // out, or it advertises an order the player is not using.
    this.press(this.els.shuffle, shuffleAll);
    this.load(true);
    this.show();
  }

  /**
   * Put `items` after everything already queued — in the order they came,
   * or shuffled among themselves with shuffle on (`appendToOrder`, tested),
   * and says what it did: the callers are over the bar, whose own count is
   * then not in view.
   *
   * Three states of the queue, three outcomes. Nothing loaded: there is no
   * end to add to, so the items become the queue and play. Played out: the
   * player is parked on its last track with `exhausted` saying nothing
   * follows, and now something does, so it moves on to the first of these
   * at once. Still going: nothing changes but the order — and a television
   * that was handed nothing to follow the current track, that having been
   * the last, is handed the first of these.
   *
   * The queue keeps its owner: the sheet that started it is still playing
   * its collection, with more after it. The cap is the one `playItems`
   * applies; past it nothing is taken, and the caller says so.
   */
  enqueue(items: Item[], context: string | null = null): void {
    if (items.length === 0) {
      showToast('Nothing to queue');
      return;
    }
    if (this.current == null) {
      // Nothing to add to: the release simply plays, and the toast says the
      // outcome rather than the mechanism.
      this.playItems(items, 0, false, context);
      const n = Math.min(items.length, QUEUE_CAP);
      showToast(`Playing ${n.toLocaleString()} track${n === 1 ? '' : 's'}`);
      return;
    }
    const taken = this.append(items);
    if (taken === 0) {
      showToast('The queue is full');
      return;
    }
    showToast(`${taken.toLocaleString()} track${taken === 1 ? '' : 's'} added to the queue`);
  }

  /**
   * The appending itself, shared by enqueue and radio: after everything
   * already queued, and a player that had run out moves on. Returns how
   * many were taken.
   */
  private append(items: Item[]): number {
    const taken = items.slice(0, Math.max(0, QUEUE_CAP - this.queue.length));
    if (taken.length === 0) return 0;
    const first = this.queue.length;
    // Not a spread: the whole library is more arguments than a call takes.
    for (const t of taken) this.queue.push(t);
    const at = appendToOrder(this.order, first, taken.length, this.shuffle);
    if (this.exhausted) {
      // The position is not necessarily the last of the old order — a
      // shuffle from the bar rebuilds the order under a parked player — so
      // it goes to where the new tracks begin rather than one step on.
      this.orderPos = at;
      this.load(true);
    } else if (this.tv && this.tv.nextUri == null) {
      void this.queueAhead();
    }
    this.renderQueue();
    this.emit();
    return taken.length;
  }

  /** Radio on or off; on, the queue is topped up at once. */
  private setRadio(on: boolean): void {
    this.radio = on;
    remember('media.radio', on ? '1' : '0');
    this.press(this.els.radio, on);
    if (on) void this.topUp();
    else showToast('Radio off');
  }

  /** A toggle's lamp and what a screen reader is told of it, together. */
  private press(btn: HTMLButtonElement, on: boolean): void {
    btn.classList.toggle('active', on);
    btn.setAttribute('aria-pressed', String(on));
  }

  /**
   * Keep the queue going: when fewer than RADIO_AHEAD tracks follow the one
   * playing, fetch the ones that sound most like it and put them after
   * everything queued, leaving out what is queued already. Asked for on
   * every track change and when the queue runs out; one ask at a time.
   */
  private async topUp(): Promise<void> {
    const it = this.current;
    if (!this.radio || this.radioBusy || !it) return;
    if (this.order.length - 1 - this.orderPos >= RADIO_AHEAD) return;
    this.radioBusy = true;
    try {
      const res = await tracksOf('similar', { id: it.id, n: 20 });
      // A fast skip meanwhile: the batch is for the track just left, and
      // the track now playing asks for its own on the next change.
      if (this.current !== it) return;
      const have = new Set(this.queue.map((q) => q.id));
      const fresh = res.tracks.filter((t) => !have.has(t.id));
      if (fresh.length > 0) this.append(fresh);
      else if (this.order.length - 1 - this.orderPos <= 0) showToast('Radio: nothing else sounds like this yet');
    } catch {
      // The next track change asks again.
    } finally {
      this.radioBusy = false;
    }
  }

  toggle(): void {
    if (!this.current) return;
    if (this.playing()) this.pausePlayback();
    else this.resumePlayback();
  }

  /** Whether sound is coming out of something, here or on a television. */
  private playing(): boolean {
    if (this.tv) return this.tv.playing;
    return !this.audio.paused;
  }

  /** Carry on from where it is, if there is anything loaded. */
  private resumePlayback(): void {
    if (!this.current) return;
    if (this.tv) {
      // The television is doing the playing, and a fade here would be a fade
      // of nothing: the decks are silent and the set has its own transport.
      if (this.tv.playing) return;
      this.tv.play();
      this.updatePlayState();
      return;
    }
    if (!this.audio.paused) return;
    this.spectrum.viz.resume();
    // Start silent and climb, so the first sample is not a step up from
    // nothing. If a fade out is still running this simply reverses it from
    // wherever it had got to.
    if (!this.fadeRaf) this.audio.volume = 0;
    void this.audio.play().catch(() => {});
    this.fade(true);
  }

  /** Stop sounding without losing anything: same track, same position. */
  private pausePlayback(): void {
    if (this.tv) {
      if (!this.tv.playing) return;
      this.tv.pause();
      this.updatePlayState();
      return;
    }
    if (this.audio.paused) return;
    // Faded out first, and only actually paused at the end of it — pausing
    // now would cut the waveform and the fade would be of silence.
    this.fade(false);
  }

  /**
   * Ramp the sounding deck to silence or back to the volume control's level.
   *
   * Two clocks, deliberately. The frames are what make it smooth, and the
   * timeout is what makes it *happen*: a browser does not run animation
   * frames for a hidden tab, and a pause pressed on a headset with the tab
   * in the background would otherwise fade half way and never stop the
   * music at all. Whichever arrives first, the endpoint is applied once.
   */
  private fade(up: boolean): void {
    const deck = this.audio;
    const gen = ++this.fadeGen;
    window.clearTimeout(this.fadeEnd);
    cancelAnimationFrame(this.fadeRaf);
    const from = deck.volume;
    const started = performance.now();

    const finish = (): void => {
      if (gen !== this.fadeGen) return;
      cancelAnimationFrame(this.fadeRaf);
      window.clearTimeout(this.fadeEnd);
      this.fadeRaf = 0;
      this.fadeEnd = 0;
      if (up) {
        deck.volume = this.volume;
        return;
      }
      deck.pause();
      // Left at the level it will be wanted at: the next thing to touch this
      // deck is a resume, or a track change that expects a working volume.
      deck.volume = this.volume;
    };

    const step = (now: number): void => {
      if (gen !== this.fadeGen) return;
      const t = Math.min(1, (now - started) / FADE_MS);
      // The target is read every frame rather than captured, so moving the
      // volume control during a fade in lands on the new level.
      const to = up ? this.volume : 0;
      deck.volume = clamp(from + (to - from) * t, 0, 1);
      if (t < 1) {
        this.fadeRaf = requestAnimationFrame(step);
        return;
      }
      finish();
    };
    this.fadeRaf = requestAnimationFrame(step);
    this.fadeEnd = window.setTimeout(finish, FADE_MS + 20);
  }

  next(auto = false): void {
    if (this.orderPos + 1 < this.order.length) {
      this.orderPos++;
      this.load(true);
    } else if (this.repeat && this.order.length > 0) {
      if (this.shuffle) this.rebuildOrder(-1);
      this.orderPos = 0;
      this.load(true);
    } else if (auto) {
      // Nothing follows: park on the finished track and record that we did,
      // so a later `repeat` toggle cannot make this look like a queue that is
      // still going. The flag is cleared by whatever genuinely starts playing
      // again — a new track through `load()`, or the element resuming, which
      // the `play` listener catches. With radio on, the top-up that follows
      // finds the player parked and moves it on to what it fetched.
      this.exhausted = true;
      if (this.radio) void this.topUp();
      this.audio.pause();
      // Nothing follows on the television either: leave the last track
      // showing there rather than stopping it, the same as here — and stop
      // asking, or the poll would find it stopped for ever and call this
      // again every few seconds for as long as the page is open.
      if (this.tv) {
        this.tv.playing = false;
        this.tv.stop();
      }
      this.updatePlayState();
    }
  }

  prev(): void {
    const at = this.tv ? this.tv.pos : this.audio.currentTime;
    if (at > 3 || this.orderPos <= 0) {
      if (this.tv) this.tv.seek(0);
      else this.audio.currentTime = 0;
      return;
    }
    this.orderPos--;
    this.load(true);
  }

  close(): void {
    this.endCast(false);
    this.cancelFade();
    for (const deck of this.decks) this.stopDeck(deck);
    this.queue = [];
    this.order = [];
    this.orderPos = -1;
    // The queue is gone, so its owner's claim goes with it: whoever started it
    // must not still be told the player is playing their collection.
    this.queueCtx = null;
    this.exhausted = false;
    this.shown = null;
    this.artAhead = '';
    this.hideQueue();
    this.spectrum.close();
    this.root.hidden = true;
    document.body.classList.remove('has-audio');
    playingAudio(null);
    this.onTrackChange(null);
    this.emit();
  }

  // ---- internals -------------------------------------------------------

  private bind(): void {
    for (const deck of this.decks) this.bindDeck(deck);
    this.bindControls();
  }

  /**
   * Wire one deck. Every handler asks whether the event came from the deck
   * that is sounding: the other one is buffering what comes next, and its
   * progress, its stalls and its failures are nobody's business but the
   * preloader's.
   */
  private bindDeck(a: HTMLAudioElement): void {
    const mine = (): boolean => a === this.audio;
    a.addEventListener('play', () => {
      if (!mine()) return;
      // Playback resumed, so the queue is no longer parked at its end —
      // whatever restarted it. `toggle()` and the OS media key both call
      // `play()` straight on an ended element, which the spec restarts from
      // zero WITHOUT going through `load()`, so clearing the flag there is
      // not enough: it would stay set while the track was audibly playing,
      // and the sheet would offer to start an album that was already sounding
      // instead of pausing it. This event fires only on genuine resumption,
      // which is exactly the moment the flag stops being true; rebuilding the
      // order (the bar's shuffle) fires nothing, so a parked queue stays
      // parked.
      this.exhausted = false;
      this.updatePlayState();
    });
    a.addEventListener('pause', () => {
      if (mine()) this.updatePlayState();
    });
    a.addEventListener('ended', () => {
      if (mine()) this.next(true);
    });
    a.addEventListener('playing', () => {
      if (mine()) this.errStreak = 0;
    });
    a.addEventListener('waiting', () => {
      if (!mine()) return;
      // The audible track has run out of buffer. Whatever the idle deck was
      // fetching for a boundary that has not arrived yet is not worth the
      // bandwidth now: the two share one connection budget and one disk.
      // It is fetched again once this track is comfortable, which is what
      // the buffered check in maybePreloadNext waits for.
      this.clearDeck(this.idle);
    });
    a.addEventListener('error', () => {
      if (!mine()) {
        // A preload that failed is simply an absent preload: the track it
        // was for is loaded the ordinary way when its turn comes, and the
        // track that is playing is not disturbed by it.
        this.clearDeck(a);
        return;
      }
      if (!this.current) return;
      showToast(`Cannot play ${this.current.name}`, 3000);
      // Stop once every queued track has failed, or repeat would spin
      // forever — and say so, since a bar that simply stops reads as a fault
      // rather than a queue nothing in could be played.
      this.errStreak++;
      if (this.errStreak >= this.order.length) {
        this.audio.pause();
        this.updatePlayState();
        showToast('Nothing in the queue could be played', 4000);
        return;
      }
      this.next(true);
    });
    a.addEventListener('timeupdate', () => {
      if (!mine()) return;
      this.maybePreloadNext();
      // Counted once per track, and only once it has really played: a queue
      // skipped through is not twenty plays.
      if (!this.played && a.currentTime >= PLAY_AFTER && this.current) {
        this.played = true;
        recordPlay(this.current.id);
      }
      if (this.seeking) return;
      this.paintTime(a.currentTime, Number.isFinite(a.duration) ? a.duration : 0);
    });
  }

  /** Write the clock and the bar, from here or from a television. */
  private paintTime(cur: number, dur: number): void {
    this.els.cur.textContent = formatDuration(cur);
    this.els.dur.textContent = formatDuration(dur);
    this.els.seek.value = dur > 0 ? String(Math.round((cur / dur) * 1000)) : '0';
    this.els.seek.style.setProperty('--fill', `${dur > 0 ? (cur / dur) * 100 : 0}%`);
  }

  /** Wire the bar's own controls, which are about the player, not a deck. */
  private bindControls(): void {
    this.on('[data-play]', () => this.toggle());
    this.on('[data-next]', () => this.next());
    this.on('[data-prev]', () => this.prev());
    this.on('[data-close]', () => this.close());
    this.on('[data-shuffle]', () => {
      this.shuffle = !this.shuffle;
      this.press(this.els.shuffle, this.shuffle);
      this.rebuildOrder(this.order[this.orderPos] ?? -1);
      this.renderQueue();
    });
    this.on('[data-repeat]', () => {
      this.repeat = !this.repeat;
      this.press(this.els.repeat, this.repeat);
    });
    this.on('[data-share]', () => this.shareTrack());
    this.on('[data-like="1"]', () => void this.rate(1));
    this.on('[data-like="-1"]', () => void this.rate(-1));
    this.on('[data-radio]', () => this.setRadio(!this.radio));
    this.on('[data-queue]', () => this.toggleQueue());
    this.on('[data-viz]', () => this.toggleViz());
    this.els.more.addEventListener('click', (ev) => {
      ev.stopPropagation();
      this.toggleMoreMenu();
    });
    // AirPlay is the element's own route, so it is offered per deck — and
    // only once WebKit says a receiver exists. Note that on a browser with
    // no capture, opening the spectrum moves the decks' output into Web
    // Audio, after which they have no route of their own to hand over; see
    // airplay.ts.
    // The decks are made rather than written into the markup, and a browser
    // that is asked what a *detached* element could be played on has one
    // fewer reason to answer usefully. They draw nothing without controls,
    // so keeping them in the bar costs nothing.
    this.root.append(...this.decks);
    for (const deck of this.decks) {
      watchAirPlay(deck, (available) => {
        if (available) this.browserReceiver.add(deck);
        else this.browserReceiver.delete(deck);
        // The idle deck holds no file and so has nothing it can be sent to;
        // it must not take the button away from the one that is playing.
        this.showReceiverButton();
      });
    }
    this.els.airplay.addEventListener('click', (ev) => {
      ev.stopPropagation();
      // A television on the network is a choice to make; without one the
      // button's only meaning is the browser's own picker.
      if (knownRenderers().length > 0) this.toggleCastMenu();
      else showAirPlayPicker(this.audio);
    });
    document.addEventListener('pointerdown', (ev) => {
      // A press anywhere but on a menu or its button closes it.
      const t = ev.target as Node;
      if (!this.castMenu.hidden && !this.castMenu.contains(t) && t !== this.els.airplay) {
        this.toggleCastMenu(false);
      }
      if (!this.moreMenu.hidden && !this.moreMenu.contains(t) && t !== this.els.more) {
        this.toggleMoreMenu(false);
      }
    });

    const seek = this.els.seek;
    seek.addEventListener('input', () => {
      this.seeking = true;
      const d = this.tv ? this.tv.dur : Number.isFinite(this.audio.duration) ? this.audio.duration : 0;
      this.els.cur.textContent = formatDuration((Number(seek.value) / 1000) * d);
      seek.style.setProperty('--fill', `${Number(seek.value) / 10}%`);
    });
    seek.addEventListener('change', () => {
      const frac = Number(seek.value) / 1000;
      if (this.tv) {
        this.tv.seek(frac * this.tv.dur);
      } else {
        const d = Number.isFinite(this.audio.duration) ? this.audio.duration : 0;
        this.audio.currentTime = frac * d;
      }
      this.seeking = false;
    });
    this.els.vol.addEventListener('input', () => this.setVolume(Number(this.els.vol.value)));
    // Remembered when the drag ends, not per pixel of it: every input event
    // was a write to storage, which is not what a slider is for.
    this.els.vol.addEventListener('change', () => remember('media.volume', this.els.vol.value));

    // Each action does what it is named rather than toggling: a control that
    // believes the music is playing and a player that has already paused
    // would otherwise argue, and whichever arrives second wins. Stop is a
    // pause that keeps its place — the queue, the track and the position all
    // stay — so the same key starts it again.
    claimMediaKeys({
      play: () => this.resumePlayback(),
      pause: () => this.pausePlayback(),
      stop: () => this.pausePlayback(),
      previous: () => this.prev(),
      next: () => this.next(),
    });
  }

  private on(sel: string, fn: () => void): void {
    this.q(sel).addEventListener('click', fn);
  }

  /** One element of the bar's own markup, typed by the caller. */
  private q<T extends HTMLElement = HTMLElement>(sel: string): T {
    return this.root.querySelector(sel) as T;
  }

  /** Copy a link to the track that is playing. */
  private shareTrack(): void {
    const it = this.current;
    if (it) void shareItem(it.id, it.name);
  }

  /**
   * The overflow: on a phone the spectrum, the radio and the link fold into
   * this one menu (the stylesheet hides their buttons below 720px and shows
   * this one), each entry saying its state so the fold costs nothing but a
   * press. Built on opening, since the states move.
   */
  private toggleMoreMenu(show = this.moreMenu.hidden): void {
    if (show) {
      this.moreMenu.innerHTML = `
        <button class="vo-menu-item" data-m="viz" aria-pressed="${this.spectrum.isOpen}"${this.els.vizBtn.disabled ? ' disabled' : ''}>Spectrum${this.spectrum.isOpen ? ' — on' : ''}</button>
        <button class="vo-menu-item" data-m="radio" aria-pressed="${this.radio}">Radio${this.radio ? ' — on' : ''}</button>
        <button class="vo-menu-item" data-m="share">Copy a link to this track</button>`;
      for (const el of this.moreMenu.querySelectorAll<HTMLElement>('[data-m]')) {
        el.addEventListener('click', () => {
          this.toggleMoreMenu(false);
          switch (el.dataset.m) {
            case 'viz':
              this.toggleViz();
              break;
            case 'radio':
              this.setRadio(!this.radio);
              break;
            case 'share':
              this.shareTrack();
              break;
          }
        });
      }
    }
    this.moreMenu.hidden = !show;
    this.els.more.setAttribute('aria-expanded', String(show));
  }

  /** Build the play order; `firstIdx` (queue index) is placed first if >= 0. */
  private rebuildOrder(firstIdx: number): void {
    this.order = this.queue.map((_, i) => i);
    if (this.shuffle) shuffleInPlace(this.order);
    if (firstIdx >= 0) {
      placeFirst(this.order, firstIdx);
      this.orderPos = 0;
    }
  }

  /**
   * Set the output level. One place, and it clamps: nothing in this player
   * is allowed to drive the output past unity, where the only thing above it
   * is clipping. The slider cannot ask for more, and neither can a stored
   * value from a browser that once could.
   */
  private setVolume(v: number): void {
    const level = clamp(Number.isFinite(v) ? v : 1, 0, 1);
    this.volume = level;
    // The deck being faded is left alone: it is on its way somewhere and
    // knows where, and writing the new level onto it mid-ramp would be a
    // jump to full volume in the middle of a fade out. It arrives at this
    // level anyway, since the ramp reads it as it goes.
    for (const deck of this.decks) {
      if (this.fadeRaf && deck === this.audio) continue;
      deck.volume = level;
    }
    // While a television is playing this, the sound is the television's.
    this.tv?.cast.volume(level * 100);
  }

  /** Point a deck at a track and remember what it is holding. */
  private setDeckSrc(a: HTMLAudioElement, url: string): void {
    a.preload = 'auto';
    a.src = url;
    this.deckUrl[this.decks.indexOf(a) as 0 | 1] = url;
  }

  /** Silence a deck and let go of what it was holding. */
  private stopDeck(a: HTMLAudioElement): void {
    a.pause();
    this.clearDeck(a);
  }

  /** Release a deck's source without touching whether it is playing. */
  private clearDeck(a: HTMLAudioElement): void {
    if (a.getAttribute('src') !== null) {
      a.removeAttribute('src');
      a.load(); // without this the element keeps buffering what it had
    }
    this.deckUrl[this.decks.indexOf(a) as 0 | 1] = '';
  }

  /**
   * The track `next()` would play, when that is knowable. With shuffle on at
   * the end of the order it is not: `next()` deals a fresh order, and which
   * track comes up has not been decided yet.
   */
  private nextItem(): Item | null {
    const pos = nextPosition(this.orderPos, this.order.length, this.repeat, this.shuffle);
    return pos == null ? null : (this.queue[this.order[pos]!] ?? null);
  }

  /**
   * Start buffering the next track as this one runs out. Held off until near
   * the end so that only one boundary's worth of the library is ever being
   * fetched twice at once — the browser gives an origin about six
   * connections, and playback needs one of them more than this does.
   */
  private maybePreloadNext(): void {
    const a = this.audio;
    const d = a.duration;
    if (!Number.isFinite(d) || d <= 0 || d - a.currentTime > PRELOAD_LEAD_SEC) return;
    // Not while this track still has fetching left to do. The two decks share
    // one connection budget and one disk, so a preload started over a track
    // that has not finished arriving takes bandwidth from the track that is
    // audible — which is heard as a gap, and the gap this exists to remove is
    // at the boundary, not in the middle. Usually a track of a few megabytes
    // is long since complete by here and this costs nothing.
    if (!this.fullyBuffered(a)) return;
    const next = this.nextItem();
    if (!next) return;
    this.prefetchArt(next);
    const url = streamUrl(next.id);
    if (this.deckUrl[1 - this.deck] === url) return; // already waiting
    this.setDeckSrc(this.idle, url);
  }

  /**
   * Fetch the next track's sleeve while this one still plays, as the deck
   * does its sound: the thumbnail is served immutable, so at the boundary
   * the bar's <img> finds it in the cache and the sleeve changes with the
   * title instead of a second behind it. The artwork the media session shows
   * goes the same way where there is one. Once per next track.
   */
  private prefetchArt(next: Item): void {
    const url = thumbUrl(next.id, 96, next.mtime);
    if (this.artAhead === url) return;
    this.artAhead = url;
    new Image().src = url;
    // The larger artwork only where a media session has actually been set
    // on this page: a desktop that never shows one would fetch it for nothing.
    if (this.sessionSet) new Image().src = thumbUrl(next.id, 360, next.mtime);
  }

  /** Whether everything left of this track is already in hand. */
  private fullyBuffered(a: HTMLAudioElement): boolean {
    const d = a.duration;
    if (!Number.isFinite(d) || d <= 0) return false;
    const b = a.buffered;
    if (b.length === 0) return false;
    // The range holding the playhead is the one that matters; a file fetched
    // in pieces can have others that do not help.
    for (let i = 0; i < b.length; i++) {
      if (a.currentTime >= b.start(i) - 0.5 && a.currentTime <= b.end(i) + 0.5) {
        return b.end(i) >= d - 0.5;
      }
    }
    return false;
  }

  private load(autoplay: boolean): void {
    const item = this.current;
    if (!item) return;
    this.exhausted = false;
    this.played = false;

    // A queue handed to a television keeps going there: each track in turn
    // is started on the set as the one before it ends, which is the poll's
    // work — the set is given one track at a time and knows nothing of a
    // queue. Everything below is the same either way, the bar showing what
    // is playing wherever it is playing.
    if (this.castTarget) this.castLoad(item);
    else this.loadDecks(item, autoplay);

    this.showTrack(item);
  }

  /**
   * Put a track on the bar. Separate from starting it, because a television
   * given the next track in advance moves on by itself: nothing here starts
   * anything, and the bar still has to follow.
   */
  private showTrack(item: Item): void {
    const title = item.title || item.name;
    this.els.title.textContent = title;
    playingAudio(title);
    this.els.title.title = item.path;
    this.els.artist.textContent = item.artist || item.album || '';
    this.markLike(item.like ?? 0);
    void this.topUp();
    // The sleeve. Another release's never stands under this title: an <img>
    // keeps its picture until the next one has loaded, and at a change of
    // release that left the old sleeve under the new track for as long as
    // the thumbnail took — measured at 0.8 s on a busy server. Within one
    // release the picture coming is the one already up, so it stays rather
    // than blinking through the placeholder at every boundary.
    const cover = this.els.cover;
    cover.hidden = !(this.shown != null && sameRelease(this.shown, item));
    this.shown = item;
    cover.onload = () => {
      cover.hidden = false;
    };
    cover.onerror = () => {
      cover.hidden = true;
    };
    cover.src = thumbUrl(item.id, 96, item.mtime);
    this.updateMediaSession(item, title);
    this.onTrackChange(item);
    this.renderQueue();
    // Announce the new track here rather than waiting for the `play` event:
    // that event is asynchronous, and it never arrives at all if autoplay is
    // rejected — the observers would then be stuck on the previous track.
    this.emit();
  }

  /**
   * The owner's verdict on the current track; pressing the lit thumb
   * withdraws it. The copy in the queue is marked at once — it is what the
   * buttons read, and what the tiles will read when the listing refetches —
   * and the server is told; a refusal puts it back, so the bar never shows
   * a verdict nobody recorded. The library bumps its version for it, which
   * is what re-sorts every popular order on screen.
   */
  private async rate(v: 1 | -1): Promise<void> {
    const it = this.current;
    if (!it) return;
    const was = it.like ?? 0;
    it.like = was === v ? 0 : v;
    this.markLike(it.like);
    // Two quick presses are two requests in flight; only the latest answer
    // may land, or the first one back would overwrite the second's choice.
    const gen = ++this.rateGen;
    try {
      const like = (await setLike(it.id, it.like)).like;
      if (gen !== this.rateGen) return;
      it.like = like;
    } catch {
      if (gen !== this.rateGen) return;
      it.like = was;
      showToast('Could not record that');
    }
    if (this.current === it) this.markLike(it.like ?? 0);
  }

  /** Light the verdict's thumb, and no other. */
  private markLike(like: number): void {
    this.press(this.els.like, like === 1);
    this.press(this.els.dislike, like === -1);
  }

  // ---- playing to a television ------------------------------------------

  /**
   * Ask what is on the network, once there is something to play to it.
   *
   * Only when the bar appears: a search is a multicast and a couple of
   * seconds of waiting, and a library being browsed with the music off has
   * nothing to send anywhere.
   */
  private async findReceivers(): Promise<void> {
    const found = await renderers();
    if (found.length === 0) return;
    this.netReceivers = true;
    this.showReceiverButton();
    this.buildCastMenu(found);
  }

  /**
   * The spectrum is of what this page is playing, so it is offered only
   * while this page is playing something. The button is disabled rather than
   * removed: it is a thing the bar has, temporarily not applicable, and a
   * control that vanishes and comes back reads as a fault.
   */
  private showSpectrumButton(): void {
    this.els.vizBtn.disabled = this.tv != null || this.castTarget != null;
    this.els.vizBtn.title = this.els.vizBtn.disabled
      ? 'The sound is on the television — there is nothing here to draw'
      : 'Spectrum';
  }

  private showReceiverButton(): void {
    this.els.airplay.hidden = !(this.browserReceiver.size > 0 || this.netReceivers);
  }

  private buildCastMenu(found: RendererInfo[]): void {
    fillReceiverMenu(this.castMenu, found, {
      here: this.castTarget != null,
      currentId: this.castTarget?.id ?? null,
      picker: airPlaySupported(this.audio) || remotePlaybackSupported(this.audio),
      onPick: (target) => this.startCast(target),
      onHere: () => {
        this.toggleCastMenu(false);
        this.endCast(true);
      },
      onPicker: () => {
        this.toggleCastMenu(false);
        showAirPlayPicker(this.audio);
      },
    });
  }

  private toggleCastMenu(show = this.castMenu.hidden): void {
    this.castMenu.hidden = !show;
    this.els.airplay.setAttribute('aria-expanded', String(show));
  }

  /**
   * Send the queue to a television, from where the track has got to.
   *
   * The decks go quiet rather than being left holding the track: two copies
   * of one song in one house is not a feature, and the spectrum — which is
   * fed by the decks — has nothing to draw either way.
   */
  private startCast(target: RendererInfo): void {
    this.toggleCastMenu(false);
    const item = this.current;
    if (!item) return;
    const at = this.audio.currentTime;
    for (const deck of this.decks) this.stopDeck(deck);
    this.castTarget = target;
    this.els.airplay.classList.add('active');
    this.spectrum.close();
    this.showSpectrumButton();
    this.castLoad(item, at);
    this.buildCastMenu(knownRenderers());
  }

  /** Start one track on the set; the queue is this side's business. */
  private castLoad(item: Item, at = 0): void {
    const target = this.castTarget;
    if (!target) return;
    // Sent when it is handed over rather than after five seconds: the set is
    // playing it and this page has no clock on it to wait for.
    recordPlay(item.id);
    // The last track's transport stands down first, or its poll would report
    // that track stopped while this one is opening.
    this.tv?.stop();
    const tv = new CastTransport(new Cast(target, item.id), this.castHooks, {
      pollEvery: CAST_POLL,
      endWindow: CAST_END_WINDOW,
      handover: CAST_HANDOVER,
    });
    this.tv = tv;
    tv.begin(at, (item.duration ?? 0) / 1000);
    this.paintTime(tv.pos, tv.dur);
    this.updatePlayState();
    void tv.cast.start(at).catch(() => {
      if (this.tv !== tv) return;
      showToast(`${target.name} cannot play ${item.name}`, 3000);
      // Move on rather than sit on a track it refused — but stop once every
      // track has been refused, or repeat would spin forever.
      this.errStreak++;
      if (this.errStreak >= this.order.length) this.endCast(false);
      else this.next(true);
    });
    tv.run();
    this.queueAhead();
  }

  /**
   * Hand the set what follows, so it can open it before it needs it.
   *
   * A renderer that will not take one leaves the transport holding nothing,
   * and the poll goes on sending each track as it sees the last one end —
   * which works, and is audibly a second or two of nothing.
   */
  private queueAhead(): void {
    const tv = this.tv;
    const next = this.nextItem();
    if (!tv || !next) return;
    // The set will move on by itself, and the bar follows: have the sleeve
    // ready for when it does.
    this.prefetchArt(next);
    void tv.queueNext(next.id);
  }

  /**
   * Follow the television into the track it started on its own.
   *
   * Everything a track change does here happens except the sending of it:
   * the position moves, the bar shows the new track, and the one after it is
   * handed over in turn.
   */
  private advanceWithSet(): void {
    if (this.orderPos + 1 < this.order.length) this.orderPos++;
    else if (this.repeat && this.order.length > 0) this.orderPos = 0;
    else return; // nothing follows: whatever it is playing is not ours
    const item = this.current;
    const tv = this.tv;
    if (!item || !tv) return;
    this.exhausted = false;
    tv.begin(0, (item.duration ?? 0) / 1000);
    this.showTrack(item);
    this.queueAhead();
  }

  /**
   * Stop playing to the set. resumeHere picks the track up in the bar at the
   * position the television reached, which is what "play here instead"
   * means; without it the bar simply stops driving anything.
   */
  private endCast(resumeHere: boolean): void {
    if (!this.castTarget) return;
    const tv = this.tv;
    const at = tv?.pos ?? 0;
    this.tv = null;
    this.castTarget = null;
    tv?.stop();
    tv?.cast.stop();
    this.els.airplay.classList.remove('active');
    this.showSpectrumButton();
    this.buildCastMenu(knownRenderers());
    const item = this.current;
    if (resumeHere && item) {
      this.loadDecks(item, true);
      this.audio.currentTime = at;
    }
    this.updatePlayState();
  }

  /**
   * Stop any fade and put both decks back at full level.
   *
   * Both, not just the one fading: a track change can swap the decks, and a
   * deck left holding a third of the volume becomes the one that plays the
   * next track — quietly, for no reason anybody could see.
   */
  private cancelFade(): void {
    this.fadeGen++;
    cancelAnimationFrame(this.fadeRaf);
    window.clearTimeout(this.fadeEnd);
    this.fadeRaf = 0;
    this.fadeEnd = 0;
    for (const deck of this.decks) deck.volume = this.volume;
  }

  /** Point the decks at a track: the way this bar plays things itself. */
  private loadDecks(item: Item, autoplay: boolean): void {
    // A new track ends whatever the last one was in the middle of. The fade
    // belongs to pausing, not to a boundary — those are gapless by deck
    // swap, and fading one would put a hole between two tracks.
    this.cancelFade();
    const url = streamUrl(item.id);
    if (this.deckUrl[1 - this.deck] === url) {
      // The preloader already holds this one. Changing decks is the whole
      // point: no request, no decode, and the silence that used to sit at
      // every track boundary was both of those.
      this.stopDeck(this.audio);
      this.deck = 1 - this.deck;
    } else if (this.deckUrl[this.deck] !== url) {
      // Somewhere the preloader did not see coming — a jump in the queue, a
      // shuffle, the track after a failure. Free the other deck: whatever it
      // was holding is not what comes next any more.
      this.stopDeck(this.idle);
      this.setDeckSrc(this.audio, url);
    }
    // Asked for from the top — but only when it is not there already. A deck
    // the preloader filled has never played and is at zero, and seeking one
    // to where it already is still costs a seek: right at a track boundary
    // that is a hiccup in the place this was supposed to have removed one.
    if (this.audio.currentTime > 0.01) this.audio.currentTime = 0;
    if (autoplay) {
      this.spectrum.viz.resume();
      void this.audio.play().catch(() => this.updatePlayState());
    }
  }

  private updateMediaSession(item: Item, title: string): void {
    if (!('mediaSession' in navigator)) return;
    this.sessionSet = true;
    navigator.mediaSession.metadata = new MediaMetadata({
      title,
      artist: item.artist ?? '',
      album: item.album ?? '',
      artwork: [{ src: thumbUrl(item.id, 360, item.mtime), sizes: '360x360', type: 'image/jpeg' }],
    });
  }

  private updatePlayState(): void {
    const playing = this.playing();
    // Through the pinned helper: this very line used to read the other way
    // around, a triangle while the music played.
    this.els.play.innerHTML = icons[playButtonIcon(playing)];
    // The label says what the press will do, as the icon does.
    this.els.play.setAttribute('aria-label', playing ? 'Pause' : 'Play');
    this.root.classList.toggle('playing', playing);
    setPlaybackState(playing);
    // Every pause/resume funnels through here — the element's own play/pause
    // events cover the bar's button, the media keys and the end of the queue.
    this.emit();
  }

  private show(): void {
    this.root.hidden = false;
    document.body.classList.add('has-audio');
    void this.findReceivers();
  }

  // ---- queue panel -----------------------------------------------------

  /**
   * Open or close the spectrum.
   *
   * The graph is built here and nowhere earlier: on a browser with no way
   * to copy a deck's sound it has to be *moved* into the graph, which is
   * irreversible, so it is not done to a player that may never be asked for
   * this. If the browser cannot manage it, the button says so once and goes
   * away rather than leaving a dead control on the bar.
   */
  private toggleViz(): void {
    if (this.spectrum.isOpen) {
      this.spectrum.close();
      return;
    }
    // Nothing here is making the sound: the decks are silent and the set is
    // decoding the file itself, so the graph would draw a flat line — and
    // where the sound has to be moved rather than copied, that flat line
    // costs the deck its own AirPlay route for good.
    if (this.tv) return;
    if (!this.spectrum.open(this.decks)) showToast('This browser cannot show the spectrum', 3000);
  }

  private toggleQueue(): void {
    if (this.queuePanel.hidden) {
      // Unhide first: renderQueue does nothing while the panel is hidden
      // (that is what keeps it cheap when tracks change with it closed),
      // so filling it before showing it left an empty box on screen.
      this.queuePanel.hidden = false;
      this.renderQueue();
      requestAnimationFrame(() => this.queuePanel.classList.add('open'));
    } else {
      this.hideQueue();
    }
  }

  private hideQueue(): void {
    this.queuePanel.classList.remove('open');
    this.queuePanel.hidden = true;
  }

  /**
   * The panel draws a window of the queue, not the queue: a queue of the
   * whole library is a hundred thousand rows, and building them all — on
   * every track change, since the current row moves — is what a browser
   * does not handle. The list is one tall spacer, the rows in view are
   * laid over it by position, and a scroll repaints the window.
   */
  private renderQueue(): void {
    if (this.queuePanel.hidden) return;
    let list = this.queuePanel.querySelector<HTMLElement>('.q-list');
    if (!list) {
      // Built once and kept: a track change updates the count and the
      // spacer rather than rebuilding the panel, which is what let the
      // listener's scroll survive it.
      this.queuePanel.innerHTML = `
        <div class="q-head">
          <span data-qcount></span>
          <button class="icon-btn sm" data-qclose aria-label="Close queue">${icons.close}</button>
        </div>
        <div class="q-list"><div class="q-space"></div></div>`;
      this.queuePanel.querySelector('[data-qclose]')!.addEventListener('click', () => this.hideQueue());
      list = this.queuePanel.querySelector<HTMLElement>('.q-list')!;
      list.addEventListener('click', (ev) => {
        const row = (ev.target as HTMLElement).closest<HTMLElement>('.q-row');
        if (!row) return;
        this.orderPos = Number(row.dataset.oi);
        this.load(true);
      });
      // Coalesced to a frame: a scroll fires far more often than the
      // screen can show, and each paint is a rebuild of the window.
      list.addEventListener(
        'scroll',
        () => {
          if (this.queueRaf) return;
          this.queueRaf = requestAnimationFrame(() => {
            this.queueRaf = 0;
            this.paintQueue();
          });
        },
        { passive: true },
      );
    }
    this.queuePanel.querySelector('[data-qcount]')!.textContent = `Queue · ${this.order.length.toLocaleString()}`;
    (list.firstElementChild as HTMLElement).style.height = `${this.order.length * Q_ROW}px`;
    // The current track is brought to the middle only when it is out of
    // view: a listener reading further down the queue was yanked back to
    // it each song.
    const top = this.orderPos * Q_ROW;
    if (top < list.scrollTop || top + Q_ROW > list.scrollTop + list.clientHeight) {
      list.scrollTop = Math.max(0, top - list.clientHeight / 2 + Q_ROW / 2);
    }
    // Whatever the window, the rows in it may have changed: a shuffle keeps
    // the window and reorders everything in it.
    this.queuePainted = { first: -1, last: -1, cur: -1 };
    this.paintQueue();
  }

  /**
   * Lay the rows in view over the spacer, and a few either side for the
   * scroll (windowRows, tested) — and nothing at all when the window and
   * the current row are the ones already drawn.
   */
  private paintQueue(): void {
    const list = this.queuePanel.querySelector<HTMLElement>('.q-list');
    const space = list?.firstElementChild as HTMLElement | null;
    if (!list || !space) return;
    const cur = this.orderPos;
    const { first, last } = windowRows(list.scrollTop, list.clientHeight, Q_ROW, this.order.length, Q_MARGIN);
    const p = this.queuePainted;
    if (p.first === first && p.last === last && p.cur === cur) return;
    this.queuePainted = { first, last, cur };
    let rows = '';
    for (let oi = first; oi < last; oi++) {
      const it = this.queue[this.order[oi]!];
      if (!it) continue;
      const current = oi === cur;
      rows += `<button class="${current ? 'q-row current' : 'q-row'}" data-oi="${oi}" style="top:${oi * Q_ROW}px"${current ? ' aria-current="true"' : ''}>
          <span class="q-num">${current ? icons.volume : oi + 1}</span>
          <span class="q-title">${esc(it.title || it.name)}</span>
          <span class="q-artist">${esc(it.artist ?? '')}</span>
        </button>`;
    }
    space.innerHTML = rows;
  }
}
