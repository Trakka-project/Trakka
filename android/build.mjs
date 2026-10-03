#!/usr/bin/env node
// Builds Trakka's Android app: a Capacitor shell (native/) that opens the PWA of whichever Trakka
// server the user connects it to, from the connect screen bundled in www/. Nothing here touches
// the Go server. Guide: docs/MOBILE_BUILD.md.
//
//   node build.mjs keystore   create the signing key (once, ever)
//   node build.mjs build      build out/trakka.apk
//
// Normally run by `make apk-keystore` and `make build-apk-capacitor`, inside the image built
// from android/Dockerfile, which provides Node.js, JDK 21 and the Android SDK. Running it
// directly works too, with Node.js 22+, JAVA_HOME pointing to a JDK 21 and ANDROID_HOME to an
// Android SDK holding the packages the Dockerfile installs.
//
// Settings come from the environment first, then apk.env, then signing/keystore.env: the first
// source that sets a variable wins. apk.env.example describes every variable.

import { spawn } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ANDROID_DIR = path.dirname(fileURLToPath(import.meta.url));
const REPO_DIR = path.dirname(ANDROID_DIR);
const NATIVE_DIR = path.join(ANDROID_DIR, 'native');
const APK_ENV_FILE = path.join(ANDROID_DIR, 'apk.env');
const KEYSTORE_ENV_FILE = path.join(ANDROID_DIR, 'signing', 'keystore.env');
const LOCKFILE = path.join(ANDROID_DIR, 'package-lock.json');
// Written after `npm ci`, so that later builds skip it until package-lock.json changes.
const INSTALL_STAMP = path.join(ANDROID_DIR, 'node_modules', '.trakka-installed-lock-sha256');
const BUILT_APK = path.join(NATIVE_DIR, 'app', 'build', 'outputs', 'apk', 'release', 'app-release.apk');
const OUT_DIR = path.join(ANDROID_DIR, 'out');
const APK_FILE = path.join(OUT_DIR, 'trakka.apk');

// What the app must declare (native/app/src/main/AndroidManifest.xml explains each one).
const REQUIRED_PERMISSIONS = [
  'android.permission.INTERNET',
  'android.permission.CAMERA',
  'android.permission.VIBRATE',
  'android.permission.POST_NOTIFICATIONS',
];

const USAGE = `Usage: node build.mjs <command>

  keystore   create the signing key (android/signing/), once
  build      build android/out/trakka.apk

Settings: android/apk.env.example. Guide: docs/MOBILE_BUILD.md.`;

// A mistake in the settings or the environment: reported without a stack trace.
class UsageError extends Error {}

const relative = (file) => path.relative(REPO_DIR, file);
const step = (message) => console.log(`\n▶ ${message}`);

// Minimal KEY=value reader for apk.env and signing/keystore.env: no interpolation, surrounding
// quotes stripped, variables already set in the environment win.
function loadEnvFile(file) {
  let text;
  try {
    text = fs.readFileSync(file, 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return;
    throw err;
  }
  text.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) return;
    const match = line.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
    if (!match) throw new UsageError(`${relative(file)}:${index + 1}: expected KEY=value.`);
    let value = match[2];
    if (/^(["']).*\1$/.test(value)) value = value.slice(1, -1);
    if (!process.env[match[1]]) process.env[match[1]] = value;
  });
}

function setting(name, fallback) {
  const value = process.env[name];
  return value === undefined || value === '' ? fallback : value;
}

function signingConfig() {
  const keystorePassword = setting('ANDROID_KEYSTORE_PASSWORD');
  return {
    keystore: path.resolve(ANDROID_DIR, setting('ANDROID_KEYSTORE', 'signing/trakka.keystore')),
    keyAlias: setting('ANDROID_KEY_ALIAS', 'trakka'),
    keystorePassword,
    keyPassword: setting('ANDROID_KEY_PASSWORD', keystorePassword),
  };
}

function versionConfig() {
  const versionCode = Number(setting('ANDROID_VERSION_CODE', '1'));
  if (!Number.isInteger(versionCode) || versionCode < 1 || versionCode > 2100000000) {
    throw new UsageError('ANDROID_VERSION_CODE must be a whole number from 1 to 2100000000.');
  }
  return { versionCode, versionName: setting('ANDROID_VERSION_NAME', '1.0.0') };
}

function checkPassword(name, value) {
  if (value === undefined) {
    throw new UsageError(`${name} is not set. \`make apk-keystore\` stores the passwords of the key it ` +
      `creates in ${relative(KEYSTORE_ENV_FILE)}; for a key of your own, set it in the environment ` +
      'or in android/apk.env.');
  }
  if (value.length < 6) throw new UsageError(`${name} must be at least 6 characters long.`);
}

function toolchain({ sdk }) {
  const javaHome = process.env.JAVA_HOME;
  const keytool = javaHome ? path.join(javaHome, 'bin', 'keytool') : '';
  if (!keytool || !fs.existsSync(keytool)) {
    throw new UsageError('JAVA_HOME must point to a JDK 21 (no keytool found there). `make apk-keystore` ' +
      'and `make build-apk-capacitor` run this script in a container that has one.');
  }
  // apksigner and the Gradle wrapper look for java on the PATH.
  const env = { ...process.env, PATH: `${path.join(javaHome, 'bin')}${path.delimiter}${process.env.PATH ?? ''}` };
  if (!sdk) return { keytool, env };

  const sdkDir = setting('ANDROID_HOME', setting('ANDROID_SDK_ROOT'));
  const buildToolsRoot = sdkDir ? path.join(sdkDir, 'build-tools') : '';
  const buildTools = buildToolsRoot && fs.existsSync(buildToolsRoot)
    ? fs.readdirSync(buildToolsRoot).sort((a, b) => a.localeCompare(b, 'en', { numeric: true })).pop()
    : undefined;
  if (!buildTools) {
    throw new UsageError('ANDROID_HOME must point to an Android SDK with build-tools installed. ' +
      '`make build-apk-capacitor` runs this script in a container that has one.');
  }
  return {
    keytool,
    env,
    apksigner: path.join(buildToolsRoot, buildTools, 'apksigner'),
    aapt2: path.join(buildToolsRoot, buildTools, 'aapt2'),
  };
}

// Runs a command with the terminal attached, or captures its stdout.
function run(command, args, { cwd = ANDROID_DIR, env = process.env, capture = false } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: [capture ? 'ignore' : 'inherit', capture ? 'pipe' : 'inherit', 'inherit'],
    });
    let output = '';
    if (capture) child.stdout.on('data', (chunk) => { output += chunk; });
    child.once('error', (err) => reject(new Error(`Could not run ${command}: ${err.message}`)));
    child.once('close', (code, signal) => {
      if (code === 0) resolve(output);
      else reject(new Error(`\`${path.basename(command)} ${args.join(' ')}\` failed (${signal ?? `exit code ${code}`}).`));
    });
  });
}

async function keystoreFingerprint(tools, signing, password) {
  const listing = await run(tools.keytool, [
    '-list', '-v', '-keystore', signing.keystore, '-alias', signing.keyAlias,
    '-storepass:env', 'TRAKKA_KEYSTORE_PASSWORD',
  ], { env: { ...tools.env, TRAKKA_KEYSTORE_PASSWORD: password }, capture: true });
  const match = listing.match(/SHA256:\s*((?:[0-9A-F]{2}:){31}[0-9A-F]{2})/i);
  if (!match) throw new Error(`keytool did not report the SHA-256 fingerprint of ${relative(signing.keystore)}.`);
  return match[1].toUpperCase();
}

async function createKeystore() {
  const signing = signingConfig();
  const tools = toolchain({ sdk: false });
  if (fs.existsSync(signing.keystore)) {
    throw new UsageError(`${relative(signing.keystore)} already exists. Refusing to replace a signing key: ` +
      'the app installed with it could no longer be updated. Delete it yourself if you really mean ' +
      'to start over (docs/MOBILE_BUILD.md, "The signing key").');
  }
  const generated = signing.keystorePassword === undefined;
  const password = generated ? crypto.randomBytes(24).toString('hex') : signing.keystorePassword;
  checkPassword('ANDROID_KEYSTORE_PASSWORD', password);
  if (signing.keyPassword !== undefined && signing.keyPassword !== password) {
    throw new UsageError('A PKCS12 keystore has a single password, used for its key too: leave ' +
      'ANDROID_KEY_PASSWORD unset, or equal to ANDROID_KEYSTORE_PASSWORD.');
  }

  step(`Creating ${relative(signing.keystore)}`);
  fs.mkdirSync(path.dirname(signing.keystore), { recursive: true, mode: 0o700 });
  await run(tools.keytool, [
    '-genkeypair', '-noprompt',
    '-keystore', signing.keystore, '-storetype', 'PKCS12',
    '-alias', signing.keyAlias,
    '-keyalg', 'RSA', '-keysize', '4096', '-validity', '10000',
    '-dname', 'CN=Trakka',
    '-storepass:env', 'TRAKKA_KEYSTORE_PASSWORD',
    '-keypass:env', 'TRAKKA_KEYSTORE_PASSWORD',
  ], { env: { ...tools.env, TRAKKA_KEYSTORE_PASSWORD: password } });
  fs.chmodSync(signing.keystore, 0o600);

  if (generated) {
    fs.mkdirSync(path.dirname(KEYSTORE_ENV_FILE), { recursive: true, mode: 0o700 });
    fs.writeFileSync(KEYSTORE_ENV_FILE, [
      `# Passwords of ${relative(signing.keystore)}, generated by \`make apk-keystore\`.`,
      '# Keep this file with the keystore, private and backed up: without both, no',
      '# update can ever be installed over the app (docs/MOBILE_BUILD.md).',
      `ANDROID_KEYSTORE_PASSWORD=${password}`,
      `ANDROID_KEY_PASSWORD=${password}`,
      '',
    ].join('\n'), { mode: 0o600 });
  }

  const fingerprint = await keystoreFingerprint(tools, signing, password);
  console.log(`
Created ${relative(signing.keystore)} (alias "${signing.keyAlias}")${generated ? `, passwords in ${relative(KEYSTORE_ENV_FILE)}` : ''}.
Certificate SHA-256: ${fingerprint}

Back both files up somewhere safe: every future APK must be signed with this
key, or Android refuses to install it over the existing app. Next: make build-apk-capacitor`);
}

// `npm ci` from package-lock.json (Capacitor's CLI, and the Android library the native project
// compiles), skipped while the lockfile is unchanged since the last install.
async function installDependencies() {
  const lockHash = crypto.createHash('sha256').update(fs.readFileSync(LOCKFILE)).digest('hex');
  let installed = '';
  try {
    installed = fs.readFileSync(INSTALL_STAMP, 'utf8').trim();
  } catch {
    // Not installed yet, or by something else than this script.
  }
  if (installed === lockHash) return;
  step('Installing the pinned npm packages (npm ci)');
  await run('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund']);
  fs.writeFileSync(INSTALL_STAMP, `${lockHash}\n`);
}

async function inspectApk(tools, apk) {
  const certificates = await run(tools.apksigner, ['verify', '--print-certs', apk], { env: tools.env, capture: true });
  const digest = certificates.match(/^Signer #1 certificate SHA-256 digest: ([0-9a-f]{64})$/m);
  const badging = await run(tools.aapt2, ['dump', 'badging', apk], { capture: true });
  const pkg = badging.match(/^package: name='([^']+)' versionCode='(\d+)' versionName='([^']*)'/m);
  if (!digest || !pkg) throw new Error(`Could not read the signer and package of ${relative(apk)}.`);
  return {
    fingerprint: digest[1].toUpperCase().match(/../g).join(':'),
    packageId: pkg[1],
    versionCode: Number(pkg[2]),
    versionName: pkg[3],
    permissions: [...badging.matchAll(/^uses-permission: name='([^']+)'/gm)].map((match) => match[1]),
  };
}

async function build() {
  const version = versionConfig();
  const signing = signingConfig();
  const tools = toolchain({ sdk: true });
  if (!fs.existsSync(signing.keystore)) {
    throw new UsageError(`No signing key at ${relative(signing.keystore)}: create one with ` +
      '`make apk-keystore`, or point ANDROID_KEYSTORE to yours.');
  }
  checkPassword('ANDROID_KEYSTORE_PASSWORD', signing.keystorePassword);
  checkPassword('ANDROID_KEY_PASSWORD', signing.keyPassword);

  await installDependencies();
  step('Copying the connect screen into the native project (cap sync)');
  await run('npx', ['--no-install', 'cap', 'sync', 'android']);

  step(`Building and signing the APK, version ${version.versionName} (${version.versionCode})`);
  fs.rmSync(BUILT_APK, { force: true });
  await run(path.join(NATIVE_DIR, 'gradlew'), [
    '--no-daemon',
    `-PtrakkaVersionCode=${version.versionCode}`,
    `-PtrakkaVersionName=${version.versionName}`,
    'assembleRelease',
  ], {
    cwd: NATIVE_DIR,
    // Passwords go through the environment, not the command line: see app/build.gradle.
    env: {
      ...tools.env,
      TRAKKA_KEYSTORE_FILE: signing.keystore,
      TRAKKA_KEYSTORE_PASSWORD: signing.keystorePassword,
      TRAKKA_KEY_ALIAS: signing.keyAlias,
      TRAKKA_KEY_PASSWORD: signing.keyPassword,
    },
  });

  step('Checking the result');
  const apk = await inspectApk(tools, BUILT_APK);
  const missing = REQUIRED_PERMISSIONS.filter((name) => !apk.permissions.includes(name));
  if (missing.length > 0) throw new Error(`The APK does not declare ${missing.join(', ')}.`);
  fs.mkdirSync(OUT_DIR, { recursive: true });
  fs.copyFileSync(BUILT_APK, APK_FILE);

  console.log(`
Built ${relative(APK_FILE)}
  package      ${apk.packageId}, version ${apk.versionName} (${apk.versionCode})
  permissions  ${apk.permissions.map((name) => name.replace(/^android\.permission\./, '')).join(', ')}
  signed by    SHA-256 ${apk.fingerprint}
Install it on the phone, open it, and connect it to your server (docs/MOBILE_BUILD.md).`);
}

const COMMANDS = { keystore: createKeystore, build };

async function main() {
  const command = process.argv[2] ?? 'build';
  if (['help', '-h', '--help'].includes(command)) {
    console.log(USAGE);
    return;
  }
  if (!Object.hasOwn(COMMANDS, command)) {
    console.error(USAGE);
    process.exitCode = 2;
    return;
  }
  loadEnvFile(APK_ENV_FILE);
  loadEnvFile(KEYSTORE_ENV_FILE);
  await COMMANDS[command]();
}

main().catch((err) => {
  console.error(`\n✘ ${err instanceof UsageError ? err.message : err.stack}`);
  process.exitCode = 1;
});
