import { describe, expect, it } from 'vitest';
import { paneFromRoute, paneToRoute } from '../src/ui/route';

describe('session routes and tmux pane ids', () => {
  it('round-trips pane ids whose % would read as URL encoding', () => {
    for (const pane of ['%21', '%1', '%204', '%25']) {
      const route = paneToRoute(pane);
      expect(route).not.toContain('%');
      expect(decodeURIComponent(route)).toBe(route);
      expect(paneFromRoute(route)).toBe(pane);
    }
  });
  it('keeps a pane id that already carries its %', () => {
    expect(paneFromRoute('%7')).toBe('%7');
  });
});
