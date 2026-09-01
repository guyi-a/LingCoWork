import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router";
import { useChatStream } from "@/hooks/useChatStream";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { Transcript } from "@/features/chat/Transcript";
import { PromptInput } from "@/features/chat/PromptInput";
import { ConversationHeader } from "@/features/chat/ConversationHeader";
import { PendingInterruptDock } from "@/features/chat/PendingInterruptDock";
import { ApprovalModeDropdown } from "@/features/chat/ApprovalModeDropdown";
import { AgentModeDropdown } from "@/features/chat/AgentModeDropdown";
import { useAgentModeStore } from "@/features/chat/agent-mode-store";
import { usePlanStore } from "@/features/chat/plan-store";
import { PlanReviewCard } from "@/features/chat/PlanReviewCard";
import { TodoPanel } from "@/features/chat/TodoPanel";
import { AttachmentChips } from "@/features/chat/AttachmentChips";
import {
  useAttachmentsStore,
  saveImageFiles,
  serializeAttachments,
  parseAttachmentMarkers,
  type AttachedFile,
} from "@/features/chat/attachments-store";
import { QueuedMessagesBar } from "@/features/chat/QueuedMessagesBar";
import { useQueueStore } from "@/features/chat/queue-store";
import { useQueueFlush } from "@/features/chat/useQueueFlush";
import { useApprovalStore, type PendingApproval } from "@/features/chat/approval-store";
import { useQuestionStore, type PendingQuestion } from "@/features/chat/question-store";
import { electronAPI } from "@/lib/electron-api";
import { cn } from "@/lib/utils";
import { WorkspacePanel } from "@/features/workspace/WorkspacePanel";
import type { AgentMode, Instruction, WorkPlan } from "@/lib/api";

// Stable empty array — Zustand selector must return the same ref when the
// store hasn't changed, otherwise useSyncExternalStore loops (same trick
// used in the approval and attachments stores).
const EMPTY_ATTACHMENTS: AttachedFile[] = [];
const EMPTY_APPROVALS: PendingApproval[] = [];
const EMPTY_QUESTIONS: PendingQuestion[] = [];
const EMPTY_PLANS: WorkPlan[] = [];

// Sidebar preview for a message whose attachment markers are already baked
// into the text. Prose wins; a marker-only message falls back to the first
// attachment's name.
function previewOf(finalText: string): string {
  const { attachments, text } = parseAttachmentMarkers(finalText);
  return text.trim() || attachments[0]?.name || "附件";
}

export function Conversation() {
  const { id } = useParams();
  if (!id) return null;

  const location = useLocation();
  const state = location.state as
    | {
        pending?: string;
        pendingInstruction?: Instruction;
        projectId?: string;
        mode?: AgentMode;
      }
    | null;
  const pending = state?.pending;
  const pendingInstruction = state?.pendingInstruction;
  const conversationProjectId = useConversationStore(
    (s) => s.items.find((item) => item.id === id)?.project_id ?? undefined,
  );
  const conversationsLoaded = useConversationStore((s) => s.loaded);
  const projectId = state?.projectId ?? conversationProjectId ?? undefined;
  const projectWorkspace = useProjectStore(
    (s) => s.items.find((item) => item.id === projectId)?.workspace,
  );
  const cachedMode = useAgentModeStore((s) => s.modes[id]);
  const setModeLocal = useAgentModeStore((s) => s.setLocal);
  const loadMode = useAgentModeStore((s) => s.load);
  const saveMode = useAgentModeStore((s) => s.save);
  const mode = cachedMode ?? state?.mode ?? "agent";

  const touch = useConversationStore((s) => s.touch);
  const refreshConvs = useConversationStore((s) => s.refresh);
  const refreshProjects = useProjectStore((s) => s.refresh);

  useEffect(() => {
    if (!conversationsLoaded) void refreshConvs();
  }, [conversationsLoaded, refreshConvs]);

  useEffect(() => {
    if (state?.mode) {
      setModeLocal(id, state.mode);
      return;
    }
    void loadMode(id).catch((err) => {
      console.error("[agent-mode] load failed:", err);
    });
  }, [id, loadMode, setModeLocal, state?.mode]);

  const attachments = useAttachmentsStore(
    (s) => s.pending[id] ?? EMPTY_ATTACHMENTS,
  );
  const addAttachments = useAttachmentsStore((s) => s.add);
  const clearAttachments = useAttachmentsStore((s) => s.clear);

  const onProjectBound = useCallback(() => {
    // Legacy replay compatibility: older streams may still carry this frame.
    refreshConvs();
    refreshProjects();
  }, [refreshConvs, refreshProjects]);

  const {
    turns,
    loading,
    streaming,
    reconnecting,
    contextLimit,
    error,
    send,
    cancel,
    resume,
    markApprovalHandled,
    markQuestionAnswered,
  } = useChatStream(id, {
    onProjectBound,
    projectId,
    mode,
  });

  // A run paused on an approval or an ask_user reports streaming=false — its
  // SSE buffer was finished at the interrupt — so `streaming` alone can't tell
  // us whether the conversation is free. The composer blocks on this, and the
  // queue holds behind it.
  const approvals = useApprovalStore((s) => s.pending[id] ?? EMPTY_APPROVALS);
  const questions = useQuestionStore((s) => s.pending[id] ?? EMPTY_QUESTIONS);
  const plan = usePlanStore((s) => s.plans[id] ?? null);
  const planHistory = usePlanStore((s) => s.history[id] ?? EMPTY_PLANS);
  const pendingPlan = usePlanStore((s) => s.pending[id]);
  const setPlan = usePlanStore((s) => s.setPlan);
  const clearPendingPlan = usePlanStore((s) => s.clearPending);
  const hitlPending =
    approvals.length > 0 || questions.length > 0 || !!pendingPlan;

  // Answering an interrupt drops it from its store immediately, but the
  // continuation run only exists once resume's probe comes back. In between,
  // every flag we have reads idle. Hold it shut explicitly.
  const [resuming, setResuming] = useState(false);

  // The conversation might have a run attached on the server side. Wider than
  // `streaming`: `reconnecting` covers the mount-time probe and `resuming` the
  // post-interrupt handoff, both of which are windows where a run is live but
  // no frames have reached us yet.
  const busy = streaming || reconnecting || resuming;

  const enqueue = useQueueStore((s) => s.enqueue);
  const setQueuePaused = useQueueStore((s) => s.setPaused);

  // The actual send, shared by the composer and the queue flusher. Takes
  // text with attachment markers already baked in.
  const dispatch = useCallback(
    async (
      finalText: string,
      instruction?: Pick<Instruction, "name" | "label">,
    ) => {
      touch(
        id,
        finalText.trim()
          ? previewOf(finalText)
          : instruction?.label ?? "快捷指令",
        { projectId },
      );
      const sent = await send(finalText, instruction);
      if (sent) {
        refreshConvs();
        refreshProjects();
      }
      return sent;
    },
    [id, projectId, touch, send, refreshConvs, refreshProjects],
  );

  const onSend = async (text: string, instruction?: Instruction) => {
    // Snapshot then clear so the chip strip disappears in the same paint
    // the user's message renders in the transcript. If the send throws we
    // don't restore — the attachments are already visible in the sent
    // message's marker text, so re-adding them would be confusing. Queued
    // messages freeze their attachments here too, so changing the selection
    // afterwards can't rewrite a message that's already in line.
    const files = attachments;
    const markers = files.length > 0 ? serializeAttachments(files) : "";
    const finalText = markers
      ? text.trim()
        ? `${markers}\n${text}`
        : markers
      : text;
    if (files.length > 0) clearAttachments(id);

    if (busy) {
      enqueue(
        id,
        finalText,
        instruction
          ? { name: instruction.name, label: instruction.label }
          : undefined,
      );
      return;
    }
    await dispatch(finalText, instruction);
  };

  useQueueFlush({
    conversationID: id,
    busy,
    hitlPending,
    error,
    dispatch,
  });

  // Stopping is an interrupt, so it holds the queue rather than letting the
  // next message fire the instant the run unwinds. The bar's "继续发送" is
  // how the user releases it.
  const onCancel = useCallback(async () => {
    setQueuePaused(id, true);
    await cancel();
  }, [id, setQueuePaused, cancel]);

  const onPickFiles = useCallback(async () => {
    if (!electronAPI) return;
    try {
      const picked = await electronAPI.pickFiles();
      if (picked.length > 0) addAttachments(id, picked);
    } catch (err) {
      console.error("[attach] pickFiles failed:", err);
    }
  }, [id, addAttachments]);

  // Paste / drag-drop images. PromptInput has already filtered these to
  // image/* Files; we just persist the bytes via Electron IPC and add the
  // resulting paths to the store so they render as image chips and go out
  // as [image:] markers on send.
  const onImageFiles = useCallback(
    (files: File[]) => {
      if (!electronAPI) return;
      void saveImageFiles(id, files, electronAPI.savePastedImage, addAttachments);
    },
    [id, addAttachments],
  );

  // After the user answers an approval, refresh the sidebar immediately so
  // the "等待审批" pill reflects that the interrupt was handled. The resumed
  // stream will refresh again when it drains for the final idle / next-pending
  // state. This also runs in the same batch as the approval store's drop,
  // which is what keeps the queue from catching a momentarily idle-looking
  // conversation before resume takes over.
  const onApprovalDecision = useCallback(async (
    item: { callId: string },
    decision: "approve" | "deny",
    resumed: boolean,
  ) => {
    if (resumed) setResuming(true);
    markApprovalHandled(item.callId, decision, resumed);
    refreshConvs();
    refreshProjects();
  }, [markApprovalHandled, refreshConvs, refreshProjects]);

  // ask_user 场景：用户答完 / 取消后立即把对应 tool 卡从 pending 打成
  // running 或 cancelled，避免 UI 卡在 pending 状态直到 resume 事件回填。
  const onQuestionDecision = useCallback(async (
    callId: string,
    cancelled: boolean,
    resumed: boolean,
  ) => {
    if (resumed) setResuming(true);
    markQuestionAnswered(callId, cancelled, resumed);
    refreshConvs();
    refreshProjects();
  }, [markQuestionAnswered, refreshConvs, refreshProjects]);

  // Both interrupt cards call this right after their decision callback, and
  // resume only settles once the continuation stream has fully drained — so
  // clearing the hold here can't reopen the gap it was covering.
  const onApprovalResume = useCallback(async () => {
    setResuming(true);
    try {
      await resume();
    } finally {
      setResuming(false);
    }
    refreshConvs();
    refreshProjects();
  }, [resume, refreshConvs, refreshProjects]);

  const onModeChange = useCallback(
    (next: AgentMode) => {
      if (busy || hitlPending) return;
      void saveMode(id, next).catch((err) => {
        console.error("[agent-mode] save failed:", err);
      });
    },
    [busy, hitlPending, id, saveMode],
  );

  const onPlanResolved = useCallback(
    async (resumed: boolean, cancelled: boolean) => {
      if (pendingPlan?.callId) {
        markQuestionAnswered(pendingPlan.callId, cancelled, resumed);
      }
      clearPendingPlan(id);
      if (!cancelled) setModeLocal(id, "agent");
      if (resumed) await onApprovalResume();
      refreshConvs();
      refreshProjects();
    },
    [
      clearPendingPlan,
      id,
      onApprovalResume,
      markQuestionAnswered,
      pendingPlan?.callId,
      refreshConvs,
      refreshProjects,
      setModeLocal,
    ],
  );

  const pendingFiredRef = useRef(false);
  useEffect(() => {
    if (loading) return;
    if (!projectId) return;
    if (pending === undefined && !pendingInstruction) return;
    if (pendingFiredRef.current) return;
    pendingFiredRef.current = true;
    window.history.replaceState({}, "");
    onSend(pending ?? "", pendingInstruction);
  }, [loading, pending, pendingInstruction, projectId]);

  if (loading || (!projectId && !conversationsLoaded)) {
    return (
      <>
        <ConversationHeader conversationId={id} projectId={projectId} />
        <div className="flex-1 flex items-center justify-center">
          <p className="text-sm text-muted">加载会话…</p>
        </div>
      </>
    );
  }

  if (!projectId) {
    return <Navigate to="/" replace />;
  }

  return (
    <>
      <ConversationHeader conversationId={id} projectId={projectId} />
      <div className="flex-1 min-h-0 flex">
        <div className="flex-1 min-w-0 flex flex-col">
          <Transcript
            turns={turns}
            streaming={streaming || resuming}
            contextLimit={contextLimit}
            plans={planHistory}
            pendingPlanID={pendingPlan?.planId}
            trailing={
              plan &&
              pendingPlan &&
              pendingPlan.planId === plan.id ? (
                <PlanReviewCard
                  conversationID={id}
                  plan={plan}
                  interruptID={pendingPlan.interruptId}
                  onPlan={(next) => setPlan(id, next)}
                  onResolved={onPlanResolved}
                />
              ) : undefined
            }
          />
          {plan &&
            !pendingPlan &&
            (plan.status === "active" ||
              plan.status === "completed" ||
              plan.status === "cancelled") && <TodoPanel plan={plan} />}
          {/* Outside the relative wrapper on purpose: the interrupt dock
              covers that wrapper edge to edge, and a queued message is worth
              seeing while deciding on an approval. */}
          <QueuedMessagesBar conversationID={id} />
          <div className="relative">
            <PromptInput
              streaming={streaming}
              blocked={hitlPending}
              context={
                projectId && projectWorkspace
                  ? {
                      conversationId: id,
                      projectId,
                      workspace: projectWorkspace,
                    }
                  : undefined
              }
              onSend={onSend}
              onCancel={onCancel}
              hasAttachments={attachments.length > 0}
              topSlot={<AttachmentChips conversationID={id} />}
              leftActions={electronAPI ? <AttachButton onClick={onPickFiles} /> : undefined}
              rightActions={
                <div className="flex items-center gap-2">
                  <AgentModeDropdown
                    mode={mode}
                    disabled={busy || hitlPending}
                    onChange={onModeChange}
                  />
                  <ApprovalModeDropdown conversationID={id} />
                </div>
              }
              onImageFiles={electronAPI ? onImageFiles : undefined}
            />
            {!streaming && (
              <PendingInterruptDock
                conversationID={id}
                onApprovalDecision={onApprovalDecision}
                onQuestionDecision={onQuestionDecision}
                onResume={onApprovalResume}
              />
            )}
          </div>
        </div>
        <WorkspacePanel
          streaming={streaming}
          projectId={projectId}
        />
      </div>
    </>
  );
}

function AttachButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="附加文件或文件夹"
      aria-label="附加文件或文件夹"
      className={cn(
        "inline-flex size-7 items-center justify-center rounded-md",
        "border border-rule/60 bg-paper text-muted transition-colors",
        "hover:bg-subtle hover:text-ink",
      )}
    >
      <PlusIcon />
    </button>
  );
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3.5" aria-hidden>
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}
