import { readFile, mkdir, copyFile, writeFile } from 'node:fs/promises';
import { Buffer } from 'node:buffer';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const mobile = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const output = path.join(mobile, 'artifacts/store-assets');
const source = await readFile(path.join(mobile, 'assets/partyline-mark.svg'), 'utf8');
const mark = source.match(/ d="([^"]+)"/)[1];
const ground = '#1b1a19';
const spectrum = '<linearGradient id="brand" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#ffaa3c"/><stop offset=".52" stop-color="#ff5e8a"/><stop offset="1" stop-color="#df5bff"/></linearGradient>';
const svg = (content, width = 1024, height = width) => `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">${content}</svg>`;
const glyph = (fill, width = 640) => `<g transform="translate(${(1024 - width) / 2} ${512 - 12.5 * width / 24}) scale(${width / 24})"><path fill="${fill}" d="${mark}"/></g>`;
const icon = style => svg(`<defs>${spectrum}</defs><rect width="1024" height="1024" fill="${style === 'signal' ? 'url(#brand)' : ground}"/>${glyph(style === 'amber' ? '#ff9838' : style === 'signal' ? ground : 'url(#brand)')}`);
const png = async (markup, destination) => sharp(Buffer.from(markup)).png().toFile(destination);
await mkdir(path.join(output, 'icons'), { recursive: true });
for (const [i, style] of ['amber', 'spectrum', 'signal'].entries()) {
  const markup = icon(style);
  await writeFile(path.join(output, 'icons', `${i + 1}-${style}.svg`), markup);
  await sharp(Buffer.from(markup)).removeAlpha().png().toFile(path.join(output, 'icons', `${i + 1}-${style}-1024.png`));
}
// The spectrum treatment uses the existing terminal wordmark gradient and phone silhouette.
if (process.argv.includes('--apply-icon')) {
  await copyFile(path.join(output, 'icons/2-spectrum-1024.png'), path.join(mobile, 'assets/icon.png'));
  await png(svg(`<defs>${spectrum}</defs>${glyph('url(#brand)', 512)}`), path.join(mobile, 'assets/android-icon-foreground.png'));
  await png(svg(glyph('#ffffff', 512)), path.join(mobile, 'assets/android-icon-monochrome.png'));
  await png(svg('<rect width="1024" height="1024" fill="#1b1a19"/>'), path.join(mobile, 'assets/android-icon-background.png'));
  await png(svg(`<defs>${spectrum}</defs>${glyph('url(#brand)')}`), path.join(mobile, 'assets/splash-icon.png'));
  await sharp(Buffer.from(icon('spectrum'))).resize(64).png().toFile(path.join(mobile, 'assets/favicon.png'));
}

const captions = [
  ['sessions', ['Your agents.', 'Within reach.'], 'Check every session. Step in when it matters.'],
  ['messages', ['Pick up the', 'conversation.'], 'Read replies and send your next instruction.'],
  ['terminal', ['A terminal.', 'In your pocket.'], 'Inspect snapshots. Send text and keys.'],
  ['loops', ['Keep an eye', 'on your loops.'], 'See what needs you, and what is moving forward.'],
  ['chat', ['One room.', 'All your agents.'], 'Stay in the conversation wherever you are.'],
  ['computers', ['Your computers.', 'Your connection.'], 'Pair with a QR code. End-to-end encrypted.'],
];
const esc = text => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
let captures = 0;
for (const [index, [screen, title, description]] of captions.entries()) {
  const raw = path.join(output, 'raw', `${screen}.png`);
  try { await readFile(raw); } catch { continue; }
  const shot = await sharp(raw).resize(936, 2035, { fit: 'contain', background: ground }).png().toBuffer();
  const rounded = await sharp(shot).composite([{ input: Buffer.from(svg('<rect width="936" height="2035" rx="44" fill="white"/>', 936, 2035)), blend: 'dest-in' }]).png().toBuffer();
  const background = svg(`<defs>${spectrum}</defs>
    <rect width="1320" height="2868" fill="${ground}"/>
    <path d="M104 96H1216" stroke="#3a3835"/>
    <text x="104" y="154" fill="#ffad62" font-family="Helvetica Neue,Helvetica,sans-serif" font-size="25" font-weight="600" letter-spacing="4">PARTYLINE REMOTE</text>
    <text x="1216" y="154" text-anchor="end" fill="#78716c" font-family="Menlo,monospace" font-size="22">0${index + 1}</text>
    <text x="98" y="297" fill="#f5f3f0" font-family="Helvetica Neue,Helvetica,sans-serif" font-size="98" font-weight="700" letter-spacing="-4">${esc(title[0])}</text>
    <text x="98" y="407" fill="url(#brand)" font-family="Helvetica Neue,Helvetica,sans-serif" font-size="98" font-weight="700" letter-spacing="-4">${esc(title[1])}</text>
    <text x="104" y="496" fill="#a8a29e" font-family="Helvetica Neue,Helvetica,sans-serif" font-size="31">${esc(description)}</text>
    <rect x="188" y="629" width="944" height="2043" rx="48" fill="#0f0e0d" stroke="#55504b" stroke-width="3"/>
    <text x="104" y="2794" fill="#78716c" font-family="Menlo,monospace" font-size="21">partyline.sh</text>
    <text x="1216" y="2794" text-anchor="end" fill="#78716c" font-family="Menlo,monospace" font-size="21">YOUR MACHINES. YOUR WORK.</text>`, 1320, 2868);
  const rendered = await sharp(Buffer.from(background)).composite([{ input: rounded, left: 192, top: 633 }]).flatten({ background: ground }).removeAlpha().png().toBuffer();
  for (const [folder, width, height] of [['iphone-large', 1320, 2868], ['iphone-medium', 1206, 2622]]) {
    await mkdir(path.join(output, folder), { recursive: true });
    const destination = path.join(output, folder, `${String(index + 1).padStart(2, '0')}-${screen}.png`);
    await sharp(rendered).resize(width, height).removeAlpha().png().toFile(destination);
    const meta = await sharp(destination).metadata();
    if (meta.width !== width || meta.height !== height || meta.hasAlpha) throw new Error(`Invalid store image: ${destination}`);
  }
  captures++;
}
// A local preview sheet; it is not intended for the App Store screenshot slots.
const tiles = await Promise.all(['amber', 'spectrum', 'signal'].map(async (style, index) => ({
  input: await sharp(Buffer.from(icon(style))).resize(300).png().toBuffer(), left: 60 + index * 360, top: 100,
})));
await sharp({ create: { width: 1140, height: 480, channels: 3, background: '#292624' } }).composite(tiles).png().toFile(path.join(output, 'icon-options.png'));
if (captures === captions.length) {
  const previews = await Promise.all(captions.map(async ([screen], i) => ({
    input: await sharp(path.join(output, 'iphone-large', `${String(i + 1).padStart(2, '0')}-${screen}.png`)).resize(300).png().toBuffer(),
    left: 20 + (i % 3) * 320, top: 20 + Math.floor(i / 3) * 672,
  })));
  await sharp({ create: { width: 980, height: 1364, channels: 3, background: '#3a3835' } }).composite(previews).png().toFile(path.join(output, 'screenshots-preview.png'));
}
await writeFile(path.join(output, 'README.txt'), `Partyline Remote — App Store image pack

icons/: three 1024x1024 opaque app-icon options.
  1-amber: existing brand treatment
  2-spectrum: established wordmark gradient (app default)
  3-signal: dark phone on a gradient background

iphone-medium/: 1206x2622 images for the required medium Dynamic Island slot.
iphone-large/: 1320x2868 images for the large Dynamic Island slot.
raw/: native iPhone captures with fictional example data.

Upload the PNGs inside iphone-medium/ and iphone-large/ to their matching slots.
The icon is supplied through a new app build, not through a screenshot slot.
TestFlight build 1 keeps its old icon until a new build is installed.

The screenshots use the actual native screens with a debug-only fixture controller.
No client projects, credentials or personal conversations appear. The screenshots
show existing features: sessions, messages, terminal snapshots, loops, chat and
paired computers. They do not advertise push notifications or terminal streaming.

Apple requirements checked 2026-10-30:
https://developer.apple.com/help/app-store-connect/reference/app-information/screenshot-specifications/
`);
for (const filename of ['1-amber-1024.png', '2-spectrum-1024.png', '3-signal-1024.png']) {
  const meta = await sharp(path.join(output, 'icons', filename)).metadata();
  if (meta.width !== 1024 || meta.height !== 1024 || meta.hasAlpha) throw new Error(`Invalid icon: ${filename}`);
}
console.log(`Icons and ${captures} native screenshot compositions exported to ${output}`);
