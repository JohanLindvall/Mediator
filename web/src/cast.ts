/**
 * Playing to a television that is not a browser.
 *
 * AirPlay and the Remote Playback API hand a *browser's* element to a
 * receiver, and neither can reach a set that the browser merely discovers —
 * a television found over DIAL is offered to no picker, and no page code
 * changes that. A DLNA renderer is driven from the server instead
 * (internal/dlna): it is told a URL on the server's own LAN address and
 * fetches the file itself, so what plays is the file, at its own quality,
 * with no conversion in the way — a set decodes 4K HEVC and its HDR where
 * the browser here would have needed a re-encode to show anything at all.
 *
 * So this module sends *instructions*, not media. Nothing is streamed from
 * this page, which is also why it works from a phone that is nowhere near
 * either the server or the television.
 */
import type { CastControl, CastStatus, RendererInfo, RenderersResponse } from './types.gen';
import { esc } from './format';

/**
 * What answered the last search, and when.
 *
 * A search is a multicast and a few small fetches, but it also waits a
 * couple of seconds for sets that hold their reply back, so the answer is
 * kept: opening one film after another must not pay for it each time. The
 * server caches this as well; this is the round trip, not the search.
 */
let known: RendererInfo[] = [];
let asked = 0;
let inFlight: Promise<RendererInfo[]> | null = null;
const TTL = 60_000;

/** Everything on the network that will play what it is sent. */
export function renderers(force = false): Promise<RendererInfo[]> {
  if (!force && asked && Date.now() - asked < TTL) return Promise.resolve(known);
  // Several viewers opening at once ask one question between them.
  if (inFlight) return inFlight;
  inFlight = fetch(`/api/renderers${force ? '?fresh=1' : ''}`)
    .then((r) => (r.ok ? (r.json() as Promise<RenderersResponse>) : { renderers: [] }))
    .then((r) => {
      known = r.renderers ?? [];
      asked = Date.now();
      return known;
    })
    .catch(() => known)
    .finally(() => {
      inFlight = null;
    });
  return inFlight;
}

/** What is known without asking — for deciding whether to offer a button. */
export function knownRenderers(): RendererInfo[] {
  return known;
}

/**
 * One receiver, playing one item.
 *
 * Every method is a request to the server, which is the only party that can
 * talk to the set; none of them is fast enough to drive a control that
 * expects an answer, so callers move their own display at once and let the
 * poll correct it.
 */
export class Cast {
  constructor(
    readonly renderer: RendererInfo,
    readonly item: string,
  ) {}

  /**
   * Start it, at t seconds — a resume point, or where the viewer was.
   *
   * sub is which sidecar subtitle to send with it, or 'off'. A set draws one
   * or none and has no menu to change it from, so the choice is made here,
   * from what the viewer already chose in the player.
   */
  async start(t: number, sub?: string, audio?: number): Promise<CastStatus | null> {
    const q = new URLSearchParams();
    if (t > 1) q.set('t', t.toFixed(0));
    if (sub) q.set('sub', sub);
    // undefined means "say nothing": the set takes the file as it is. Zero
    // is a real choice — track 0 over a non-zero default — and the guard
    // that treated it as absence dropped exactly the language asked for.
    if (audio !== undefined) q.set('audio', String(audio));
    const query = q.toString();
    const res = await fetch(
      `/api/renderers/${this.renderer.id}/play/${this.item}${query ? `?${query}` : ''}`,
      { method: 'POST' },
    );
    if (!res.ok) throw new Error(await res.text().catch(() => res.statusText));
    return (await res.json().catch(() => null)) as CastStatus | null;
  }

  /**
   * Tell the set what follows this, so the boundary costs nothing: it has
   * the next file open before this one ends. Optional in the protocol —
   * `null` back means this renderer will not, and the caller goes on sending
   * each track when it sees the last one finish.
   */
  async queueNext(item: string, audio?: number): Promise<string | null> {
    // undefined is "say nothing", as in start: zero is a real choice.
    const q = audio !== undefined ? `?audio=${audio}` : '';
    const res = await fetch(`/api/renderers/${this.renderer.id}/next/${item}${q}`, {
      method: 'POST',
    }).catch(() => null);
    if (!res || !res.ok) return null;
    const st = (await res.json().catch(() => null)) as CastStatus | null;
    return st?.uri ?? null;
  }

  private control(body: CastControl): Promise<Response> {
    return fetch(`/api/renderers/${this.renderer.id}/control`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  }

  play(): void {
    void this.control({ action: 'play' }).catch(() => {});
  }

  pause(): void {
    void this.control({ action: 'pause' }).catch(() => {});
  }

  stop(): void {
    void this.control({ action: 'stop' }).catch(() => {});
  }

  seek(seconds: number): void {
    void this.control({ action: 'seek', seconds }).catch(() => {});
  }

  /** 0..100, where the set has a volume of its own to set. */
  volume(volume: number): void {
    if (!this.renderer.volume) return;
    void this.control({ action: 'volume', volume: Math.round(volume) }).catch(() => {});
  }

  /** Where it says it has got to, or null when it cannot be reached. */
  async status(): Promise<CastStatus | null> {
    const res = await fetch(`/api/renderers/${this.renderer.id}`).catch(() => null);
    if (!res || !res.ok) return null;
    return (await res.json().catch(() => null)) as CastStatus | null;
  }
}

/**
 * Fill a receiver menu: the renderers on the network, "play here instead"
 * while one of them holds the sound, and the browser's own picker where it
 * has one. The player and the music bar offer the same choice from the same
 * button, so the markup and the wiring live here once — the two copies had
 * already started to drift in how they marked the active set.
 */
export function fillReceiverMenu(
  menu: HTMLElement,
  found: RendererInfo[],
  o: {
    /** Offer "Play here instead": something is on a television now. */
    here: boolean;
    /** The renderer currently playing, to mark in the list. */
    currentId: string | null;
    /** Offer the browser's own picker as a final entry. */
    picker: boolean;
    onPick: (r: RendererInfo) => void;
    onHere: () => void;
    onPicker: () => void;
  },
): void {
  const here = o.here ? `<button class="vo-menu-item" data-here>Play here instead</button>` : '';
  menu.innerHTML =
    here +
    found
      .map((r) => `<button class="vo-menu-item" data-rid="${esc(r.id)}">${esc(r.name)}</button>`)
      .join('') +
    // The browser's own picker, where it has one, is one more entry rather
    // than a second button: they are the same intention.
    (o.picker ? `<button class="vo-menu-item" data-picker>Other devices…</button>` : '');
  for (const el of menu.querySelectorAll<HTMLElement>('[data-rid]')) {
    const rid = el.dataset.rid ?? '';
    el.classList.toggle('on', rid === o.currentId);
    el.addEventListener('click', () => {
      const target = knownRenderers().find((r) => r.id === rid);
      if (target) o.onPick(target);
    });
  }
  menu.querySelector('[data-here]')?.addEventListener('click', o.onHere);
  menu.querySelector('[data-picker]')?.addEventListener('click', o.onPicker);
}
