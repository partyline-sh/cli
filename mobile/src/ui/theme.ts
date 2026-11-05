import { Platform } from 'react-native';

/** partyline's dark palette, from the approved prototype (docs/mockups/mobile-pwa/prototype.html). */
export const c = {
  ground: '#1b1a19', surface: '#232220', raised: '#2d2b29', line: '#3a3835',
  text: '#f5f3f0', muted: '#a8a29e', faint: '#78716c',
  accent: '#ff5e8a', claude: '#ff9a3c', codex: '#c4b5fd', gemini: '#7dd3fc',
  ok: '#4ade80', warn: '#fbbf24', term: '#12110f', termText: '#d6d3cf',
} as const;

/** The system monospace until JetBrains Mono is bundled (it needs the app config, which the runtime owns). */
export const mono = Platform.select({ ios: 'Menlo', android: 'monospace', default: 'ui-monospace, Menlo, monospace' });

export const engineColour = (engine: string) =>
  engine === 'claude' ? c.claude : engine === 'codex' ? c.codex : engine === 'gemini' ? c.gemini : c.faint;

/** A short age: "now", "40s", "8m", "3h", "2d". */
export function age(at?: number, now = Date.now()): string {
  if (!at) return '';
  const s = Math.max(0, Math.round((now - at) / 1000));
  if (s < 10) return 'now';
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 172800) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

/** "9:41", for stale labels. */
export const clock = (at: number) => new Date(at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
