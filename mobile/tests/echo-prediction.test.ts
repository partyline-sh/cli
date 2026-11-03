import { expect, it } from 'vitest';
import { EchoPrediction, type EchoFrame } from '../src/ui/live-terminal/prediction';
const frame = (line = '$ ', seq = 0, echo = 0, x = line.length): EchoFrame => ({ lines: [line, ''], cells: [Array.from(line), []], x, y: 0, columns: 80, cursor: true, typing: true, alternate: false, copy: false, acceptedSeq: seq, echoSeq: echo });
it('hides a fresh epoch until remote echo proves it and avoids double echo', () => {
  const p = new EchoPrediction(); p.reconcile(frame());
  p.input(1, 'text', 'a'); p.input(2, 'text', 'b');
  expect(p.overlay()).toBeUndefined();
  p.reconcile(frame('$ a', 2));
  expect(p.overlay()).toMatchObject({ text: 'ab', start: 2, x: 4 });
  p.reconcile(frame('$ ab', 2)); expect(p.overlay()).toBeUndefined();
  p.input(3, 'text', 'c'); expect(p.overlay()).toMatchObject({ text: 'c', start: 4 });
});
it('does not reject on accepted writes before the server echo eligibility window', () => {
  const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a');
  p.reconcile(frame('$ a', 1)); p.input(2, 'text', 'b');
  p.reconcile(frame('$ a', 2, 0)); expect(p.overlay()?.text).toBe('b');
  p.reconcile(frame('$ a', 2, 2)); expect(p.overlay()).toBeUndefined();
  p.input(3, 'text', 'secret'); expect(p.overlay()).toBeUndefined();
});
it('never predicts a fresh non-echoing password epoch or newlines', () => {
  const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a'); p.reconcile(frame('$ a', 1, 1));
  p.input(2, 'key', 'Enter'); expect(p.overlay()).toBeUndefined();
  p.input(3, 'text', 'secret'); expect(p.overlay()).toBeUndefined();
  p.reconcile(frame('Password: ', 3, 3)); p.input(4, 'text', 'more'); expect(p.overlay()).toBeUndefined();
});
it('supports spaces and speculative backspace at a proven end of line', () => {
  const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a'); p.reconcile(frame('$ a', 1));
  p.input(2, 'text', ' b'); expect(p.overlay()?.text).toBe(' b');
  p.input(3, 'key', 'BSpace'); expect(p.overlay()).toMatchObject({ text: ' ', x: 4 });
});
it('clears predictions for cursor moves, alternate screens, copy mode, unicode and wrapping', () => {
  for (const special of ['Left', 'Enter', 'Tab', 'C-c']) {
    const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a'); p.reconcile(frame('$ a', 1));
    p.input(2, 'key', special); p.input(3, 'text', 'b'); expect(p.overlay()).toBeUndefined();
  }
  for (const mode of [{ alternate: true }, { copy: true }, { cursor: false }, { typing: false }]) {
    const p = new EchoPrediction(); p.reconcile({ ...frame(), ...mode }); p.input(1, 'text', 'a'); expect(p.overlay()).toBeUndefined();
  }
  const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a'); p.reconcile(frame('$ a', 1)); p.input(2, 'text', 'é'); expect(p.overlay()).toBeUndefined();
  p.reconcile({ ...frame('$ a', 2, 2), columns: 5 }); p.input(3, 'text', 'xx'); expect(p.overlay()).toBeUndefined();
});
it('a desktop edit disables confidence instead of retaining a stale local line', () => {
  const p = new EchoPrediction(); p.reconcile(frame()); p.input(1, 'text', 'a'); p.reconcile(frame('$ a', 1));
  p.reconcile(frame('$ desktop', 1, 1)); p.input(2, 'text', 'x'); expect(p.overlay()).toBeUndefined();
});

it('uses terminal cells for wide prompt glyphs and ignores right prompts and autosuggestions', () => {
  const p = new EchoPrediction();
  const prompt = ['界', '', '\ue0b0', ' ', '$', ' '];
  const rich = (typed: string, seq: number): EchoFrame => ({ ...frame('', seq), lines: ['界\ue0b0 $ ' + typed + 'suggestion       12:34', ''], cells: [[...prompt, ...Array.from(typed), ...Array.from('suggestion       12:34')], []], x: prompt.length + typed.length });
  p.reconcile(rich('', 0)); p.input(1, 'text', 'a');
  p.reconcile(rich('a', 1)); p.input(2, 'text', 'b');
  expect(p.overlay()).toMatchObject({ start: 7, x: 8, text: 'b' });
  p.reconcile(rich('ab', 2)); expect(p.overlay()).toBeUndefined();
});
