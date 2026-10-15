'use strict';

// Barcode scanner for "Courses" lists (quick-shop.js's 📷 button).
// TrakkaScanner.scan() resolves with the scanned digits, or null when the
// user gave up. Three ways, best first:
//
//   1. Inside the Android app, its native scanner (TrakkaApp.scanBarcode:
//      CameraX + ZXing, on the phone, no Google Play services needed). The
//      app never hands the camera to web pages (MainActivity refuses every
//      getUserMedia, whatever Android's own camera permission says), so this
//      is the only camera path there: an app too old to have scanBarcode
//      says it needs updating, rather than trying the browser camera and
//      reporting a refusal the system settings contradict.
//   2. In a browser, the camera in #barcode-scanner-sheet, frames decoded by
//      the native BarcodeDetector API when it reads EAN codes, else by
//      barcode.js (Firefox, Safari, Chromium without Google Play services).
//      Needs the server's Permissions-Policy to allow camera=(self).
//   3. Typing the digits printed under the bars, in the same sheet — always
//      offered, and the only way left without a camera.
(function () {
  const els = {
    sheet: document.getElementById('barcode-scanner-sheet'),
    close: document.getElementById('close-barcode-scanner-button'),
    viewport: document.getElementById('barcode-scanner-viewport'),
    video: document.getElementById('barcode-scanner-video'),
    status: document.getElementById('barcode-scanner-status'),
    form: document.getElementById('barcode-scanner-manual-form'),
    input: document.getElementById('barcode-scanner-manual-input'),
  };

  const t = (key, vars) => window.TrakkaI18n.t(key, vars);
  const FRAME_INTERVAL_MS = 140;
  // Frames are decoded at most this wide: enough pixels per bar for an EAN
  // filling the guide, cheap enough to run several times a second.
  const MAX_DECODE_WIDTH = 960;
  const DETECTOR_FORMATS = ['ean_13', 'ean_8', 'upc_a', 'upc_e'];

  // The scan in progress: { resolve, stream, timer, detector, canvas,
  // lastCode, restoreFocus }, or null.
  let session = null;

  // The Android app's native plugin, or null in a browser (see settings.js's
  // androidAppPlugin, the same feature test).
  function appPlugin() {
    const plugins = window.Capacitor && window.Capacitor.Plugins;
    return (plugins && plugins.TrakkaApp) || null;
  }

  function nativeScanner() {
    const plugin = appPlugin();
    // An app built before scanBarcode existed exposes no such method (the
    // bridge's plugin objects only hold real methods).
    return plugin && typeof plugin.scanBarcode === 'function' ? plugin : null;
  }

  // Digits only, 8 to 14 of them (EAN-8 to GTIN-14) — the server checks
  // again (internal/validate.Barcode).
  function normalizeCode(raw) {
    const digits = String(raw || '').replace(/[\s-]/g, '');
    return /^[0-9]{8,14}$/.test(digits) ? digits : null;
  }

  async function scan() {
    if (appPlugin()) {
      const native = nativeScanner();
      if (!native) return openSheet({ cameraAllowed: false, message: t('scanner.appUpdateNeeded') });
      try {
        const result = await native.scanBarcode();
        return normalizeCode(result && result.code);
      } catch (err) {
        if (err && err.code === 'cancelled') return null;
        // The scanner asked Android for the camera and was refused (now, or
        // for good: then Android no longer even asks), or the camera
        // wouldn't start: typing the code is what's left.
        const denied = err && err.code === 'camera_denied';
        return openSheet({ cameraAllowed: false, message: t(denied ? 'scanner.appCameraDenied' : 'scanner.cameraUnavailable'), tone: 'error' });
      }
    }
    return openSheet({ cameraAllowed: true });
  }

  function setStatus(message, tone = 'info') {
    els.status.textContent = message;
    els.status.classList.toggle('text-rose-600', tone === 'error');
    els.status.classList.toggle('dark:text-rose-400', tone === 'error');
    els.status.classList.toggle('text-slate-600', tone !== 'error');
    els.status.classList.toggle('dark:text-slate-300', tone !== 'error');
  }

  function openSheet({ cameraAllowed, message, tone = 'info' }) {
    if (session) finish(null);
    return new Promise((resolve) => {
      session = { resolve, stream: null, timer: null, detector: null, canvas: null, lastCode: null, restoreFocus: document.activeElement };
      els.input.value = '';
      els.viewport.hidden = true;
      els.sheet.hidden = false;
      TrakkaScrollLock.lock();
      if (cameraAllowed && navigator.mediaDevices && typeof navigator.mediaDevices.getUserMedia === 'function') {
        setStatus(t('scanner.starting'));
        startCamera(session);
      } else {
        setStatus(message || t('scanner.noCamera'), tone);
        els.input.focus();
      }
    });
  }

  async function startCamera(current) {
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: false,
        video: { facingMode: { ideal: 'environment' }, width: { ideal: 1280 }, height: { ideal: 720 } },
      });
    } catch (err) {
      if (session !== current) return;
      const denied = err && (err.name === 'NotAllowedError' || err.name === 'SecurityError');
      setStatus(t(denied ? 'scanner.cameraDenied' : 'scanner.cameraUnavailable'), 'error');
      els.input.focus();
      return;
    }
    if (session !== current) {
      stream.getTracks().forEach((track) => track.stop());
      return;
    }
    current.stream = stream;
    els.video.srcObject = stream;
    els.viewport.hidden = false;
    try {
      await els.video.play();
    } catch {
      // Autoplay of a muted inline video is allowed; nothing else to try.
    }
    current.detector = await nativeDetector();
    if (session !== current) return;
    setStatus(t('scanner.aim'));
    scheduleFrame(current);
  }

  // A BarcodeDetector able to read EAN codes, or null: Chromium on a device
  // without Google Play services reports no supported format at all.
  async function nativeDetector() {
    if (typeof window.BarcodeDetector !== 'function') return null;
    try {
      const supported = await window.BarcodeDetector.getSupportedFormats();
      const formats = DETECTOR_FORMATS.filter((f) => supported.includes(f));
      return formats.includes('ean_13') ? new window.BarcodeDetector({ formats }) : null;
    } catch {
      return null;
    }
  }

  function scheduleFrame(current) {
    current.timer = setTimeout(() => readFrame(current), FRAME_INTERVAL_MS);
  }

  async function readFrame(current) {
    if (session !== current) return;
    const video = els.video;
    if (video.readyState >= 2 && video.videoWidth > 0) {
      const code = current.detector ? await detectNative(current, video) : detectFallback(current, video);
      if (session !== current) return;
      if (code) {
        if (navigator.vibrate) navigator.vibrate(60);
        finish(code);
        return;
      }
    }
    scheduleFrame(current);
  }

  async function detectNative(current, video) {
    try {
      const results = await current.detector.detect(video);
      for (const result of results) {
        const code = normalizeCode(result.rawValue);
        if (code) return code;
      }
    } catch {
      // A transient failure on one frame; the next one is tried.
    }
    return null;
  }

  // barcode.js on the current frame. A code read on two scanlines of one
  // frame, or on two frames in a row, is accepted: one line alone could be
  // a misread that happens to pass the check digit.
  function detectFallback(current, video) {
    const scale = Math.min(1, MAX_DECODE_WIDTH / video.videoWidth);
    const width = Math.round(video.videoWidth * scale);
    const height = Math.round(video.videoHeight * scale);
    if (!current.canvas) current.canvas = document.createElement('canvas');
    const canvas = current.canvas;
    if (canvas.width !== width) canvas.width = width;
    if (canvas.height !== height) canvas.height = height;
    const ctx = canvas.getContext('2d', { willReadFrequently: true });
    ctx.drawImage(video, 0, 0, width, height);
    const pixels = ctx.getImageData(0, 0, width, height);
    const result = window.TrakkaBarcode.decode(window.TrakkaBarcode.luminanceFromRGBA(pixels.data, width, height), width, height);
    if (!result) {
      current.lastCode = null;
      return null;
    }
    if (result.votes >= 2 || result.code === current.lastCode) return result.code;
    current.lastCode = result.code;
    return null;
  }

  function stopCamera(current) {
    clearTimeout(current.timer);
    if (current.stream) current.stream.getTracks().forEach((track) => track.stop());
    els.video.srcObject = null;
  }

  // Ends the scan in progress with code (null: cancelled).
  function finish(code) {
    const current = session;
    if (!current) return;
    session = null;
    stopCamera(current);
    els.sheet.hidden = true;
    TrakkaScrollLock.unlock();
    current.resolve(code);
  }

  els.form.addEventListener('submit', (event) => {
    event.preventDefault();
    const code = normalizeCode(els.input.value);
    if (!code) {
      setStatus(t('scanner.invalidCode'), 'error');
      els.input.focus();
      return;
    }
    finish(code);
  });
  els.close.addEventListener('click', () => finish(null));
  // The backdrop closes it, as every other sheet: also what gestures.js's
  // system back relies on (the id ends in -sheet).
  els.sheet.addEventListener('click', (event) => {
    if (event.target === els.sheet) finish(null);
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && session) finish(null);
  });
  // Never keep the camera running behind a backgrounded tab or app.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden && session) finish(null);
  });

  window.TrakkaScanner = { scan, normalizeCode };
})();
