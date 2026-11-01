import type { LiveEvent, LiveHandle, LiveScreen } from '../runtime/live-terminal';
/** Debug-only synthetic echo for native rendering; never connects to a shell. */
export function openPreviewTerminal(receive: (event: LiveEvent) => void): LiveHandle {
  let active = true, revision = 0, text = '';
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const later = (callback: () => void, delay: number) => { const timer = setTimeout(() => { timers.delete(timer); if (active) callback(); }, delay); timers.add(timer); };
  const screen = (sequence: number): LiveScreen => ({ typing: true, foreground: 'zsh', ansi: '\x1b[38;5;208mPARTYLINE\x1b[0m · direct terminal\n\n$ ls\nprojects/  notes/  README.md\n\n\x1b[38;5;205m~/projects\x1b[0m $ ' + text, columns: 44, rows: 24, x: 13 + text.length, y: 5, cursor: true, alternate: false, copy: false, acceptedSeq: sequence, echoSeq: sequence });
  later(() => { receive({ type: 'ready' }); receive({ type: 'screen', screen: screen(0), revision: ++revision }); }, 50);
  return { send(sequence, kind, data) { if (!active) return; if (kind === 'text') text += data; else if (kind === 'key' && data === 'BSpace') text = text.slice(0, -1); else if (kind === 'key' && data === 'Enter') text = ''; later(() => receive({ type: 'screen', screen: screen(sequence), revision: ++revision }), 350); }, close() { active = false; for (const timer of timers) clearTimeout(timer); timers.clear(); } };
}
