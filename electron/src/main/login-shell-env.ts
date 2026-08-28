import { execFile } from 'node:child_process';
import { basename } from 'node:path';

/**
 * Apps launched from Finder inherit launchd's minimal PATH instead of the
 * user's terminal PATH. Capture a login shell once before starting the Go
 * sidecar so MCP stdio servers and other external commands can be resolved.
 *
 * A login shell reads files such as .zprofile, but not the interactive zsh
 * and bash rc files where nvm/fnm/asdf are commonly initialized, so source
 * those explicitly.
 */
export interface CaptureLoginShellEnvOptions {
  shell?: string;
  home?: string;
  platform?: NodeJS.Platform;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 5_000;

const RC_SOURCE_BY_SHELL: Record<string, string> = {
  zsh: 'source ~/.zshrc 2>/dev/null; ',
  bash: 'source ~/.bashrc 2>/dev/null; ',
};

export function parseNullDelimitedEnv(output: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const entry of output.split('\0')) {
    if (!entry) continue;
    const separator = entry.indexOf('=');
    if (separator === -1) continue;
    env[entry.slice(0, separator)] = entry.slice(separator + 1);
  }
  return env;
}

/**
 * Best-effort by design: an absent shell, a broken rc file, or a timeout must
 * not prevent LingCoWork from starting with its original environment.
 */
export function captureLoginShellEnv(
  options: CaptureLoginShellEnvOptions = {},
): Promise<Record<string, string> | undefined> {
  const platform = options.platform ?? process.platform;
  if (platform === 'win32') {
    return Promise.resolve(undefined);
  }

  const shell = options.shell ?? process.env.SHELL ?? '/bin/zsh';
  const rcSource = RC_SOURCE_BY_SHELL[basename(shell)] ?? '';
  const seedEnv: Record<string, string> = {
    HOME: options.home ?? process.env.HOME ?? '',
    USER: process.env.USER ?? '',
    SHELL: shell,
    TERM: 'xterm',
  };

  return new Promise((resolve) => {
    execFile(
      shell,
      ['-l', '-c', `${rcSource}env -0`],
      { timeout: options.timeoutMs ?? DEFAULT_TIMEOUT_MS, env: seedEnv },
      (error, stdout) => {
        if (error) {
          resolve(undefined);
          return;
        }
        resolve(parseNullDelimitedEnv(stdout));
      },
    );
  });
}

type CaptureFn = (
  options: CaptureLoginShellEnvOptions,
) => Promise<Record<string, string> | undefined>;

let cachedCapture: Promise<Record<string, string> | undefined> | undefined;

export interface ApplyLoginShellEnvFixOptions extends CaptureLoginShellEnvOptions {
  capture?: CaptureFn;
}

/**
 * Preserve the existing PATH order and append only directories discovered
 * from the user's shell. The capture promise is shared for the process
 * lifetime so repeated callers never start additional shells.
 */
export async function applyLoginShellEnvFix(
  env: NodeJS.ProcessEnv = process.env,
  options: ApplyLoginShellEnvFixOptions = {},
): Promise<void> {
  const capture = options.capture ?? captureLoginShellEnv;
  if (!cachedCapture) {
    cachedCapture = capture(options).catch(() => undefined);
  }

  const capturedPath = (await cachedCapture)?.PATH;
  if (!capturedPath) return;

  const existing = new Set((env.PATH ?? '').split(':').filter(Boolean));
  const additions = capturedPath
    .split(':')
    .filter((directory) => directory && !existing.has(directory));
  if (additions.length === 0) return;

  env.PATH = [env.PATH, ...additions].filter(Boolean).join(':');
}
