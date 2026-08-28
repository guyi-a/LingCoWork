import { useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { useApprovalStore, type PendingApproval } from "@/features/chat/approval-store";

// Stable empty array — avoids a fresh `[]` from the selector on every render,
// which would give useSyncExternalStore a new reference each read and hang
// React in a maximum-update-depth loop.
const EMPTY: PendingApproval[] = [];

// Human-readable label for each builtin tool we prompt on. A tool that isn't
// listed falls back to its effect kind, which is how a tool this frontend has
// never heard of — an MCP server's — still gets a card that says what it does
// instead of showing a bare identifier.
const TOOL_TITLES: Record<string, string> = {
  write_file: "写入文件",
  apply_patch: "应用代码补丁",
  write_file_chunked: "写入长文件",
  rm: "删除",
  mv: "移动 / 重命名",
  cp: "复制",
  run_command: "执行命令",
  read_file: "读取文件",
  list_files: "列出目录",
  glob: "查找文件",
  grep: "搜索代码",
  file_info: "查看文件信息",
  extract_document_text: "提取文档文本",
};

const EFFECT_TITLES: Record<string, string> = {
  "filesystem-read": "读取文件",
  "filesystem-write": "写入文件",
  "filesystem-structure": "创建目录",
  "filesystem-transfer": "移动 / 复制文件",
  "process-exec": "执行命令",
  "network-request": "访问网络",
  "skill-load": "加载技能",
  "readonly-query": "只读查询",
  "mcp-call": "调用外部服务",
  unknown: "未知操作",
};

// Mirrors internal/effect.Effect. Every field is optional because only the
// ones relevant to the kind are populated, and because an older backend can
// send a shape this build doesn't know about.
type Effect = {
  kind?: string;
  scope?: string;
  path?: string;
  path_scope?: string;
  dest_path?: string;
  dest_scope?: string;
  destructive?: boolean;
  command?: string;
  cwd?: string;
  classification?: string;
  url?: string;
  agent?: string;
  note?: string;
  // mcp-call
  server?: string;
  remote_tool?: string;
  transport?: string;
  read_only?: boolean;
  open_world?: boolean;
  trust_annotations?: boolean;
  auto_approved?: boolean;
};

const REASON_MAX = 500;

function parseEffect(effectJson: string | undefined): Effect | null {
  if (!effectJson) return null;
  try {
    const parsed = JSON.parse(effectJson) as Effect;
    return parsed && typeof parsed === "object" && parsed.kind ? parsed : null;
  } catch {
    return null;
  }
}

// Describes a call from its effect alone, with no knowledge of the tool. This
// is the path an MCP tool takes.
function summarizeEffect(e: Effect): string {
  switch (e.kind) {
    case "process-exec":
      return e.cwd ? `${e.command ?? ""} · cwd=${e.cwd}` : (e.command ?? "");
    case "network-request":
      return e.url ?? e.note ?? "";
    case "filesystem-transfer": {
      const src = scopeTag(e.path, e.path_scope);
      const dst = scopeTag(e.dest_path, e.dest_scope);
      return src && dst ? `${src} → ${dst}` : src || dst;
    }
    case "skill-load":
      return e.note ?? "";
    case "mcp-call":
      // The remote name is what the server documents and what a user would
      // look up; the prefixed name in the title is our invention.
      return `${e.server ?? "?"} · ${e.remote_tool ?? "?"}`;
    default:
      return scopeTag(e.path, e.scope) || e.note || "";
  }
}

// Marks a path that leaves the workspace. The distinction is the whole reason
// external reads reach this card at all, so it has to be visible.
function scopeTag(path: string | undefined, scope: string | undefined): string {
  if (!path) return "";
  return scope === "external" ? `${path}（工作区外）` : path;
}

// One-line summary of a builtin tool call's arguments. Aims for signal
// density under ~80 chars: enough for the user to recognise the operation
// without unfolding the raw JSON.
//
// Returns null for a tool it has no case for, rather than guessing from a
// path-ish field. Guessing would beat the effect to the punch and produce a
// worse line — a bare path with no indication of whether it leaves the
// workspace or what is about to happen to it.
function summarize(tool: string, argsJson: string): string | null {
  if (!argsJson) return null;
  let args: Record<string, unknown> = {};
  try {
    args = JSON.parse(argsJson) as Record<string, unknown>;
  } catch {
    return argsJson.length > 80 ? argsJson.slice(0, 80) + "…" : argsJson;
  }
  switch (tool) {
    case "write_file":
    case "apply_patch":
      return typeof args.path === "string" ? args.path : "";
    case "glob":
    case "grep": {
      const pattern = typeof args.pattern === "string" ? args.pattern : "";
      const path = typeof args.path === "string" ? args.path : ".";
      return pattern ? `${pattern} · in ${path}` : path;
    }
    case "write_file_chunked": {
      const path = typeof args.path === "string" ? args.path : "";
      const mode = typeof args.mode === "string" ? args.mode : "";
      return path && mode ? `${path} · ${mode}` : path || mode;
    }
    case "rm": {
      const path = typeof args.path === "string" ? args.path : "";
      const recursive = args.recursive === true;
      return recursive ? `${path} · recursive` : path;
    }
    case "mv": {
      const src = typeof args.src === "string" ? args.src : "";
      const dst = typeof args.dst === "string" ? args.dst : "";
      return src && dst ? `${src} → ${dst}` : src || dst;
    }
    case "run_command": {
      const cmd = typeof args.command === "string" ? args.command : "";
      const cwd = typeof args.cwd === "string" ? args.cwd : "";
      return cwd ? `${cmd} · cwd=${cwd}` : cmd;
    }
    default:
      return null;
  }
}

// Last resort when neither a tool case nor an effect produced anything:
// scrape any path-ish field. Reached by approvals persisted before effects
// existed, for a tool with no case above.
function summarizeAnyArgs(argsJson: string): string {
  if (!argsJson) return "";
  let args: Record<string, unknown> = {};
  try {
    args = JSON.parse(argsJson) as Record<string, unknown>;
  } catch {
    return "";
  }
  for (const key of ["path", "target", "file", "src", "url", "command", "name"]) {
    const v = args[key];
    if (typeof v === "string" && v) return v;
  }
  return "";
}

function toolTitle(tool: string, effect: Effect | null): string {
  const known = TOOL_TITLES[tool];
  if (known) return known;
  if (effect?.kind === "delegate-agent") return `委派 ${effect.agent ?? tool}`;
  if (effect?.kind === "mcp-call") {
    return `调用外部工具 ${effect.remote_tool ?? tool}`;
  }
  const byKind = effect?.kind ? EFFECT_TITLES[effect.kind] : undefined;
  return byKind ? `${byKind} · ${tool}` : tool;
}

export function ApprovalBar({
  conversationID,
  onDecision,
  onResume,
}: {
  conversationID: string;
  onDecision?: (
    item: PendingApproval,
    decision: "approve" | "deny",
  ) => Promise<void> | void;
  onResume?: () => Promise<void> | void;
}) {
  const pending = useApprovalStore(
    (s) => s.pending[conversationID] ?? EMPTY,
  );
  const decide = useApprovalStore((s) => s.decide);
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"idle" | "denying">("idle");
  const [reason, setReason] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const current: PendingApproval | undefined = pending[0];
  const effect = useMemo(
    () => parseEffect(current?.effectJson),
    [current?.effectJson],
  );
  // The per-tool summary comes first for the builtins: it's tuned to each
  // one's arguments and reads better than the generic form. The effect covers
  // everything else, including tools this build has never seen.
  const summary = useMemo(() => {
    if (!current) return "";
    const byTool = summarize(current.tool, current.argsJson);
    if (byTool) return byTool;
    const byEffect = effect ? summarizeEffect(effect) : "";
    if (byEffect) return byEffect;
    return summarizeAnyArgs(current.argsJson);
  }, [current, effect]);

  // Reset local state whenever the visible pending item changes — otherwise
  // a reason typed for tool A would leak into the prompt for tool B.
  useEffect(() => {
    setMode("idle");
    setReason("");
    setBusy(false);
  }, [current?.interruptId]);

  // Autofocus so the user can just start typing after clicking 拒绝.
  useEffect(() => {
    if (mode === "denying") textareaRef.current?.focus();
  }, [mode]);

  if (!current) return null;

  const submit = async (decision: "approve" | "deny", withReason?: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await decide(conversationID, current.interruptId, decision, withReason);
      await onDecision?.(current, decision);
      // Backend spun up a fresh run into a new SSE buffer; reconnect so the
      // continuation streams into the same conversation view.
      await onResume?.();
    } finally {
      setBusy(false);
    }
  };

  const onApprove = () => submit("approve");
  const onStartDeny = () => setMode("denying");
  const onCancelDeny = () => {
    setMode("idle");
    setReason("");
  };
  const onConfirmDeny = () => submit("deny", reason);

  // 外层定位（absolute + max-width 容器）由 PendingInterruptDock 统一负责，
  // 这里只输出纯卡片，方便和 QuestionCard 共享同一 dock 外壳。
  return (
    <div
      className={cn(
        "rounded-xl border border-rule bg-paper px-4 py-3",
        "space-y-3",
        "shadow-[0_-8px_24px_-8px_rgba(0,0,0,0.14),0_-2px_6px_-2px_rgba(0,0,0,0.08)]",
      )}
    >
          <div className="flex items-center gap-2.5">
            <ShieldIcon />
            <span className="text-[15px] font-semibold text-ink">
              {toolTitle(current.tool, effect)}
            </span>
            {effect?.destructive && (
              <span className="rounded bg-red-50 px-1.5 py-0.5 font-mono text-[10px] font-medium text-red-700">
                不可撤销
              </span>
            )}
            {effect?.kind === "unknown" && (
              <span className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted">
                未知工具
              </span>
            )}
            {/* An MCP call runs on someone else's machine and the only account
                of what it does is that machine's own. Saying so is the point
                of the card — without it the user is approving a name. */}
            {effect?.kind === "mcp-call" && !effect.trust_annotations && (
              <span className="rounded bg-amber-50 px-1.5 py-0.5 font-mono text-[10px] font-medium text-amber-700">
                未信任的服务器
              </span>
            )}
            {pending.length > 1 && (
              <span className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted tabular-nums">
                1/{pending.length}
              </span>
            )}
          </div>

          {mode === "idle" ? (
            <>
              {summary && (
                <code className="block min-h-9 w-full overflow-hidden truncate rounded-lg bg-subtle/65 px-3 py-2 font-mono text-[12px] leading-5 text-muted">
                  {summary}
                </code>
              )}

              {effect?.kind === "mcp-call" && effect.note && (
                <p className="px-0.5 text-[12px] leading-5 text-muted">
                  {effect.note}
                </p>
              )}

              <div className="flex items-center justify-center gap-2.5">
                <button
                  type="button"
                  disabled={busy}
                  onClick={onStartDeny}
                  className={cn(
                    "inline-flex h-8 items-center gap-1.5 rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink",
                    "shadow-[0_1px_2px_rgba(20,30,50,0.06)] transition-colors hover:bg-subtle",
                    busy && "pointer-events-none opacity-50",
                  )}
                >
                  <XIcon />
                  拒绝
                </button>
                <button
                  type="button"
                  disabled={busy}
                  onClick={onApprove}
                  className={cn(
                    "inline-flex h-8 items-center gap-1.5 rounded-lg bg-ink px-4 text-sm font-medium text-paper",
                    "shadow-[0_1px_2px_rgba(20,30,50,0.12)] transition-opacity hover:opacity-90",
                    busy && "pointer-events-none opacity-50",
                  )}
                >
                  <CheckIcon />
                  允许
                </button>
              </div>
            </>
          ) : (
            <>
              <textarea
                ref={textareaRef}
                value={reason}
                onChange={(e) => setReason(e.target.value.slice(0, REASON_MAX))}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.preventDefault();
                    onCancelDeny();
                  } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault();
                    onConfirmDeny();
                  }
                }}
                placeholder="告诉模型为什么不允许（可选，⌘/Ctrl+Enter 提交）"
                rows={2}
                className={cn(
                  "block w-full resize-none rounded-lg border border-rule bg-subtle/65 px-3 py-2",
                  "text-sm leading-5 text-ink placeholder:text-muted",
                  "focus:outline-none focus:ring-1 focus:ring-ink/20",
                )}
              />

              <div className="flex items-center justify-between gap-2.5">
                <span className="font-mono text-[10px] text-muted tabular-nums">
                  {reason.length}/{REASON_MAX}
                </span>
                <div className="flex items-center gap-2.5">
                  <button
                    type="button"
                    disabled={busy}
                    onClick={onCancelDeny}
                    className={cn(
                      "inline-flex h-8 items-center rounded-lg px-3 text-sm font-medium text-muted",
                      "transition-colors hover:bg-subtle hover:text-ink",
                      busy && "pointer-events-none opacity-50",
                    )}
                  >
                    取消
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={onConfirmDeny}
                    className={cn(
                      "inline-flex h-8 items-center gap-1.5 rounded-lg bg-ink px-4 text-sm font-medium text-paper",
                      "shadow-[0_1px_2px_rgba(20,30,50,0.12)] transition-opacity hover:opacity-90",
                      busy && "pointer-events-none opacity-50",
                    )}
                  >
                    <XIcon />
                    确认拒绝
                  </button>
                </div>
              </div>
            </>
          )}
    </div>
  );
}

function ShieldIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0 text-muted"
    >
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden
    >
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden
    >
      <path d="M20 6 9 17l-5-5" />
    </svg>
  );
}
