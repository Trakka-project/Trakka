// Headless test of static/js/barcode.js, the scanner's own EAN decoder
// (used where the browser has no BarcodeDetector): renders synthetic
// EAN-13/EAN-8 symbols into luminance frames — fractional module widths,
// blur, sensor noise, a lighting gradient, upside down, vertical — and checks
// they're read back, that random stripes and a wrong check digit are not,
// and how long a frame takes. No browser needed.
//
// Usage (see README.md): cd scripts && npm run test:barcode

import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
await import(pathToFileURL(path.join(REPO_ROOT, 'static/js/barcode.js')).href);
const { decode } = globalThis.TrakkaBarcode;

const L = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011'];
const G = L.map((p) => p.split('').map((c) => (c === '1' ? '0' : '1')).reverse().join(''));
const R = L.map((p) => p.split('').map((c) => (c === '1' ? '0' : '1')).join(''));
const PAR = ['LLLLLL', 'LLGLGG', 'LLGGLG', 'LLGGGL', 'LGLLGG', 'LGGLLG', 'LGGGLL', 'LGLGLG', 'LGLGGL', 'LGGLGL'];

function modules13(code) {
  const d = code.split('').map(Number);
  let s = '101';
  for (let i = 1; i <= 6; i++) s += (PAR[d[0]][i - 1] === 'L' ? L : G)[d[i]];
  s += '01010';
  for (let i = 7; i <= 12; i++) s += R[d[i]];
  return s + '101';
}
function modules8(code) {
  const d = code.split('').map(Number);
  let s = '101';
  for (let i = 0; i < 4; i++) s += L[d[i]];
  s += '01010';
  for (let i = 4; i < 8; i++) s += R[d[i]];
  return s + '101';
}

let seed = 42;
function rand() { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; }

// Renders `mods` horizontally across a w×h frame, module width m pixels,
// starting at x0, with blur, noise and a lighting gradient.
function render(mods, { w = 640, h = 480, m = 3.3, x0 = 80, blur = 1, noise = 18, gradient = 60, vertical = false, flip = false }) {
  const W = vertical ? h : w;
  const line = new Float64Array(W);
  for (let x = 0; x < W; x++) {
    const pos = (x - x0) / m;
    let dark = false;
    if (pos >= 0 && pos < mods.length) dark = mods[Math.floor(pos)] === '1';
    line[x] = dark ? 40 : 215;
  }
  if (flip) line.reverse();
  // box blur
  let blurred = line;
  for (let pass = 0; pass < blur; pass++) {
    const out = new Float64Array(W);
    for (let x = 0; x < W; x++) out[x] = (blurred[Math.max(0, x - 1)] + blurred[x] + blurred[Math.min(W - 1, x + 1)]) / 3;
    blurred = out;
  }
  const lum = new Uint8ClampedArray(w * h);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const along = vertical ? y : x;
      const g = gradient * (x / w) - gradient / 2;
      const inBand = vertical ? (x > w * 0.2 && x < w * 0.8) : (y > h * 0.2 && y < h * 0.8);
      const base = inBand ? blurred[along] : 200;
      lum[y * w + x] = base + g + (rand() - 0.5) * 2 * noise;
    }
  }
  return lum;
}

let failures = 0;
function expect(name, got, want) {
  const ok = (got && got.code) === want;
  if (!ok) failures++;
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}: got ${got ? got.code + ' (' + got.votes + ' votes)' : 'null'}, want ${want}`);
}

const codes13 = ['3017620422003', '4006381333931', '0036000291452', '5000112637922', '3274080005003', '7622210449283'];
for (const code of codes13) {
  expect(`EAN-13 ${code}`, decode(render(modules13(code), {}), 640, 480), code);
}
expect('EAN-13 small modules (2.2px)', decode(render(modules13('3017620422003'), { m: 2.2, x0: 120 }), 640, 480), '3017620422003');
expect('EAN-13 small modules (1.9px), no blur', decode(render(modules13('3017620422003'), { m: 1.9, x0: 120, blur: 0 }), 640, 480), '3017620422003');
expect('EAN-13 large modules (5.4px)', decode(render(modules13('4006381333931'), { m: 5.4, x0: 60 }), 640, 480), '4006381333931');
expect('EAN-13 upside down', decode(render(modules13('5000112637922'), { flip: true, x0: 130 }), 640, 480), '5000112637922');
expect('EAN-13 heavy blur', decode(render(modules13('3274080005003'), { blur: 2, m: 4 }), 640, 480), '3274080005003');
expect('EAN-13 noisy + strong gradient', decode(render(modules13('7622210449283'), { noise: 30, gradient: 120 }), 640, 480), '7622210449283');
expect('EAN-13 vertical', decode(render(modules13('3017620422003'), { vertical: true, m: 3, x0: 60 }), 640, 480), '3017620422003');
expect('EAN-8 96385074', decode(render(modules8('96385074'), { m: 4 }), 640, 480), '96385074');
expect('EAN-8 40170725 upside down', decode(render(modules8('40170725'), { m: 4, flip: true, x0: 200 }), 640, 480), '40170725');

// No false positives on noise or on a symbol with a wrong check digit.
let falsePositives = 0;
for (let k = 0; k < 40; k++) {
  const w = 640, h = 480;
  const lum = new Uint8ClampedArray(w * h);
  // random stripes
  let x = 0;
  const line = new Uint8ClampedArray(w);
  while (x < w) {
    const width = 1 + Math.floor(rand() * 9);
    const v = rand() < 0.5 ? 40 : 215;
    for (let i = 0; i < width && x < w; i++, x++) line[x] = v;
  }
  for (let y = 0; y < h; y++) for (let i = 0; i < w; i++) lum[y * w + i] = line[i] + (rand() - 0.5) * 20;
  if (decode(lum, w, h)) falsePositives++;
}
const badCheck = modules13('3017620422003').slice(0, 92 - 7) + R[4] + '101'; // last digit 4 instead of 3
const bad = decode(render(badCheck, {}), 640, 480);
if (bad) falsePositives++;
console.log(`${falsePositives === 0 ? 'ok  ' : 'FAIL'} no false positives on random stripes / bad check digit (${falsePositives})`);
if (falsePositives) failures++;

// Speed: one frame should take a few milliseconds.
const frame = render(modules13('3017620422003'), {});
const t0 = process.hrtime.bigint();
for (let i = 0; i < 50; i++) decode(frame, 640, 480);
const ms = Number(process.hrtime.bigint() - t0) / 1e6 / 50;
console.log(`avg ${ms.toFixed(2)} ms per 640x480 frame`);

process.exit(failures ? 1 : 0);
