'use strict';

// A small EAN-13 / EAN-8 (and UPC-A, read as EAN-13) barcode decoder for the
// scanner's fallback path (static/js/scanner.js): browsers without the
// native BarcodeDetector API — Firefox, Safari, and Chromium builds with no
// Google Play services (Vanadium on GrapheneOS) — decode camera frames here.
// No dependency (frontend rule: no build step, no third-party code), no DOM:
// it works on a plain array of luminance values, so it can be tested
// headless.
//
// How: a few horizontal and vertical scanlines are read across the frame,
// each binarized against a local mean, turned into run lengths, and searched
// for the run-length signature of an EAN symbol (start guard, digits, middle
// guard, digits, end guard). Each digit is matched against the reference
// widths the way ZXing does (smallest total width variance, each bar within
// tolerance), and the check digit must hold. Several scanlines agreeing is
// what makes a read trustworthy: decode() reports how many did.
(function (root) {
  // Run widths (in modules) of every digit, starting with the first run of
  // the digit's 7 modules: L-code digits start with a space, R-code ones with
  // a bar, and in run widths both read the same; G-code is L reversed.
  const L_PATTERNS = [
    [3, 2, 1, 1], [2, 2, 2, 1], [2, 1, 2, 2], [1, 4, 1, 1], [1, 1, 3, 2],
    [1, 2, 3, 1], [1, 1, 1, 4], [1, 3, 1, 2], [1, 2, 1, 3], [3, 1, 1, 2],
  ];
  const G_PATTERNS = L_PATTERNS.map((p) => p.slice().reverse());
  // EAN-13's first digit is carried by which of its first six digits use
  // G-code (1) rather than L-code (0).
  const FIRST_DIGIT_PARITIES = ['000000', '001011', '001101', '001110', '010011', '011001', '011100', '010101', '010110', '011010'];

  const MAX_AVG_VARIANCE = 0.48;
  const MAX_INDIVIDUAL_VARIANCE = 0.7;
  const MIN_CONTRAST = 32;

  // How far runs may stray from one module in a guard pattern.
  function isGuard(runs, start, count, unit) {
    for (let i = 0; i < count; i++) {
      const modules = runs[start + i] / unit;
      if (modules < 0.5 || modules > 1.7) return false;
    }
    return true;
  }

  // Variance of 4 observed run widths against a 7-module pattern, in modules.
  function patternVariance(widths, pattern) {
    const total = widths[0] + widths[1] + widths[2] + widths[3];
    const unit = total / 7;
    let sum = 0;
    for (let i = 0; i < 4; i++) {
      const v = Math.abs(widths[i] / unit - pattern[i]);
      if (v > MAX_INDIVIDUAL_VARIANCE) return Infinity;
      sum += v;
    }
    return sum / 7;
  }

  // The best-matching digit (and whether it was G-code) for 4 run widths.
  function matchDigit(widths, allowG) {
    let best = { digit: -1, g: false, variance: MAX_AVG_VARIANCE };
    for (let d = 0; d < 10; d++) {
      const v = patternVariance(widths, L_PATTERNS[d]);
      if (v < best.variance) best = { digit: d, g: false, variance: v };
      if (allowG) {
        const vg = patternVariance(widths, G_PATTERNS[d]);
        if (vg < best.variance) best = { digit: d, g: true, variance: vg };
      }
    }
    return best.digit < 0 ? null : best;
  }

  function checksumOk(digits) {
    let sum = 0;
    const n = digits.length;
    for (let i = 0; i < n - 1; i++) {
      // Weights 3,1,3,1... counted from the digit next to the check digit.
      sum += digits[i] * ((n - 1 - i) % 2 === 1 ? 3 : 1);
    }
    return (10 - (sum % 10)) % 10 === digits[n - 1];
  }

  function readDigits(runs, start, count, allowG) {
    const out = [];
    for (let i = 0; i < count; i++) {
      const at = start + i * 4;
      const match = matchDigit([runs[at], runs[at + 1], runs[at + 2], runs[at + 3]], allowG);
      if (!match) return null;
      out.push(match);
    }
    return out;
  }

  // Tries to read an EAN-13 symbol whose start guard begins at runs[i] (a
  // bar). 59 runs: guard 3, 6 digits × 4, middle guard 5, 6 × 4, guard 3.
  function decodeEan13At(runs, i) {
    if (i + 59 > runs.length) return null;
    let total = 0;
    for (let k = 0; k < 59; k++) total += runs[i + k];
    const unit = total / 95;
    // Quiet zone: some white before the symbol (lenient — frames are tight).
    if (i > 0 && runs[i - 1] < unit * 2.5) return null;
    if (!isGuard(runs, i, 3, unit) || !isGuard(runs, i + 27, 5, unit) || !isGuard(runs, i + 56, 3, unit)) return null;
    const left = readDigits(runs, i + 3, 6, true);
    if (!left) return null;
    const right = readDigits(runs, i + 32, 6, false);
    if (!right) return null;
    const parity = left.map((m) => (m.g ? '1' : '0')).join('');
    const first = FIRST_DIGIT_PARITIES.indexOf(parity);
    if (first < 0) return null;
    const digits = [first, ...left.map((m) => m.digit), ...right.map((m) => m.digit)];
    return checksumOk(digits) ? digits.join('') : null;
  }

  // EAN-8: 43 runs — guard 3, 4 L-digits, middle 5, 4 R-digits, guard 3.
  function decodeEan8At(runs, i) {
    if (i + 43 > runs.length) return null;
    let total = 0;
    for (let k = 0; k < 43; k++) total += runs[i + k];
    const unit = total / 67;
    if (i > 0 && runs[i - 1] < unit * 2.5) return null;
    if (!isGuard(runs, i, 3, unit) || !isGuard(runs, i + 19, 5, unit) || !isGuard(runs, i + 40, 3, unit)) return null;
    const left = readDigits(runs, i + 3, 4, false);
    if (!left) return null;
    const right = readDigits(runs, i + 24, 4, false);
    if (!right) return null;
    const digits = [...left, ...right].map((m) => m.digit);
    return checksumOk(digits) ? digits.join('') : null;
  }

  // Binarizes one line of luminance values and returns its run lengths,
  // starting with the first dark run, or null for a line without enough
  // contrast to hold a barcode. Each pixel is compared with the midpoint of
  // the darkest and lightest values around it (so a lighting gradient across
  // the label doesn't swallow half the bars, and the white margin next to
  // the symbol doesn't widen its outer bars the way a local mean would); in
  // a flat stretch — the margin itself, where that midpoint would just
  // follow the noise — the line's overall midpoint is used instead.
  function scanlineRuns(line) {
    const n = line.length;
    let min = 255;
    let max = 0;
    for (let i = 0; i < n; i++) {
      if (line[i] < min) min = line[i];
      if (line[i] > max) max = line[i];
    }
    const range = max - min;
    if (range < MIN_CONTRAST) return null;
    const globalMid = (min + max) / 2;

    // Block minima/maxima make the windowed min/max cheap.
    const BLOCK = 8;
    const blocks = Math.ceil(n / BLOCK);
    const blockMin = new Uint8ClampedArray(blocks).fill(255);
    const blockMax = new Uint8ClampedArray(blocks);
    for (let i = 0; i < n; i++) {
      const k = (i / BLOCK) | 0;
      if (line[i] < blockMin[k]) blockMin[k] = line[i];
      if (line[i] > blockMax[k]) blockMax[k] = line[i];
    }
    const reach = Math.max(2, Math.round(n / 16 / BLOCK));
    // Hysteresis: a pixel close to the threshold keeps the current colour,
    // so sensor noise on a bar's edge doesn't split it into extra runs.
    const margin = range * 0.05;

    const runs = [];
    let current = -1; // 1 = dark
    let length = 0;
    for (let i = 0; i < n; i++) {
      const k = (i / BLOCK) | 0;
      let lo = 255;
      let hi = 0;
      for (let j = Math.max(0, k - reach); j <= Math.min(blocks - 1, k + reach); j++) {
        if (blockMin[j] < lo) lo = blockMin[j];
        if (blockMax[j] > hi) hi = blockMax[j];
      }
      const threshold = hi - lo >= range * 0.35 ? (lo + hi) / 2 : globalMid;
      let dark;
      if (current === 1) dark = line[i] > threshold + margin ? 0 : 1;
      else if (current === 0) dark = line[i] < threshold - margin ? 1 : 0;
      else dark = line[i] < threshold ? 1 : 0;
      if (dark === current) {
        length++;
      } else {
        if (current === 1 || (current === 0 && runs.length > 0)) runs.push(length);
        current = dark;
        length = 1;
      }
    }
    if (current === 1) runs.push(length);
    return runs;
  }

  // Every EAN code readable on one scanline, in either direction (the
  // symbol may be upside down).
  function decodeRuns(runs, found) {
    const passes = [runs, runs.slice().reverse()];
    for (const r of passes) {
      // A bar starts at every even index of r when r begins with a bar;
      // reversed, it still ends with one, so the parity holds.
      for (let i = 0; i < r.length; i += 2) {
        const code = decodeEan13At(r, i) || decodeEan8At(r, i);
        if (code) {
          found.push(code);
          return;
        }
      }
    }
  }

  // decode reads a frame of luminance values (one byte per pixel, row
  // major) and returns { code, votes } — the code read on the most scanlines
  // and on how many — or null when none was read.
  // Each scanline averages BAND adjacent rows (or columns): a barcode's bars
  // run across them, sensor noise doesn't.
  const BAND = 5;

  function decode(luminance, width, height) {
    const found = [];
    const rows = 14;
    const line = new Uint8ClampedArray(width);
    for (let k = 0; k < rows; k++) {
      const y0 = Math.min(height - BAND, Math.floor(height * (0.25 + (0.5 * k) / (rows - 1))));
      for (let x = 0; x < width; x++) {
        let sum = 0;
        for (let b = 0; b < BAND; b++) sum += luminance[(y0 + b) * width + x];
        line[x] = sum / BAND;
      }
      const runs = scanlineRuns(line);
      if (runs) decodeRuns(runs, found);
    }
    // A few vertical lines too, for a barcode held across the guide.
    const cols = 6;
    const column = new Uint8ClampedArray(height);
    for (let k = 0; k < cols; k++) {
      const x0 = Math.min(width - BAND, Math.floor(width * (0.3 + (0.4 * k) / (cols - 1))));
      for (let y = 0; y < height; y++) {
        let sum = 0;
        for (let b = 0; b < BAND; b++) sum += luminance[y * width + x0 + b];
        column[y] = sum / BAND;
      }
      const runs = scanlineRuns(column);
      if (runs) decodeRuns(runs, found);
    }
    if (found.length === 0) return null;
    const counts = new Map();
    let best = null;
    for (const code of found) {
      const c = (counts.get(code) || 0) + 1;
      counts.set(code, c);
      if (!best || c > best.votes) best = { code, votes: c };
    }
    return best;
  }

  // luminanceFromRGBA turns canvas ImageData pixels into luminance values.
  function luminanceFromRGBA(data, width, height) {
    const out = new Uint8ClampedArray(width * height);
    for (let i = 0, j = 0; j < out.length; i += 4, j++) {
      out[j] = (data[i] * 77 + data[i + 1] * 150 + data[i + 2] * 29) >> 8;
    }
    return out;
  }

  root.TrakkaBarcode = { decode, luminanceFromRGBA, checksumOk };
})(typeof self !== 'undefined' ? self : globalThis);
