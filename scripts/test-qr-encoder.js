// Headless test of static/js/qrcode.js, the QR code encoder of the
// "Connecter l'application mobile" modal: encodes texts of every length a
// version 1–40 symbol takes (the app's sign-in URL among them, UTF-8 too),
// renders each symbol into an RGBA frame and reads it back with jsQR, an
// independent decoder. Also checks a symbol's structure (size, finder
// patterns, quiet zone) and that text too long for a QR code is refused.
// No browser needed.
//
// Usage (see README.md): cd scripts && npm install && npm run test:qr-encoder

import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import jsQR from 'jsqr';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
await import(pathToFileURL(path.join(REPO_ROOT, 'static/js/qrcode.js')).href);
const { encode } = globalThis.TrakkaQR;

let failures = 0;
function check(name, ok, detail = '') {
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
  if (!ok) failures++;
}

// The symbol, with a 4-module quiet zone, at `scale` pixels per module.
function render({ size, modules }, scale = 4) {
  const border = 4;
  const width = (size + border * 2) * scale;
  const data = new Uint8ClampedArray(width * width * 4).fill(255);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (!modules[y][x]) continue;
      for (let dy = 0; dy < scale; dy++) {
        for (let dx = 0; dx < scale; dx++) {
          const offset = (((y + border) * scale + dy) * width + (x + border) * scale + dx) * 4;
          data[offset] = data[offset + 1] = data[offset + 2] = 0;
        }
      }
    }
  }
  return { data, width };
}

function decode(symbol) {
  const { data, width } = render(symbol);
  const result = jsQR(data, width, width, { inversionAttempts: 'dontInvert' });
  return result ? { text: result.data, version: result.version } : null;
}

let seed = 7;
function rand() { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; }
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
function randomText(length) {
  let s = '';
  for (let i = 0; i < length; i++) s += ALPHABET[Math.floor(rand() * ALPHABET.length)];
  return s;
}

// What qr-login.js encodes: the server's origin and a 43-character code.
const loginUrl = `https://trakka.example.com/#trakka-qr-login=${randomText(43)}`;
for (const text of [
  loginUrl,
  `https://a-much-longer-host-name.home.example.org:8443/#trakka-qr-login=${randomText(43)}`,
  'https://trakka.exemple.fr/',
  'Liste de courses : pâtes, crème fraîche, œufs — 3 € 50',
  'x',
]) {
  const symbol = encode(text);
  const decoded = decode(symbol);
  check(`reads back ${JSON.stringify(text.length > 60 ? `${text.slice(0, 57)}…` : text)}`,
    decoded && decoded.text === text,
    decoded ? `version ${symbol.version}` : 'not decoded');
}

// Every version: the largest text that still fits it, and one byte more,
// which must move to the next version.
let previousVersion = 0;
let versionsSeen = 0;
for (let length = 1; length <= 2331; length += length < 300 ? 7 : 61) {
  const text = randomText(length);
  const symbol = encode(text);
  if (symbol.version < previousVersion) {
    check(`version grows with the text (${length} bytes)`, false, `${previousVersion} then ${symbol.version}`);
  }
  if (symbol.version !== previousVersion) {
    versionsSeen++;
    const decoded = decode(symbol);
    check(`version ${symbol.version} (${length} bytes) reads back`,
      decoded && decoded.text === text && decoded.version === symbol.version,
      decoded ? `decoded as version ${decoded.version}` : 'not decoded');
    previousVersion = symbol.version;
  }
}
check('covers versions 1 to 40', previousVersion === 40 && versionsSeen >= 30, `${versionsSeen} versions, up to ${previousVersion}`);

// Version 40-M holds 2331 bytes in byte mode.
const largest = encode(randomText(2331));
check('2331 bytes fit version 40', largest.version === 40 && largest.size === 177);
let tooLong = false;
try {
  encode(randomText(2332));
} catch (err) {
  tooLong = err instanceof RangeError;
}
check('2332 bytes are refused', tooLong);

// Structure of the sign-in URL's symbol.
const symbol = encode(loginUrl);
check('size is 4 × version + 17', symbol.size === symbol.version * 4 + 17 && symbol.modules.length === symbol.size);
const finderAt = (x0, y0) => {
  for (let dy = 0; dy < 7; dy++) {
    for (let dx = 0; dx < 7; dx++) {
      const ring = Math.max(Math.abs(dx - 3), Math.abs(dy - 3));
      if (symbol.modules[y0 + dy][x0 + dx] !== (ring !== 2)) return false;
    }
  }
  return true;
};
const s = symbol.size;
check('three finder patterns', finderAt(0, 0) && finderAt(s - 7, 0) && finderAt(0, s - 7));
check('dark module', symbol.modules[s - 8][8] === true);

console.log(failures ? `\n${failures} check(s) failed` : '\nall checks passed');
process.exit(failures ? 1 : 0);
