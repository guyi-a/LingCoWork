/**
 * Interview-Agent Electron main process (dev-only shell).
 *
 * Chromium window pointed at the Vite dev server on :5173. Frontend still
 * hits the Go backend directly on :9001 — no proxy, no API-base injection.
 *
 * IPC surface (kept intentionally tiny — see preload):
 *   'pick-files'         → PickedLocalFile[]  (native file/folder picker)
 *   'save-pasted-image'  → { path, name }     (persist pasted/dropped image
 *                                              bytes to a scratch dir so the
 *                                              backend can read from disk on
 *                                              send AND on replay)
 *
 * Protocol surface:
 *   local-file://<abs-path>                    (renderer <img> loads local
 *                                              image files by absolute path
 *                                              — chip thumbnails, etc.)
 *
 * Explicitly NOT doing: spawning the Go backend, port probing, packaging,
 * auto-update, generic IPC bridge.
 */

import { app, BrowserWindow, dialog, ipcMain, net, protocol } from 'electron';
import { existsSync } from 'node:fs';
import { mkdir, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const DEV_RENDERER_URL = 'http://localhost:5173';

app.setName('Interview Agent');

// local-file must be registered as privileged BEFORE app.whenReady per the
// Electron contract; the handler is installed inside whenReady. Purpose:
// give the renderer a scheme it can point <img> at for local image
// thumbnails (chip previews, transcript replay). Dev-only shell + trusted
// renderer, so no path allowlist — the renderer already knows the absolute
// paths it received via pick-files / save-pasted-image.
protocol.registerSchemesAsPrivileged([
  {
    scheme: 'local-file',
    privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true },
  },
]);

// Structured result the renderer will see. Kept as an interface for the
// preload's TS side (which shares this shape via manual duplication —
// nothing worth a shared package for two fields).
interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

interface SavedPastedImage {
  path: string;
  name: string;
}

// Where pasted / dropped images go. Kept next to the backend's workspace
// state so `<repo>/.workspace/` is the single Finder location a user can
// look at to see everything the app writes to disk. Layout:
//   <repo>/.workspace/
//     <project-id>/          ← project workspaces (backend-owned)
//     _attachments/          ← pasted / dropped images (this handler)
//
// The main.js file lives at <repo>/electron/out/main/main.js, so three
// levels up from __dirname is the repo root. If that path doesn't
// contain a .workspace/ (e.g. Electron is somehow launched detached from
// the repo layout), fall back to Electron's per-app userData dir so
// paste still works — just in a less-discoverable spot, with a warning.
function attachmentsDir(): string {
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const preferred = path.join(repoRoot, '.workspace', '_attachments');
  const workspaceRoot = path.join(repoRoot, '.workspace');
  if (existsSync(workspaceRoot)) {
    return preferred;
  }
  const fallback = path.join(app.getPath('userData'), 'attachments');
  console.warn(
    `[attachments] .workspace not found at ${workspaceRoot}, falling back to ${fallback}`,
  );
  return fallback;
}

// Best-effort file extension guess from a MIME type. Pasted images from
// browsers usually arrive as image/png; drops carry their real extension
// which the renderer can suggest to us.
function extFromMime(mime: string): string {
  switch (mime) {
    case 'image/png': return '.png';
    case 'image/jpeg': return '.jpg';
    case 'image/webp': return '.webp';
    case 'image/gif': return '.gif';
    case 'image/bmp': return '.bmp';
    default: return '.png';
  }
}

function registerIpc(): void {
  ipcMain.handle('pick-files', async (): Promise<PickedLocalFile[]> => {
    const res = await dialog.showOpenDialog({
      properties: ['openFile', 'openDirectory', 'multiSelections'],
    });
    if (res.canceled || res.filePaths.length === 0) return [];

    return Promise.all(
      res.filePaths.map(async (p) => {
        let isDirectory = false;
        try {
          const s = await stat(p);
          isDirectory = s.isDirectory();
        } catch {
          // stat failure is rare (the OS just handed us this path) but not
          // worth aborting the whole selection over. Treat as file.
        }
        return {
          path: p,
          name: path.basename(p),
          isDirectory,
        };
      }),
    );
  });

  ipcMain.handle(
    'save-pasted-image',
    async (
      _event,
      payload: { bytes: Uint8Array; mimeType: string; suggestedName?: string },
    ): Promise<SavedPastedImage> => {
      // Renderer serialises Uint8Array over IPC as Buffer-alike; wrap
      // explicitly so fs.writeFile is happy on all node versions.
      const bytes = Buffer.from(payload.bytes);
      const dir = attachmentsDir();
      await mkdir(dir, { recursive: true });

      const suggested = (payload.suggestedName ?? '').trim();
      // Strip anything path-shaped from the suggested name to keep the
      // filesystem hierarchy flat and predictable.
      const safeSuggested = suggested
        ? path.basename(suggested).replace(/[/\\]/g, '_')
        : '';
      const ext = safeSuggested
        ? path.extname(safeSuggested).toLowerCase() || extFromMime(payload.mimeType)
        : extFromMime(payload.mimeType);
      const stem = safeSuggested
        ? path.basename(safeSuggested, path.extname(safeSuggested)) || 'pasted'
        : 'pasted';
      const uuid = randomUUID().slice(0, 8);
      const name = `${stem}-${uuid}${ext}`;
      const abs = path.join(dir, name);

      await writeFile(abs, bytes);
      return { path: abs, name };
    },
  );
}

function registerLocalFileProtocol(): void {
  protocol.handle('local-file', (req) => {
    // URL shape: local-file://l/Users/guyi/Downloads/x.png
    //   → host === 'l' (stub — see localFileURL in the renderer for why)
    //   → pathname === '/Users/guyi/Downloads/x.png'
    // Decode percent-encoded chars so paths with spaces / CJK work.
    const url = new URL(req.url);
    const abs = decodeURIComponent(url.pathname);
    return net.fetch(pathToFileURL(abs).toString());
  });
}

function createWindow(): void {
  const preloadPath = path.join(__dirname, '../preload/preload.js');

  const win = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 1024,
    minHeight: 640,
    frame: false,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      preload: preloadPath,
      devTools: true,
    },
  });

  win.loadURL(DEV_RENDERER_URL);

  if (shouldOpenDevTools()) {
    win.webContents.openDevTools();
  }
}

// Opt-in only. DevTools stay available via the menu / keyboard shortcut
// (webPreferences.devTools is on); this just controls whether the panel is
// already open when the window appears.
function shouldOpenDevTools(): boolean {
  const raw = (process.env.INTERVIEW_ELECTRON_DEVTOOLS ?? '').trim().toLowerCase();
  return ['1', 'true', 'on', 'yes'].includes(raw);
}

app.whenReady().then(() => {
  registerIpc();
  registerLocalFileProtocol();
  createWindow();

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    }
  });
});

app.on('window-all-closed', () => {
  app.quit();
});
