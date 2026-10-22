'use strict';

// A small QR code encoder (ISO/IEC 18004) for the "Connecter l'application
// mobile" modal (static/js/qr-login.js), which shows the Android app a
// sign-in code to scan. No dependency (frontend rule: no build step, no
// third-party code), and no DOM in encode(): it returns the module matrix,
// so it can be tested headless (scripts/test-qr-encoder.js decodes its
// output with a real QR reader). toSvg() draws that matrix for the page.
//
// What it covers is what a URL needs: byte mode (UTF-8), error correction
// level M (15 % of the symbol can be lost), the smallest of versions 1–40
// that fits, and the mask with the lowest penalty score, as the standard
// prescribes. Its structure follows Project Nayuki's reference
// implementation (https://www.nayuki.io/page/qr-code-generator-library).
(function (root) {
  // Per version (index 0 unused), for error correction level M: the error
  // correction codewords of each block, and the number of blocks.
  const ECC_CODEWORDS_PER_BLOCK = [
    -1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26,
    26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28,
  ];
  const NUM_ERROR_CORRECTION_BLOCKS = [
    -1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16,
    17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49,
  ];
  // Level M's two format bits.
  const ECL_FORMAT_BITS = 0;
  const MIN_VERSION = 1;
  const MAX_VERSION = 40;

  function bit(value, index) {
    return ((value >>> index) & 1) !== 0;
  }

  // Modules left for data and error correction once every function pattern
  // is drawn, remainder bits included.
  function numRawDataModules(version) {
    let result = (16 * version + 128) * version + 64;
    if (version >= 2) {
      const numAlign = Math.floor(version / 7) + 2;
      result -= (25 * numAlign - 10) * numAlign - 55;
      if (version >= 7) result -= 36;
    }
    return result;
  }

  function numDataCodewords(version) {
    return Math.floor(numRawDataModules(version) / 8)
      - ECC_CODEWORDS_PER_BLOCK[version] * NUM_ERROR_CORRECTION_BLOCKS[version];
  }

  // Centers of the alignment patterns, on both axes.
  function alignmentPatternPositions(version) {
    if (version === 1) return [];
    const numAlign = Math.floor(version / 7) + 2;
    const step = Math.floor((version * 8 + numAlign * 3 + 5) / (numAlign * 4 - 4)) * 2;
    const result = [6];
    for (let pos = version * 4 + 10; result.length < numAlign; pos -= step) {
      result.splice(1, 0, pos);
    }
    return result;
  }

  // ---- Reed–Solomon over GF(2^8), modulo x^8 + x^4 + x^3 + x^2 + 1 ----

  function rsMultiply(x, y) {
    let z = 0;
    for (let i = 7; i >= 0; i--) {
      z = (z << 1) ^ ((z >>> 7) * 0x11d);
      z ^= ((y >>> i) & 1) * x;
    }
    return z;
  }

  function rsDivisor(degree) {
    const result = new Array(degree).fill(0);
    result[degree - 1] = 1;
    let rootValue = 1;
    for (let i = 0; i < degree; i++) {
      for (let j = 0; j < result.length; j++) {
        result[j] = rsMultiply(result[j], rootValue);
        if (j + 1 < result.length) result[j] ^= result[j + 1];
      }
      rootValue = rsMultiply(rootValue, 0x02);
    }
    return result;
  }

  function rsRemainder(data, divisor) {
    const result = divisor.map(() => 0);
    for (const b of data) {
      const factor = b ^ result.shift();
      result.push(0);
      divisor.forEach((coefficient, i) => {
        result[i] ^= rsMultiply(coefficient, factor);
      });
    }
    return result;
  }

  // Splits the data codewords into blocks, appends each block's error
  // correction, and interleaves them.
  function addEccAndInterleave(data, version) {
    const numBlocks = NUM_ERROR_CORRECTION_BLOCKS[version];
    const blockEccLen = ECC_CODEWORDS_PER_BLOCK[version];
    const rawCodewords = Math.floor(numRawDataModules(version) / 8);
    const numShortBlocks = numBlocks - (rawCodewords % numBlocks);
    const shortBlockLen = Math.floor(rawCodewords / numBlocks);
    const divisor = rsDivisor(blockEccLen);

    const blocks = [];
    for (let i = 0, k = 0; i < numBlocks; i++) {
      const dat = data.slice(k, k + shortBlockLen - blockEccLen + (i < numShortBlocks ? 0 : 1));
      k += dat.length;
      const ecc = rsRemainder(dat, divisor);
      // A placeholder that keeps the short blocks aligned with the long ones.
      if (i < numShortBlocks) dat.push(0);
      blocks.push(dat.concat(ecc));
    }

    const result = [];
    for (let i = 0; i < blocks[0].length; i++) {
      blocks.forEach((block, j) => {
        if (i !== shortBlockLen - blockEccLen || j >= numShortBlocks) result.push(block[i]);
      });
    }
    return result;
  }

  // ---- Data encoding ----

  function dataCodewords(bytes, version) {
    const bits = [];
    const append = (value, length) => {
      for (let i = length - 1; i >= 0; i--) bits.push((value >>> i) & 1);
    };
    append(0x4, 4); // byte mode
    append(bytes.length, version <= 9 ? 8 : 16);
    for (const b of bytes) append(b, 8);

    const capacityBits = numDataCodewords(version) * 8;
    append(0, Math.min(4, capacityBits - bits.length)); // terminator
    append(0, (8 - (bits.length % 8)) % 8);
    for (let pad = 0xec; bits.length < capacityBits; pad ^= 0xec ^ 0x11) append(pad, 8);

    const result = [];
    for (let i = 0; i < bits.length; i += 8) {
      let b = 0;
      for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j];
      result.push(b);
    }
    return result;
  }

  function chooseVersion(byteLength) {
    for (let version = MIN_VERSION; version <= MAX_VERSION; version++) {
      const usedBits = 4 + (version <= 9 ? 8 : 16) + byteLength * 8;
      if (usedBits <= numDataCodewords(version) * 8) return version;
    }
    throw new RangeError('Text too long for a QR code');
  }

  // ---- The symbol ----

  function createSymbol(version) {
    const size = version * 4 + 17;
    const grid = () => Array.from({ length: size }, () => new Array(size).fill(false));
    return { version, size, modules: grid(), isFunction: grid() };
  }

  function setFunctionModule(symbol, x, y, dark) {
    symbol.modules[y][x] = dark;
    symbol.isFunction[y][x] = true;
  }

  function drawFunctionPatterns(symbol) {
    const { size, version } = symbol;
    for (let i = 0; i < size; i++) {
      setFunctionModule(symbol, 6, i, i % 2 === 0);
      setFunctionModule(symbol, i, 6, i % 2 === 0);
    }

    // Finder patterns, with their separators.
    for (const [cx, cy] of [[3, 3], [size - 4, 3], [3, size - 4]]) {
      for (let dy = -4; dy <= 4; dy++) {
        for (let dx = -4; dx <= 4; dx++) {
          const x = cx + dx;
          const y = cy + dy;
          if (x < 0 || x >= size || y < 0 || y >= size) continue;
          const distance = Math.max(Math.abs(dx), Math.abs(dy));
          setFunctionModule(symbol, x, y, distance !== 2 && distance !== 4);
        }
      }
    }

    // Alignment patterns, except where they would overlap a finder.
    const positions = alignmentPatternPositions(version);
    const n = positions.length;
    for (let i = 0; i < n; i++) {
      for (let j = 0; j < n; j++) {
        if ((i === 0 && j === 0) || (i === 0 && j === n - 1) || (i === n - 1 && j === 0)) continue;
        for (let dy = -2; dy <= 2; dy++) {
          for (let dx = -2; dx <= 2; dx++) {
            setFunctionModule(symbol, positions[i] + dx, positions[j] + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
          }
        }
      }
    }

    // Reserves the format areas (drawn for real once the mask is known).
    drawFormatBits(symbol, 0);
    drawVersion(symbol);
  }

  function drawFormatBits(symbol, mask) {
    const { size } = symbol;
    const data = (ECL_FORMAT_BITS << 3) | mask;
    let remainder = data;
    for (let i = 0; i < 10; i++) remainder = (remainder << 1) ^ ((remainder >>> 9) * 0x537);
    const bits = ((data << 10) | remainder) ^ 0x5412;

    // Around the top-left finder.
    for (let i = 0; i <= 5; i++) setFunctionModule(symbol, 8, i, bit(bits, i));
    setFunctionModule(symbol, 8, 7, bit(bits, 6));
    setFunctionModule(symbol, 8, 8, bit(bits, 7));
    setFunctionModule(symbol, 7, 8, bit(bits, 8));
    for (let i = 9; i < 15; i++) setFunctionModule(symbol, 14 - i, 8, bit(bits, i));

    // The copy split between the two other finders.
    for (let i = 0; i < 8; i++) setFunctionModule(symbol, size - 1 - i, 8, bit(bits, i));
    for (let i = 8; i < 15; i++) setFunctionModule(symbol, 8, size - 15 + i, bit(bits, i));
    setFunctionModule(symbol, 8, size - 8, true); // the dark module
  }

  function drawVersion(symbol) {
    const { size, version } = symbol;
    if (version < 7) return;
    let remainder = version;
    for (let i = 0; i < 12; i++) remainder = (remainder << 1) ^ ((remainder >>> 11) * 0x1f25);
    const bits = (version << 12) | remainder;
    for (let i = 0; i < 18; i++) {
      const dark = bit(bits, i);
      const a = size - 11 + (i % 3);
      const b = Math.floor(i / 3);
      setFunctionModule(symbol, a, b, dark);
      setFunctionModule(symbol, b, a, dark);
    }
  }

  // Places the codewords in the zigzag order, two columns at a time from the
  // bottom-right corner, skipping the vertical timing pattern.
  function drawCodewords(symbol, codewords) {
    const { size, modules, isFunction } = symbol;
    let i = 0;
    for (let right = size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5;
      for (let vertical = 0; vertical < size; vertical++) {
        for (let j = 0; j < 2; j++) {
          const x = right - j;
          const upward = ((right + 1) & 2) === 0;
          const y = upward ? size - 1 - vertical : vertical;
          if (!isFunction[y][x] && i < codewords.length * 8) {
            modules[y][x] = bit(codewords[i >>> 3], 7 - (i & 7));
            i++;
          }
          // Modules past the codewords (remainder bits) stay light.
        }
      }
    }
  }

  function maskApplies(mask, x, y) {
    switch (mask) {
      case 0: return (x + y) % 2 === 0;
      case 1: return y % 2 === 0;
      case 2: return x % 3 === 0;
      case 3: return (x + y) % 3 === 0;
      case 4: return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
      case 5: return ((x * y) % 2) + ((x * y) % 3) === 0;
      case 6: return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
      default: return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0;
    }
  }

  // XORs the mask over the data modules: applying it twice undoes it.
  function applyMask(symbol, mask) {
    const { size, modules, isFunction } = symbol;
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        if (!isFunction[y][x] && maskApplies(mask, x, y)) modules[y][x] = !modules[y][x];
      }
    }
  }

  // The standard's four penalty rules: long runs, 2×2 blocks, finder-like
  // patterns, and an unbalanced dark/light ratio. The lowest score wins.
  function penaltyScore(symbol) {
    const { size, modules } = symbol;
    const at = (x, y, vertical) => (vertical ? modules[x][y] : modules[y][x]);
    let score = 0;

    for (const vertical of [false, true]) {
      for (let line = 0; line < size; line++) {
        let runLength = 1;
        for (let i = 1; i <= size; i++) {
          if (i < size && at(i, line, vertical) === at(i - 1, line, vertical)) {
            runLength++;
            continue;
          }
          if (runLength >= 5) score += 3 + (runLength - 5);
          runLength = 1;
        }
        // 1:1:3:1:1 dark-light-dark-light-dark with four light modules on
        // one side (light outside the symbol counts, as the quiet zone).
        for (let i = -4; i < size; i++) {
          const window = [];
          for (let k = 0; k < 11; k++) {
            const p = i + k;
            window.push(p >= 0 && p < size ? at(p, line, vertical) : false);
          }
          const pattern = window.map((dark) => (dark ? '1' : '0')).join('');
          if (pattern === '10111010000' || pattern === '00001011101') score += 40;
        }
      }
    }

    for (let y = 0; y < size - 1; y++) {
      for (let x = 0; x < size - 1; x++) {
        const c = modules[y][x];
        if (c === modules[y][x + 1] && c === modules[y + 1][x] && c === modules[y + 1][x + 1]) score += 3;
      }
    }

    let dark = 0;
    for (const row of modules) for (const m of row) if (m) dark++;
    const total = size * size;
    const k = Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1;
    score += k * 10;
    return score;
  }

  // encode returns { size, modules }: modules[y][x] is true for a dark
  // module. No quiet zone included: toSvg adds it.
  function encode(text) {
    const bytes = Array.from(new TextEncoder().encode(String(text)));
    const version = chooseVersion(bytes.length);
    const symbol = createSymbol(version);
    drawFunctionPatterns(symbol);
    drawCodewords(symbol, addEccAndInterleave(dataCodewords(bytes, version), version));

    let bestMask = 0;
    let bestScore = Infinity;
    for (let mask = 0; mask < 8; mask++) {
      applyMask(symbol, mask);
      drawFormatBits(symbol, mask);
      const score = penaltyScore(symbol);
      if (score < bestScore) {
        bestScore = score;
        bestMask = mask;
      }
      applyMask(symbol, mask);
    }
    applyMask(symbol, bestMask);
    drawFormatBits(symbol, bestMask);
    return { version, size: symbol.size, modules: symbol.modules };
  }

  // toSvg draws text's QR code as an <svg> element (built with DOM calls,
  // never markup): one path of dark modules on a light background, with the
  // standard four-module quiet zone. Always dark on light, whatever the
  // page's theme: inverted QR codes are not read by every scanner.
  function toSvg(text, { title } = {}) {
    const { size, modules } = encode(text);
    const border = 4;
    const extent = size + border * 2;
    const NS = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', `0 0 ${extent} ${extent}`);
    svg.setAttribute('shape-rendering', 'crispEdges');
    svg.setAttribute('role', 'img');
    if (title) svg.setAttribute('aria-label', title);

    const background = document.createElementNS(NS, 'rect');
    background.setAttribute('width', String(extent));
    background.setAttribute('height', String(extent));
    background.setAttribute('fill', '#ffffff');

    let d = '';
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        if (modules[y][x]) d += `M${x + border},${y + border}h1v1h-1z`;
      }
    }
    const path = document.createElementNS(NS, 'path');
    path.setAttribute('d', d);
    path.setAttribute('fill', '#000000');

    svg.append(background, path);
    return svg;
  }

  root.TrakkaQR = { encode, toSvg };
})(typeof self !== 'undefined' ? self : globalThis);
