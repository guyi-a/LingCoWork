/**
 * LingCoWork Electron main process.
 *
 * Development loads Vite on :5173. Packaged builds serve web assets from
 * Resources/web through the restricted lingcowork://app protocol.
 *
 * IPC surface (kept intentionally tiny — see preload):
 *   'pick-files'         → PickedLocalFile[]  (native file/folder picker)
 *   'pick-directory'     → string | null      (project workspace picker)
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
 * The renderer talks directly to the local Go backend on 127.0.0.1:9001.
 */

import { app, BrowserWindow, dialog, ipcMain, net, protocol, shell } from 'electron';
import { existsSync } from 'node:fs';
import { chmod, copyFile, mkdir, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';

import { BackendSidecar } from './backend.js';
import { applyLoginShellEnvFix } from './login-shell-env.js';

const DEV_RENDERER_URL = 'http://localhost:5173';
const PROD_RENDERER_URL = 'lingcowork://app/';
let backendSidecar: BackendSidecar | null = null;
let quitAfterBackendStops = false;

app.setName('LingCoWork');
const userDataOverride = process.env.LINGCOWORK_USER_DATA_DIR?.trim();
if (userDataOverride) {
  app.setPath('userData', path.resolve(userDataOverride));
}

// local-file must be registered as privileged BEFORE app.whenReady per the
// Electron contract; the handler is installed inside whenReady. Purpose:
// give the renderer a scheme it can point <img> at for local image
// thumbnails (chip previews, transcript replay). Dev-only shell + trusted
// renderer, so no path allowlist — the renderer already knows the absolute
// paths it received via pick-files / save-pasted-image.
protocol.registerSchemesAsPrivileged([
  {
    scheme: 'lingcowork',
    privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true },
  },
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
  if (app.isPackaged) {
    return path.join(app.getPath('userData'), 'attachments');
  }

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
  ipcMain.handle('pick-directory', async (): Promise<string | null> => {
    const res = await dialog.showOpenDialog({
      properties: ['openDirectory', 'createDirectory'],
    });
    if (res.canceled || res.filePaths.length === 0) return null;
    return res.filePaths[0] ?? null;
  });

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

function registerAppProtocol(): void {
  if (!app.isPackaged) return;

  const webRoot = path.join(process.resourcesPath, 'web');
  const indexPath = path.join(webRoot, 'index.html');

  protocol.handle('lingcowork', async (req) => {
    const url = new URL(req.url);
    if (url.host !== 'app') {
      return new Response('Not found', { status: 404 });
    }

    const relativePath = decodeURIComponent(url.pathname).replace(/^\/+/, '');
    const candidate = path.resolve(webRoot, relativePath || 'index.html');
    const insideWebRoot =
      candidate === webRoot || candidate.startsWith(`${webRoot}${path.sep}`);

    if (insideWebRoot) {
      try {
        if ((await stat(candidate)).isFile()) {
          return net.fetch(pathToFileURL(candidate).toString());
        }
      } catch {
        // BrowserRouter deep links intentionally fall through to index.html.
      }
    }

    return net.fetch(pathToFileURL(indexPath).toString());
  });
}

function createWindow(): void {
  const preloadPath = path.join(__dirname, '../preload/preload.js');
  const iconPath = app.isPackaged
    ? path.join(process.resourcesPath, 'icon.png')
    : path.join(__dirname, '..', '..', 'assets', 'icon-source.png');

  const win = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 1024,
    minHeight: 640,
    frame: false,
    show: false,
    icon: iconPath,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      preload: preloadPath,
      devTools: !app.isPackaged || shouldOpenDevTools(),
    },
  });

  win.once('ready-to-show', () => win.show());
  void win.loadURL(app.isPackaged ? PROD_RENDERER_URL : DEV_RENDERER_URL).catch((error) => {
    dialog.showErrorBox('LingCoWork failed to load', String(error));
  });

  if (shouldOpenDevTools()) {
    win.webContents.openDevTools();
  }
}

// Opt-in only. DevTools stay available via the menu / keyboard shortcut
// (webPreferences.devTools is on); this just controls whether the panel is
// already open when the window appears.
function shouldOpenDevTools(): boolean {
  const raw = (
    process.env.LINGCOWORK_ELECTRON_DEVTOOLS ??
    process.env.INTERVIEW_ELECTRON_DEVTOOLS ??
    ''
  ).trim().toLowerCase();
  return ['1', 'true', 'on', 'yes'].includes(raw);
}

async function ensureRuntimeConfig(): Promise<boolean> {
  const runtimeHome = app.getPath('userData');
  const configPath = path.join(runtimeHome, '.env');
  if (existsSync(configPath)) return true;

  await mkdir(runtimeHome, { recursive: true });
  const choice = await dialog.showMessageBox({
    type: 'info',
    title: 'Set up LingCoWork',
    message: 'LingCoWork needs an environment file before its first launch.',
    detail:
      'Import an existing .env file, or create a template in the application data directory and fill in DEEPSEEK_API_KEY.',
    buttons: ['Import .env', 'Create template and exit'],
    defaultId: 0,
    cancelId: 1,
  });

  if (choice.response === 0) {
    const selected = await dialog.showOpenDialog({
      title: 'Import LingCoWork environment file',
      message: 'Choose the .env file that contains your API configuration.',
      buttonLabel: 'Import',
      properties: ['openFile', 'showHiddenFiles'],
    });
    if (!selected.canceled && selected.filePaths[0]) {
      await copyFile(selected.filePaths[0], configPath);
      await chmod(configPath, 0o600);
      return true;
    }
  }

  const templatePath = path.join(process.resourcesPath, 'config', '.env.example');
  await copyFile(templatePath, configPath);
  await chmod(configPath, 0o600);
  shell.showItemInFolder(configPath);
  await dialog.showMessageBox({
    type: 'info',
    title: 'Configuration template created',
    message: 'Fill in the environment file, then reopen LingCoWork.',
    detail: configPath,
    buttons: ['OK'],
  });
  return false;
}

async function startPackagedBackend(): Promise<void> {
  const runtimeHome = app.getPath('userData');
  const executableName = process.platform === 'win32'
    ? 'lingcowork-api.exe'
    : 'lingcowork-api';
  const executablePath = path.join(process.resourcesPath, 'backend', executableName);

  backendSidecar = new BackendSidecar({
    executablePath,
    runtimeHome,
    onUnexpectedExit: (message) => {
      dialog.showErrorBox(
        'LingCoWork backend stopped',
        `${message}\n\nSee ${path.join(runtimeHome, 'logs', 'backend.log')} for details.`,
      );
      app.quit();
    },
  });
  await backendSidecar.start();
}

app.whenReady().then(async () => {
  registerIpc();
  registerLocalFileProtocol();
  registerAppProtocol();

  if (app.isPackaged) {
    try {
      // Finder-launched apps inherit a minimal launchd PATH. Restore the
      // user's shell PATH before the Go sidecar can spawn MCP servers or
      // other commands such as node, npx, and uvx.
      await applyLoginShellEnvFix();
      if (!(await ensureRuntimeConfig())) {
        app.quit();
        return;
      }
      await startPackagedBackend();
    } catch (error) {
      const runtimeHome = app.getPath('userData');
      dialog.showErrorBox(
        'LingCoWork could not start',
        `${error instanceof Error ? error.message : String(error)}\n\nSee ${path.join(runtimeHome, 'logs', 'backend.log')} for details.`,
      );
      app.quit();
      return;
    }
  }

  createWindow();

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    }
  });
});

app.on('before-quit', (event) => {
  if (quitAfterBackendStops || !backendSidecar) return;

  event.preventDefault();
  void backendSidecar.stop().finally(() => {
    backendSidecar = null;
    quitAfterBackendStops = true;
    app.quit();
  });
});

app.on('window-all-closed', () => {
  app.quit();
});
