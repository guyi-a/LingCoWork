import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { API_BASE } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useWorkspaceStore } from "./store";

type TerminalStatus = "connecting" | "ready" | "exited" | "error";

type TerminalSession = {
  id: string;
  label: string;
};

type TerminalMeta = {
  status: TerminalStatus;
  cwd: string;
  exitCode: number | null;
};

export function WorkspaceTerminal({
  conversationId,
  projectId,
  active,
}: {
  conversationId: string;
  projectId?: string;
  active: boolean;
}) {
  const sequenceRef = useRef(1);
  const [sessions, setSessions] = useState<TerminalSession[]>([
    { id: terminalSessionID(), label: "Terminal 1" },
  ]);
  const [activeID, setActiveID] = useState(() => sessions[0].id);
  const [metaByID, setMetaByID] = useState<Record<string, TerminalMeta>>({});
  const [listOpen, setListOpen] = useState(false);
  const activeSession = sessions.find((session) => session.id === activeID);
  const activeMeta = activeSession ? metaByID[activeSession.id] : undefined;

  const addTerminal = () => {
    if (sessions.length >= 8) return;
    const number = ++sequenceRef.current;
    const session = {
      id: terminalSessionID(),
      label: `Terminal ${number}`,
    };
    setSessions((current) => [...current, session]);
    setActiveID(session.id);
    setListOpen(false);
  };

  const removeActiveTerminal = () => {
    if (!activeSession) return;
    const index = sessions.findIndex((item) => item.id === activeSession.id);
    const nextSessions = sessions.filter((item) => item.id !== activeSession.id);
    const replacement =
      nextSessions[Math.min(index, Math.max(0, nextSessions.length - 1))];
    setSessions(nextSessions);
    setActiveID(replacement?.id ?? "");
    setMetaByID((current) => {
      const next = { ...current };
      delete next[activeSession.id];
      return next;
    });
    setListOpen(false);
  };

  return (
    <div className="relative flex min-h-0 flex-1 flex-col bg-[#171717]">
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-white/10 px-2 font-mono text-[10px] text-white/45">
        <StatusDot status={activeMeta?.status ?? "connecting"} />
        <span
          className="min-w-0 flex-1 truncate"
          title={activeMeta?.cwd || activeSession?.label}
        >
          {activeMeta?.cwd || activeSession?.label || "No terminal"}
        </span>
        {activeMeta?.exitCode !== null &&
          activeMeta?.exitCode !== undefined && (
            <span>exit {activeMeta.exitCode}</span>
          )}
        <TerminalIconButton
          label="新建终端"
          onClick={addTerminal}
          disabled={sessions.length >= 8}
        >
          <PlusIcon />
        </TerminalIconButton>
        <TerminalIconButton
          label="终端列表"
          onClick={() => setListOpen((open) => !open)}
          active={listOpen}
        >
          <TerminalListIcon />
          {sessions.length > 1 && (
            <span className="ml-0.5 text-[9px]">{sessions.length}</span>
          )}
        </TerminalIconButton>
        <TerminalIconButton
          label="删除当前终端"
          onClick={removeActiveTerminal}
          disabled={!activeSession}
        >
          <TrashIcon />
        </TerminalIconButton>
      </div>

      {listOpen && (
        <div className="absolute right-2 top-9 z-30 min-w-48 overflow-hidden rounded-md border border-white/10 bg-[#222] py-1 shadow-xl">
          {sessions.length === 0 ? (
            <div className="px-3 py-2 text-[11px] text-white/35">暂无终端</div>
          ) : (
            sessions.map((session) => {
              const meta = metaByID[session.id];
              return (
                <button
                  key={session.id}
                  type="button"
                  onClick={() => {
                    setActiveID(session.id);
                    setListOpen(false);
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 px-3 py-2 text-left font-mono text-[11px] text-white/70",
                    session.id === activeID
                      ? "bg-white/10"
                      : "hover:bg-white/5",
                  )}
                >
                  <StatusDot status={meta?.status ?? "connecting"} />
                  <span className="min-w-0 flex-1 truncate">
                    {session.label}
                  </span>
                  {meta?.exitCode !== null &&
                    meta?.exitCode !== undefined && (
                      <span className="text-[9px] text-white/35">
                        exit {meta.exitCode}
                      </span>
                    )}
                </button>
              );
            })
          )}
        </div>
      )}

      {sessions.length === 0 ? (
        <button
          type="button"
          onClick={addTerminal}
          className="flex min-h-0 flex-1 items-center justify-center text-[12px] text-white/35 hover:text-white/60"
        >
          点击 + 创建终端
        </button>
      ) : (
        sessions.map((session) => (
          <TerminalInstance
            key={session.id}
            conversationId={conversationId}
            projectId={projectId}
            active={active && session.id === activeID}
            visible={session.id === activeID}
            onMeta={(meta) =>
              setMetaByID((current) => ({
                ...current,
                [session.id]: meta,
              }))
            }
          />
        ))
      )}
    </div>
  );
}

function TerminalInstance({
  conversationId,
  projectId,
  active,
  visible,
  onMeta,
}: {
  conversationId: string;
  projectId?: string;
  active: boolean;
  visible: boolean;
  onMeta: (meta: TerminalMeta) => void;
}) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const refreshTimerRef = useRef<number | null>(null);
  const refreshFiles = useWorkspaceStore((state) => state.refreshFiles);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    onMeta({ status: "connecting", cwd: "", exitCode: null });

    const terminal = new Terminal({
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily:
        'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
      fontSize: 12,
      lineHeight: 1.25,
      scrollback: 5000,
      theme: {
        background: "#171717",
        foreground: "#d4d4d4",
        cursor: "#d4d4d4",
        selectionBackground: "#3f3f46",
        red: "#f87171",
        green: "#4ade80",
        yellow: "#facc15",
        blue: "#60a5fa",
        magenta: "#c084fc",
        cyan: "#22d3ee",
      },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    terminalRef.current = terminal;
    fitRef.current = fit;

    const socket = new WebSocket(
      terminalURL(conversationId, projectId, terminal.cols, terminal.rows),
    );
    socket.binaryType = "arraybuffer";
    let terminalExited = false;
    let terminalCwd = "";
    const scheduleWorkspaceRefresh = (delay = 400) => {
      if (refreshTimerRef.current !== null) {
        window.clearTimeout(refreshTimerRef.current);
      }
      refreshTimerRef.current = window.setTimeout(() => {
        refreshTimerRef.current = null;
        refreshFiles();
      }, delay);
    };
    const sendResize = () => {
      if (socket.readyState !== WebSocket.OPEN) return;
      socket.send(
        JSON.stringify({
          type: "resize",
          cols: terminal.cols,
          rows: terminal.rows,
        }),
      );
    };
    const resizeObserver = new ResizeObserver(() => {
      if (host.clientWidth === 0 || host.clientHeight === 0) return;
      try {
        fit.fit();
        sendResize();
      } catch {
        // A hidden terminal has no measurable viewport.
      }
    });
    resizeObserver.observe(host);

    const inputDisposable = terminal.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(new TextEncoder().encode(data));
        if (data.includes("\r") || data.includes("\n")) {
          scheduleWorkspaceRefresh(800);
        }
      }
    });
    socket.onmessage = (event) => {
      if (typeof event.data !== "string") {
        terminal.write(
          event.data instanceof ArrayBuffer
            ? new Uint8Array(event.data)
            : event.data,
        );
        scheduleWorkspaceRefresh();
        return;
      }
      try {
        const frame = JSON.parse(event.data) as {
          type?: string;
          cwd?: string;
          exit_code?: number;
          message?: string;
        };
        if (frame.type === "ready") {
          terminalCwd = frame.cwd ?? "";
          onMeta({ status: "ready", cwd: terminalCwd, exitCode: null });
          window.requestAnimationFrame(() => {
            fit.fit();
            sendResize();
            if (active) terminal.focus();
          });
        } else if (frame.type === "exit") {
          terminalExited = true;
          onMeta({
            status: "exited",
            cwd: terminalCwd,
            exitCode: frame.exit_code ?? -1,
          });
        } else if (frame.type === "error") {
          onMeta({ status: "error", cwd: "", exitCode: null });
          terminal.writeln(
            `\r\n\x1b[31m${frame.message ?? "terminal error"}\x1b[0m`,
          );
        }
      } catch {
        terminal.write(event.data);
      }
    };
    socket.onerror = () => {
      onMeta({ status: "error", cwd: "", exitCode: null });
      terminal.writeln("\r\n\x1b[31mTerminal connection failed\x1b[0m");
    };
    socket.onclose = () => {
      if (!terminalExited) {
        onMeta({ status: "error", cwd: terminalCwd, exitCode: null });
      }
    };

    return () => {
      inputDisposable.dispose();
      resizeObserver.disconnect();
      if (refreshTimerRef.current !== null) {
        window.clearTimeout(refreshTimerRef.current);
      }
      socket.close();
      terminal.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, [conversationId, projectId, refreshFiles]);

  useEffect(() => {
    if (!active) return;
    window.requestAnimationFrame(() => {
      try {
        fitRef.current?.fit();
        terminalRef.current?.focus();
      } catch {
        // The panel may still be transitioning its width.
      }
    });
  }, [active]);

  return (
    <div
      ref={hostRef}
      className={cn(
        "min-h-0 flex-1 overflow-hidden px-1 py-1.5",
        visible ? "block" : "hidden",
      )}
      onClick={() => terminalRef.current?.focus()}
    />
  );
}

function terminalURL(
  conversationId: string,
  projectId: string | undefined,
  cols: number,
  rows: number,
): string {
  const url = new URL(API_BASE);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = `/conversations/${encodeURIComponent(conversationId)}/workspace/terminal`;
  url.search = "";
  if (projectId) url.searchParams.set("project_id", projectId);
  url.searchParams.set("cols", String(cols));
  url.searchParams.set("rows", String(rows));
  return url.toString();
}

function terminalSessionID(): string {
  return `terminal-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

function StatusDot({ status }: { status: TerminalStatus }) {
  return (
    <span
      className={cn(
        "size-1.5 shrink-0 rounded-full",
        status === "ready"
          ? "bg-emerald-400"
          : status === "connecting"
            ? "bg-amber-400"
            : "bg-red-400",
      )}
    />
  );
}

function TerminalIconButton({
  label,
  onClick,
  disabled,
  active,
  children,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  active?: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={label}
      aria-label={label}
      className={cn(
        "inline-flex h-6 min-w-6 items-center justify-center rounded px-1 text-white/50 transition-colors",
        active ? "bg-white/10 text-white/80" : "hover:bg-white/10 hover:text-white/75",
        disabled && "cursor-not-allowed opacity-30",
      )}
    >
      {children}
    </button>
  );
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor">
      <path d="M8 3v10M3 8h10" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

function TerminalListIcon() {
  return (
    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor">
      <rect x="2.5" y="3" width="11" height="9.5" rx="1.5" strokeWidth="1.2" />
      <path d="m5 6 2 1.7L5 9.4M8.5 9.5h2.5" strokeWidth="1.2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function TrashIcon() {
  return (
    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor">
      <path d="M3.5 5h9M6 5V3.5h4V5m1.5 0-.5 8H5L4.5 5" strokeWidth="1.2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
