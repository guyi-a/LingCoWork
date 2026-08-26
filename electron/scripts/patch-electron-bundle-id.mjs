#!/usr/bin/env node
// Rewrite this project's local Electron.app CFBundleIdentifier to a
// project-specific value so macOS LaunchServices can tell it apart from
// other dev-mode Electron apps on the same machine (krow-app, PentaLoom,
// etc.). Symptom without this patch: launching one dev app pops up the
// wrong Dock icon or the wrong project's default_app welcome page.
//
// Idempotent, macOS-only, non-fatal on every error path — a failure here
// must never break `pnpm install`. Runs from `postinstall`.

import { execSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';

if (process.platform !== 'darwin') process.exit(0);

const NEW_ID = 'com.guyi.lingcowork.dev';
const LSREGISTER =
  '/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister';

const require = createRequire(import.meta.url);
let electronDir;
try {
  electronDir = path.dirname(require.resolve('electron/package.json'));
} catch {
  console.warn('[patch-electron-bundle-id] electron not installed, skipping');
  process.exit(0);
}

const appPath = path.join(electronDir, 'dist', 'Electron.app');
const plistPath = path.join(appPath, 'Contents', 'Info.plist');
if (!existsSync(plistPath)) {
  console.warn(`[patch-electron-bundle-id] ${plistPath} not found, skipping`);
  process.exit(0);
}

let current;
try {
  current = execSync(
    `/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "${plistPath}"`,
    { encoding: 'utf8' },
  ).trim();
} catch (err) {
  console.warn('[patch-electron-bundle-id] read plist failed, skipping:', err.message);
  process.exit(0);
}

if (current === NEW_ID) {
  console.log(`[patch-electron-bundle-id] already ${NEW_ID}, nothing to do`);
  process.exit(0);
}

try {
  execSync(
    `/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier ${NEW_ID}" "${plistPath}"`,
    { stdio: 'pipe' },
  );
} catch (err) {
  console.warn('[patch-electron-bundle-id] write plist failed, skipping:', err.message);
  process.exit(0);
}

// Prebuilt Electron is signed adhoc and its Info.plist isn't bound, so
// changing CFBundleIdentifier "works" without a resign — but we resign
// anyway so LaunchServices picks up the new identity on the very next
// launch instead of caching the old one.
try {
  execSync(`codesign --force --deep --sign - "${appPath}"`, { stdio: 'pipe' });
} catch (err) {
  console.warn('[patch-electron-bundle-id] adhoc resign skipped:', err.message);
}

try {
  execSync(`"${LSREGISTER}" "${appPath}"`, { stdio: 'pipe' });
} catch {
  // lsregister failure is fine — next app launch will reindex anyway.
}

console.log(`[patch-electron-bundle-id] patched Electron bundle id: ${current} -> ${NEW_ID}`);
