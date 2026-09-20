/**
 * What went wrong here, said where the rest of it is written down.
 *
 * Half of what this app does happens where the server cannot see it: which
 * route a film took, whether a conversion's connection came apart and what
 * the browser said about it, a fetch that failed on the way, an error the
 * page itself raised. The server's log has the other half — every request,
 * every conversion, every probe, with timestamps — and until now the two
 * could only be put together by asking somebody to open a console and read
 * it out.
 *
 * So the page posts its faults to `/api/log` and they land in that same log.
 * Nothing waits on it: a report is sent and forgotten, `keepalive` so it
 * survives the page being closed on the way, and a failure to report is
 * never itself reported — which is what keeps one broken thing from
 * becoming a loop of reports about it.
 */
import type { ClientFault } from './types.gen';

/**
 * How many faults one page load reports before it goes quiet.
 *
 * A page in trouble has a handful of things to say; a page saying more than
 * this is in a loop, and the log is no place for it. The server bounds this
 * as well, per process — this is the courtesy, that is the guarantee.
 */
export const REPORT_CAP = 40;

/**
 * How long the same fault stays uninteresting. A conversion that reconnects
 * every few seconds is one fault, not fifty; the first is worth a line and
 * the rest are worth a count, which the server's own dropped tally gives.
 */
export const REPORT_QUIET_MS = 30_000;

/**
 * Whether to send this one, given what has been sent already: not past the
 * cap, and not the same thing again within the quiet window.
 *
 * Pure, and tested, because a report that floods is worse than no report at
 * all — it fills the log that the reports exist to make readable.
 */
export function shouldReport(
  now: number,
  sent: number,
  lastSaid: number | undefined,
): boolean {
  if (sent >= REPORT_CAP) return false;
  return lastSaid === undefined || now - lastSaid >= REPORT_QUIET_MS;
}

const said = new Map<string, number>();
let sent = 0;

/** Send one fault, unless it would be noise. */
export function reportFault(fault: ClientFault): void {
  const key = `${fault.what}|${fault.detail ?? ''}|${fault.status ?? ''}`;
  const now = Date.now();
  if (!shouldReport(now, sent, said.get(key))) return;
  said.set(key, now);
  sent++;
  try {
    void fetch('/api/log', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(fault),
      keepalive: true,
    }).catch(() => {
      // A report that cannot be sent is not a second fault. Whatever is
      // wrong with the connection is what the report was about.
    });
  } catch {
    // Same.
  }
}

/**
 * Report what the page itself raises: an uncaught error, and a promise
 * nobody caught. Installed once, from main.
 *
 * These are the ones nothing else can catch — a fault in a handler that
 * leaves the interface half-drawn used to exist only in a console nobody
 * had open.
 */
export function reportPageFaults(): void {
  window.addEventListener('error', (ev) => {
    // A failed <img> or <video> load raises an error event on the element
    // and bubbles here with no message. Those are ordinary — a thumbnail
    // that is not made yet — and the player says what it makes of its own.
    if (!ev.message) return;
    reportFault({
      what: 'script',
      detail: ev.message,
      where: `${shortSource(ev.filename)}:${ev.lineno}:${ev.colno}`,
    });
  });
  window.addEventListener('unhandledrejection', (ev) => {
    reportFault({ what: 'script', detail: `unhandled rejection: ${String(ev.reason)}` });
  });
}

/** The file a script error came from, without the origin in front of it. */
function shortSource(url: string): string {
  if (!url) return '';
  try {
    return new URL(url, location.href).pathname;
  } catch {
    return url;
  }
}
