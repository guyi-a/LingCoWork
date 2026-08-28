import { useCallback, useEffect, useRef, useState } from "react";
import {
  cancelChat,
  listPendingApprovals,
  listMessages,
  postChat,
  resumeChat,
  type MessageHistory,
  type PersistedMessage,
  type Instruction,
  type UserInstructionSnapshot,
} from "@/lib/api";
import { useWorkspaceStore } from "@/features/workspace/store";
import { useApprovalStore } from "@/features/chat/approval-store";
import { useQuestionStore } from "@/features/chat/question-store";

export type ToolCall = {
  id: string;
  name: string;
  argsJson: string;
  status: "pending" | "running" | "ok" | "error" | "cancelled";
  content?: string;
  error?: string;
};

// One ReAct iteration: what the assistant said, then the tools it called off
// the back of it. Reasoning is merged on ChatTurn across all iterations.
//
// The two fields are ordered: `content` always comes BEFORE `tools`. That is
// not a layout preference, it is the shape of an assistant message — content
// precedes tool_calls, and tool_calls end the message, so a segment can never
// hold text that came after its own tools. Nothing in this type enforces it,
// which is exactly how the renderer once drew tools first and made a reload
// disagree with the live stream. Anything consuming a segment must respect it.
export type ReactSegment = {
  content: string;
  tools: ToolCall[];
};

// One captured event from a sub-agent (e.g. deep_research) inside a single
// assistant turn. The wire shape mirrors PersistedSubAgentEvent so live SSE
// frames and history replay produce the same structure. parentToolCallId
// links the event back to the root tool_call card.
export type SubAgentEvent = {
  seq: number;
  agent: string;
  parentToolCallId?: string;
  type: "thinking" | "text" | "tool_call" | "tool_result" | "error";
  content?: string;
  toolCallId?: string;
  name?: string;
  argsJson?: string;
  ok?: boolean;
  error?: string;
};

// role "context_compacted" is a divider, not a message: everything above it
// was folded into a summary before the next turn ran. It carries no content
// — the summary goes to the model, not to the screen.
export type ChatTurn = {
  id: string;
  role: "user" | "assistant" | "context_compacted";
  content: string;
  reasoning: string;
  streamPhase?: "thinking" | "text" | "tool";
  tools: ToolCall[];
  segments: ReactSegment[];
  subEvents: SubAgentEvent[];
  createdAt: string;
  done: boolean;
  error?: string;
  replacedCount?: number;
  // Context size this turn ran against, as the provider counted it. Same
  // number the compaction estimator uses, so the readout never disagrees
  // with the thing that decides when to fold.
  totalTokens?: number;
  // Wall-clock time from the user hitting send to the run finishing.
  durationMs?: number;
  instruction?: UserInstructionSnapshot;
};

type Frame = {
  type:
    | "text"
    | "thinking"
    | "tool_call"
    | "tool_result"
    | "project_bound"
    | "usage"
    | "approval_required"
    | "question_required"
    | "context_compacted"
    | "done"
    | "error";
  // Routing
  agent?: string;
  parent_tool_call_id?: string;
  // Common
  content?: string;
  message?: string;
  // Tool
  id?: string;
  name?: string;
  args_json?: string;
  ok?: boolean;
  error?: string;
  // tool_result — the call was denied or the run was cancelled. Orthogonal to
  // `ok`: a denial returns successfully with a "you may not" payload.
  cancelled?: boolean;
  // Project (PR B, ignored for now if it ever shows up early)
  project_id?: string;
  project_name?: string;
  workspace_path?: string;
  // approval_required / question_required — links the paused tool call to
  // the resume endpoint. questions_json 只在 question_required frame 上有值。
  checkpoint_id?: string;
  interrupt_id?: string;
  questions_json?: string;
  // approval_required — 后端 effect 的序列化结果，卡片优先用它描述这次调用。
  effect_json?: string;
  // usage — one frame per model call, so `total` is the context that call
  // saw, not a running sum. The last one to arrive is the turn's occupancy.
  prompt?: number;
  completion?: number;
  total?: number;
  // context_compacted — history was folded before this run started.
  compaction_id?: number;
  replaced_count?: number;
};

const WORKSPACE_TOOL_NAMES = new Set([
  "write_file",
  "apply_patch",
  "write_file_chunked",
  "rm",
  "mv",
  "cp",
  "create_file",
  "delete_file",
  "rename_file",
  "move_file",
  "mkdir",
  "create_directory",
  "remove_file",
  "shell",
  "bash",
  "run_shell",
  "run_command",
]);

function mayAffectWorkspace(name?: string): boolean {
  if (!name) return false;
  const normalized = name.toLowerCase();
  if (WORKSPACE_TOOL_NAMES.has(normalized)) return true;
  return (
    normalized.includes("file") ||
    normalized.includes("workspace") ||
    normalized.includes("shell") ||
    normalized.includes("command")
  );
}

function parseFrames(buffer: string): { frames: Frame[]; rest: string } {
  const frames: Frame[] = [];
  let rest = buffer;
  while (true) {
    const idx = rest.indexOf("\n\n");
    if (idx < 0) break;
    const block = rest.slice(0, idx);
    rest = rest.slice(idx + 2);
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).trimStart());
      }
    }
    if (dataLines.length === 0) continue;
    try {
      frames.push(JSON.parse(dataLines.join("\n")) as Frame);
    } catch (err) {
      console.warn("[sse] bad frame", err, dataLines);
    }
  }
  return { frames, rest };
}

function commandExitCode(content?: string): number | undefined {
  if (!content) return undefined;
  try {
    const parsed = JSON.parse(content) as { exit_code?: unknown };
    return typeof parsed.exit_code === "number" ? parsed.exit_code : undefined;
  } catch {
    return undefined;
  }
}

// Whether a result is a cancellation is decided by the backend and arrives as
// a flag — see stream.IsCanceledResult. It used to be inferred here by looking
// for "cancel" anywhere in the result text, which reads a control signal out
// of arbitrary content: a tool whose output merely discussed cancellation got
// labelled cancelled despite having run fine. Harmless while every tool was
// ours and terse; an MCP server returning a page of documentation broke it.
function normalizeToolStatus(
  status: "pending" | "running" | "ok" | "error" | "cancelled" | undefined,
  ok: boolean | undefined,
  cancelled: boolean | undefined,
  content?: string,
  toolName?: string,
): ToolCall["status"] {
  if (status === "cancelled" || cancelled) {
    return "cancelled";
  }
  if (toolName === "run_command") {
    const exitCode = commandExitCode(content);
    if (exitCode !== undefined && exitCode !== 0) return "error";
  }
  if (status) return status;
  return ok ? "ok" : "error";
}

function emptySegment(): ReactSegment {
  return { content: "", tools: [] };
}

function mapPersistedTool(t: {
  id: string;
  name: string;
  args_json?: string;
  ok?: boolean;
  status?: ToolCall["status"];
  content?: string;
  error?: string;
}): ToolCall {
  return {
    id: t.id,
    name: t.name,
    argsJson: t.args_json ?? "",
    // The fold already resolved cancellation into status, including for rows
    // written before the flag existed.
    status: normalizeToolStatus(t.status, t.ok, false, t.content, t.name),
    content: t.content,
    error: t.error,
  };
}

/** Keep flat content/tools derived from segments for copy + legacy paths. */
function withDerivedFlat(
  t: ChatTurn,
  segments: ReactSegment[],
  patch?: Partial<ChatTurn>,
): ChatTurn {
  const content = segments
    .map((s) => s.content)
    .filter((c) => c.length > 0)
    .join("\n\n");
  const tools = segments.flatMap((s) => s.tools);
  return { ...t, ...patch, segments, content, tools };
}

// Move one tool card to a new status, addressing it by call id.
//
// Segments are the rendered structure; `tools` is only a flattened view that
// withDerivedFlat rebuilds from them. Patching `tools` alone therefore appears
// to work until the next frame arrives and derives the stale status back over
// it — which is how an approved call used to jump straight from PENDING to
// DONE, never showing that it was running.
//
// Returns the turn untouched when the id isn't found, so a decision arriving
// for a call this turn doesn't own can't invent a card for it.
function withToolStatus(
  t: ChatTurn,
  callId: string,
  status: ToolCall["status"],
): ChatTurn {
  for (let si = 0; si < t.segments.length; si++) {
    const ti = t.segments[si].tools.findIndex((tc) => tc.id === callId);
    if (ti < 0) continue;
    const segments = t.segments.slice();
    const seg = { ...segments[si] };
    const tools = seg.tools.slice();
    tools[ti] = { ...tools[ti], status };
    seg.tools = tools;
    segments[si] = seg;
    return withDerivedFlat(t, segments);
  }
  return t;
}

function ensureLiveSegments(t: ChatTurn): ReactSegment[] {
  if (t.segments.length > 0) return t.segments.slice();
  return [emptySegment()];
}

// Builds the divider turn for a compaction fold point.
function compactedTurn(
  id: string,
  createdAt: string,
  replacedCount?: number,
): ChatTurn {
  return {
    id,
    role: "context_compacted",
    content: "",
    reasoning: "",
    tools: [],
    segments: [],
    subEvents: [],
    createdAt,
    done: true,
    replacedCount,
  };
}

// Reconstructs a finished turn's duration from timestamps alone: an assistant
// row is written when its run ends, the user row that provoked it when the run
// began. Returns undefined when there's no user row above (a resumed or
// orphaned turn), since guessing would be worse than showing nothing.
function elapsedSincePrecedingUser(
  rows: PersistedMessage[],
  assistantIdx: number,
): number | undefined {
  for (let i = assistantIdx - 1; i >= 0; i--) {
    if (rows[i].role !== "user") continue;
    const ms = Date.parse(rows[assistantIdx].created_at) - Date.parse(rows[i].created_at);
    return Number.isFinite(ms) && ms >= 0 ? ms : undefined;
  }
  return undefined;
}

function fromPersisted(rows: PersistedMessage[]): ChatTurn[] {
  return rows
    .filter(
      (r) =>
        r.role === "user" ||
        r.role === "assistant" ||
        r.role === "context_compacted",
    )
    .map((r, i, all) => {
      if (r.role === "context_compacted") {
        return compactedTurn(
          `compaction-${r.compaction_id ?? r.seq}`,
          r.created_at,
          r.replaced_count,
        );
      }
      const flatTools = (r.tools ?? []).map(mapPersistedTool);
      let segments: ReactSegment[];
      if (r.segments && r.segments.length > 0) {
        segments = r.segments.map((s) => ({
          content: s.content ?? "",
          tools: (s.tools ?? []).map(mapPersistedTool),
        }));
      } else if (r.role === "assistant") {
        // Legacy API without segments → one segment from flat fields.
        segments = [{ content: r.content, tools: flatTools }];
      } else {
        segments = [];
      }
      const derived =
        r.role === "assistant"
          ? withDerivedFlat(
              {
                id: `db-${r.seq}`,
                role: "assistant",
                content: "",
                reasoning: r.reasoning_content ?? "",
                tools: [],
                segments: [],
                subEvents: [],
                createdAt: r.created_at,
                done: true,
                totalTokens: r.total_tokens,
                durationMs: elapsedSincePrecedingUser(all, i),
              },
              segments,
            )
          : null;
      if (derived) {
        return {
          ...derived,
          subEvents: (r.sub_events ?? []).map((e) => ({
            seq: e.seq,
            agent: e.agent,
            parentToolCallId: e.parent_tool_call_id,
            type: e.type,
            content: e.content,
            toolCallId: e.tool_call_id,
            name: e.name,
            argsJson: e.args_json,
            ok: e.ok,
            error: e.error,
          })),
        };
      }
      return {
        id: `db-${r.seq}`,
        role: "user" as const,
        content:
          (r.user_instruction ?? r.instruction)?.raw_input ?? r.content,
        reasoning: "",
        tools: [],
        segments: [],
        subEvents: [],
        createdAt: r.created_at,
        done: true,
        instruction: r.user_instruction ?? r.instruction,
      };
    });
}

export type ProjectBoundEvent = {
  projectId: string;
  projectName: string;
  workspacePath: string;
};

// Drives an already-opened SSE Response: reads frames and routes them to the
// caller-provided mutators. Returns normally on `done`/`error` frame or when
// the server closes the stream. Throws AbortError when the underlying fetch
// is aborted — caller handles that.
async function runSSELoop(
  res: Response,
  updateAssistant: (fn: (t: ChatTurn) => ChatTurn) => void,
  upsertTool: (id: string, patch: Partial<ToolCall>) => void,
  appendSubEvent: (
    agentName: string,
    partial: Omit<SubAgentEvent, "seq" | "agent">,
  ) => void,
  onProjectBound: ((e: ProjectBoundEvent) => void) | undefined,
  onWorkspaceChanged: (() => void) | undefined,
  // onInterruptRequired 处理任意 HITL 中断 frame（approval_required /
  // question_required）。上游按 f.type 分发到不同 store。
  onInterruptRequired: ((frame: Frame) => void) | undefined,
  onCompacted: ((frame: Frame) => void) | undefined,
  // Epoch ms of when this run was started, or undefined when we joined a run
  // already in flight (resume) and therefore can't time it honestly.
  startedAtMs: number | undefined,
  onError: (msg: string) => void,
) {
  if (!res.ok || !res.body) {
    throw new Error(`SSE: ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let finished = false;
  while (!finished) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const { frames, rest } = parseFrames(buf);
    buf = rest;
    for (const f of frames) {
      // Sub-agent frames (e.g. deep_research's internal thinking + tool
      // calls) route to subEvents so they neither pollute the supervisor's
      // content/reasoning nor compete with the supervisor's tool cards.
      if (f.agent) {
        switch (f.type) {
          case "thinking":
          case "text":
            if (f.content) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: f.type,
                content: f.content,
              });
            }
            break;
          case "tool_call":
            if (f.id) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: "tool_call",
                toolCallId: f.id,
                name: f.name,
                argsJson: f.args_json,
              });
            }
            break;
          case "tool_result":
            if (f.id) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: "tool_result",
                toolCallId: f.id,
                name: f.name,
                ok: f.ok,
                content: f.ok ? f.content : undefined,
                error: f.ok ? undefined : f.error ?? f.message,
              });
              if (f.ok && mayAffectWorkspace(f.name)) onWorkspaceChanged?.();
            }
            break;
          case "error":
            appendSubEvent(f.agent, {
              parentToolCallId: f.parent_tool_call_id,
              type: "error",
              error: f.message ?? f.error ?? "unknown error",
            });
            break;
          // usage / project_bound / done from a sub-agent are ignored —
          // those are root-agent concerns.
        }
        continue;
      }

      switch (f.type) {
        case "text":
          if (f.content)
            updateAssistant((t) => {
              const segs = ensureLiveSegments(t);
              // After tools landed on the current segment, a new text chunk
              // starts the next ReAct iteration.
              if (segs[segs.length - 1].tools.length > 0) {
                segs.push(emptySegment());
              }
              const last = { ...segs[segs.length - 1] };
              last.content = last.content + f.content!;
              segs[segs.length - 1] = last;
              return withDerivedFlat(t, segs, { streamPhase: "text" });
            });
          break;
        case "thinking":
          if (f.content)
            updateAssistant((t) => ({
              ...t,
              streamPhase: "thinking",
              reasoning: t.reasoning + f.content,
            }));
          break;
        case "tool_call":
          if (f.id) {
            updateAssistant((t) => ({ ...t, streamPhase: "tool" }));
            upsertTool(f.id, {
              name: f.name ?? "",
              argsJson: f.args_json ?? "",
              status: "running",
            });
          }
          break;
        case "tool_result":
          if (f.id) {
            updateAssistant((t) => ({ ...t, streamPhase: "tool" }));
            const status = normalizeToolStatus(
              undefined,
              f.ok,
              f.cancelled,
              f.content,
              f.name,
            );
            upsertTool(f.id, {
              name: f.name ?? undefined,
              status,
              content: f.ok ? f.content : undefined,
              error: f.ok ? undefined : f.error ?? f.message,
            });
            if (status === "ok" && mayAffectWorkspace(f.name)) {
              onWorkspaceChanged?.();
            }
          }
          break;
        case "project_bound":
          if (f.project_id) {
            onProjectBound?.({
              projectId: f.project_id,
              projectName: f.project_name ?? "",
              workspacePath: f.workspace_path ?? "",
            });
          }
          break;
        case "approval_required":
        case "question_required":
          if (f.interrupt_id) {
            onInterruptRequired?.(f);
          }
          break;
        case "context_compacted":
          onCompacted?.(f);
          break;
        case "usage":
          // Sub-agent frames never reach here (filtered above), so every
          // usage frame measures the main thread. A ReAct loop emits one per
          // model call and each is a superset of the last — overwrite.
          if (f.total) {
            updateAssistant((t) => ({ ...t, totalTokens: f.total }));
          }
          break;
        case "done":
          updateAssistant((t) => ({
            ...t,
            done: true,
            streamPhase: undefined,
            durationMs:
              startedAtMs === undefined
                ? t.durationMs
                : Date.now() - startedAtMs,
          }));
          finished = true;
          break;
        case "error":
          updateAssistant((t) => ({
            ...t,
            done: true,
            streamPhase: undefined,
            error: f.message ?? "unknown error",
          }));
          onError(f.message ?? "unknown error");
          finished = true;
          break;
      }
      if (finished) break;
    }
  }
}

export function useChatStream(
  conversationID: string,
  opts?: {
    onProjectBound?: (e: ProjectBoundEvent) => void;
    projectId?: string;
  },
) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [contextLimit, setContextLimit] = useState(0);
  const [loading, setLoading] = useState(true);
  const [streaming, setStreaming] = useState(false);
  // True from mount until the reconnect probe has settled. `loading` clears
  // earlier — the moment history is on screen — leaving a window where a run
  // is very much alive on the server but `streaming` is still false. Anything
  // that decides "is this conversation free" has to wait out that window.
  const [reconnecting, setReconnecting] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const onProjectBoundRef = useRef(opts?.onProjectBound);
  onProjectBoundRef.current = opts?.onProjectBound;
  const refreshWorkspaceFiles = useWorkspaceStore((s) => s.refreshFiles);
  const refreshWorkspaceFilesRef = useRef(refreshWorkspaceFiles);
  refreshWorkspaceFilesRef.current = refreshWorkspaceFiles;
  const projectIdRef = useRef(opts?.projectId);
  projectIdRef.current = opts?.projectId;
  const addApproval = useApprovalStore((s) => s.add);
  const clearApprovals = useApprovalStore((s) => s.clear);
  const addApprovalRef = useRef(addApproval);
  const clearApprovalsRef = useRef(clearApprovals);
  addApprovalRef.current = addApproval;
  clearApprovalsRef.current = clearApprovals;
  const addQuestion = useQuestionStore((s) => s.add);
  const clearQuestions = useQuestionStore((s) => s.clear);
  const addQuestionRef = useRef(addQuestion);
  const clearQuestionsRef = useRef(clearQuestions);
  addQuestionRef.current = addQuestion;
  clearQuestionsRef.current = clearQuestions;
  const turnsRef = useRef(turns);
  turnsRef.current = turns;

  // Drives a Response into the assistant turn with the given id. Owns the
  // streaming flag, abort-ref bookkeeping, and AbortError/error -> turn-state
  // translation. Used by both send (POST) and the mount-time resume (GET).
  const runStreamingResponse = useCallback(
    async (
      res: Response,
      assistantTurnId: string,
      controller: AbortController,
      startedAtMs?: number,
    ) => {
      const updateAssistant = (fn: (t: ChatTurn) => ChatTurn) => {
        setTurns((prev) => {
          const next = prev.slice();
          for (let i = next.length - 1; i >= 0; i--) {
            if (next[i].id === assistantTurnId) {
              next[i] = fn(next[i]);
              break;
            }
          }
          return next;
        });
      };

      const upsertTool = (id: string, patch: Partial<ToolCall>) => {
        updateAssistant((t) => {
          const segs = ensureLiveSegments(t);
          // Prefer updating an existing tool anywhere; otherwise append to
          // the current (last) segment.
          let segIdx = -1;
          let toolIdx = -1;
          for (let si = 0; si < segs.length; si++) {
            const ti = segs[si].tools.findIndex((tc) => tc.id === id);
            if (ti >= 0) {
              segIdx = si;
              toolIdx = ti;
              break;
            }
          }
          const clean: Partial<ToolCall> = {};
          (Object.keys(patch) as (keyof ToolCall)[]).forEach((k) => {
            const v = patch[k];
            if (v === "") return;
            (clean as Record<string, unknown>)[k] = v;
          });

          if (segIdx < 0) {
            const nextTool: ToolCall = {
              id,
              name: patch.name ?? "",
              argsJson: patch.argsJson ?? "",
              status: patch.status ?? "running",
              content: patch.content,
              error: patch.error,
            };
            const last = { ...segs[segs.length - 1] };
            last.tools = [...last.tools, nextTool];
            segs[segs.length - 1] = last;
            return withDerivedFlat(t, segs);
          }

          const seg = { ...segs[segIdx] };
          const tools = seg.tools.slice();
          tools[toolIdx] = { ...tools[toolIdx], ...clean };
          seg.tools = tools;
          segs[segIdx] = seg;
          return withDerivedFlat(t, segs);
        });
      };

      // Append one sub-agent event. Consecutive thinking/text chunks from the
      // same agent are coalesced into the previous event so the rendered
      // narrative reads as continuous prose rather than per-token noise.
      // Everything else (tool_call / tool_result / error) pushes a new entry.
      const appendSubEvent = (
        agentName: string,
        partial: Omit<SubAgentEvent, "seq" | "agent">,
      ) => {
        updateAssistant((t) => {
          const next = t.subEvents.slice();
          const last = next[next.length - 1];
          const coalescable =
            partial.type === "thinking" || partial.type === "text";
          if (
            coalescable &&
            last &&
            last.agent === agentName &&
            last.type === partial.type &&
            last.parentToolCallId === partial.parentToolCallId
          ) {
            next[next.length - 1] = {
              ...last,
              content: (last.content ?? "") + (partial.content ?? ""),
            };
          } else {
            next.push({
              seq: next.length + 1,
              agent: agentName,
              ...partial,
            });
          }
          return { ...t, subEvents: next };
        });
      };

      try {
        await runSSELoop(
          res,
          updateAssistant,
          upsertTool,
          appendSubEvent,
          onProjectBoundRef.current,
          refreshWorkspaceFilesRef.current,
          (f) => {
            if (!f.interrupt_id) return;
            if (f.id) {
              // Patch only — do NOT create a new tool card. Sub-agent 内的
              // 中断带的 CallID 来自子 agent 的 id 空间，跟顶层 tool_call
              // 对不上；upsertTool 会造出幽灵 "(unnamed) PENDING" 卡。
              // 底部 dock 仍然按 interrupt_id 显示，不受影响。
              updateAssistant((t) => {
                const segs = ensureLiveSegments(t);
                let changed = false;
                const nextSegs = segs.map((seg) => {
                  const idx = seg.tools.findIndex((tc) => tc.id === f.id);
                  if (idx < 0) return seg;
                  changed = true;
                  const tools = seg.tools.slice();
                  tools[idx] = { ...tools[idx], status: "pending" };
                  return { ...seg, tools };
                });
                if (!changed) return t;
                return withDerivedFlat(t, nextSegs);
              });
            }
            if (f.type === "question_required") {
              addQuestionRef.current(conversationID, {
                interruptId: f.interrupt_id,
                callId: f.id ?? "",
                questionsJson: f.questions_json ?? "",
              });
            } else {
              addApprovalRef.current(conversationID, {
                interruptId: f.interrupt_id,
                callId: f.id ?? "",
                tool: f.name ?? "",
                argsJson: f.args_json ?? "",
                effectJson: f.effect_json ?? "",
              });
            }
          },
          (f) => {
            // The fold covered everything BEFORE this run, so the divider
            // belongs above the user message that started it — not where
            // the frame happens to arrive in the stream.
            setTurns((prev) => {
              const assistantIdx = prev.findIndex(
                (t) => t.id === assistantTurnId,
              );
              if (assistantIdx < 0) return prev;
              let at = assistantIdx;
              for (let i = assistantIdx - 1; i >= 0; i--) {
                if (prev[i].role === "user") {
                  at = i;
                  break;
                }
              }
              const marker = compactedTurn(
                `compaction-${f.compaction_id ?? Date.now()}`,
                new Date().toISOString(),
                f.replaced_count,
              );
              if (prev.some((t) => t.id === marker.id)) return prev;
              return [...prev.slice(0, at), marker, ...prev.slice(at)];
            });
          },
          startedAtMs,
          setError,
        );
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") {
          updateAssistant((t) => ({ ...t, done: true, error: "已取消" }));
        } else {
          console.error("[chat] stream error:", err);
          const msg = String(err);
          updateAssistant((t) => ({ ...t, done: true, error: msg }));
          setError(msg);
        }
      } finally {
        refreshWorkspaceFilesRef.current();
        setStreaming(false);
        if (abortRef.current === controller) abortRef.current = null;
      }
    },
    [conversationID],
  );

  useEffect(() => {
    let cancelled = false;
    setTurns([]);
    setLoading(true);
    setReconnecting(true);
    setError(null);

    const controller = new AbortController();

    (async () => {
      let history: MessageHistory;
      try {
        history = await listMessages(conversationID);
      } catch (err) {
        if (cancelled) return;
        console.error("[chat] load history failed:", err);
        setError(String(err));
        setLoading(false);
        setReconnecting(false);
        return;
      }
      if (cancelled) return;
      setContextLimit(history.contextLimit);
      const initialTurns = fromPersisted(history.messages);
      setTurns(initialTurns);

      try {
        const items = await listPendingApprovals(conversationID);
        if (cancelled) return;
        // 两类中断共用一个 REST 端点，前端拉回来后按 kind 分发到各自的 store。
        clearApprovalsRef.current(conversationID);
        clearQuestionsRef.current(conversationID);
        for (const item of items) {
          if (!item.interrupt_id) continue;
          if (item.kind === "question") {
            addQuestionRef.current(conversationID, {
              interruptId: item.interrupt_id,
              callId: item.call_id ?? "",
              questionsJson: item.questions_json ?? "",
            });
          } else {
            addApprovalRef.current(conversationID, {
              interruptId: item.interrupt_id,
              callId: item.call_id ?? "",
              tool: item.tool ?? "",
              argsJson: item.args_json ?? "",
              effectJson: item.effect_json ?? "",
            });
          }
        }
      } catch (err) {
        if (!cancelled) console.error("[approval] load pending failed:", err);
      }

      setLoading(false);

      // Always probe for an in-flight stream — backend returns 204 when there
      // is no live buffer, so we don't need to guess from the persisted rows.
      let res: Response | null;
      try {
        res = await resumeChat(conversationID, controller.signal);
      } catch (err) {
        // Never clear the flag once cancelled: the effect has already re-run
        // for a different conversation and re-armed it, and this stale
        // closure would leave that one's probe window unguarded.
        if (cancelled || (err as { name?: string }).name === "AbortError")
          return;
        console.error("[chat] resume probe failed:", err);
        setReconnecting(false);
        return;
      }
      if (cancelled) return;
      if (!res) {
        setReconnecting(false);
        return;
      }

      const existingTurn = [...initialTurns]
        .reverse()
        .find(
          (t) =>
            t.role === "assistant" &&
            t.tools.some(
              (tool) => tool.status === "pending" || tool.status === "running",
            ),
        );

      let targetId: string;
      if (existingTurn) {
        targetId = existingTurn.id;
        setTurns((prev) =>
          prev.map((t) =>
            t.id === targetId ? { ...t, done: false, error: undefined } : t,
          ),
        );
      } else {
        const nowIso = new Date().toISOString();
        const assistantTurn: ChatTurn = {
          id: `a-resume-${nowIso}`,
          role: "assistant",
          content: "",
          reasoning: "",
          tools: [],
          segments: [{ content: "", tools: [] }],
          subEvents: [],
          createdAt: nowIso,
          done: false,
        };
        targetId = assistantTurn.id;
        setTurns((prev) => [...prev, assistantTurn]);
      }
      setStreaming(true);
      setReconnecting(false);
      abortRef.current = controller;
      await runStreamingResponse(res, targetId, controller);
    })();

    return () => {
      cancelled = true;
      controller.abort();
      if (abortRef.current === controller) abortRef.current = null;
    };
  }, [conversationID, runStreamingResponse]);

  const send = useCallback(
    async (
      text: string,
      instruction?: Pick<Instruction, "name" | "label">,
    ) => {
      if (streaming) return false;
      const trimmed = text.trim();
      if (!trimmed && !instruction) return false;

      const startedAtMs = Date.now();
      const nowIso = new Date(startedAtMs).toISOString();
      const userTurn: ChatTurn = {
        id: `u-${nowIso}`,
        role: "user",
        content: trimmed,
        reasoning: "",
        tools: [],
        segments: [],
        subEvents: [],
        createdAt: nowIso,
        done: true,
        instruction: instruction
          ? {
              name: instruction.name,
              label: instruction.label,
              raw_input: trimmed,
            }
          : undefined,
      };
      const assistantTurn: ChatTurn = {
        id: `a-${nowIso}`,
        role: "assistant",
        content: "",
        reasoning: "",
        tools: [],
        segments: [{ content: "", tools: [] }],
        subEvents: [],
        createdAt: nowIso,
        done: false,
      };
      setTurns((prev) => [...prev, userTurn, assistantTurn]);
      setStreaming(true);
      setError(null);

      const controller = new AbortController();
      abortRef.current = controller;

      let res: Response;
      try {
        res = await postChat(conversationID, trimmed, controller.signal, {
          projectId: projectIdRef.current,
          instruction: instruction ? { name: instruction.name } : undefined,
        });
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") {
          setTurns((prev) =>
            prev.map((t) =>
              t.id === assistantTurn.id
                ? { ...t, done: true, error: "已取消" }
                : t,
            ),
          );
        } else {
          const msg = String(err);
          console.error("[chat] post failed:", err);
          setTurns((prev) =>
            prev.map((t) =>
              t.id === assistantTurn.id ? { ...t, done: true, error: msg } : t,
            ),
          );
          setError(msg);
        }
        setStreaming(false);
        if (abortRef.current === controller) abortRef.current = null;
        return false;
      }

      await runStreamingResponse(res, assistantTurn.id, controller, startedAtMs);
      return true;
    },
    [conversationID, streaming, runStreamingResponse],
  );

  const cancel = useCallback(async () => {
    abortRef.current?.abort();
    abortRef.current = null;
    await cancelChat(conversationID);
  }, [conversationID]);

  const markApprovalHandled = useCallback(
    (callId: string, decision: "approve" | "deny") => {
      if (!callId) return;
      const status: ToolCall["status"] =
        decision === "approve" ? "running" : "cancelled";
      setTurns((prev) =>
        prev.map((turn) =>
          turn.role === "assistant" ? withToolStatus(turn, callId, status) : turn,
        ),
      );
    },
    [],
  );

  // markQuestionAnswered 是 ask_user 版本：用户答完 → tool 卡从 pending 打
  // 回 running（等真正 resume 后再 flip 到 ok / error）；取消 → 标 cancelled。
  const markQuestionAnswered = useCallback(
    (callId: string, cancelled: boolean) => {
      if (!callId) return;
      const status: ToolCall["status"] = cancelled ? "cancelled" : "running";
      setTurns((prev) =>
        prev.map((turn) =>
          turn.role === "assistant" ? withToolStatus(turn, callId, status) : turn,
        ),
      );
    },
    [],
  );

  // Reconnect to a freshly created SSE stream — used after an approval
  // decision, where the backend has spun up a new run into a new buffer
  // and the previous SSE connection has already closed. Continuation events
  // belong to the SAME assistant turn that was mid-flight when the interrupt
  // fired: reusing its id lets tool_result frames find the matching tool_call
  // entry (which was left in the "running" state pre-interrupt) and flip it
  // to done, instead of orphaning both.
  const resume = useCallback(async () => {
    if (streaming) return;
    const controller = new AbortController();
    let res: Response | null;
    try {
      res = await resumeChat(conversationID, controller.signal);
    } catch (err) {
      if ((err as { name?: string }).name === "AbortError") return;
      console.error("[chat] resume after approval failed:", err);
      return;
    }
    if (!res) return;

    const lastAssistant = [...turnsRef.current]
      .reverse()
      .find((t) => t.role === "assistant");

    let targetId: string;
    if (lastAssistant) {
      targetId = lastAssistant.id;
      setTurns((prev) =>
        prev.map((t) =>
          t.id === targetId ? { ...t, done: false, error: undefined } : t,
        ),
      );
    } else {
      const nowIso = new Date().toISOString();
      const assistantTurn: ChatTurn = {
        id: `a-resume-${nowIso}`,
        role: "assistant",
        content: "",
        reasoning: "",
        tools: [],
        segments: [{ content: "", tools: [] }],
        subEvents: [],
        createdAt: nowIso,
        done: false,
      };
      targetId = assistantTurn.id;
      setTurns((prev) => [...prev, assistantTurn]);
    }

    setStreaming(true);
    abortRef.current = controller;
    await runStreamingResponse(res, targetId, controller);
  }, [conversationID, streaming, runStreamingResponse]);

  return {
    turns,
    contextLimit,
    loading,
    streaming,
    reconnecting,
    error,
    send,
    cancel,
    resume,
    markApprovalHandled,
    markQuestionAnswered,
  };
}
