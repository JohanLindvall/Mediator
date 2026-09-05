/**
 * Full-screen video player overlay with custom controls: play/pause, seek with
 * buffer display, ±10s skip, volume/mute, playback rate, PiP, fullscreen and
 * keyboard shortcuts. Playback position is persisted to the backend.
 */
import {
  getCrop,
  getItem,
  convertProgress,
  hlsUrl,
  keyframeStart,
  listSubs,
  playsHLS,
  PLAY_AFTER,
  previewUrl,
  recordPlay,
  remuxUrl,
  savePosition,
  streamUrl,
  subUrl,
  setFlags,
  transcodeUrl,
  type Item,
  type Subtitle,
} from './api';
import {
  airPlaySupported,
  playingRemotely,
  remotePlaybackSupported,
  showAirPlayPicker,
  watchAirPlay,
  watchRemoteState,
} from './airplay';
import { Cast, fillReceiverMenu, knownRenderers, renderers } from './cast';
import { CastTransport, type CastHooks } from './casting';
import type { RendererInfo } from './types.gen';
import { preferredLang, rememberTrack, rememberedTrack } from './audiopref';
import { claimMediaKeys, setPlaybackState } from './mediakeys';
import { playingVideo } from './nowplaying';
import { SpectrumPanel, spectrumTakesOutput } from './visualizer';
import { holdScroll, releaseScroll } from './scrollhold';
import { clamp, esc, formatDuration } from './format';
import { icons } from './icons';
import { findKind, type ItemSource } from './sources';
import {
  audioSilent,
  shouldSave,
  PLAYER_KEYS,
  cropScale,
  pickAudioTrack,
  playButtonIcon,
  trackLabel,
  decodesHEVC,
  castAudioChoice,
  convertMode,
  opensDirectly,
  pictureRoute,
  plannedRoute,
  readFault,
  resumeStart,
  playsOnReceiver,
  rewrapWorthTheWait,
  type CropBox,
} from './playback';
import { forget, recall, remember } from './remember';
import { SlideDeck, watchSwipes } from './swipe';
import { holdThumbs, releaseThumbs } from './thumbs';
import { showToast } from './toast';
import { shareItem } from './links';

export interface VideoOpts {
  /**
   * The listing the item was opened from, so a swipe can step to the video
   * next to it and a finished one can roll on by itself.
   */
  nav?: { src: ItemSource; index: number };
  /** Last saved position for an item, if any. Asked per item, not once. */
  resumeFor?: (id: string) => { t: number; d: number } | undefined;
  /** Called (throttled) whenever a position is persisted. */
  onPosition?: (id: string, t: number, d: number) => void;
  onClose?: () => void;
}

const RATES = [0.5, 0.75, 1, 1.25, 1.5, 2];
/**
 * How long an audio-only conversion is given to produce a picture before it
 * is taken for one that never will. Long enough for ffmpeg to start and a
 * frame to arrive over a slow link; short enough that nobody waits it out
 * twice.
 */
const AUDIO_MODE_GRACE_MS = 8000;

/**
 * Whether this browser has been shown to give a *video* element's audio to the
 * page at all.
 *
 * Measured once and remembered for the session: WebKit on a phone accepts the
 * routing for a video and passes nothing — the film plays out of the speakers
 * and the analyser reads digital silence for ever — while the same browser,
 * the same graph and the same page do it perfectly well for the music bar's
 * audio elements. So it is the element kind that decides, which is nothing a
 * feature test can ask about beforehand and only playing can answer.
 *
 * Once one film has answered it, later ones are not offered the button: a
 * control that cannot do anything is worse than none, which is the judgement
 * the receiver button already makes. It stays offered until then, because the
 * panel is where the explanation is written and a viewer should get to read
 * it once rather than be told nothing.
 */
let videoAudioIsReadable = true;

/**
 * How often to ask whether the soundtrack-converted file is ready.
 *
 * The ask is itself the thing that starts it being made, and the server keeps
 * making it after whoever asked has gone — so this is a poll rather than one
 * long wait, which nothing in between (a proxy, a phone changing network)
 * can quietly drop without it being retried.
 */
const SOUND_FIX_POLL_MS = 5000;
/**
 * How long one of those asks may hang before it is given up on and retried.
 * A file already made answers in milliseconds; one still being made would
 * hold the connection for the whole conversion, and a browser has only a
 * handful of those to give the page.
 */
const SOUND_FIX_WAIT_MS = 4000;

const SAVE_EVERY_MS = 5000;
/** Quiet time after which the controls and the pointer withdraw. */
const HIDE_CONTROLS_MS = 2600;
/**
 * How much playback a new source gets before the decode check asks about
 * it. The frame counter starts again at zero for every source, and a check
 * landing between the seek and the first decoded frame reports failure about
 * a file that is about to play — and sends it to be re-encoded for nothing.
 */
const DECODE_SETTLE_S = 1.5;

/**
 * How often a television is actually asked where it has got to, in seconds
 * of the clock this player carries forward between answers. Every second
 * would be a SOAP round trip per second for a film that runs for hours; the
 * readout does not need it and neither does the set.
 */
const CAST_POLL = 4;

export function openVideo(item: Item, opts: VideoOpts = {}): void {
  new VideoOverlay(item, opts);
}

/**
 * Run `tick` every `ms` until the returned function is called, and at once
 * when `now` is set. Three things in the player poll — the progress badge,
 * the soundtrack file, the television — and each used to keep its own
 * timer id and clear it in three places; one that goes wrong is a poll that
 * outlives the file it was about.
 */
function poll(ms: number, tick: () => void, now = false): () => void {
  if (now) tick();
  const id = window.setInterval(tick, ms);
  return () => window.clearInterval(id);
}

class VideoOverlay {
  private root: HTMLElement;
  private video: HTMLVideoElement;
  private seekEl: HTMLElement;
  private seekFill: HTMLElement;
  private seekBuf: HTMLElement;
  private timeEl: HTMLElement;
  private playBtn: HTMLButtonElement;
  private muteBtn: HTMLButtonElement;
  private volSlider: HTMLInputElement;
  private rateBtn: HTMLButtonElement;
  private ccBtn: HTMLButtonElement;
  private subMenu: HTMLElement;
  private airplayBtn!: HTMLButtonElement;
  private vizBtn!: HTMLButtonElement;
  private spectrum!: SpectrumPanel;
  /**
   * Whether the sound has been routed into the analyser. It cannot be
   * undone, and what it costs is the element's own AirPlay route — so this
   * is remembered to keep a button that can no longer work off the screen.
   */
  private routed = false;
  private castMenu!: HTMLElement;
  private onTv!: HTMLElement;
  /**
   * What is wrong with this file, when nothing can be done about it: a disk
   * that will not hand it over, a format nothing here can play. Persistent
   * where a toast is not, since the viewer is looking at a picture that is
   * never going to start.
   */
  private faultEl!: HTMLElement;
  /** Whether the file has been asked for at all yet, once per file. */
  private readChecked = false;
  /**
   * Nothing more is to be tried for this file: it cannot be read, or nothing
   * here plays it. Every route that would start another stream stands down
   * on it, or the codecs arriving a moment after the verdict would start a
   * conversion of a file the disk will not hand over.
   */
  private faulted = false;
  /**
   * The television this film is playing on, if it is not playing here.
   *
   * While this is set the element is paused and holding nothing, and every
   * part of the transport answers for the set instead: the clock is the one
   * the transport carries (casting.ts, the same one the music bar uses) —
   * what the set last reported, moved on a second at a time between polls,
   * since a readout that only moved every few seconds would look broken —
   * and the buttons are requests to it rather than actions on an element.
   */
  private tv: CastTransport<Cast> | null = null;
  /** What the player does with what the set says; the transport decides what it means. */
  private readonly castHooks: CastHooks = {
    seeking: () => this.seeking,
    onClock: () => this.paint(),
    onPoll: () => {
      this.playBtn.innerHTML = icons[playButtonIcon(this.tv?.playing ?? false)];
      this.persist(false);
    },
    // Stopped at the end is the film ending, and the season rolls on;
    // stopped half way is somebody with the remote, and the page stops
    // being one.
    onEnded: () => void this.rollOnSet(),
    onStopped: () => this.endCast(false),
  };
  // Which of the two kinds of receiver exist. Either shows the button, so
  // neither may write its visibility directly — the browser answers "no
  // AirPlay" long after the network has answered "here is a television".
  private browserReceiver = false;
  private netReceivers = false;
  /** Whether this file has been counted, and where its playing began. */
  private played = false;
  private playFrom = -1;
  /**
   * The set's volume is a SOAP round trip per change, and a slider being
   * dragged changes dozens of times a second: the level is sent once the
   * finger has settled, and the last value wins.
   */
  private castVolTimer = 0;
  private audioBtn!: HTMLButtonElement;
  private audioMenu!: HTMLElement;
  /** Which soundtrack the viewer should hear, as ffmpeg numbers them. */
  private audioTrack = 0;
  /**
   * Which soundtrack the stream on the element actually carries, or null
   * before anything settled it. The menu marks audioTrack, so the two must
   * be brought together — natively where the browser exposes its audio
   * tracks, by the rewrap or the conversion otherwise — or the menu shows a
   * choice that is not what is playing (see applyAudioChoice).
   */
  private appliedTrack: number | null = null;
  /** Where the picture sits inside this file's frame, once the server says. */
  private crop: CropBox | null = null;
  /** Whether the borders are being trimmed, as the owner last left it. */
  private cropping = false;
  private cropBtn!: HTMLButtonElement;
  private fsBtn: HTMLButtonElement;
  private centerFlash: HTMLElement;
  private titleEl: HTMLElement;
  private dlLink: HTMLAnchorElement;
  // The still that covers the picture while a file is being loaded, and the
  // three layers a drag moves. Without them a switch is a black rectangle
  // for as long as the next file takes to open.
  private poster: HTMLImageElement;
  // What a conversion is doing, while it is doing it. A file that has to be
  // converted before it can be played takes seconds to tens of seconds, and
  // a spinner for that long is indistinguishable from a failure.
  private prep: HTMLElement;
  private prepMsg: HTMLElement;
  private stopPrep: () => void = () => {};
  private prepGen = 0;
  private slideCur: HTMLCanvasElement;
  /** The keys, listed on request. */
  private helpEl!: HTMLElement;
  /** The three menus, so dismissing, closing and asking about them is one loop. */
  private menus: Array<{ el: HTMLElement; button: HTMLElement }> = [];
  /** Puts down the receiver watch armed on the element. */
  private stopAirPlayWatch: () => void = () => {};

  // Where this item sits in the listing it was opened from, and the token
  // that drops a step whose search was overtaken by a later one.
  private navIndex = -1;
  private stepGen = 0;
  /** The position saved for the item on screen, if any. */
  private resume?: { t: number; d: number };

  private hideTimer = 0;
  private saveTimer = 0;
  private lastSaved = -1;
  private seeking = false;
  /** Where the finger is on the bar while a converted stream is being scrubbed. */
  private seekAt = 0;
  private closed = false;

  // Transcode fallback: when the browser cannot decode the video track
  // (black picture, e.g. MPEG-4 Part 2 or HEVC), playback switches to the
  // server's live H.264 stream. That stream has no ranges, so seeking
  // reopens it at an offset and tcOffset maps element time to media time.
  // Rewrapping is tried before converting, once per file: the browser
  // refusing a container says nothing about the streams inside it.
  private remuxed = false;
  private transcoding = false;
  // Which conversion route is in use, and whether the segmented one has
  // already let this file down — a browser that says it plays HLS and does
  // not should still end up watching something.
  private usingHLS = false;
  private hlsFailed = false;
  private tcMode: 'full' | 'audio' = 'full';
  private tcOffset = 0;
  private tcGen = 0; // drops the answer of a seek that a newer one replaced
  private decodeChecked = false;
  /** Timer behind an audio-only conversion; see watchAudioMode. */
  private audioWatch = 0;
  // The upgrade from the piped soundtrack conversion to the file of it: the
  // timer that keeps asking, and the request in flight, so both can be
  // dropped the moment the file being played changes.
  private soundFixPolling = false;
  private stopSoundFixPoll: () => void = () => {};
  private soundFixAsk: AbortController | null = null;
  /** Hands the media keys back to whatever had them before. */
  private releaseKeys: () => void = () => {};
  /**
   * Where the file now loading should start, from what was saved for it —
   * and whether the element is showing that file yet.
   *
   * Until it is, `video.currentTime` still reads the *previous* file's
   * position, and every route that switches source asked the element where
   * it was. A container the browser will not open never reaches the element
   * at all, so its conversion opened at wherever the last file happened to
   * be: step back to a shorter one and it began past its end, ended at
   * once, and rolled straight on to the next — which made moving to the
   * previous video look as though it did nothing.
   */
  private startAt = 0;
  private sourced = false;
  /**
   * How far playback must have got before the decode check is worth asking.
   * The frame counter starts at zero for every new stream, so asking too
   * soon reports "nothing decoded" about one that is merely still starting.
   */
  private checkAt = 0.5;

  private subs: Subtitle[] = [];
  private subIndex = -1; // -1 = subtitles off

  private rotation = 0; // quarter turns; a viewing aid for sideways footage

  /**
   * Apply the current rotation. Quarter-turned video swaps its constraints:
   * the element is sized to the overlay's opposite axis so the rotated
   * picture still fits, and object-fit keeps the aspect within that box.
   */
  private applyRotation(): void {
    const v = this.video;
    const odd = this.rotation % 180 !== 0;
    // A quarter-turned element keeps its unrotated layout box, so it is
    // sized to the overlay's swapped axes, taken out of flow and centered;
    // the rotation then happens around that center.
    v.classList.toggle('rotated', odd);
    v.style.width = odd ? `${this.root.clientHeight}px` : '';
    v.style.height = odd ? `${this.root.clientWidth}px` : '';
    // The file's own black borders are pushed off the edges by scaling the
    // picture about its centre; the overlay clips what goes past. Composed
    // with the turn rather than fighting it: both are transforms of the same
    // element, and the scale is written last so it applies first.
    const scale = this.cropping ? this.cropScaleNow(this.crop) : 1;
    const zoom = scale > 1 ? ` scale(${scale.toFixed(4)})` : '';
    v.classList.toggle('cropped', scale > 1);
    this.cropBtn.classList.toggle('active', this.cropping && scale > 1);
    v.style.transform = odd
      ? `translate(-50%, -50%) rotate(${this.rotation}deg)${zoom}`
      : this.rotation
        ? `rotate(${this.rotation}deg)${zoom}`
        : zoom.trim();
  }

  /**
   * Ask where the picture really is, and trim to it unless the owner said
   * not to. The answer costs the server a few seconds of ffmpeg once per
   * file and nothing afterwards, so it is asked for every video rather than
   * on demand — a border nobody has to think about is the point.
   */
  private async findBorders(item: Item): Promise<void> {
    let box: CropBox;
    try {
      box = await getCrop(item.id);
    } catch {
      return; // no borders known; the film plays as the file describes it
    }
    if (this.closed || this.item.id !== item.id) return;
    this.crop = box;
    // Offered only where there is something to offer, which depends on the
    // screen as much as on the file — see cropScale.
    this.cropBtn.hidden = this.cropScaleNow(box) === 1;
    this.applyRotation();
  }

  /** What trimming this file would buy on the screen it is being shown on. */
  private cropScaleNow(box: CropBox | null): number {
    if (!box) return 1;
    const odd = this.rotation % 180 !== 0;
    const w = odd ? this.root.clientHeight : this.root.clientWidth;
    const h = odd ? this.root.clientWidth : this.root.clientHeight;
    // The size the element lays the picture out at, which for anamorphic
    // video — every DVD — is not the size it is stored at. Zero until the
    // metadata is in, and then cropScale falls back to the frame's own
    // dimensions; loadedmetadata asks again.
    const shown = { w: this.video.videoWidth, h: this.video.videoHeight };
    return cropScale(box, w, h, shown);
  }

  /** Keep or trim this file's borders, and remember which. */
  private toggleCrop(): void {
    this.cropping = !this.cropping;
    this.cropBtn.classList.toggle('active', this.cropping);
    this.applyRotation();
    const id = this.item.id;
    void setFlags(id, { nocrop: !this.cropping }).then((flags) => {
      if (!this.closed && this.item.id === id) this.item.nocrop = flags[id]?.nocrop ?? false;
    });
  }

  /**
   * Playback has moved to a television, or come back from one.
   *
   * A receiver decodes what it decodes and never says which — so a file in a
   * codec it is unlikely to have is converted the moment it is sent there,
   * rather than arriving as sound with a black screen behind it. AV1 is the
   * case that bites: a recent phone plays it perfectly and the set it is
   * being sent to has no decoder for it at all.
   *
   * Coming back is left alone. The conversion plays here as well as the file
   * did, and switching source twice for one gesture is a stall each way.
   */
  private onRemoteChange = (): void => {
    if (!playingRemotely(this.video) || this.transcoding) return;
    if (playsOnReceiver(this.item.vcodec)) return;
    showToast('Converting for the television…', 4000);
    this.fallbackToTranscode('full');
  };

  // ---- playing to a television ------------------------------------------

  /**
   * Offer the button where there is something to send to.
   *
   * Two different things can put it on screen and they are asked in
   * different ways: the browser answers about its own receivers when it
   * feels like it (watchAirPlay), and the network is asked about renderers
   * here. Either is reason enough for the button; which of them it opens is
   * decided when it is pressed.
   */
  private async findReceivers(): Promise<void> {
    const found = await renderers();
    if (this.closed || found.length === 0) return;
    this.netReceivers = true;
    this.showReceiverButton();
    this.buildCastMenu(found);
  }

  private showReceiverButton(): void {
    // A receiver the browser hands the element to has nothing to take once
    // the sound has been *moved* inside a Web Audio graph, so that button
    // goes rather than staying on screen doing nothing. Where the sound was
    // copied instead the element still has its route and keeps the button. A television driven over DLNA
    // is untouched — the set fetches the file itself and this element is
    // not involved — so the button stays wherever one of those was found.
    const browser = this.browserReceiver && !this.routed;
    this.airplayBtn.hidden = !(browser || this.netReceivers);
  }

  /**
   * Show the spectrum of what is playing, or put it away.
   *
   * The same analyser the music bar uses, on the film's own soundtrack. Two
   * things are worth knowing before pressing it, and the tooltip says the
   * first: routing an element into Web Audio **cannot be undone**, and it
   * spends that element's AirPlay route — so the receiver button goes once
   * this is opened, until the film is reopened. Casting to a television is
   * unaffected, that being the server's business and not this element's.
   *
   * The second is why it is refused while a set is playing: the element is
   * paused and holding nothing, the sound is coming out of the television,
   * and the graph would draw a flat line.
   */
  private toggleViz(): void {
    if (this.spectrum.isOpen) {
      this.spectrum.close();
      return;
    }
    if (this.tv) return;
    if (!this.spectrum.open([this.video])) {
      showToast('This browser cannot show the spectrum', 3000);
      return;
    }
    // Only where the sound had to be moved rather than copied: elsewhere the
    // element keeps its own output and its own route with it.
    this.routed = this.spectrum.viz.movedOutput;
    this.showReceiverButton();
  }

  /**
   * Dim the button while a television is playing the film. The element here
   * is paused and holding nothing — the set fetched the file itself — so
   * the graph would draw a flat line. Dimmed rather than removed: a control
   * that vanishes and comes back reads as a fault.
   */
  private syncCastControls(): void {
    // Only for a film that has not asked yet: the panel now open on the one
    // that did is carrying the explanation, and taking it away mid-sentence
    // would be the fault this is meant to end.
    if (!videoAudioIsReadable && !this.spectrum.isOpen) this.vizBtn.hidden = true;
    this.vizBtn.disabled = this.tv != null;
    this.vizBtn.title = this.vizBtn.disabled
      ? 'The spectrum is of what this page is playing'
      : spectrumTakesOutput(this.video)
        ? 'Spectrum — ends AirPlay for this film until it is reopened'
        : 'Spectrum';
    // The speed is the element's, and the element is not what is playing.
    this.rateBtn.disabled = this.tv != null;
    this.rateBtn.title = this.tv ? 'The speed is the television\'s' : 'Playback speed';
  }

  private buildCastMenu(found: RendererInfo[]): void {
    fillReceiverMenu(this.castMenu, found, {
      here: this.tv != null,
      currentId: this.tv?.cast.renderer.id ?? null,
      picker: airPlaySupported(this.video) || remotePlaybackSupported(this.video),
      onPick: (target) => void this.startCast(target),
      onHere: () => {
        this.toggleCastMenu(false);
        this.endCast(true);
      },
      onPicker: () => {
        this.toggleCastMenu(false);
        showAirPlayPicker(this.video);
      },
    });
  }

  private markCastMenu(): void {
    for (const el of this.castMenu.querySelectorAll('[data-rid]')) {
      el.classList.toggle('on', (el as HTMLElement).dataset.rid === this.tv?.cast.renderer.id);
    }
  }

  private toggleCastMenu(show = this.castMenu.hidden): void {
    this.toggleMenu(this.castMenu, this.airplayBtn, show);
  }

  /**
   * Hand this film to a television.
   *
   * What is playing here stops, because the set is about to play the same
   * thing and two copies of one film in one house is not a feature. The
   * position goes with it: the whole point of the transport being the
   * server's is that the film carries on from where the viewer was.
   */
  private async startCast(target: RendererInfo): Promise<void> {
    this.toggleCastMenu(false);
    const at = this.switchAt();
    const dur = this.totT() || (this.item.duration ?? 0) / 1000;
    this.video.pause();
    // Sent again — another soundtrack, the next episode — the last
    // transport stands down first, or its poll would report the set stopped
    // while this file is opening.
    this.tv?.stop();
    const tv = new CastTransport(new Cast(target, this.item.id), this.castHooks, { pollEvery: CAST_POLL });
    tv.begin(at, dur);
    this.tv = tv;
    this.root.classList.add('vo-casting');
    this.spectrum.close();
    this.syncCastControls();
    this.seekBuf.style.width = '0%';
    this.showPoster(this.item);
    this.onTv.hidden = false;
    this.onTv.textContent = `Opening on ${target.name}…`;
    this.playBtn.innerHTML = icons[playButtonIcon(true)];
    this.markCastMenu();
    this.paint();

    // Counted here rather than after five seconds of playing: the set is
    // playing it and this page has no clock on it to wait for.
    if (!this.played) {
      this.played = true;
      recordPlay(this.item.id);
    }
    try {
      // The set draws the subtitle this viewer chose — sidecar or embedded,
      // the numbering is the listing's own — or none where they turned them
      // off; it has no way to choose one for itself. Nor can it be told
      // which soundtrack, so that choice travels as the file it is given
      // (castSource), and it is named only where it differs from the
      // default the set would pick anyway (castAudioChoice, tested).
      await tv.cast.start(
        at,
        this.subIndex >= 0 ? String(this.subIndex) : 'off',
        castAudioChoice(this.item.tracks ?? [], this.audioTrack),
      );
    } catch (err) {
      if (this.closed || this.tv !== tv) return;
      // A set that will not play it says so on our behalf; coming back here
      // is better than a screen that says it is playing somewhere it is not.
      this.onTv.textContent =
        err instanceof Error && err.message ? err.message : `${target.name} would not play this`;
      window.setTimeout(() => {
        if (this.tv === tv) this.endCast(true);
      }, 4000);
      return;
    }
    if (this.closed || this.tv !== tv) return;
    this.onTv.textContent = `Playing on ${target.name}`;
    this.buildCastMenu(knownRenderers());
    tv.run();
  }

  /**
   * Stop playing to the set.
   *
   * resumeHere reopens the film in the player at the position the television
   * reached, which is what "play here instead" means; without it the player
   * simply stops being a remote control — the set was stopped by someone
   * else, or has reached the end.
   */
  private endCast(resumeHere: boolean): void {
    const tv = this.tv;
    const at = tv?.pos ?? 0;
    this.tv = null;
    this.syncCastControls();
    tv?.stop();
    tv?.cast.stop();
    this.root.classList.remove('vo-casting');
    this.onTv.hidden = true;
    this.markCastMenu();
    this.buildCastMenu(knownRenderers());
    if (resumeHere) this.load(this.item, at);
    else this.paint();
  }

  /**
   * Turn the picture, and remember it.
   *
   * Footage shot sideways is sideways every time it is opened, so this is a
   * correction to the file rather than a way of looking at it once — it is
   * kept with the other things the owner has said about the item, which
   * means the phone gets it right too. Failing to write it down is not worth
   * interrupting anyone over: the turn holds for this sitting either way.
   */
  private rotate(quarter: 1 | -1): void {
    this.rotation = (this.rotation + quarter * 90 + 360) % 360;
    this.applyRotation();
    this.flash(quarter > 0 ? icons.rotateCw : icons.rotateCcw);
    const id = this.item.id;
    void setFlags(id, { rotation: this.rotation / 90 }).then((flags) => {
      // Keep the item in step, so stepping away and back does not undo it.
      if (!this.closed && this.item.id === id) this.item.rotation = flags[id]?.rotation ?? 0;
    });
  }

  private onResize = (): void => this.applyRotation();

  /** Current position in media time. */
  private curT(): number {
    if (this.tv) return this.tv.pos;
    return this.tcOffset + this.video.currentTime;
  }

  /**
   * Where a change of source should pick up.
   *
   * Once the element is showing this file, that is wherever it has got to.
   * Before it is — a container the browser was never handed, routed straight
   * to a rewrap or a conversion — the element is still holding the position
   * of the file before it, and this file's own resume point is the answer.
   */
  private switchAt(): number {
    // A television playing this is the one thing that knows where it is:
    // after a season rolled on there the element never showed this file,
    // and asking it would restart the episode from its resume point.
    if (this.tv) return this.tv.pos;
    return this.sourced ? this.curT() : this.startAt;
  }

  /** Total duration in media time (probed metadata while transcoding). */
  private totT(): number {
    // A television measures the file itself and says so; until it has, what
    // the library measured is the better answer than the element's, which is
    // holding nothing at all.
    if (this.tv) return this.tv.dur || (this.item.duration ?? 0) / 1000;
    if (!this.transcoding) return Number.isFinite(this.video.duration) ? this.video.duration : 0;
    return (this.item.duration ?? 0) / 1000 || this.resume?.d || 0;
  }

  /** Seek to an absolute media time, reopening the transcode if needed. */
  private seekTo(t: number): void {
    if (this.tv) {
      // The set is asked, and the readout moves at once rather than waiting
      // a round trip and a poll to agree with the finger that moved it.
      this.tv.seek(t);
      this.paint();
      return;
    }
    if (!this.transcoding) {
      this.video.currentTime = t;
      return;
    }
    const tot = this.totT();
    void this.startTranscodeAt(clamp(t, 0, tot > 0 ? Math.max(0, tot - 1) : t));
  }

  /**
   * Reopen the conversion at t.
   *
   * When the video stream is copied, ffmpeg can only start on a keyframe, so
   * the stream really begins at the last one at or before t — up to ten
   * seconds earlier on a 4K release. Ask the server where that is and make it
   * the origin of the element's clock, so the position readout, the saved
   * resume point and the subtitle cues all describe the picture on screen.
   *
   * Landing on the exact frame asked for is not on offer: the conversion is
   * a live stream with no ranges, and the browser reports it as unseekable
   * (seekable is empty even where it has buffered), so the remainder cannot
   * be skipped from this side. Starting at the keyframe is what a copied
   * stream can do, and now it says so.
   */
  private async startTranscodeAt(t: number): Promise<void> {
    const gen = ++this.tcGen;
    const target = Math.max(0, t);
    let origin = target;
    if (this.tcMode === 'audio' && target > 0) {
      try {
        origin = (await keyframeStart(this.item.id, target)).start;
      } catch {
        // Without an answer the safest assumption is an accurate seek: the
        // timeline may be off, but nothing else breaks.
      }
      if (this.closed || gen !== this.tcGen) return; // a newer seek won
    }
    this.tcOffset = origin;
    this.seekBuf.style.width = '0%';
    // Safari will not play the piped conversion at all — it opens with a
    // range request the pipe cannot answer — so it is given the same
    // conversion as segments. Everything downstream is unchanged: the clock
    // still starts at the keyframe, and the subtitles are still rebased onto
    // it, because it is the same seek either way.
    this.usingHLS = playsHLS() && !this.hlsFailed;
    // Ask for the conversion at the keyframe, not at the time that was
    // asked for. With the picture copied, ffmpeg can only start it on a
    // keyframe and takes the one at or before the seek — but it starts the
    // *soundtrack* where it was told, since that stream is re-encoded. Ask
    // for the seek itself and the two come out several seconds apart: the
    // subtitles follow the sound, which is right, and the picture is behind
    // both. Asking for the keyframe puts every stream at the same place,
    // which is also where the clock has been told the stream begins.
    this.startSource(
      this.usingHLS
        ? hlsUrl(this.item.id, origin, this.tcMode, this.audioTrack, this.subIndex)
        : transcodeUrl(this.item.id, origin, this.tcMode, this.audioTrack),
      { track: this.audioTrack },
    );
    this.retimeSubtitles(); // cues are absolute; this stream's clock is not
  }

  /**
   * Hand the element a source. Every route that changes what plays — the
   * file, the rewrap, the soundtrack file, the conversion — comes through
   * here, because each used to write the same five things and one of them
   * forgot the settle: a new stream is a new question about whether it
   * decodes, and asking before its first frame reports failure about a
   * file that is about to play. `at` is where it starts; `settle` is how
   * much playback the decode check waits for, from there; `track` is the
   * soundtrack the stream carries, which applyAudioChoice reads back.
   */
  private startSource(url: string, o: { at?: number; track?: number | null; settle?: number }): void {
    this.video.src = url;
    if (o.track !== undefined) this.appliedTrack = o.track;
    this.sourced = true;
    if (o.settle !== undefined) {
      this.decodeChecked = false;
      this.checkAt = (o.at ?? 0) + o.settle;
    }
    // Before metadata arrives this is the default playback start position,
    // which is what makes it survive the load the new source just started.
    if (o.at !== undefined && o.at > 0) this.video.currentTime = o.at;
    void this.video.play().catch(() => this.showControls());
  }

  /**
   * Whether the browser will even attempt this file's container.
   *
   * An empty answer from canPlayType is the browser's own definitive no, so
   * it is worth believing; anything else — including "maybe" — is left to
   * the element, which is the one that knows about the codecs inside.
   */
  private opensDirectly(item: Item): boolean {
    return opensDirectly(item.name, (t) => this.video.canPlayType(t));
  }

  /**
   * Go straight to a conversion, for a container the browser will not open.
   *
   * The rewrap comes first wherever it is worth the wait — always for a
   * browser whose only alternative is a pipe it cannot seek in, and up to a
   * size for one that can start on an HLS segment instead. What it buys is
   * the file itself, playing natively and seekable throughout, for a copy
   * that runs at disk speed. Past that size the segmented conversion starts
   * in a third of a second and is the better trade.
   */
  private async convertDirectly(): Promise<void> {
    if (rewrapWorthTheWait(this.item.size, playsHLS()) && this.tryRemux()) return;
    await this.convertForContainer();
  }

  /**
   * Rewrap rather than convert, when the container is the only problem.
   *
   * What comes back is an ordinary file — ranges, a length, seeking the
   * browser does itself — so none of the conversion's corrections apply:
   * the timestamps are the file's own, which leaves the clock, the resume
   * point and the subtitle cues already agreeing. It is also the only shape
   * iOS will play, since it will not touch a media URL that cannot answer a
   * range request, and a live conversion never can.
   *
   * Tried once per file, before the converter and never after it.
   */
  private tryRemux(mode?: 'track'): boolean {
    // A copy is exactly the thing that cannot help a stream that lies about
    // its reordering: the server would answer 404 to say so, but the
    // question need not be asked.
    if (this.remuxed || this.transcoding || this.item.reencode || this.faulted) return false;
    this.remuxed = true;
    // A rewrap is a new stream, and whether it decodes is a new question —
    // asked for because nothing decoded, it has to show that now it does,
    // after a moment of playback. The copy keeps one soundtrack, and it is
    // the chosen one: the server keys rewraps by track, and a dual-language
    // release rewrapped for its container used to come back always carrying
    // the first.
    this.startSource(remuxUrl(this.item.id, this.audioTrack, mode), {
      at: this.switchAt(),
      track: this.audioTrack,
      settle: DECODE_SETTLE_S,
    });
    this.watchConversion();
    return true;
  }

  /**
   * The browser could not open the file at all, so nothing has been decoded
   * and the usual evidence — a black picture, a silent soundtrack — does not
   * exist. What decides the conversion here is what the file holds.
   *
   * H.264 is decoded everywhere, so when that is the picture, only the
   * container and the soundtrack are in the way and the picture is copied:
   * measured on a 1080p film, that is the difference between a conversion
   * that keeps well ahead of playback and one that re-encodes every frame of
   * a picture the browser was always able to show. The codecs are worth
   * waiting for when they are not known yet, since guessing wrong here costs
   * far more than the request does.
   */
  private async convertForContainer(): Promise<void> {
    if (!this.item.vcodec) await this.refreshItem();
    if (this.closed) return;
    // Nothing has decoded here — the browser would not open the container —
    // so nothing about the picture is proven, and only one positively known
    // to play is copied through. That is not the question `trackMode` asks,
    // where the file is already on screen and the picture is proven by being
    // there; collapsing the two sent a WMV to be copied into a container that
    // cannot hold it.
    this.fallbackToTranscode(
      convertMode(this.item.vcodec, (t) => this.video.canPlayType(t), false, this.item.reencode),
    );
  }

  /**
   * Switch to the server-side conversion, keeping the current position.
   * 'audio' re-encodes only the soundtrack (picture was fine); 'full' also
   * re-encodes the video.
   */
  private fallbackToTranscode(mode: 'full' | 'audio'): void {
    if (this.closed || this.faulted) return;
    // Audio mode may still be escalated to a full conversion if the copied
    // video turns out to be undecodable too; nothing else re-enters.
    if (this.transcoding && !(this.tcMode === 'audio' && mode === 'full')) return;
    // A full conversion is what a picture the browser cannot decode needs,
    // and the soundtrack-converted file copies that very picture through: it
    // would arrive as the same black screen, later. Stop waiting for it.
    if (mode === 'full') this.stopSoundFix();
    this.transcoding = true;
    this.tcMode = mode;
    if (mode === 'audio') {
      this.watchAudioMode();
      this.upgradeToSoundFix();
    }
    // An audio-only conversion has not proven its picture yet.
    this.decodeChecked = mode === 'full';
    const at = this.switchAt();
    void this.startTranscodeAt(at > 1 ? at : 0);
  }

  /**
   * The way out of an audio conversion that plays nothing.
   *
   * That conversion copies the picture through, so if the picture turns out
   * to be undecodable as well, playback never starts — and the decode check
   * cannot rescue it, because it runs off `timeupdate` and a stream that
   * never plays never fires one. The file just sits there.
   *
   * So after a grace period long enough for a conversion to have started and
   * a frame to have come out of it, a stream still at zero with nothing
   * decoded is escalated to a full conversion, which re-encodes the picture
   * as well.
   */
  private watchAudioMode(): void {
    window.clearTimeout(this.audioWatch);
    this.audioWatch = window.setTimeout(() => {
      if (this.closed || this.tcMode !== 'audio') return;
      const v = this.video as HTMLVideoElement & {
        getVideoPlaybackQuality?: () => { totalVideoFrames: number };
      };
      const frames = v.getVideoPlaybackQuality?.().totalVideoFrames ?? (v.videoWidth > 0 ? 1 : 0);
      if (frames === 0 && this.video.currentTime === 0) this.fallbackToTranscode('full');
    }, AUDIO_MODE_GRACE_MS);
  }

  /**
   * Move a soundtrack conversion off the pipe and onto a file of itself.
   *
   * The pipe cannot answer a range request, so a browser managing its own
   * buffer disconnects when it is full and reconnects asking for the byte it
   * wants next — and is answered from the beginning, every time. Measured on
   * a 4K release over one viewing, 963 MB crossed the link to move the stream
   * 167 MB. That waste is not a constant to be tuned away either: each
   * reconnect re-reads from zero, so it grows with the playback position and
   * is worst at the end of a film, which is exactly where giving up hurts.
   *
   * So the pipe starts playback, as it always did, and the file is made
   * behind it. Measured, the picture is copied at disk speed and the
   * soundtrack encodes at about 36x, so the file lands after roughly a
   * thirty-sixth of the film's length — within the first three per cent of
   * playback, which is the part the pipe carries cheaply. The two failure
   * modes cancel: the pipe is only asked for what it is good at.
   *
   * Nothing is lost if it never arrives. A 404 says a copy would not help
   * and the poll stops; anything else simply leaves the film playing the way
   * it plays today.
   */
  private upgradeToSoundFix(): void {
    if (this.soundFixPolling || this.closed) return;
    // Only where the pipe is what plays. Safari is given the conversion as
    // segments instead, and a segment is an ordinary file with a length and
    // ranges — so there is no re-read to save there, and changing source
    // under a film in iOS's own fullscreen would cost more than it bought.
    if (playsHLS()) return;
    const ask = (): void => {
      const item = this.item;
      const track = this.audioTrack;
      // The ask is what starts the file being made, and the server goes on
      // making it after this has been given up on — so a short wait costs
      // nothing but the retry, and holds none of the handful of connections
      // the page has for the playback that is running meanwhile.
      const ctl = new AbortController();
      this.soundFixAsk = ctl;
      const giveUp = window.setTimeout(() => ctl.abort(), SOUND_FIX_WAIT_MS);
      fetch(remuxUrl(item.id, track, 'audio'), {
        headers: { Range: 'bytes=0-0' },
        signal: ctl.signal,
      })
        .then((r) => {
          // The answer is the status; the byte asked for is not wanted, and
          // an undrained body holds a connection open for nothing.
          void r.body?.cancel();
          if (r.ok) {
            // Still the same film, and still the same soundtrack: a swipe or
            // a change of language while this was in flight makes the answer
            // about a file nobody is watching.
            if (!this.closed && this.item.id === item.id && this.audioTrack === track) {
              this.switchToSoundFix(track);
            }
            return;
          }
          // 404 is the server saying a copy would not help. Nothing else
          // will change that, so stop asking.
          if (r.status === 404) this.stopSoundFix();
        })
        .catch(() => {
          /* aborted, or the network went away: the next tick asks again */
        })
        .finally(() => {
          window.clearTimeout(giveUp);
          if (this.soundFixAsk === ctl) this.soundFixAsk = null;
        });
    };
    this.soundFixPolling = true;
    this.stopSoundFixPoll = poll(SOUND_FIX_POLL_MS, ask, true);
  }

  /** Stop waiting for the file, and drop any ask still in flight. */
  private stopSoundFix(): void {
    this.stopSoundFixPoll();
    this.soundFixPolling = false;
    this.soundFixAsk?.abort();
    this.soundFixAsk = null;
  }

  /**
   * Play the file instead of the pipe, from where the pipe had got to.
   *
   * Everything the conversion needed goes with it: its clock started at a
   * keyframe and the file's does not, so the offset is dropped and the
   * subtitles are re-pointed at their own absolute times. The watchdog goes
   * too — it exists to escalate a conversion that never produced a picture,
   * and there is no conversion left to escalate.
   */
  private switchToSoundFix(track: number): void {
    const at = this.switchAt();
    this.stopSoundFix();
    window.clearTimeout(this.audioWatch);
    this.tcGen++; // a keyframe answer still in flight is about the pipe
    this.transcoding = false;
    this.usingHLS = false;
    this.tcOffset = 0;
    this.seekBuf.style.width = '0%';
    // A copy has now been tried, and this is it. Without saying so, a
    // picture that turned out not to decode would send the player back for
    // the plain rewrap of the same film — which is the same picture again.
    this.remuxed = true;
    // A new source is a new question about whether it decodes, held off for
    // a moment of playback like the rewrap's.
    this.startSource(remuxUrl(this.item.id, track, 'audio'), { at, track, settle: DECODE_SETTLE_S });
    this.retimeSubtitles(); // the file's clock is the film's own
  }

  /**
   * Shortly after playback starts, check that the browser actually decoded
   * something. A codec it cannot handle produces a black picture (no video
   * frames) or silence (no audio bytes) rather than an error, so this is
   * the only reliable signal.
   */
  private checkDecodes(): void {
    this.decodeChecked = true;
    const v = this.video as HTMLVideoElement & {
      getVideoPlaybackQuality?: () => { totalVideoFrames: number };
      webkitAudioDecodedByteCount?: number;
      mozHasAudio?: boolean;
    };
    const q = v.getVideoPlaybackQuality?.();
    // Nothing decoded is not one situation but two, and they want opposite
    // treatment: a picture the browser could always decode but the file
    // labels in a way it refuses needs its bytes copied, while one it cannot
    // decode at all needs every frame converted. A 404 from the rewrap says
    // it would not have helped and arrives as an error, which goes on to the
    // converter exactly as before.
    const route = pictureRoute({
      frames: q ? q.totalVideoFrames : null,
      width: v.videoWidth,
      rewrapAvailable: !this.remuxed && !this.transcoding,
      hevcDecodes: decodesHEVC((t) => this.video.canPlayType(t)),
    });
    if (route !== 'ok') {
      if (route === 'rewrap' && this.tryRemux()) return;
      this.fallbackToTranscode('full'); // also escalates from audio mode
      return;
    }
    if (this.transcoding) return; // audio already handled by this stream
    // Picture is fine. If the file is known to carry audio but none is
    // being decoded, only the soundtrack needs converting.
    if (!this.item.acodec) return;
    if (audioSilent(v)) this.fallbackToTranscode('audio');
  }

  /**
   * Convert the soundtrack because of what it is, rather than waiting to
   * hear that it is silent.
   *
   * A codec the browser cannot decode produces no error, only silence, so
   * the alternative is to play for a moment, notice nothing was decoded and
   * change source mid-playback — which is a stall a few seconds in, and a
   * jump back to the nearest keyframe.
   *
   * It also settles which soundtrack is heard. A file offering an AC3 track
   * and an AAC commentary gives Chrome one it cannot decode and one it can,
   * and it plays the commentary — reasonably enough, and not what anyone
   * wants. Converting from the track the file leads with is the whole fix.
   */
  private useKnownCodecs(): void {
    if (this.closed || this.remuxed || this.faulted) return;
    // The order of the questions is plannedRoute's, tested. A conversion
    // already running is left alone by fallbackToTranscode, except that
    // the audio mode is escalated to the full one — which is what the
    // server's own verdict on a reordering lie, arriving after a soundtrack
    // conversion started on the codecs alone, needs.
    const route = plannedRoute(this.item, (t) => this.video.canPlayType(t));
    if (route) this.fallbackToTranscode(route);
  }

  /**
   * Ask the server for this item's metadata, which also pushes it to the
   * front of the enrichment queue. Until it answers we do not know which
   * codecs the file holds, and the silent-playback check needs that.
   */
  private async refreshItem(): Promise<void> {
    try {
      const fresh = await getItem(this.item.id);
      if (!this.closed) this.item = fresh;
    } catch {
      // Metadata is an optimisation here; playback carries on without it.
    }
  }

  // ---- subtitles -------------------------------------------------------

  /** Load external subtitle files and expose them through the CC button. */
  private async loadSubs(): Promise<void> {
    let subs: Subtitle[] = [];
    try {
      subs = (await listSubs(this.item.id)).subs;
    } catch {
      return;
    }
    if (this.closed || subs.length === 0) return;
    this.subs = subs;
    for (const s of subs) {
      const track = document.createElement('track');
      track.kind = 'subtitles';
      track.label = s.label;
      if (s.lang) track.srclang = s.lang;
      track.src = subUrl(this.item.id, s.index, this.tcOffset);
      this.video.appendChild(track);
    }
    this.ccBtn.hidden = false;
    this.buildSubMenu();
    // Restore the last language chosen in this browser, if it is offered.
    const remembered = recall('media.subtitle');
    const match = subs.findIndex((s) => (s.lang || s.label) === remembered);
    if (match >= 0) {
      this.subIndex = match;
      this.applySubtitle();
    }
  }

  /**
   * Re-fetch the tracks against the current stream's clock. Cue times are
   * absolute in the file while a conversion restarts its clock at whichever
   * keyframe it opened on, so every seek changes what the cues have to be
   * rebased by.
   */
  private retimeSubtitles(): void {
    const tracks = this.video.querySelectorAll('track');
    tracks.forEach((track, i) => {
      const sub = this.subs[i];
      if (!sub) return;
      const url = subUrl(this.item.id, sub.index, this.tcOffset);
      if (!track.src.endsWith(url)) track.src = url;
    });
    this.applySubtitle(); // a new src resets text-track modes
  }

  /** Point the browser at the selected track (or none). */
  private applySubtitle(): void {
    // On the HLS path the *stream* draws the subtitles: the master playlist
    // carries them as renditions and the chosen one is marked DEFAULT, which
    // is what reaches the native fullscreen and an AirPlay receiver. The
    // element's textTracks then hold rendition tracks as well as our own
    // <track>s, in an order nothing here controls — so showing by index
    // would light a random one, and doubling the stream's rendering is the
    // other way to be wrong. Everything local stays disabled.
    if (this.usingHLS) {
      const tracks = this.video.textTracks;
      for (let i = 0; i < tracks.length; i++) tracks[i].mode = 'disabled';
      this.markSubMenu();
      return;
    }
    const tracks = this.video.textTracks;
    for (let i = 0; i < tracks.length; i++) {
      tracks[i].mode = i === this.subIndex ? 'showing' : 'disabled';
    }
    this.markSubMenu();
  }

  /**
   * Light the chosen entry. One place for both routes: the HLS branch used
   * to mark it with a class the stylesheet does not know, so on Safari the
   * menu showed no choice at all.
   */
  private markSubMenu(): void {
    this.ccBtn.classList.toggle('active', this.subIndex >= 0);
    for (const el of this.subMenu.querySelectorAll('[data-sub]')) {
      el.classList.toggle('on', Number((el as HTMLElement).dataset.sub) === this.subIndex);
    }
  }

  /** Fill the subtitle menu: Off, then every track found beside the video. */
  private buildSubMenu(): void {
    this.subMenu.innerHTML = [{ index: -1, label: 'Off' }, ...this.subs.map((s, i) => ({ index: i, label: s.label }))]
      .map((o) => `<button class="vo-menu-item" data-sub="${o.index}">${esc(o.label)}</button>`)
      .join('');
    for (const el of this.subMenu.querySelectorAll('[data-sub]')) {
      el.addEventListener('click', () => {
        this.selectSubtitle(Number((el as HTMLElement).dataset.sub));
        this.toggleSubMenu(false);
      });
    }
  }

  /**
   * Offer the film's other soundtracks, when it has any.
   *
   * Choosing one is not something a browser can be asked for — it picks a
   * stream itself and will not be talked out of it — so a choice is served
   * by conversion, with that stream mapped. Which means it costs what a
   * conversion costs and is worth doing only when asked for, or when the
   * viewer's own language is sitting there unplayed.
   */
  private buildAudioMenu(): void {
    const tracks = this.item.tracks ?? [];
    this.audioBtn.hidden = tracks.length < 2;
    if (tracks.length < 2) return;
    this.audioTrack = pickAudioTrack(tracks, {
      remembered: rememberedTrack(this.item.id),
      prefer: preferredLang(),
    });
    this.audioMenu.innerHTML = tracks
      .map(
        (t, i) =>
          `<button class="vo-menu-item" data-track="${t.index}">${esc(trackLabel(t, i))}</button>`,
      )
      .join('');
    for (const el of this.audioMenu.querySelectorAll('[data-track]')) {
      el.addEventListener('click', () => {
        this.selectAudio(Number((el as HTMLElement).dataset.track));
        this.toggleAudioMenu(false);
      });
    }
    this.markAudioMenu();
  }

  private markAudioMenu(): void {
    for (const el of this.audioMenu.querySelectorAll('[data-track]')) {
      el.classList.toggle('on', Number((el as HTMLElement).dataset.track) === this.audioTrack);
    }
  }

  private toggleAudioMenu(show = this.audioMenu.hidden): void {
    this.toggleMenu(this.audioMenu, this.audioBtn, show);
  }

  /**
   * Open or close one of the three menus. The controls are shown as well,
   * and re-armed: the menu itself is what blocks their hide while it is up.
   */
  private toggleMenu(menu: HTMLElement, button: HTMLElement, show: boolean): void {
    menu.hidden = !show;
    button.setAttribute('aria-expanded', String(show));
    this.showControls();
  }

  /** Play this film's other soundtrack, from where it has got to. */
  private selectAudio(track: number): void {
    if (track === this.audioTrack && this.appliedTrack === track) return;
    this.audioTrack = track;
    this.markAudioMenu();
    rememberTrack(this.item.id, track, this.item.tracks ?? []);
    if (this.tv) {
      // The television is playing a file, not a stream we are making: the
      // choice is the file, so it is sent again with the other soundtrack
      // in it, from where it had got to.
      void this.startCast(this.tv.cast.renderer);
      return;
    }
    this.applyAudioChoice();
  }

  /** Cycle through the soundtracks, for the keyboard. */
  private cycleAudio(): void {
    const tracks = this.item.tracks ?? [];
    if (tracks.length < 2) return;
    const at = tracks.findIndex((t) => t.index === this.audioTrack);
    const next = (at + 1) % tracks.length;
    this.selectAudio(tracks[next]!.index);
    showToast(`Soundtrack: ${trackLabel(tracks[next]!, next)}`, 1500);
  }

  /** The track the element plays when nothing chooses: the file's own. */
  private fileDefaultTrack(): number {
    const tracks = this.item.tracks ?? [];
    return tracks.find((t) => t.default)?.index ?? tracks[0]?.index ?? 0;
  }

  /**
   * Bring what is playing to the track the viewer should hear.
   *
   * The menu marks audioTrack, so the two must not disagree — and they did:
   * the pick was computed after the conversion had already started with the
   * first track, and on a file that played natively it was never applied at
   * all, so the remembered language was shown as on while the file's default
   * played. Costs are taken in order: agreeing with the file's default is
   * free; the element's own audio tracks, where the browser exposes them
   * (Safari), are free; then the rewrap or the conversion carries it.
   */
  private applyAudioChoice(): void {
    const tracks = this.item.tracks ?? [];
    if (this.closed || this.tv || tracks.length < 2) return;
    if (!this.sourced) return; // the route that sets a source settles it
    if (this.appliedTrack === this.audioTrack) return;
    if (this.transcoding || this.remuxed) {
      // A stream made for another track — the pick landed after the
      // conversion had started. Reopen it carrying the right one.
      this.startTrack();
      return;
    }
    if (this.audioTrack === this.fileDefaultTrack()) {
      this.appliedTrack = this.audioTrack; // native playback plays this one
      return;
    }
    if (this.enableNativeTrack(this.audioTrack)) {
      this.appliedTrack = this.audioTrack;
      return;
    }
    this.startTrack();
  }

  /**
   * Switch the element's own audio track, where the browser offers that.
   * Safari does; Blink and Gecko keep the API behind flags — they return
   * undefined and the caller converts instead. The element lists tracks in
   * stream order, which is the order ffmpeg numbers them by.
   */
  private enableNativeTrack(track: number): boolean {
    type NativeAudioTracks = { length: number; [i: number]: { enabled: boolean } };
    const list = (this.video as HTMLVideoElement & { audioTracks?: NativeAudioTracks })
      .audioTracks;
    if (!list || list.length < 2 || track < 0 || track >= list.length) return false;
    for (let i = 0; i < list.length; i++) list[i]!.enabled = i === track;
    return true;
  }

  /**
   * Serve the chosen soundtrack by changing source, from where playback is.
   *
   * A browser cannot be told which stream of a source to decode, so the
   * source has to carry the right track. The rewrap goes first where the
   * wait is worth it — a lossless copy at disk speed, cached, keyed by
   * track on the server — and its 404 falls through to the converter
   * exactly as it does for containers, which re-encodes only the sound
   * wherever the picture is one the browser decodes.
   */
  private startTrack(): void {
    if (this.transcoding) {
      this.transcoding = false; // reopening with another track, not escalating
      this.fallbackToTranscode(this.tcMode);
      return;
    }
    // The rewrap is keyed by track, so another choice is a fresh ask: the
    // once-per-file latch is about failures, not about this.
    this.remuxed = false;
    // A file this browser already opens needs nothing moved: the same streams
    // with one soundtrack in them is a copy, in the container they came in.
    // That matters most for exactly the files that carry a choice worth
    // making — a video shipping automatic dubs is Matroska holding one Opus
    // track per language, and converting one of them would be a generational
    // loss on a soundtrack that was already what the browser wanted.
    const copy = this.opensDirectly(this.item) ? 'track' : undefined;
    if ((copy || rewrapWorthTheWait(this.item.size, playsHLS())) && this.tryRemux(copy)) return;
    this.fallbackToTranscode(this.trackMode());
  }

  /**
   * How much has to be converted to change the soundtrack. The file is
   * playing, so the picture is proven whatever it is called — an unknown
   * codec gets the copy, backed by the audio-mode watchdog if nothing comes
   * out of it after all.
   */
  private trackMode(): 'full' | 'audio' {
    return convertMode(this.item.vcodec, (t) => this.video.canPlayType(t), true, this.item.reencode);
  }

  private toggleSubMenu(show = this.subMenu.hidden): void {
    this.toggleMenu(this.subMenu, this.ccBtn, show);
  }

  private selectSubtitle(index: number): void {
    this.subIndex = index >= 0 && index < this.subs.length ? index : -1;
    this.applySubtitle();
    // The choice lives in the playlist on this path, so changing it means
    // reopening the stream where it is — the same cost a soundtrack change
    // pays there, and rare enough to pay it.
    if (this.usingHLS) void this.startTranscodeAt(this.switchAt());
    const chosen = this.subs[this.subIndex];
    if (chosen) {
      remember('media.subtitle', chosen.lang || chosen.label);
      this.flash(icons.cc);
      showToast(`Subtitles: ${chosen.label}`, 1500);
    } else {
      forget('media.subtitle');
      showToast('Subtitles off', 1500);
    }
  }

  /** Cycle Off → first → … → last → Off, for the keyboard. */
  private cycleSubtitle(): void {
    if (this.subs.length === 0) return;
    this.selectSubtitle(this.subIndex + 1 >= this.subs.length ? -1 : this.subIndex + 1);
  }

  constructor(private item: Item, private opts: VideoOpts) {
    holdThumbs(); // the grid is covered: give playback every connection
    this.root = document.createElement('div');
    this.root.className = 'overlay video-overlay';
    this.root.innerHTML = `
      <video playsinline preload="auto" x-webkit-airplay="allow"></video>
      <img class="vo-poster" data-poster alt="" aria-hidden="true">
      <div class="vo-on-tv" data-ontv hidden aria-live="polite"></div>
      <div class="vo-fault" data-fault hidden aria-live="polite"></div>
      <div class="vo-prep" data-prep hidden aria-live="polite"><span data-prepmsg></span></div>
      <div class="vo-help" data-help hidden role="dialog" aria-label="Keys"></div>
      <div class="vo-slides" data-slides aria-hidden="true">
        <canvas class="vo-slide" data-cur></canvas>
        <img class="vo-slide vo-slide-prev" data-prev alt="">
        <img class="vo-slide vo-slide-next" data-next alt="">
      </div>
      <div class="vo-spinner"><div class="spinner"></div></div>
      <div class="vo-center" aria-hidden="true"></div>
      <div class="vo-top vo-fade">
        <div class="vo-title"></div>
        <a class="icon-btn" data-dl download title="Download">${icons.download}</a>
        <button class="icon-btn" data-close aria-label="Close (Esc)">${icons.close}</button>
      </div>
      <div class="vo-controls vo-fade">
        <div class="seek" role="slider" aria-label="Seek" aria-valuemin="0" aria-valuemax="0" aria-valuenow="0" tabindex="0">
          <div class="seek-track"></div>
          <div class="seek-buf"></div>
          <div class="seek-fill"><div class="seek-dot"></div></div>
        </div>
        <div class="vo-row">
          <button class="icon-btn" data-play aria-label="Play/Pause (Space)">${icons.play}</button>
          <button class="icon-btn" data-back aria-label="Back 10 seconds">${icons.back10}</button>
          <button class="icon-btn" data-fwd aria-label="Forward 10 seconds">${icons.fwd10}</button>
          <div class="vol-group">
            <button class="icon-btn" data-mute aria-label="Mute (M)">${icons.volume}</button>
            <input class="vol" data-vol type="range" min="0" max="1" step="0.02" value="1" aria-label="Volume">
          </div>
          <span class="vo-time">0:00 / 0:00</span>
          <span class="vo-spacer"></span>
          <button class="txt-btn" data-rate aria-label="Playback speed" title="Playback speed">1×</button>
          <div class="vo-menu-wrap">
            <button class="icon-btn" data-airplay aria-label="Play on a TV" aria-haspopup="menu" aria-expanded="false" hidden>${icons.airplay}</button>
            <div class="vo-menu" data-castmenu role="menu" hidden></div>
          </div>
          <div class="vo-menu-wrap">
            <button class="icon-btn" data-audio aria-label="Soundtrack" aria-haspopup="menu" aria-expanded="false" hidden>${icons.volume}</button>
            <div class="vo-menu" data-audiomenu role="menu" hidden></div>
          </div>
          <div class="vo-menu-wrap">
            <button class="icon-btn" data-cc aria-label="Subtitles (C)" aria-haspopup="menu" aria-expanded="false" hidden>${icons.cc}</button>
            <div class="vo-menu" data-ccmenu role="menu" hidden></div>
          </div>
          <button class="icon-btn" data-share aria-label="Copy a link to this film">${icons.link}</button>
          <button class="icon-btn" data-viz aria-label="Spectrum">${icons.grid}</button>
          <button class="icon-btn" data-crop aria-label="Trim the file's black borders" hidden>${icons.crop}</button>
          <button class="icon-btn vo-rotate" data-rotate aria-label="Rotate (R)">${icons.rotateCw}</button>
          <button class="icon-btn vo-pip" data-pip aria-label="Picture in picture">${icons.pip}</button>
          <button class="icon-btn" data-fs aria-label="Fullscreen (F)">${icons.maximize}</button>
        </div>
      </div>`;

    this.vizBtn = this.q('[data-viz]');
    this.spectrum = new SpectrumPanel(this.vizBtn, 'vo-viz');
    // Inside the control furniture, not floating over the picture: it then
    // fades with the controls, and sits exactly above them however many rows
    // they have wrapped onto — a fixed offset from the bottom collides with
    // the second row at precisely the width that has least room to spare.
    this.q('.vo-controls').prepend(this.spectrum.el);
    // The moment it is measured, so a later film is not offered a control this
    // browser cannot honour.
    this.spectrum.viz.onDeaf = () => {
      videoAudioIsReadable = false;
    };

    this.video = this.q('video');
    this.seekEl = this.q('.seek');
    this.seekFill = this.q('.seek-fill');
    this.seekBuf = this.q('.seek-buf');
    this.timeEl = this.q('.vo-time');
    this.playBtn = this.q('[data-play]');
    this.muteBtn = this.q('[data-mute]');
    this.volSlider = this.q('[data-vol]');
    this.rateBtn = this.q('[data-rate]');
    this.ccBtn = this.q('[data-cc]');
    this.subMenu = this.q('[data-ccmenu]');
    this.cropBtn = this.q('[data-crop]');
    this.airplayBtn = this.q('[data-airplay]');
    this.castMenu = this.q('[data-castmenu]');
    this.onTv = this.q('[data-ontv]');
    this.faultEl = this.q('[data-fault]');
    this.audioBtn = this.q('[data-audio]');
    this.audioMenu = this.q('[data-audiomenu]');
    this.menus = [
      { el: this.subMenu, button: this.ccBtn },
      { el: this.audioMenu, button: this.audioBtn },
      { el: this.castMenu, button: this.airplayBtn },
    ];
    this.helpEl = this.q('[data-help]');
    this.helpEl.innerHTML = `<table>${PLAYER_KEYS.map(
      (k) => `<tr><td>${k.keys.map((key) => `<kbd>${esc(key)}</kbd>`).join(' ')}</td><td>${esc(k.does)}</td></tr>`,
    ).join('')}</table>`;
    this.fsBtn = this.q('[data-fs]');
    this.centerFlash = this.q('.vo-center');
    this.titleEl = this.q('.vo-title');
    this.dlLink = this.q('[data-dl]');
    this.poster = this.q('[data-poster]');
    this.prep = this.q('[data-prep]');
    this.prepMsg = this.q('[data-prepmsg]');
    this.slideCur = this.q('[data-cur]');
    this.syncCastControls();

    document.getElementById('overlays')!.appendChild(this.root);
    document.body.classList.add('no-scroll', 'viewing');
    holdScroll();
    // The keys belong to what is in front, which is now this. Prev and next
    // are deliberately not taken: a film is not a track list, and a key that
    // skipped to another film would be a surprise nobody asked for.
    this.releaseKeys = claimMediaKeys({
      play: () => void this.video.play().catch(() => {}),
      pause: () => this.video.pause(),
      stop: () => this.video.pause(),
    });
    this.bind();

    const saved = Number(recall('media.videoVolume') ?? '1');
    this.video.volume = Number.isFinite(saved) ? clamp(saved, 0, 1) : 1;
    this.volSlider.value = String(this.video.volume);

    this.navIndex = opts.nav?.index ?? -1;
    this.load(item);
    this.scheduleHide();
    requestAnimationFrame(() => this.root.classList.add('open'));
  }

  /**
   * Point the player at an item — the one it opened on, or a neighbour it
   * has stepped to.
   *
   * Everything the previous file established belongs to that file and is
   * cleared here: the conversion it turned out to need, the subtitle tracks
   * found beside it, a rotation applied to sideways footage. Volume, speed
   * and the chosen subtitle language are the viewer's rather than the
   * file's, so they carry over.
   */
  private load(item: Item, at?: number): void {
    this.beginFile(item, at);

    if (!this.opensDirectly(item)) {
      // The browser has already said it cannot open this container, so
      // handing it the file anyway is not a cheap thing that fails fast:
      // measured, Safari pulled 664 MiB of one film over 68 s — and 7.6 GiB
      // across the attempts — hunting for something it could play before
      // giving up and letting the fallback run. Start where it was going to
      // end up.
      void this.convertDirectly();
      return;
    }
    // Half a second of playback from where it actually starts, not from
    // zero, which a resume is already past.
    this.startSource(streamUrl(item.id), { at: this.startAt, settle: 0.5 });
    if (this.startAt > 0) showToast(`Resuming at ${formatDuration(this.startAt)}`);
    void this.loadSubs();
    // What the file holds decides the soundtrack, and it decides it now: the
    // codecs may already be known from the listing, and if they are not, the
    // moment they arrive is still before anything has had to fail. The menu
    // — and with it the picked track — was built above, so a conversion
    // started here carries the remembered choice rather than track one.
    this.useKnownCodecs();
    void this.refreshItem().then(() => {
      // The soundtracks come out of the probe the metadata request runs, so
      // this is the first moment a first open has a menu to build — and the
      // pick has to land before the codec check that may act on it.
      this.buildAudioMenu();
      this.useKnownCodecs();
      this.applyAudioChoice();
    });
    // The readout is reset by hand rather than through onTime, whose decode
    // check keys off currentTime — which a resume has already moved. Called
    // here it would report no decoded frames before there had been a chance
    // to decode any, and convert a file that plays perfectly well. The real
    // check runs from timeupdate, which means playback has begun.
    this.timeEl.textContent = '0:00 / 0:00';
    this.seekFill.style.width = '0%';
    this.showControls();
  }

  /**
   * Everything that belongs to one file, cleared or restored for the next:
   * the conversion it needed, its subtitle tracks, its resume point, its
   * rotation. Shared by load, which then hands the element a source, and
   * by castItem, which hands the file to a television instead — the two
   * used to differ in nothing but that, and a reset spelled out twice is
   * one that drifts. Volume, speed and the remembered languages are the
   * viewer's and are not touched.
   */
  private beginFile(item: Item, at?: number): void {
    this.item = item;
    this.resume = this.opts.resumeFor?.(item.id);
    this.lastSaved = -1;

    // A new file is always tried directly first; only its own failures put
    // it back through the server. The generation bump drops a keyframe
    // answer still in flight for the file being left behind.
    this.tcGen++;
    this.remuxed = false;
    this.stopSoundFix();
    this.transcoding = false;
    this.usingHLS = false;
    this.hlsFailed = false;
    this.tcMode = 'full';
    this.tcOffset = 0;
    this.decodeChecked = false;
    this.checkAt = 0.5;
    window.clearTimeout(this.audioWatch);
    this.sourced = false;
    this.readChecked = false;
    this.faulted = false;
    this.faultEl.hidden = true;
    this.played = false;
    this.playFrom = -1;
    // at is where a television had got to when the film was taken back off
    // it; otherwise this file's own resume point.
    this.startAt = at ?? resumeStart(this.resume, item.duration);

    for (const track of this.video.querySelectorAll('track')) track.remove();
    this.subs = [];
    this.subIndex = -1;
    this.ccBtn.hidden = true;
    this.ccBtn.classList.remove('active');
    this.audioMenu.hidden = true;
    this.audioBtn.hidden = true;
    this.audioTrack = 0;
    this.appliedTrack = null;
    // The pick has to exist before any route starts a stream, or the stream
    // carries the first track while the menu marks the remembered one. The
    // tracks may already be here from the listing; refreshItem below covers
    // a first open, whose probe is what discovers them.
    this.buildAudioMenu();
    this.subMenu.hidden = true;
    this.subMenu.innerHTML = '';
    this.ccBtn.setAttribute('aria-expanded', 'false');

    void this.findReceivers();

    this.hidePrep();
    // As the owner last left this file, not as the last file was left.
    this.rotation = ((item.rotation ?? 0) % 4) * 90;
    this.crop = null;
    this.cropping = !item.nocrop;
    this.cropBtn.hidden = true;
    this.applyRotation();
    void this.findBorders(item);

    this.titleEl.textContent = item.name;
    playingVideo(item.name);
    this.titleEl.title = item.path;
    this.dlLink.href = streamUrl(item.id, true);
    // Cover the picture until the new file renders. The element goes black
    // the moment its source changes and stays black until the first frame is
    // decoded, which on a large file over a slow disk is a second or more of
    // nothing where the previous picture used to be.
    this.showPoster(item);
    this.seekBuf.style.width = '0%';

  }

  /**
   * Say how a wait is going, while there is one.
   *
   * Only the rewrap has one: it writes a whole file before a single byte is
   * playable, which for a film is seconds to tens of seconds of nothing. The
   * segmented conversion starts after its first segment and then runs on
   * ahead of the viewer — there is nothing to wait for and nothing to report,
   * and a badge over a playing picture is just something in the way.
   *
   * Polled rather than pushed: a number that matters for a few seconds and
   * only while somebody is waiting on it is not worth a second event stream.
   */
  private watchConversion(): void {
    this.stopPrep();
    const gen = ++this.prepGen;
    const tick = async (): Promise<void> => {
      if (this.closed || gen !== this.prepGen) return;
      let active = false;
      let text = '';
      try {
        const p = await convertProgress(this.item.id);
        active = p.active;
        text = p.kind === 'rewrap' ? `Preparing this file… ${p.percent}%` : `Converting… ${p.percent}%`;
      } catch {
        active = false;
      }
      // Checked again after the await: hidePrep may have run while this was
      // in flight, and a late answer that put the badge back would leave it
      // there for good — there is no further tick to take it away.
      if (this.closed || gen !== this.prepGen) return;
      // Whatever the server is still doing, a viewer with a picture is not
      // waiting for it.
      if (!active || this.playable()) {
        this.hidePrep();
        return;
      }
      this.prep.hidden = false;
      this.prepMsg.textContent = text;
    };
    this.stopPrep = poll(800, () => void tick(), true);
  }

  /** Whether there is something on screen to watch. */
  private playable(): boolean {
    return this.video.readyState >= 2 /* HAVE_CURRENT_DATA */ || !this.video.paused;
  }

  /**
   * The element could not play what it was given.
   *
   * Before any of the ways round a format the browser refuses, the file
   * itself is asked for — once per file, one byte of it — because none of
   * those ways helps a file the disk will not hand over: every one fails at
   * the same open, and the ten seconds they took to fail one after another
   * ended in blaming the format. Measured on a mount that had shut itself
   * down: stream 404, rewrap 404, segments 503, the pipe twice with nothing
   * in it, and then "this format cannot be played" over an ordinary MP4.
   */
  private async onMediaError(): Promise<void> {
    this.root.classList.remove('buffering');
    if (this.faulted) return;
    if (!this.readChecked) {
      this.readChecked = true;
      // By id, not by object: refreshItem replaces the item for the same
      // film when /api/item answers, and that answer raced this one — three
      // times out of four it landed first, and the fault was dropped as if
      // the viewer had moved on to another film.
      const id = this.item.id;
      const fault = await this.fileFault();
      if (this.closed || this.item.id !== id) return;
      if (fault) {
        this.giveUp(fault);
        return;
      }
    }
    // A container the browser will not open is not the same thing as a
    // codec it cannot decode: try moving the streams before re-encoding
    // them. The server declines with a 404 where that would not help,
    // which arrives here as another error and goes on to the converter.
    if (this.tryRemux()) return;
    if (!this.transcoding) {
      // Container or codec the browser rejects outright. Which of the two
      // it is decides how much has to be converted, and nothing was
      // decoded here to tell them apart — so the file's own codecs do.
      void this.convertForContainer();
      return;
    }
    if (this.usingHLS) {
      // It said it could play this and could not. The pipe is what every
      // other browser uses and it needs nothing of the sort.
      this.hlsFailed = true;
      this.usingHLS = false;
      void this.startTranscodeAt(this.tcOffset);
      return;
    }
    this.giveUp('This format cannot be played by your browser');
  }

  /**
   * Ask the stream endpoint for one byte of the file and read the answer
   * (readFault, tested). A network that went away says nothing about the
   * file, so the ordinary route carries on.
   */
  private async fileFault(): Promise<string | null> {
    try {
      const res = await fetch(streamUrl(this.item.id), { headers: { Range: 'bytes=0-0' } });
      // The reason rides in the body of a refusal; a byte of the film is
      // not wanted, and an undrained body holds a connection for nothing.
      const body = res.ok ? '' : await res.text().catch(() => '');
      if (res.ok) void res.body?.cancel();
      return readFault(res.status, body);
    } catch {
      return null;
    }
  }

  /**
   * Stop trying. Whatever was in flight for this file — the soundtrack
   * poll, the audio-mode watchdog, the progress badge — is dropped, and the
   * reason stays on screen for as long as the file does: a toast that
   * vanishes leaves a spinner that says nothing.
   */
  private giveUp(reason: string): void {
    this.faulted = true;
    this.stopSoundFix();
    window.clearTimeout(this.audioWatch);
    this.hidePrep();
    // With a way back: a disk that stopped answering usually comes back,
    // and until this the film could only be tried again by closing the
    // player and reopening it.
    this.faultEl.innerHTML = `${esc(reason)} <button type="button" class="vo-retry" data-retry>Try again</button>`;
    this.faultEl.hidden = false;
    this.showControls();
  }

  /** Try the file again from the start of its route, as if freshly opened. */
  private retry(): void {
    if (this.closed) return;
    this.load(this.item);
  }

  private hidePrep(): void {
    // The generation bump is what stops a tick already in flight from
    // putting the badge back after this.
    this.prepGen++;
    this.stopPrep();
    this.prep.hidden = true;
  }

  /** Show the item's thumbnail over the picture until the video renders. */
  private showPoster(item: Item): void {
    this.poster.src = previewUrl(item.id, item.mtime);
    this.root.classList.add('posted');
  }

  private hidePoster(): void {
    this.root.classList.remove('posted');
  }

  /**
   * Draw what is on screen into the drag layer.
   *
   * The element is same-origin whichever route is playing it — the file, the
   * rewrap or the conversion — so this is allowed; a frame that cannot be
   * read leaves the layer as it was and the drag simply starts from black,
   * which is what it used to do all the time.
   */
  private captureFrame(): boolean {
    if (!this.opts.nav?.src) return false; // nothing to step through
    const v = this.video;
    const w = v.videoWidth;
    const h = v.videoHeight;
    if (!w || !h) return true;
    const c = this.slideCur;
    if (c.width !== w || c.height !== h) {
      c.width = w;
      c.height = h;
    }
    try {
      c.getContext('2d')?.drawImage(v, 0, 0, w, h);
    } catch {
      // Nothing to show from this file; the layer keeps whatever it had.
    }
    return true;
  }

  /**
   * Move to the neighbouring video in the listing, reporting whether there
   * was one. Items of other kinds in between are passed over: this viewer
   * has nothing to show for a picture or a track.
   */
  private async step(dir: 1 | -1): Promise<boolean> {
    const src = this.opts.nav?.src;
    if (!src) return false;
    const gen = ++this.stepGen;
    const found = await findKind(src, this.navIndex + dir, dir, 'video');
    // A second swipe while the first was still searching wins.
    if (!found || this.closed || gen !== this.stepGen) return false;
    this.goTo(found.item, found.index);
    this.flash(dir > 0 ? icons.next : icons.prev);
    return true;
  }

  /**
   * Open a neighbour that has already been found — here, or on the
   * television that is playing this, which is then handed the next file
   * as it would be handed this one.
   */
  private goTo(item: Item, index: number): void {
    if (this.closed) return;
    this.persist(true); // the file being left, at where it was left
    this.navIndex = index;
    if (this.tv) void this.castItem(item);
    else this.load(item);
  }

  /**
   * The season rolls on where a television is playing it, as it does here.
   * The per-file state is reset exactly as load resets it — the soundtrack
   * picked by the remembered language, the subtitle by the remembered
   * language — and then, instead of the element being given a source, the
   * set is given the file, from the file's own resume point. The poll
   * stands down meanwhile: the set reports the last film stopped until it
   * has the next, and that report must not read as a second ending.
   */
  private async castItem(item: Item): Promise<void> {
    const tv = this.tv;
    if (!tv || this.closed) return;
    tv.stop();
    this.beginFile(item);
    // The readout shows the film that is coming, from its own resume point,
    // while what to send with it is fetched; startCast picks it up from here.
    tv.pos = this.startAt;
    tv.dur = (item.duration ?? 0) / 1000;
    this.showPoster(item);
    this.onTv.textContent = `Opening on ${tv.cast.renderer.name}…`;
    const gen = ++this.stepGen;
    // The soundtracks come from the probe the item request runs, and the
    // subtitles from their own listing; both have to be in hand before the
    // set is told which of them to play.
    await this.refreshItem();
    if (this.closed || gen !== this.stepGen || this.tv !== tv) return;
    this.buildAudioMenu();
    await this.loadSubs();
    if (this.closed || gen !== this.stepGen || this.tv !== tv) return;
    await this.startCast(tv.cast.renderer);
  }

  /** The film ended on the set: the next episode, or nothing more to do. */
  private async rollOnSet(): Promise<void> {
    const moved = await this.step(1);
    if (!moved && this.tv && !this.closed) {
      this.endCast(false);
      this.flash(icons.restart);
    }
  }

  private q<T extends HTMLElement = HTMLElement>(sel: string): T {
    return this.root.querySelector(sel) as T;
  }

  // ---- wiring ----------------------------------------------------------

  private bind(): void {
    const v = this.video;
    v.addEventListener('play', this.onPlayState);
    v.addEventListener('pause', this.onPlayState);
    v.addEventListener('timeupdate', this.onTime);
    v.addEventListener('durationchange', this.onTime);
    v.addEventListener('progress', this.onProgress);
    v.addEventListener('waiting', () => this.root.classList.add('buffering'));
    v.addEventListener('playing', () => {
      this.root.classList.remove('buffering');
      this.hidePoster();
      this.hidePrep();
    });
    v.addEventListener('canplay', () => this.root.classList.remove('buffering'));
    // The first decoded frame is the moment there is something behind the
    // still worth showing; waiting for 'playing' alone would keep it up
    // through a file that opens paused.
    v.addEventListener('loadeddata', () => this.hidePoster());
    // The display size arrives with the metadata, and the trim depends on
    // it: until then a stretched picture is measured in the wrong units.
    v.addEventListener('loadedmetadata', () => {
      if (this.crop) this.cropBtn.hidden = this.cropScaleNow(this.crop) === 1;
      this.applyRotation();
      // The element's own audio tracks exist from here, so this is the
      // first moment a remembered language can be switched to for free.
      this.applyAudioChoice();
    });
    v.addEventListener('ended', this.onEnded);
    v.addEventListener('error', () => void this.onMediaError());
    v.addEventListener('volumechange', () => {
      this.muteBtn.innerHTML = v.muted || v.volume === 0 ? icons.mute : v.volume < 0.5 ? icons.volumeLow : icons.volume;
      this.volSlider.value = String(v.muted ? 0 : v.volume);
      if (!v.muted) remember('media.videoVolume', String(v.volume));
    });
    v.addEventListener('click', () => this.togglePlay());
    v.addEventListener('dblclick', () => this.toggleFullscreen());

    this.q('[data-close]').addEventListener('click', () => this.close());
    this.playBtn.addEventListener('click', () => this.togglePlay());
    this.q('[data-back]').addEventListener('click', () => this.skip(-10));
    this.q('[data-fwd]').addEventListener('click', () => this.skip(10));
    this.muteBtn.addEventListener('click', () => {
      v.muted = !v.muted;
    });
    this.volSlider.addEventListener('input', () => this.setVolume(Number(this.volSlider.value)));
    this.faultEl.addEventListener('click', (ev) => {
      if ((ev.target as HTMLElement).closest('[data-retry]')) this.retry();
    });
    this.rateBtn.addEventListener('click', () => this.cycleRate());
    this.vizBtn.addEventListener('click', () => this.toggleViz());
    this.ccBtn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      this.toggleSubMenu();
    });
    // Offered only once the browser has said there is something to play to.
    this.stopAirPlayWatch = watchAirPlay(this.video, (available) => {
      this.browserReceiver = available;
      this.showReceiverButton();
    });
    // And when it goes there, what it is given has to be something the
    // receiver can decode; see onRemoteChange.
    watchRemoteState(this.video, this.onRemoteChange);
    this.airplayBtn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      // Where the network has renderers on it there is a choice to make, so
      // the button opens the list. Where it has none, the only thing behind
      // it is the browser's own picker and a menu of one would be furniture.
      if (knownRenderers().length > 0) this.toggleCastMenu();
      else showAirPlayPicker(this.video);
    });
    this.audioBtn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      this.toggleAudioMenu();
    });
    // Anywhere else dismisses the menus, including the video itself — but
    // not a menu's own button, whose click is what toggles it closed.
    this.root.addEventListener('pointerdown', (ev) => {
      const target = ev.target as Node;
      for (const m of this.menus) {
        if (!m.el.hidden && !m.el.contains(target) && !m.button.contains(target)) {
          this.toggleMenu(m.el, m.button, false);
        }
      }
    });
    this.cropBtn.addEventListener('click', () => this.toggleCrop());
    this.q('[data-rotate]').addEventListener('click', () => this.rotate(1));
    // A link to this film, on top of whatever listing it was opened from —
    // so closing it leaves the recipient somewhere, rather than nowhere.
    this.q('[data-share]').addEventListener('click', () => {
      void shareItem(this.item.id, this.item.name);
    });
    window.addEventListener('resize', this.onResize);
    this.fsBtn.addEventListener('click', () => this.toggleFullscreen());
    const pipBtn = this.q('[data-pip]');
    if (!('requestPictureInPicture' in HTMLVideoElement.prototype)) pipBtn.hidden = true;
    pipBtn.addEventListener('click', () => {
      if (document.pictureInPictureElement) void document.exitPictureInPicture();
      else void this.video.requestPictureInPicture().catch(() => {});
    });

    this.bindSeek();

    // Dragging up and down moves between videos, and moves the picture with
    // the finger while it does: the file being left stays on screen, the one
    // being reached comes in behind it, and neither of them is the black
    // rectangle a bare source change puts there. The controls are excluded
    // where the gesture starts — a finger travelling along the seek bar is
    // scrubbing.
    // The drag between films; the deck is shared with the picture viewer.
    new SlideDeck<Item>({
      root: this.root,
      layer: this.q('[data-slides]'),
      prev: this.q('[data-prev]'),
      next: this.q('[data-next]'),
      axis: 'y',
      ignore: '.vo-controls, .vo-top',
      extent: () => this.root.clientHeight,
      capture: () => this.captureFrame(),
      neighbour: (dir) => findKind(this.opts.nav!.src, this.navIndex + dir, dir, 'video'),
      preview: (item) => previewUrl(item.id, item.mtime),
      // The still goes up before the layer comes down, showing the very
      // picture the layer just carried into place: without that order there
      // is a frame of the file being left, which is what the drag was
      // supposed to have moved away from.
      arrive: (n) => this.showPoster(n.item),
      // Go to the item that was previewed rather than searching again. A
      // second search could land somewhere else if the listing changed
      // while the finger was down, and then the picture that slid in would
      // not be the file that opened.
      open: (n) => this.goTo(n.item, n.index),
      fallback: (dir) => void this.step(dir),
      closed: () => this.closed,
    });
    // A flick fast enough to arrive with no movement in between never
    // becomes a drag, so the same gesture is recognised the other way as
    // well. watchSwipes stands down for anything the drag already handled.
    watchSwipes(
      this.root,
      (sw) => {
        if (sw.dir !== 'up' && sw.dir !== 'down') return false;
        void this.step(sw.dir === 'up' ? 1 : -1);
        return true;
      },
      '.vo-controls, .vo-top',
    );

    this.root.addEventListener('pointermove', this.onActivity);
    this.root.addEventListener('pointerdown', this.onActivity);
    document.addEventListener('keydown', this.onKey, true);
    document.addEventListener('fullscreenchange', this.onFsChange);

    this.saveTimer = window.setInterval(() => this.persist(false), SAVE_EVERY_MS);
  }

  /**
   * The bar follows the finger whatever is behind it. Where the element
   * seeks for free it seeks as the finger moves; where a seek is a request
   * — a television, or a conversion that reopens per seek — the bar and
   * the clock move with the finger and the request is sent once, on
   * release. A converted stream used to commit on the first touch and
   * ignore the rest, which on a phone made a drag a seek to wherever the
   * finger first landed.
   */
  private bindSeek(): void {
    const toTime = (ev: PointerEvent): number => {
      const rect = this.seekEl.getBoundingClientRect();
      const frac = clamp((ev.clientX - rect.left) / rect.width, 0, 1);
      return frac * this.totT();
    };
    const follow = (ev: PointerEvent): void => {
      const t = toTime(ev);
      if (this.tv) this.tv.pos = t;
      else if (this.transcoding) this.seekAt = t;
      else this.video.currentTime = t;
      this.onTime();
    };
    this.seekEl.addEventListener('pointerdown', (ev) => {
      if (this.totT() <= 0) return;
      this.seeking = true;
      this.seekEl.setPointerCapture(ev.pointerId);
      follow(ev);
    });
    this.seekEl.addEventListener('pointermove', (ev) => {
      if (this.seeking) follow(ev);
    });
    const end = (): void => {
      if (!this.seeking) return;
      this.seeking = false;
      if (this.tv) this.seekTo(this.tv.pos);
      else if (this.transcoding) this.seekTo(this.seekAt);
    };
    this.seekEl.addEventListener('pointerup', end);
    this.seekEl.addEventListener('pointercancel', end);
  }

  // ---- event handlers --------------------------------------------------

  private onPlayState = (): void => {
    const playing = !this.video.paused;
    setPlaybackState(playing); // so the desktop's own controls follow
    this.playBtn.innerHTML = icons[playButtonIcon(playing)];
    this.flash(playing ? icons.play : icons.pause);
    if (playing) this.scheduleHide();
    else {
      this.showControls();
      this.persist(true);
    }
  };

  private onTime = (): void => {
    if (!this.decodeChecked && this.video.currentTime > this.checkAt) {
      this.checkDecodes();
    }
    this.countPlay();
    this.paint();
  };

  /**
   * Count this file as played, once, when it has actually played.
   *
   * Measured against the element's own clock rather than the wall's, so a
   * paused film never creeps over the line — and against how far it has run
   * *since this file was loaded*, so resuming a film at ninety minutes does
   * not count a play the instant the clock is read.
   */
  private countPlay(): void {
    if (this.played) return;
    const at = this.curT();
    if (this.playFrom < 0) this.playFrom = at;
    if (at - this.playFrom < PLAY_AFTER) return;
    this.played = true;
    recordPlay(this.item.id);
  }

  /** Write the clock and the bar from wherever playback currently is. */
  private paint(): void {
    // While a converted stream is being scrubbed the bar shows the finger,
    // since the stream will not move until it is released.
    const t = this.seeking && this.transcoding && !this.tv ? this.seekAt : this.curT();
    const dur = this.totT();
    this.timeEl.textContent = `${formatDuration(t)} / ${formatDuration(dur)}`;
    this.seekFill.style.width = dur > 0 ? `${clamp((t / dur) * 100, 0, 100)}%` : '0%';
    // The slider says where it is, for a reader that cannot see the bar.
    this.seekEl.setAttribute('aria-valuenow', String(Math.round(t)));
    this.seekEl.setAttribute('aria-valuemax', String(Math.round(dur)));
    this.seekEl.setAttribute('aria-valuetext', `${formatDuration(t)} of ${formatDuration(dur)}`);
  }

  private onProgress = (): void => {
    const v = this.video;
    const dur = this.totT();
    if (dur <= 0 || v.buffered.length === 0) return;
    const end = this.tcOffset + v.buffered.end(v.buffered.length - 1);
    this.seekBuf.style.width = `${clamp((end / dur) * 100, 0, 100)}%`;
  };

  private onEnded = (): void => {
    this.persist(true);
    this.showControls();
    // A finished file rolls on to the next video by itself. If it was the
    // last one the player stays put, where the flash offers a replay.
    void this.step(1).then((moved) => {
      if (!moved && !this.closed) this.flash(icons.restart);
    });
  };

  private onKey = (ev: KeyboardEvent): void => {
    if (this.closed) return;
    switch (ev.key) {
      case 'Escape':
        // Whatever is on top is the thing Escape closes: the key help, then
        // any of the three menus, then the player.
        if (!this.helpEl.hidden) {
          this.toggleHelp(false);
          break;
        }
        if (this.anyMenuOpen()) {
          this.closeMenus();
          break;
        }
        if (document.fullscreenElement) break; // browser exits fullscreen first
        this.close();
        break;
      case '?':
        this.toggleHelp();
        break;
      case ' ':
      case 'k':
        this.togglePlay();
        break;
      case 'ArrowLeft':
      case 'j':
        this.skip(-10);
        break;
      case 'ArrowRight':
      case 'l':
        this.skip(10);
        break;
      case 'Home':
        this.seekTo(0);
        break;
      case 'End':
        this.seekTo(Math.max(0, this.totT() - 1));
        break;
      case 'ArrowUp':
        this.nudgeVolume(0.05);
        break;
      case 'ArrowDown':
        this.nudgeVolume(-0.05);
        break;
      case 'f':
        this.toggleFullscreen();
        break;
      case 'm':
        this.video.muted = !this.video.muted;
        break;
      case 'c':
        this.cycleSubtitle();
        break;
      case 'a':
        this.cycleAudio();
        break;
      case 'r':
        this.rotate(1);
        break;
      case 'R':
        this.rotate(-1);
        break;
      default:
        return;
    }
    ev.preventDefault();
    ev.stopPropagation();
    this.onActivity();
  };

  private onFsChange = (): void => {
    this.fsBtn.innerHTML = document.fullscreenElement ? icons.minimize : icons.maximize;
    this.syncViz();
    // Entering or leaving fullscreen changes the overlay's dimensions,
    // which a quarter-turned video is sized against.
    requestAnimationFrame(() => this.applyRotation());
  };

  private onActivity = (): void => this.showControls();

  // ---- behaviors -------------------------------------------------------

  /**
   * Move the volume the way the slider does, wherever the sound is: while a
   * television is playing, the element is silent and the set's own volume
   * is the one that means anything — the keys used to write to the element
   * anyway, which changed nothing anyone could hear.
   */
  private nudgeVolume(delta: number): void {
    const level = clamp(Number(this.volSlider.value) + delta, 0, 1);
    this.volSlider.value = String(level);
    this.setVolume(level);
  }

  /**
   * Set the level wherever the sound is. While a television is playing,
   * the element is silent and the set's own volume is the one that means
   * anything — and it is a round trip per change, so a slider being dragged
   * is sent once it has settled rather than dozens of times a second.
   */
  private setVolume(level: number): void {
    if (this.tv) {
      window.clearTimeout(this.castVolTimer);
      this.castVolTimer = window.setTimeout(() => this.tv?.cast.volume(level * 100), 150);
      return;
    }
    this.video.muted = false;
    this.video.volume = level;
  }

  private togglePlay(): void {
    if (this.tv) {
      this.tv.toggle();
      this.playBtn.innerHTML = icons[playButtonIcon(this.tv.playing)];
      return;
    }
    if (this.video.paused) void this.video.play().catch(() => {});
    else this.video.pause();
  }

  private skip(sec: number): void {
    const d = this.totT() || Infinity;
    this.seekTo(clamp(this.curT() + sec, 0, d));
    this.flash(sec < 0 ? icons.back10 : icons.fwd10);
  }

  private cycleRate(): void {
    if (this.tv) return; // the element is not what is playing
    const i = RATES.indexOf(this.video.playbackRate);
    const rate = RATES[(i + 1) % RATES.length] ?? 1;
    this.video.playbackRate = rate;
    this.rateBtn.textContent = `${rate}×`;
  }

  private toggleFullscreen(): void {
    if (document.fullscreenElement) {
      void document.exitFullscreen().catch(() => {});
    } else if (this.root.requestFullscreen) {
      void this.root.requestFullscreen().catch(() => {
        // iOS Safari: fall back to the native video fullscreen.
        const v = this.video as HTMLVideoElement & { webkitEnterFullscreen?: () => void };
        v.webkitEnterFullscreen?.();
      });
    } else {
      const v = this.video as HTMLVideoElement & { webkitEnterFullscreen?: () => void };
      v.webkitEnterFullscreen?.();
    }
  }

  private flash(icon: string): void {
    this.centerFlash.innerHTML = icon;
    this.centerFlash.classList.remove('pop');
    void this.centerFlash.offsetWidth; // restart animation
    this.centerFlash.classList.add('pop');
  }

  /**
   * Reveal the controls, and arm their retreat again if something is
   * playing. Re-arming here rather than at each call site is what keeps them
   * from becoming permanent: showControls runs from several places that are
   * not user activity — a rejected play(), a stream error, the end of the
   * file — and a converted video reaches one of them on every seek, because
   * replacing the source interrupts the play() it just issued.
   */
  private showControls(): void {
    this.root.classList.remove('idle');
    this.syncViz();
    if (this.video.paused) window.clearTimeout(this.hideTimer);
    else this.scheduleHide();
  }

  /**
   * Paint only while it is on screen.
   *
   * The panel lives in the control furniture and goes with it, and a
   * fullscreen film has no furniture at all — so the analyser is told to
   * stop rather than drawing every frame into something nobody can see.
   * Reading the analyser is what makes the first sound after silence appear
   * in the next frame, but nothing here is being read while it is hidden.
   */
  private syncViz(): void {
    if (!this.spectrum.isOpen) return;
    if (this.root.classList.contains('idle') || document.fullscreenElement) this.spectrum.viz.stop();
    else this.spectrum.viz.start();
  }

  /** Whether any of the player's menus is open — they anchor to controls. */
  private anyMenuOpen(): boolean {
    return this.menus.some((m) => !m.el.hidden);
  }

  private closeMenus(): void {
    for (const m of this.menus) if (!m.el.hidden) this.toggleMenu(m.el, m.button, false);
  }

  /** Show the keys, or put them away. */
  private toggleHelp(show = this.helpEl.hidden): void {
    this.helpEl.hidden = !show;
    this.showControls();
  }

  private scheduleHide(): void {
    window.clearTimeout(this.hideTimer);
    this.hideTimer = window.setTimeout(() => {
      // Not while a menu is open: it is anchored to a control, and the
      // subtitle menu was the only one this asked about until the
      // soundtrack and TV menus faded away under a reading viewer.
      if (!this.video.paused && !this.seeking && !this.anyMenuOpen()) {
        this.root.classList.add('idle');
        this.syncViz();
      }
    }, HIDE_CONTROLS_MS);
  }

  /** Persist the playback position (deduplicated unless forced). */
  private persist(force: boolean): void {
    const t = this.curT();
    const d = this.totT();
    if (!shouldSave(t, this.lastSaved, force)) return; // the rule is tested
    this.lastSaved = t;
    savePosition(this.item.id, t, d);
    this.opts.onPosition?.(this.item.id, t, d);
  }

  private close(): void {
    if (this.closed) return;
    this.closed = true;
    releaseThumbs();
    this.persist(true);
    // A film left playing on a television with nothing on screen to stop it
    // would need the set's own remote to end; closing the player ends it.
    if (this.tv) this.endCast(false);
    window.clearInterval(this.saveTimer);
    this.stopPrep();
    window.clearTimeout(this.audioWatch);
    window.clearTimeout(this.castVolTimer);
    this.stopSoundFix();
    this.stopAirPlayWatch();
    window.clearTimeout(this.hideTimer);
    document.removeEventListener('keydown', this.onKey, true);
    document.removeEventListener('fullscreenchange', this.onFsChange);
    window.removeEventListener('resize', this.onResize);
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    this.video.pause();
    this.video.removeAttribute('src');
    this.video.load();
    // The graph and its context go with the element that was routed into
    // it: a browser allows only a handful of contexts, and a viewer who
    // opens the spectrum on film after film would use them all up.
    this.spectrum.viz.dispose();
    this.root.classList.remove('open');
    document.body.classList.remove('no-scroll', 'viewing');
    releaseScroll();
    this.releaseKeys();
    playingVideo(null);
    window.setTimeout(() => this.root.remove(), 200);
    this.opts.onClose?.();
  }
}
