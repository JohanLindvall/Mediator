/**
 * AirPlay, where the browser offers it.
 *
 * Safari hands a media element to a receiver through its own route picker,
 * which a page with custom controls has to raise itself — the button in
 * Safari's default controls is not there when the controls are ours. The API
 * is WebKit's alone and prefixed, so this is all feature detection: on
 * anything else the button never appears.
 *
 * Two things are worth knowing about what it can and cannot carry.
 *
 * The picker offers targets for *this element*, and an element that has been
 * routed into a Web Audio graph no longer has a route of its own to give —
 * opening the spectrum takes the decks into the graph for the life of the
 * page, and AirPlay of those decks stops working until it is reloaded. That
 * is the price of drawing the sound, and it is why the button is offered on
 * the element rather than on the app.
 *
 * And the receiver fetches the media itself, over the internet, from
 * whatever address the page was loaded from. A television that cannot reach
 * that address — or cannot agree with its certificate, which older ones
 * often cannot — shows the file's name and a spinner and gets no further.
 */

/**
 * The other half of this: Chrome's Remote Playback API, which is the same
 * idea written down as a standard. It offers the receivers Chrome knows
 * about — a Chromecast, a speaker group, a television with Cast built in —
 * and, like AirPlay, hands over the URL rather than the pixels, so a signed
 * one is what makes it work behind a password.
 *
 * A television that is only *discovered* by Chrome is not the same as one it
 * can cast to: an LG with AirPlay is found over DIAL and Chrome will say
 * "available for specific video sites", meaning it can launch YouTube there
 * and nothing else. Nothing in a page can change that; the way to such a set
 * from a desktop is AirPlay from an Apple device, or DLNA, which is not the
 * browser's to do.
 */
/**
 * What Chrome's Remote Playback offers on an element. Declared here rather
 * than taken from the DOM library because the library has it as always
 * present, and the whole point is asking whether it is.
 */
interface RemoteTarget extends EventTarget {
  watchAvailability(cb: (available: boolean) => void): Promise<number>;
  cancelWatchAvailability(id?: number): Promise<void>;
  prompt(): Promise<void>;
  state: string;
}

function remoteOf(el: HTMLMediaElement): RemoteTarget | undefined {
  return (el as unknown as { remote?: RemoteTarget }).remote;
}

interface AirPlayElement extends HTMLMediaElement {
  webkitShowPlaybackTargetPicker?: () => void;
  webkitCurrentPlaybackTargetIsWireless?: boolean;
}

/** Whether this browser can hand media to a receiver at all. */
export function airPlaySupported(el: HTMLMediaElement): boolean {
  return typeof (el as AirPlayElement).webkitShowPlaybackTargetPicker === 'function';
}

/** Whether it can do it the standard way instead. */
export function remotePlaybackSupported(el: HTMLMediaElement): boolean {
  return typeof remoteOf(el)?.prompt === 'function';
}

/**
 * Say when this element connects to or leaves a receiver, by whichever route
 * the browser has. What is decoded there is not what the browser decodes, so
 * a caller uses this to reconsider the file it is sending.
 */
export function watchRemoteState(el: HTMLMediaElement, onChange: () => void): void {
  el.addEventListener('webkitcurrentplaybacktargetiswirelesschanged', onChange);
  const remote = remoteOf(el);
  if (!remote) return;
  remote.addEventListener('connect', onChange);
  remote.addEventListener('disconnect', onChange);
}

/**
 * Watch for receivers and say when the answer changes.
 *
 * WebKit only tells a page whether *any* target exists, and only after it
 * has looked — so a button shown before the first event would be a button
 * that does nothing on a network with no Apple TV in it.
 */
export function watchAirPlay(
  el: HTMLMediaElement,
  onChange: (available: boolean) => void,
): () => void {
  if (airPlaySupported(el)) {
    const onAvailability = (ev: Event): void => {
      onChange((ev as Event & { availability?: string }).availability === 'available');
    };
    el.addEventListener('webkitplaybacktargetavailabilitychanged', onAvailability);
    return () => el.removeEventListener('webkitplaybacktargetavailabilitychanged', onAvailability);
  }
  // Chrome's version of the same question, and it is not quite the same
  // question: it answers for **this element's current media**, so a watch
  // armed on a player's empty element is answered about nothing and never
  // asked again. Both viewers build their element long before they are given
  // a file, so the watch is armed again whenever one arrives — and the
  // previous one cancelled, or switching files would leave a watcher behind
  // per file played.
  const remote = remoteOf(el);
  if (!remote) return () => {};
  let watch: number | undefined;
  const arm = () => {
    if (watch !== undefined) void remote.cancelWatchAvailability(watch).catch(() => {});
    watch = undefined;
    remote.watchAvailability(onChange).then(
      (id) => {
        watch = id;
      },
      // A browser that will not answer the question has not answered it "no".
      // Chrome rejects this where it cannot monitor, and hiding the button
      // then leaves the picker — which does know — unreachable. So the
      // button is offered, and the browser's own dialog is the authority on
      // what is out there.
      () => onChange(true),
    );
  };
  arm();
  el.addEventListener('loadedmetadata', arm);
  // Returned so a viewer can put the watch down when it closes, as it does
  // every other listener it armed: the element goes with the viewer, but a
  // watch left armed on it is still a watch.
  return () => {
    el.removeEventListener('loadedmetadata', arm);
    if (watch !== undefined) void remote.cancelWatchAvailability(watch).catch(() => {});
    watch = undefined;
  };
}

/** Raise the picker for this element, whichever kind the browser has. */
export function showAirPlayPicker(el: HTMLMediaElement): void {
  const airplay = el as AirPlayElement;
  if (typeof airplay.webkitShowPlaybackTargetPicker === 'function') {
    airplay.webkitShowPlaybackTargetPicker();
    return;
  }
  void remoteOf(el)?.prompt().catch(() => {
    // Dismissed, or nothing to connect to: neither is worth saying.
  });
}

/** Whether this element is currently playing to a receiver. */
export function playingRemotely(el: HTMLMediaElement): boolean {
  if ((el as AirPlayElement).webkitCurrentPlaybackTargetIsWireless === true) return true;
  return remoteOf(el)?.state === 'connected';
}
