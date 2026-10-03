/**
 * The music bar's media elements, kept separate from its controls so source
 * changes and play/pause races can be exercised without a browser UI.
 */
export class AudioDecks {
  readonly elements: readonly [HTMLAudioElement, HTMLAudioElement];
  readonly reuse: boolean;
  private index = 0;
  private urls = ['', ''];
  private attempt = 0;

  constructor(elements: readonly [HTMLAudioElement, HTMLAudioElement], reuse: boolean) {
    this.elements = elements;
    this.reuse = reuse;
  }

  get current(): HTMLAudioElement {
    return this.elements[this.index]!;
  }

  get idle(): HTMLAudioElement {
    return this.elements[1 - this.index]!;
  }

  /**
   * iOS keeps the element the listener actually started. Playing an empty
   * spare and immediately pausing it is not proof that a later background
   * play will be allowed, and can disturb the active audio session.
   * Other browsers retain the prebuffered, alternating decks.
   */
  load(url: string, autoplay: boolean): Promise<void> {
    this.attempt++;
    const previous = this.current;
    if (this.urls[this.index] !== url) {
      if (this.reuse) this.setSource(this.current, url);
      else {
        this.preload(url);
        this.index = 1 - this.index;
      }
    }
    this.current.loop = true;
    if (previous !== this.current) previous.loop = false;
    if (this.current.currentTime > 0.01) this.current.currentTime = 0;

    // No await, load(), or pause() between replacing the iOS source and
    // requesting playback. This remains in the tap/media-event callback.
    const started = autoplay ? this.play() : Promise.resolve();
    if (!autoplay) this.pause();
    if (previous !== this.current) this.stop(previous);
    return started;
  }

  play(): Promise<void> {
    const attempt = ++this.attempt;
    return this.current.play().catch((error: unknown) => {
      // A late rejection for a skipped track, a pause, or a closed queue
      // must not report a refusal against the track now on the screen.
      if (attempt === this.attempt) throw error;
    });
  }

  pause(): void {
    this.attempt++;
    this.current.pause();
  }

  preload(url: string): void {
    if (this.reuse || this.urls[1 - this.index] === url) return;
    this.setSource(this.idle, url);
  }

  private setSource(element: HTMLAudioElement, url: string): void {
    element.preload = 'auto';
    element.src = url;
    this.urls[this.elements.indexOf(element)] = url;
  }

  stop(element: HTMLAudioElement): void {
    if (element === this.current) this.pause();
    else element.pause();
    this.clear(element);
  }

  clear(element: HTMLAudioElement): void {
    if (element === this.current) this.attempt++;
    if (element.getAttribute('src') !== null) {
      element.removeAttribute('src');
      element.load();
    }
    this.urls[this.elements.indexOf(element)] = '';
  }
}
