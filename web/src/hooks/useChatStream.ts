import { useCallback, useEffect, useRef, useState } from "react";
import {
  cancelChat,
  listPendingApprovals,
  listMessages,
  postChat,
  resumeChat,
  type PersistedMessage,
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

// One ReAct iteration: tools for that iteration + assistant text. Reasoning
// is merged on ChatTurn across all iterations.
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

export type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning: string;
  streamPhase?: "thinking" | "text" | "tool";
  tools: ToolCall[];
  segments: ReactSegment[];
  subEvents: SubAgentEvent[];
  createdAt: string;
  done: boolean;
  error?: string;
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
  // Project (PR B, ignored for now if it ever shows up early)
  project_id?: string;
  project_name?: string;
  workspace_path?: string;
  // approval_required / question_required — links the paused tool call to
  // the resume endpoint. questions_json 只在 question_required frame 上有值。
  checkpoint_id?: string;
  interrupt_id?: string;
  questions_json?: string;
};

const WORKSPACE_TOOL_NAMES = new Set([
  "write_file",
  "edit_file",
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

function isCancelledToolResult(content?: string, error?: string): boolean {
  const value = `${content ?? ""}\n${error ?? ""}`.toLowerCase();
  return (
    value.includes("用户拒绝执行") ||
    value.includes("[canceled]") ||
    value.includes("[cancelled]") ||
    value.includes("canceled") ||
    value.includes("cancelled")
  );
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

function normalizeToolStatus(
  status: "pending" | "running" | "ok" | "error" | "cancelled" | undefined,
  ok: boolean | undefined,
  content?: string,
  error?: string,
  toolName?: string,
): ToolCall["status"] {
  if (status === "cancelled" || isCancelledToolResult(content, error)) {
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
    status: normalizeToolStatus(t.status, t.ok, t.content, t.error, t.name),
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

function ensureLiveSegments(t: ChatTurn): ReactSegment[] {
  if (t.segments.length > 0) return t.segments.slice();
  return [emptySegment()];
}

function fromPersisted(rows: PersistedMessage[]): ChatTurn[] {
  return rows
    .filter((r) => r.role === "user" || r.role === "assistant")
    .map((r) => {
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
        content: r.content,
        reasoning: "",
        tools: [],
        segments: [],
        subEvents: [],
        createdAt: r.created_at,
        done: true,
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
              f.content,
              f.error ?? f.message,
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
        case "usage":
          break;
        case "done":
          updateAssistant((t) => ({ ...t, done: true, streamPhase: undefined }));
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
  const [loading, setLoading] = useState(true);
  const [streaming, setStreaming] = useState(false);
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
              });
            }
          },
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
    setError(null);

    const controller = new AbortController();

    (async () => {
      let rows: PersistedMessage[];
      try {
        rows = await listMessages(conversationID);
      } catch (err) {
        if (cancelled) return;
        console.error("[chat] load history failed:", err);
        setError(String(err));
        setLoading(false);
        return;
      }
      if (cancelled) return;
      const initialTurns = fromPersisted(rows);
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
        if (cancelled || (err as { name?: string }).name === "AbortError")
          return;
        console.error("[chat] resume probe failed:", err);
        return;
      }
      if (cancelled || !res) return;

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
    async (text: string) => {
      if (streaming) return;
      const trimmed = text.trim();
      if (!trimmed) return;

      const nowIso = new Date().toISOString();
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
        return;
      }

      await runStreamingResponse(res, assistantTurn.id, controller);
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
        prev.map((turn) => {
          if (turn.role !== "assistant") return turn;
          let changed = false;
          const tools = turn.tools.map((tool) => {
            if (tool.id !== callId) return tool;
            changed = true;
            return { ...tool, status };
          });
          return changed ? { ...turn, tools } : turn;
        }),
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
        prev.map((turn) => {
          if (turn.role !== "assistant") return turn;
          let changed = false;
          const tools = turn.tools.map((tool) => {
            if (tool.id !== callId) return tool;
            changed = true;
            return { ...tool, status };
          });
          return changed ? { ...turn, tools } : turn;
        }),
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
    loading,
    streaming,
    error,
    send,
    cancel,
    resume,
    markApprovalHandled,
    markQuestionAnswered,
  };
}
