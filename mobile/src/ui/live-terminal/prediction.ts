export interface EchoFrame {
  lines: string[]; cells: string[][]; x: number; y: number; columns: number;
  typing: boolean; alternate: boolean; copy: boolean; cursor: boolean;
  acceptedSeq: number; echoSeq: number;
}
interface Guess { seq: number; text: string; x: number; y: number; start: number; end: number }
/** Display-only guesses. This class cannot send input or execute a command. */
export class EchoPrediction {
  private frame?: EchoFrame;
  private pending: Guess[] = [];
  private confident = false;
  private barrier = 0;
  private origin = 0;
  clear() { this.frame = undefined; this.reset(); }
  reset(sequence = 0) { this.pending = []; this.confident = false; this.barrier = sequence; this.origin = this.frame?.x ?? 0; }
  input(seq: number, kind: string, data: string) {
    const f = this.frame;
    if (!f || this.barrier > f.echoSeq) return;
    const last = this.pending.at(-1);
    const x = last?.x ?? f.x, y = last?.y ?? f.y;
    let text = last?.text ?? '';
    let nextX = x;
    if (!f.typing || f.alternate || f.copy || !f.cursor) { this.reset(seq); return; }
    if (kind === 'text' && /^[\x20-\x7e]+$/.test(data) && x + data.length < f.columns - 1) {
      text += data; nextX += data.length;
    } else if (kind === 'key' && data === 'BSpace' && x > this.origin && text.length) {
      text = text.slice(0, -1); nextX--;
    } else { this.reset(seq); return; }
    const guess = { seq, text, x: nextX, y, start: this.origin, end: Math.max(last?.end ?? x, nextX) };
    if (last?.seq === seq) this.pending[this.pending.length - 1] = guess;
    else this.pending.push(guess);
    if (this.pending.length > 64) this.reset(seq);
  }
  reconcile(frame: EchoFrame) {
    const previous = this.frame;
    if (previous && (frame.acceptedSeq < previous.acceptedSeq || frame.echoSeq < previous.echoSeq)) return;
    const safe = frame.typing && !frame.alternate && !frame.copy && frame.cursor;
    if (!safe || previous && (previous.columns !== frame.columns || previous.lines.length !== frame.lines.length || frame.y !== previous.y)) {
      this.frame = frame; this.reset(frame.acceptedSeq); return;
    }
    if (this.pending.length) {
      let match = -1;
      for (let i = 0; i < this.pending.length; i++) {
        const guess = this.pending[i];
        // Cell coordinates survive wide prompt glyphs. Only the typed prefix is
        // echo evidence: right prompts and autosuggestions are not user input.
        if (guess.seq <= frame.acceptedSeq && guess.x === frame.x && guess.y === frame.y &&
          frame.cells[frame.y]?.slice(guess.start, guess.x).join('') === guess.text &&
          (!previous || previous.cells[frame.y]?.slice(0, guess.start).join('') === frame.cells[frame.y]?.slice(0, guess.start).join('')) &&
          (!previous || previous.lines.every((line, row) => row === frame.y || line === frame.lines[row]))) match = i;
      }
      if (match >= 0) { this.pending.splice(0, match + 1); this.confident = true; }
      else if (frame.echoSeq >= this.pending[0].seq) { this.frame = frame; this.reset(frame.acceptedSeq); return; }
    } else if (previous && (previous.x !== frame.x || previous.y !== frame.y || previous.lines.some((line, i) => line !== frame.lines[i]))) {
      this.frame = frame; this.reset(frame.acceptedSeq); return;
    }
    this.frame = frame;
    if (!this.pending.length) this.origin = frame.x;
  }
  overlay(): Guess | undefined { return this.confident ? this.pending.at(-1) : undefined; }
}
