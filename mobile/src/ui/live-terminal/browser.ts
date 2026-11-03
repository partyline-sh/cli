// Built into a local-only WebView document by scripts/build-live-terminal.mjs.
// No network, storage, clipboard-read or navigation privileges are needed.
import { Terminal } from '@xterm/xterm';
import type { LiveScreen } from '../../runtime/live-terminal';
import { EchoPrediction } from './prediction';

declare global { interface Window { ReactNativeWebView?: { postMessage(message: string): void }; liveReceive: (message: unknown) => void } }
const send = (message: unknown) => window.ReactNativeWebView?.postMessage(JSON.stringify(message));
const options = { cols: 80, rows: 24, scrollback: 0, fontSize: 14, fontFamily: 'Menlo, monospace', convertEol: true, cursorBlink: true, theme: { background: '#0c1016', foreground: '#e5e7eb', cursor: '#ff9b55' } };
const terminal = new Terminal(options), authority = new Terminal({ ...options, disableStdin: true });
terminal.open(document.getElementById('terminal')!);
terminal.options.disableStdin = true;
terminal.textarea?.setAttribute('autocorrect', 'off');
terminal.textarea?.setAttribute('autocapitalize', 'off');
terminal.textarea?.setAttribute('spellcheck', 'false');
const prediction = new EchoPrediction();
let generation = 0;
let sequence = 0, ready = false, composing = false, lastRevision = 0;
let screen: LiveScreen | undefined;
let batch = '', batchTimer: ReturnType<typeof setTimeout> | undefined;
let lastViewport = '';
let rendering = false, redraw = false;
let parsing = false;
let queued: { screen: LiveScreen; revision: number } | undefined;
function render() {
  if (!screen) return;
  if (rendering) { redraw = true; return; }
  rendering = true;
  const current = screen;
  const guess = prediction.overlay();
  const overlay = guess ? `\x1b[${guess.y + 1};${guess.start + 1}H\x1b[4m${guess.text.padEnd(guess.end - guess.start, ' ')}\x1b[0m\x1b[${guess.y + 1};${guess.x + 1}H` : `\x1b[${current.y + 1};${current.x + 1}H`;
  terminal.resize(current.columns, current.rows);
  terminal.reset();
  terminal.write(`\x1b[?25l\x1b[H${current.ansi.replace(/\n/g, '\r\n')}${overlay}${current.cursor ? '\x1b[?25h' : '\x1b[?25l'}`, () => {
    rendering = false;
    if (redraw) { redraw = false; render(); }
    requestAnimationFrame(fit);
  });
}
function fit() {
  // Scale the complete authoritative screen without resizing the desktop window.
  const element = terminal.element;
  if (!element) return;
  const width = element.querySelector('.xterm-screen')?.getBoundingClientRect().width ?? 0;
  element.style.transform = '';
  const natural = (element.querySelector('.xterm-screen') as HTMLElement | null)?.offsetWidth ?? width;
  const scale = natural ? Math.min(1, document.documentElement.clientWidth / natural) : 1;
  element.style.transformOrigin = 'top left'; element.style.transform = `scale(${scale})`;
  const grid = element.querySelector('.xterm-screen') as HTMLElement | null;
  const cell = natural / terminal.cols || (terminal.options.fontSize ?? 14) * .6;
  const line = (grid?.offsetHeight ?? 0) / terminal.rows || (terminal.options.fontSize ?? 14) * 1.2;
  const viewport = { type: 'viewport', columns: Math.max(20, Math.min(300, Math.floor(document.documentElement.clientWidth / cell))), rows: Math.max(8, Math.min(200, Math.floor(document.documentElement.clientHeight / line))) };
  const signature = JSON.stringify(viewport);
  if (signature !== lastViewport) { lastViewport = signature; send(viewport); }
}
function flush() {
  clearTimeout(batchTimer); batchTimer = undefined;
  if (!batch) return;
  const data = batch; batch = '';
  send({ type: 'input', seq: ++sequence, kind: 'text', data });
}
const keys: Record<string, string> = { '\r': 'Enter', '\t': 'Tab', '\x7f': 'BSpace', '\x08': 'BSpace', '\x1b': 'Escape', '\x1b[A': 'Up', '\x1b[B': 'Down', '\x1b[C': 'Right', '\x1b[D': 'Left', '\x1b[H': 'Home', '\x1b[F': 'End', '\x1b[3~': 'DC', '\x1b[5~': 'PageUp', '\x1b[6~': 'PageDown', '\x1b[Z': 'BTab' };
for (const letter of 'abcdefklnpruwz') keys[String.fromCharCode(letter.charCodeAt(0) - 96)] = `C-${letter}`;
function key(name: string) {
  if (!ready || !screen || !screen.typing && name !== 'C-c' && name !== 'C-z') return;
  flush();
  const seq = ++sequence;
  prediction.input(seq, 'key', name);
  send({ type: 'input', seq, kind: 'key', data: name });
  render();
}
terminal.onData(data => {
  if (!ready || !screen) return;
  if (keys[data]) { key(keys[data]); return; }
  if (!screen.typing) return;
  if (/[\x00-\x1f\x7f-\x9f]/.test(data)) { prediction.reset(sequence); send({ type: 'notice', text: 'This terminal key is not supported yet.' }); return; }
  if (new TextEncoder().encode(batch + data).length > 1900) flush();
  if (new TextEncoder().encode(data).length > 1900) { send({ type: 'notice', text: 'Paste using the terminal paste action.' }); return; }
  batch += data;
  if (composing) prediction.reset(sequence + 1); else prediction.input(sequence + 1, 'text', data);
  if (!batchTimer) batchTimer = setTimeout(flush, 100);
  render();
});
terminal.textarea?.addEventListener('compositionstart', () => { composing = true; prediction.reset(sequence); });
terminal.textarea?.addEventListener('compositionend', () => { composing = false; });
document.addEventListener('paste', event => {
  event.preventDefault(); event.stopImmediatePropagation();
  if (!ready) return;
  const text = event.clipboardData?.getData('text/plain') ?? '';
  send({ type: 'paste', text });
}, true);
window.liveReceive = (value: unknown) => {
  const message = value as { type: string; screen?: LiveScreen; revision?: number; key?: string; text?: string; font?: number };
  if (message.type === 'size' && [10, 11.5, 13, 15].includes(message.font!)) { terminal.options.fontSize = message.font; requestAnimationFrame(fit); }
  if (message.type === 'ready') { generation++; queued = undefined; sequence = 0; lastRevision = 0; batch = ''; clearTimeout(batchTimer); prediction.clear(); ready = true; terminal.options.disableStdin = true; }
  if (message.type === 'closed') { generation++; queued = undefined; ready = false; terminal.options.disableStdin = true; batch = ''; clearTimeout(batchTimer); prediction.reset(sequence); render(); }
  if (message.type === 'key' && message.key) key(message.key);
  if (message.type === 'paste' && message.text && ready) {
    flush(); prediction.reset(sequence + 1);
    send({ type: 'input', seq: ++sequence, kind: 'paste', data: message.text });
  }
  if (message.type === 'focus') terminal.focus();
  if (message.type === 'screen' && message.screen && message.revision! > lastRevision) {
    lastRevision = message.revision!;
    queued = { screen: message.screen, revision: lastRevision };
    parseNext();
  }
};
window.addEventListener('resize', fit);
document.getElementById('terminal')!.addEventListener('click', () => terminal.focus());
send({ type: 'loaded' });

function parseNext() {
  if (parsing || !queued) return;
  const { screen: current, revision } = queued;
  const parsedGeneration = generation;
  queued = undefined; parsing = true;
  authority.resize(current.columns, current.rows); authority.reset();
  authority.write(current.ansi.replace(/\n/g, '\r\n'), () => {
    parsing = false;
    if (parsedGeneration === generation && revision === lastRevision) {
      screen = current;
      terminal.options.disableStdin = !ready || !current.typing;
      const lines = Array.from({ length: current.rows }, (_, row) => authority.buffer.active.getLine(row)?.translateToString(false) ?? '');
      const cells = Array.from({ length: current.rows }, (_, row) => Array.from({ length: current.columns }, (_, col) => authority.buffer.active.getLine(row)?.getCell(col)?.getChars() || ' '));
      prediction.reconcile({ ...current, lines, cells }); render();
    }
    parseNext();
  });
}
