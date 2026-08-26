import { spawn, type ChildProcess } from 'node:child_process';
import { createWriteStream, type WriteStream } from 'node:fs';
import { mkdir } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';

const BACKEND_ORIGIN = 'http://127.0.0.1:9001';
const HEALTH_URL = `${BACKEND_ORIGIN}/healthz`;
const START_TIMEOUT_MS = 20_000;
const STOP_TIMEOUT_MS = 7_000;

export interface BackendOptions {
  executablePath: string;
  runtimeHome: string;
  onUnexpectedExit: (message: string) => void;
}

export class BackendSidecar {
  private child: ChildProcess | null = null;
  private logStream: WriteStream | null = null;
  private stopping = false;
  private ready = false;
  private stopPromise: Promise<void> | null = null;

  constructor(private readonly options: BackendOptions) {}

  async start(): Promise<void> {
    if (await isPortOpen(9001, '127.0.0.1')) {
      throw new Error(
        '127.0.0.1:9001 is already in use. Stop the process using this port and reopen LingCoWork.',
      );
    }

    await mkdir(path.join(this.options.runtimeHome, 'logs'), { recursive: true });
    this.logStream = createWriteStream(
      path.join(this.options.runtimeHome, 'logs', 'backend.log'),
      { flags: 'a' },
    );
    this.logStream.write(`\n--- LingCoWork backend start ${new Date().toISOString()} ---\n`);

    const child = spawn(this.options.executablePath, [], {
      cwd: this.options.runtimeHome,
      env: {
        ...process.env,
        LINGCOWORK_HOME: this.options.runtimeHome,
      },
      detached: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    this.child = child;

    child.stdout?.pipe(this.logStream, { end: false });
    child.stderr?.pipe(this.logStream, { end: false });
    child.once('exit', (code, signal) => {
      const message = `Backend exited (code=${String(code)}, signal=${String(signal)}).`;
      this.logStream?.write(`${message}\n`);
      this.child = null;
      if (this.ready && !this.stopping) {
        this.options.onUnexpectedExit(message);
      }
    });

    const earlyExit = new Promise<never>((_resolve, reject) => {
      child.once('error', reject);
      child.once('exit', (code, signal) => {
        if (!this.ready && !this.stopping) {
          reject(
            new Error(
              `Backend exited before becoming ready (code=${String(code)}, signal=${String(signal)}).`,
            ),
          );
        }
      });
    });

    try {
      await Promise.race([waitForHealth(START_TIMEOUT_MS), earlyExit]);
      this.ready = true;
    } catch (error) {
      await this.stop();
      throw error;
    }
  }

  async stop(): Promise<void> {
    if (this.stopPromise) return this.stopPromise;
    this.stopPromise = this.stopInternal();
    return this.stopPromise;
  }

  private async stopInternal(): Promise<void> {
    this.stopping = true;
    this.ready = false;

    const child = this.child;
    if (child?.pid && child.exitCode === null) {
      const exited = new Promise<void>((resolve) => {
        child.once('exit', () => resolve());
      });

      signalProcessGroup(child.pid, 'SIGTERM');
      const graceful = await Promise.race([
        exited.then(() => true),
        delay(STOP_TIMEOUT_MS).then(() => false),
      ]);
      if (!graceful && child.exitCode === null) {
        signalProcessGroup(child.pid, 'SIGKILL');
        await Promise.race([exited, delay(1_000)]);
      }
    }

    this.child = null;
    this.logStream?.end();
    this.logStream = null;
  }
}

async function waitForHealth(timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError = 'backend did not respond';

  while (Date.now() < deadline) {
    try {
      const response = await fetch(HEALTH_URL, {
        signal: AbortSignal.timeout(1_000),
      });
      if (response.ok) return;
      lastError = `health endpoint returned HTTP ${response.status}`;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    await delay(150);
  }

  throw new Error(`Backend health check timed out: ${lastError}`);
}

function isPortOpen(port: number, host: string): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.createConnection({ port, host });
    socket.setTimeout(500);
    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });
    const unavailable = () => {
      socket.destroy();
      resolve(false);
    };
    socket.once('timeout', unavailable);
    socket.once('error', unavailable);
  });
}

function signalProcessGroup(pid: number, signal: NodeJS.Signals): void {
  try {
    process.kill(-pid, signal);
  } catch {
    try {
      process.kill(pid, signal);
    } catch {
      // The process may already have exited.
    }
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
