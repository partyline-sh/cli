// A pane snapshot is tmux's capture of the screen: text with SGR codes. This splits it into styled
// spans. Colours, backgrounds, bold, dim and inverse are kept (agent menus mark the selected row with
// a background or inverse video); every other escape is dropped, never printed.

export type Span = { text: string; fg?: string; bg?: string; bold?: boolean; dim?: boolean };
type Style = { fg?: string; bg?: string; bold?: boolean; dim?: boolean; inverse?: boolean };

const basic = ['#1b1a19', '#f87171', '#4ade80', '#fbbf24', '#60a5fa', '#c084fc', '#22d3ee', '#d6d3cf'];
const bright = ['#78716c', '#fca5a5', '#86efac', '#fde68a', '#93c5fd', '#d8b4fe', '#67e8f9', '#f5f3f0'];

function xterm256(n: number): string {
  if (n < 8) return basic[n];
  if (n < 16) return bright[n - 8];
  if (n >= 232) { const v = 8 + (n - 232) * 10; return `rgb(${v},${v},${v})`; }
  const i = n - 16, steps = [0, 95, 135, 175, 215, 255];
  return `rgb(${steps[Math.floor(i / 36)]},${steps[Math.floor(i / 6) % 6]},${steps[i % 6]})`;
}

/** An extended colour (38/48;5;n or 38/48;2;r;g;b) starting at p[i]; returns it and how many params it used. */
function extended(p: number[], i: number): [string | undefined, number] {
  if (p[i + 1] === 5) return [xterm256(p[i + 2] ?? 7), 2];
  if (p[i + 1] === 2) return [`rgb(${p[i + 2] ?? 0},${p[i + 3] ?? 0},${p[i + 4] ?? 0})`, 4];
  return [undefined, 0];
}

/** Applies one SGR parameter list to a style. */
function apply(cur: Style, p: number[]): Style {
  let s = { ...cur };
  for (let i = 0; i < p.length; i++) {
    const v = p[i];
    if (v === 0) s = {};
    else if (v === 1) s.bold = true;
    else if (v === 2) s.dim = true;
    else if (v === 22) { s.bold = false; s.dim = false; }
    else if (v === 7) s.inverse = true;
    else if (v === 27) s.inverse = false;
    else if (v >= 30 && v <= 37) s.fg = basic[v - 30];
    else if (v >= 90 && v <= 97) s.fg = bright[v - 90];
    else if (v === 39) s.fg = undefined;
    else if (v >= 40 && v <= 47) s.bg = basic[v - 40];
    else if (v >= 100 && v <= 107) s.bg = bright[v - 100];
    else if (v === 49) s.bg = undefined;
    else if (v === 38 || v === 48) { const [colour, used] = extended(p, i); if (v === 38) s.fg = colour; else s.bg = colour; i += used; }
    else if (v === 58) i += extended(p, i)[1]; // underline colour: not drawn
  }
  return s;
}

/** Spans of the snapshot. Inverse swaps foreground and background; a default colour stays undefined
 * on the side it lands, and `defaults` fills it so an inverted default is still visible. */
export function ansiSpans(ansi: string, defaults = { fg: '#d6d3cf', bg: '#12110f' }): Span[] {
  const out: Span[] = [];
  let cur: Style = {};
  const push = (text: string) => {
    if (!text) return;
    const { fg, bg, bold, dim, inverse } = cur;
    out.push(inverse ? { text, fg: bg ?? defaults.bg, bg: fg ?? defaults.fg, bold, dim } : { text, fg, bg, bold, dim });
  };
  const text = ansi.replace(/\r\n?/g, '\n');
  const re = /\x1b\[([0-9;:]*)m|\x1b\[[0-9;?<=>]*[ -/]*[@-ln-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()*+][0-9A-Za-z]|\x1b[@-Z\\-_]|[\x00-\x08\x0b-\x1f\x7f]/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    push(text.slice(last, m.index));
    last = re.lastIndex;
    if (m[1] !== undefined) cur = apply(cur, m[1] === '' ? [0] : m[1].split(/[;:]/).map(Number));
  }
  push(text.slice(last));
  return out;
}
