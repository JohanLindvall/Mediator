/**
 * What the browser tab says.
 *
 * A library open in one tab of several looks like every other tab until it
 * says what it is doing, and what it is doing is nearly always one file. So
 * the tab carries it: the video the player has open, or failing that the
 * track the bottom bar is on, and the name of the app when there is neither.
 *
 * The player wins over the bar because it is in front of everything, and
 * because closing it hands the tab straight back to whatever the bar is
 * still holding — which is the truth again the moment the overlay is gone.
 */

/** The app's own title, as index.html spells it. Read once, and lazily, so
 * this module can be imported somewhere without a document. */
let base = '';

function appTitle(): string {
  if (!base) base = document.title || 'Mediator';
  return base;
}

/** Compose the tab's title. Pure, and the whole rule. */
export function nowPlayingTitle(
  video: string | null,
  audio: string | null,
  app: string,
): string {
  const playing = video ?? audio;
  return playing ? `${playing} — ${app}` : app;
}

let video: string | null = null;
let audio: string | null = null;

function render(): void {
  document.title = nowPlayingTitle(video, audio, appTitle());
}

/** The player opened a file, or closed (null). */
export function playingVideo(name: string | null): void {
  video = name;
  render();
}

/** The bottom bar loaded a track, or was closed (null). */
export function playingAudio(name: string | null): void {
  audio = name;
  render();
}
