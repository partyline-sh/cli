export interface TerminalViewport { columns: number; rows: number }
// capture-pane terminates its final row with LF; Text would turn that delimiter
// into an extra blank row. Keep every actual blank screen row, removing just one LF.
export const terminalSnapshotText = (text: string) => text.endsWith('\n') ? text.slice(0, -1) : text;
export function phoneTerminalViewport(width: number, height: number, cellWidth: number, lineHeight: number): TerminalViewport | undefined {
  if (![width, height, cellWidth, lineHeight].every(n => Number.isFinite(n) && n > 0)) return undefined;
  return {
    columns: Math.max(20, Math.min(300, Math.floor((width - 24) / cellWidth))),
    rows: Math.max(5, Math.min(200, Math.floor((height - 28) / lineHeight))),
  };
}
export function fitTerminalFont(width: number, columns: number | undefined, preferredFont: number, cellWidth: number): number {
  if (!columns || columns < 1 || width <= 24 || cellWidth <= 0) return preferredFont;
  return Math.min(preferredFont, (width - 24) / (columns * cellWidth) * preferredFont);
}

/** Wrap ordinary shell output locally; keep app screens and box layouts exact. */
export function defaultTerminalDisplay(engine: string | undefined, ansi: string): 'wrap' | 'grid' {
  const plainShell = !engine || ['shell', 'sh', 'bash', 'zsh', 'fish', 'pwsh', 'powershell'].includes(engine.toLowerCase());
  return plainShell && !/[\u2500-\u257f]/.test(ansi) ? 'wrap' : 'grid';
}
