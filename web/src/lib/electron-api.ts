/**
 * Electron renderer-side bridge. Mirrors the shape exposed by
 * electron/src/preload/preload.ts — must stay in sync.
 *
 * The whole app runs inside the Electron shell (see dev.sh), so `electronAPI`
 * is assumed to exist. No browser-mode fallback.
 */

export interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

export interface SavedPastedImage {
  path: string;
  name: string;
}

export interface ElectronAPI {
  readonly runtimeConfig: {
    readonly apiBase: string;
  };
  pickFiles: () => Promise<PickedLocalFile[]>;
  savePastedImage: (
    bytes: Uint8Array,
    mimeType: string,
    suggestedName?: string,
  ) => Promise<SavedPastedImage>;
}

declare global {
  interface Window {
    electronAPI: ElectronAPI;
  }
}

export const electronAPI: ElectronAPI = window.electronAPI;

// Image extensions we treat as "inline this via the multimodal channel" —
// same set the backend accepts in internal/agent/multimodal/attachments.go.
// Keep in sync.
const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "webp", "gif"]);

export function isImagePath(nameOrPath: string): boolean {
  const idx = nameOrPath.lastIndexOf(".");
  if (idx < 0) return false;
  return IMAGE_EXTS.has(nameOrPath.slice(idx + 1).toLowerCase());
}

// URL that the renderer can point <img src> at to display a local image
// file. Backed by the custom local-file:// protocol registered in the
// Electron main process.
//
// Format: local-file://l/<absolute-path-with-encoded-segments>
// The "l" host is a stub — Chromium normalises standard-scheme URLs with
// empty hosts (e.g. local-file:///Users/...) by absorbing the first path
// segment as the host and lowercasing it, which mangles the real path.
// Giving the URL an explicit host sidesteps that; the main handler
// ignores host and reads pathname.
export function localFileURL(absPath: string): string {
  const encoded = absPath
    .split("/")
    .map((seg) => encodeURIComponent(seg))
    .join("/");
  return `local-file://l${encoded}`;
}
