import { describe, expect, it } from 'vitest';
import { ansiSpans } from '../src/ui/ansi';

describe('captured terminal readability', () => {
  it('keeps black-on-white selected menu rows visible and resets the following text', () => {
    const [selected, normal] = ansiSpans('\x1b[30;47m Selected option \x1b[0m Normal');
    expect(selected).toMatchObject({ text: ' Selected option ', fg: '#1b1a19', bg: '#d6d3cf' });
    expect(normal).toMatchObject({ text: ' Normal', fg: undefined, bg: undefined });
  });
  it('inverts defaults so inverse-video selection is visible without explicit colours', () => {
    const [selected, normal] = ansiSpans('\x1b[7mYes\x1b[27m No');
    expect(selected).toMatchObject({ fg: '#12110f', bg: '#d6d3cf' });
    expect(normal.fg).toBeUndefined(); expect(normal.bg).toBeUndefined();
  });
  it('swaps and restores explicit foreground and background for inverse menus', () => {
    const [normal, selected, restored] = ansiSpans('\x1b[31;44mA\x1b[7mB\x1b[27mC');
    expect(selected.fg).toBe(normal.bg); expect(selected.bg).toBe(normal.fg);
    expect(restored.fg).toBe(normal.fg); expect(restored.bg).toBe(normal.bg);
  });
  it('preserves indexed and true-colour backgrounds and their reset', () => {
    const [indexed, rgb, reset] = ansiSpans('\x1b[48;5;196mA\x1b[48;2;12;34;56mB\x1b[49mC');
    expect(indexed.bg).toBe('rgb(255,0,0)'); expect(rgb.bg).toBe('rgb(12,34,56)'); expect(reset.bg).toBeUndefined();
  });
  it('drops OSC hyperlinks and cursor controls while retaining the visible label', () => {
    const rendered = ansiSpans('\x1b[2J\x1b[H\x1b]8;;javascript:unexpected()\x1b\\Visible label\x1b]8;;\x1b\\\x1b[?25l').map(s => s.text).join('');
    expect(rendered).toBe('Visible label');
  });
  it('preserves Unicode, column spaces, and line breaks in captured rows', () => {
    const text = '店  📦   ready\r\nnext    column';
    expect(ansiSpans(text).map(s => s.text).join('')).toBe('店  📦   ready\nnext    column');
  });
});
