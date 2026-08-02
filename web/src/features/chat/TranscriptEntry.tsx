import { useState } from "react";
import { formatClock, cn } from "@/lib/utils";
import type {
  ChatTurn,
  ReactSegment,
  SubAgentEvent,
  ToolCall,
} from "@/hooks/useChatStream";
import { MessageBody } from "./MessageBody";
import { UserAttachmentChips } from "./UserAttachmentChips";
import { parseAttachmentMarkers } from "@/features/chat/attachments-store";

function CopyIcon() {
  return (
    <svg
      width="11"
      height="11"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="4" y="4" width="9" height="9" rx="1.5" />
      <path d="M3 10V3.5A1.5 1.5 0 0 1 4.5 2H10" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg
      width="11"
      height="11"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 8.5l3 3 7-7" />
    </svg>
  );
}

function ThinkingCard({
  content,
  label,
  dense,
  streaming,
  defaultOpen,
}: {
  content: string;
  label: string;
  dense?: boolean;
  streaming?: boolean;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(Boolean(defaultOpen));
  const trimmed = content?.trim() ?? "";
  const isEmpty = trimmed.length === 0;
  const clickable = !isEmpty;
  return (
    <div className={dense ? "my-2" : "my-3"}>
      <button
        type="button"
        onClick={() => clickable && setOpen((o) => !o)}
        disabled={!clickable}
        className={cn(
          "flex items-center gap-2 text-[13px] font-medium text-ink",
          clickable && "hover:text-accent transition-colors cursor-pointer",
          !clickable && "opacity-60",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "inline-block size-1.5 rounded-full bg-accent",
            streaming && "animate-pulse",
          )}
        />
        <span>{label}</span>
      </button>
      {open && !isEmpty && (
        <div
          className={cn(
            "mt-2 pl-4 border-l-2 border-ink/15",
            "italic whitespace-pre-wrap text-muted leading-relaxed",
            dense ? "text-[13px]" : "text-sm",
          )}
        >
          {content}
        </div>
      )}
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          /* clipboard may be unavailable in some contexts; silently ignore */
        }
      }}
      className={cn(
        "ml-auto inline-flex items-center gap-1.5 px-2 py-1 rounded",
        // Sized explicitly rather than inherited: this sits in the turn
        // footer, where the surrounding text is body-sized.
        "font-mono text-[10px] leading-none",
        "text-muted hover:text-ink hover:bg-subtle",
        "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
        "transition-opacity",
        copied && "opacity-100 text-ink",
      )}
      aria-label={copied ? "已复制" : "复制消息"}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      <span>{copied ? "已复制" : "复制"}</span>
    </button>
  );
}

function formatTokens(n: number): string {
  return n < 1000 ? String(n) : `${(n / 1000).toFixed(1)}k`;
}

function formatDuration(ms: number): string {
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(Math.round(seconds % 60)).padStart(2, "0")}s`;
}

// TurnFooter closes out a finished assistant turn: how full the model's
// context was when it ran, how long it took, and the copy affordance.
//
// The token figure is context occupancy, not spend. Summing each model call's
// usage across a ReAct loop would count the same history once per call and
// tell you nothing about how close the conversation is to being folded —
// which, with compaction in play, is the question worth answering. contextLimit
// is 0 when compaction is off, and the denominator is dropped accordingly.
function TurnFooter({
  text,
  totalTokens,
  durationMs,
  contextLimit,
}: {
  text: string;
  totalTokens?: number;
  durationMs?: number;
  contextLimit: number;
}) {
  const parts: string[] = [];
  if (totalTokens) {
    parts.push(
      contextLimit > 0
        ? `${formatTokens(totalTokens)} / ${formatTokens(contextLimit)}`
        : `${formatTokens(totalTokens)} tokens`,
    );
  }
  if (durationMs !== undefined) parts.push(formatDuration(durationMs));

  const tooltip =
    totalTokens && contextLimit > 0
      ? `上下文 ${totalTokens.toLocaleString()} / ${contextLimit.toLocaleString()} tokens（${Math.round(
          (totalTokens / contextLimit) * 100,
        )}%），超过阈值后历史会被折叠为摘要`
      : undefined;

  if (parts.length === 0 && !text) return null;

  return (
    <div className="mt-3 flex items-center gap-3">
      {parts.length > 0 && (
        <span
          className="font-mono text-[10px] text-muted tabular-nums"
          {...(tooltip ? { title: tooltip } : {})}
        >
          {parts.join(" · ")}
        </span>
      )}
      {text ? <CopyButton text={text} /> : null}
    </div>
  );
}

const ROLE_LABEL: Record<ChatTurn["role"], string> = {
  user: "CANDIDATE",
  assistant: "INTERVIEWER",
  context_compacted: "",
};

// Divider marking where history was folded into a summary to fit the model's
// context window. Everything above it is still rendered in full — only what
// the model receives was condensed, so this is an informational rule rather
// than a gap in the transcript.
function CompactedDivider({ replacedCount }: { replacedCount?: number }) {
  return (
    <div className="my-8 flex items-center gap-3" role="separator">
      <span className="h-px flex-1 bg-rule" />
      <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted whitespace-nowrap">
        上下文已压缩
        {replacedCount ? ` · ${replacedCount} 条` : ""}
      </span>
      <span className="h-px flex-1 bg-rule" />
    </div>
  );
}

// Sub-agent tools — wrapped via adk.NewAgentTool on the backend. When
// these appear in tool_call events, label them as AGENT so the UI reflects
// delegation, not a plain tool invocation.
const AGENT_TOOL_NAMES = new Set([
  "job_search",
  "deep_research",
  "resume_analyzer",
  "question_planner",
]);

// GROUPABLE_TOOL_NAMES: 这些工具在同一 turn 里连续调用 ≥2 次时会折叠成
// 单行"TOOL name × N status"，避免侧栏被大量同名条目撑满。目前只有
// rag_search 这类高频、每条价值低的工具适合折叠；其他工具（write_file 等）
// 语义上每次都不同，不合并。
const GROUPABLE_TOOL_NAMES = new Set(["rag_search"]);

type ToolRow =
  | { kind: "single"; tool: ToolCall; index: number }
  | { kind: "group"; tools: ToolCall[]; index: number };

// groupConsecutiveTools 把 tools 数组按"连续同名可折叠工具"合并成 rows。
// 可折叠工具（GROUPABLE_TOOL_NAMES）**总是**走 ToolGroup 渲染，即使只有 1 次
// 也显示 "× 1"，保持视觉一致（避免第 1 次渲染成可展开 ToolEntry、第 2 次
// 突然变成紧凑组的跳变）。
function groupConsecutiveTools(tools: ToolCall[]): ToolRow[] {
  const rows: ToolRow[] = [];
  let i = 0;
  while (i < tools.length) {
    const t = tools[i];
    if (GROUPABLE_TOOL_NAMES.has(t.name)) {
      let j = i + 1;
      while (j < tools.length && tools[j].name === t.name) j++;
      rows.push({ kind: "group", tools: tools.slice(i, j), index: i });
      i = j;
      continue;
    }
    rows.push({ kind: "single", tool: t, index: i });
    i++;
  }
  return rows;
}

// renderSegments 给出一个回合要画的 ReAct 段落。
//
// 段内顺序是固定的：先文本，后工具。这不是排版偏好，是协议决定的——一条
// assistant 消息里 content 必然先于 tool_calls，tool_calls 一出现该消息就结束
// 了。直播和刷新的切段粒度不同（直播把没有文本间隔的多次调用堆进同一段，刷新
// 则每条 assistant 行独立成段），但只要段内守住这个顺序，两条链路画出来的先后
// 就是一致的。
function renderSegments(turn: ChatTurn): ReactSegment[] {
  if (turn.segments.length > 0) return turn.segments;
  // 老数据没有 segments：content 是整个回合文本的拼接，tools 是它调过的全部
  // 工具，交错关系当时就没存下来。对这种形状，"工具在前、结论在后"是唯一还
  // 原得出的读法，所以摊成两段交给同一套渲染顺序，而不是在下面加分支。
  const legacy = [
    { content: "", tools: turn.tools },
    { content: turn.content, tools: [] as ToolCall[] },
  ].filter((seg) => seg.content !== "" || seg.tools.length > 0);
  return legacy.length > 0 ? legacy : [{ content: "", tools: [] }];
}

// aggregateStatus 汇总一组同名工具的整体状态：任何错误 → error；任何
// running → running；任何 pending → pending；否则 ok。cancelled 归入 ok
// 以免整组"部分取消"闪红。
function aggregateStatus(tools: ToolCall[]): ToolCall["status"] {
  let hasRunning = false;
  let hasPending = false;
  for (const t of tools) {
    if (t.status === "error") return "error";
    if (t.status === "running") hasRunning = true;
    if (t.status === "pending") hasPending = true;
  }
  if (hasRunning) return "running";
  if (hasPending) return "pending";
  return "ok";
}

export function TranscriptEntry({
  turn,
  allSubEvents,
  ownedToolIds,
  showRule,
  streaming,
  contextLimit,
}: {
  turn: ChatTurn;
  // Conversation-wide subEvents pool. Provided so tool cards can pick up
  // matching child events that were persisted into a later turn's Extra —
  // this happens whenever a sub-agent got interrupted mid-flight and its
  // tool_result arrived on a subsequent resume run.
  allSubEvents: SubAgentEvent[];
  // Union of tool_call ids across every turn — a subEvent whose parent id
  // matches one is "adopted" by that tool no matter which turn owns the
  // event's row; only genuinely un-parented events fall to orphan render.
  ownedToolIds: Set<string>;
  showRule: boolean;
  streaming: boolean;
  // Token threshold at which history gets folded, or 0 when compaction is
  // off. Only used to give the footer's occupancy figure a denominator.
  contextLimit: number;
}) {
  if (turn.role === "context_compacted") {
    return <CompactedDivider replacedCount={turn.replacedCount} />;
  }
  const isUser = turn.role === "user";
  return (
    <article
      className={cn(
        "group",
        isUser && "ml-auto max-w-[85%] flex flex-col items-end",
        showRule &&
          (isUser ? "mt-8" : "border-t border-rule pt-8 mt-8"),
      )}
    >
      <header
        className={cn(
          "font-mono text-[10px] tracking-[0.18em] uppercase text-muted mb-3 flex items-center gap-3",
          isUser && "justify-end",
        )}
      >
        <span>{ROLE_LABEL[turn.role]}</span>
        <span aria-hidden="true">·</span>
        <span>{formatClock(turn.createdAt)}</span>
        {streaming && (
          <span className="text-accent normal-case tracking-normal lowercase">
            ● streaming
          </span>
        )}
      </header>

      {turn.reasoning && (
        <ThinkingCard
          content={turn.reasoning}
          label={
            streaming && !turn.content && turn.tools.length === 0
              ? "Thinking"
              : "Thoughts"
          }
          streaming={streaming && !turn.content && turn.tools.length === 0}
        />
      )}

      {turn.role === "assistant"
        ? renderSegments(turn).map((seg, segIdx, segs) => {
            const isLastSeg = segIdx === segs.length - 1;
            return (
              <div key={`seg-${segIdx}`}>
                {seg.content ? (
                  <div className="text-ink">
                    <MessageBody
                      content={seg.content}
                      streaming={streaming && isLastSeg}
                    />
                  </div>
                ) : (
                  streaming &&
                  isLastSeg &&
                  seg.tools.length === 0 && (
                    <span className="text-muted">…</span>
                  )
                )}
                {seg.tools.length > 0 && (
                  <div className="my-4 space-y-3">
                    {groupConsecutiveTools(seg.tools).map((row) => {
                      if (row.kind === "group") {
                        return (
                          <ToolGroup
                            key={`grp-${row.tools[0].id || row.index}`}
                            tools={row.tools}
                          />
                        );
                      }
                      const tc = row.tool;
                      return (
                        <ToolEntry
                          key={tc.id}
                          tool={tc}
                          subEvents={allSubEvents.filter(
                            (e) => e.parentToolCallId === tc.id,
                          )}
                        />
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })
        : null}

      {(() => {
        // Orphan = a subEvent captured in THIS turn's Extra whose parent
        // isn't a tool anywhere in the transcript. Cross-turn owned ones
        // are already picked up by their tool's ToolEntry via allSubEvents,
        // so filtering against the transcript-wide set of tool ids avoids
        // rendering them twice.
        const orphans = turn.subEvents.filter((e) => {
          if (!e.parentToolCallId) return true;
          return !ownedToolIds.has(e.parentToolCallId);
        });
        return orphans.length > 0 ? (
          <SubAgentTimeline events={orphans} />
        ) : null;
      })()}

      {isUser
        ? (() => {
            const { attachments, text } = parseAttachmentMarkers(turn.content);
            return (
              <>
                <div className="text-ink rounded-2xl bg-subtle px-4 py-3">
                  {attachments.length > 0 && (
                    <UserAttachmentChips attachments={attachments} />
                  )}
                  {text ? (
                    <MessageBody content={text} streaming={streaming} />
                  ) : attachments.length === 0 && streaming ? (
                    <span className="text-muted">…</span>
                  ) : null}
                </div>
                {/* Copies the parsed text, not turn.content: the raw row still
                    carries the attachment marker lines, which are chips on
                    screen and were never typed as that syntax. */}
                {text ? (
                  <div className="mt-2 flex">
                    <CopyButton text={text} />
                  </div>
                ) : null}
              </>
            );
          })()
        : null}

      {turn.role === "assistant" && !streaming && (
        <TurnFooter
          text={turn.content}
          totalTokens={turn.totalTokens}
          durationMs={turn.durationMs}
          contextLimit={contextLimit}
        />
      )}

      {turn.error && (
        <p className="mt-2 text-sm text-red-700">⚠ {turn.error}</p>
      )}
    </article>
  );
}

// SubAgentTimeline renders sub-agent events as a compact mini assistant turn:
// all thinking is merged into one Thoughts card, all tools become tool cards,
// and all text is merged into one markdown body. This mirrors the root agent
// presentation instead of exposing the raw interleaved event stream.
function SubAgentTimeline({ events }: { events: SubAgentEvent[] }) {
  type Block = {
    agent: string;
    reasoning: string;
    content: string;
    tools: ToolCall[];
    errors: string[];
  };

  const blocks: Block[] = [];
  const blockByAgent = new Map<string, Block>();
  const toolByID = new Map<string, ToolCall>();

  const blockFor = (agent: string) => {
    const existing = blockByAgent.get(agent);
    if (existing) return existing;
    const created: Block = {
      agent,
      reasoning: "",
      content: "",
      tools: [],
      errors: [],
    };
    blockByAgent.set(agent, created);
    blocks.push(created);
    return created;
  };

  for (const e of events) {
    const block = blockFor(e.agent);
    if (e.type === "tool_call") {
      const id = e.toolCallId ?? "";
      const tool: ToolCall = {
        id,
        name: e.name ?? "",
        argsJson: e.argsJson ?? "",
        status: "running",
      };
      block.tools.push(tool);
      if (id) toolByID.set(id, tool);
    } else if (e.type === "tool_result") {
      const id = e.toolCallId ?? "";
      const prev = toolByID.get(id);
      if (prev) {
        Object.assign(prev, {
          ...prev,
          name: prev.name || e.name || "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        });
      } else {
        const tool: ToolCall = {
          id,
          name: e.name ?? "",
          argsJson: "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        };
        block.tools.push(tool);
        if (id) toolByID.set(id, tool);
      }
    } else if (e.type === "thinking") {
      block.reasoning += e.content ?? "";
    } else if (e.type === "text") {
      block.content += e.content ?? "";
    } else {
      block.errors.push(e.error ?? e.content ?? "unknown error");
    }
  }

  if (blocks.length === 0) return null;

  return (
    <section className="my-4 pl-4 border-l border-ink/15 space-y-3">
      {blocks.map((block) => (
        <div key={block.agent} className="space-y-3">
          {block.reasoning && (
            <ThinkingCard
              content={block.reasoning}
              label="Thoughts"
              dense
              defaultOpen
            />
          )}
          {block.tools.length > 0 && (
            <div className="space-y-3">
              {groupConsecutiveTools(block.tools).map((row) => {
                if (row.kind === "group") {
                  return (
                    <ToolGroup
                      key={`grp-${block.agent}-${row.index}`}
                      tools={row.tools}
                    />
                  );
                }
                const tool = row.tool;
                return (
                  <ToolEntry
                    key={tool.id || `${block.agent}-${row.index}`}
                    tool={tool}
                  />
                );
              })}
            </div>
          )}
          {block.content && (
            <div className="text-ink/80">
              <MessageBody content={block.content} dense />
            </div>
          )}
          {block.errors.map((err, i) => (
            <div key={i} className="text-[13px] leading-relaxed whitespace-pre-wrap text-red-700">
              {err}
            </div>
          ))}
        </div>
      ))}
    </section>
  );
}

// ToolGroup 渲染折叠后的连续同名工具组。样式对齐 ToolEntry 但不做展开，
// 单行显示 "TOOL name × N status"。
function ToolGroup({ tools }: { tools: ToolCall[] }) {
  const name = tools[0].name;
  const status = aggregateStatus(tools);
  const { dot, label, labelClass } = statusBits(status);
  return (
    <aside className="pl-4 border-l-2 border-accent font-mono text-[12px] leading-relaxed">
      <div className="flex items-baseline gap-2">
        <span className="text-[11px] tracking-[0.14em] uppercase font-semibold shrink-0 text-ink/75">
          tool
        </span>
        <span className="text-ink">{name}</span>
        <span className="text-ink/60 tabular-nums">× {tools.length}</span>
        <span
          className={cn(
            "inline-flex items-center gap-1.5 shrink-0 ml-1 text-[11px] uppercase tracking-[0.12em]",
            labelClass,
          )}
        >
          {dot}
          <span>{label}</span>
        </span>
      </div>
    </aside>
  );
}

function ToolEntry({
  tool,
  subEvents,
}: {
  tool: ToolCall;
  subEvents?: SubAgentEvent[];
}) {
  const [open, setOpen] = useState(false);

  const argsParsed = tryParseJson(tool.argsJson);
  const hasArgs = argsParsed !== undefined && tool.argsJson !== "";
  const hasResult = Boolean(tool.content || tool.error);
  const hasSubEvents = Boolean(subEvents && subEvents.length > 0);
  const expandable = hasArgs || hasResult || hasSubEvents;
  const argLabel = toolArgLabel(argsParsed);
  const isAgent = AGENT_TOOL_NAMES.has(tool.name);

  const { dot, label, labelClass } = statusBits(tool.status);

  return (
    <aside className="pl-4 border-l-2 border-accent font-mono text-[12px] leading-relaxed">
      <button
        type="button"
        onClick={() => expandable && setOpen((v) => !v)}
        className={cn(
          "flex items-baseline gap-2 w-full text-left",
          expandable && "cursor-pointer",
        )}
      >
        <span
          className={cn(
            "text-[11px] tracking-[0.14em] uppercase font-semibold shrink-0",
            isAgent ? "text-accent" : "text-ink/75",
          )}
        >
          {isAgent ? "agent" : "tool"}
        </span>
        <span className="text-ink">{tool.name || "(unnamed)"}</span>
        {argLabel && (
          <span
            className="min-w-0 truncate text-muted normal-case tracking-normal"
            title={argLabel}
          >
            <span className="text-ink/70">{argLabel}</span>
          </span>
        )}
        <span
          className={cn(
            "inline-flex items-center gap-1.5 shrink-0 ml-1 text-[11px] uppercase tracking-[0.12em]",
            labelClass,
          )}
        >
          {dot}
          <span>{label}</span>
        </span>
      </button>

      {open && expandable && (
        <div className="mt-2 space-y-2">
          {hasSubEvents && subEvents && (
            <SubAgentTimeline events={subEvents} />
          )}
          {!hasSubEvents && hasArgs && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Args
              </div>
              <pre className="text-[11px] text-muted whitespace-pre-wrap break-all">
                {prettyJson(argsParsed)}
              </pre>
            </div>
          )}
          {!hasSubEvents && tool.content && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Result
              </div>
              <MessageBody content={truncate(tool.content, 1200)} dense />
            </div>
          )}
          {tool.error && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-red-700 mb-1">
                Error
              </div>
              <pre className="text-[11px] text-red-700 whitespace-pre-wrap break-all">
                {tool.error}
              </pre>
            </div>
          )}
        </div>
      )}
    </aside>
  );
}

function statusBits(status: ToolCall["status"]): {
  dot: React.ReactNode;
  label: string;
  labelClass: string;
} {
  if (status === "pending") {
    return {
      dot: <span className="inline-block size-1.5 rounded-full bg-amber-500" />,
      label: "pending",
      labelClass: "text-amber-700 font-medium",
    };
  }
  if (status === "running") {
    return {
      dot: (
        <span className="inline-block size-1.5 rounded-full bg-accent animate-pulse" />
      ),
      label: "running",
      labelClass: "text-accent font-medium",
    };
  }
  if (status === "ok") {
    return {
      dot: (
        <span
          aria-hidden
          className="inline-flex items-center justify-center size-3 text-emerald-600 leading-none"
        >
          <svg
            width="10"
            height="10"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M2.5 6.5l2.5 2.5 4.5-5" />
          </svg>
        </span>
      ),
      label: "done",
      labelClass: "text-emerald-600 font-medium",
    };
  }
  if (status === "cancelled") {
    return {
      dot: <span className="inline-block size-1.5 rounded-full bg-muted" />,
      label: "cancelled",
      labelClass: "text-muted font-medium",
    };
  }
  return {
    dot: (
      <span
        aria-hidden
        className="inline-flex items-center justify-center size-3 text-red-600 leading-none"
      >
        <svg
          width="10"
          height="10"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M3 3l6 6M9 3l-6 6" />
        </svg>
      </span>
    ),
    label: "failed",
    labelClass: "text-red-600 font-medium",
  };
}

function tryParseJson(s: string): unknown {
  try {
    return s ? JSON.parse(s) : undefined;
  } catch {
    return s;
  }
}

function prettyJson(v: unknown): string {
  if (v === undefined) return "";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function toolArgLabel(v: unknown): string {
  if (!v || typeof v !== "object" || Array.isArray(v)) return "";
  const args = v as Record<string, unknown>;
  // run_command 的 command 直接展示，不走 basename（命令行不是路径）。
  // CSS 的 truncate + title 属性会处理长命令。
  const command = args.command;
  if (typeof command === "string" && command) return command;
  // mv / cp 的 src → dst 组合。
  const src = args.src;
  const dst = args.dst;
  if (typeof src === "string" && src && typeof dst === "string" && dst) {
    return `${basename(src)} → ${basename(dst)}`;
  }
  for (const key of ["path", "file_path", "filepath", "target_path", "target", "output_path"]) {
    const value = args[key];
    if (typeof value === "string" && value) return basename(value);
  }
  const action = args.action;
  if (typeof action === "string" && action) return action;
  const name = args.name;
  if (typeof name === "string" && name) return name;
  return "";
}

function basename(path: string): string {
  const normalized = path.replace(/\\/g, "/").replace(/\/+$/, "");
  const idx = normalized.lastIndexOf("/");
  return idx >= 0 ? normalized.slice(idx + 1) || path : path;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n) + "…";
}
