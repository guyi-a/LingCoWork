import { useCallback, useEffect, useRef } from "react";
import { useLocation, useParams } from "react-router";
import { useChatStream } from "@/hooks/useChatStream";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { Transcript } from "@/features/chat/Transcript";
import { PromptInput } from "@/features/chat/PromptInput";
import { ConversationHeader } from "@/features/chat/ConversationHeader";
import { PendingInterruptDock } from "@/features/chat/PendingInterruptDock";
import { ApprovalModeDropdown } from "@/features/chat/ApprovalModeDropdown";
import { AttachmentChips } from "@/features/chat/AttachmentChips";
import {
  useAttachmentsStore,
  saveImageFiles,
  serializeAttachments,
  type AttachedFile,
} from "@/features/chat/attachments-store";
import { electronAPI } from "@/lib/electron-api";
import { cn } from "@/lib/utils";
import { WorkspacePanel } from "@/features/workspace/WorkspacePanel";

// Stable empty array — Zustand selector must return the same ref when the
// store hasn't changed, otherwise useSyncExternalStore loops (same trick
// used in the approval and attachments stores).
const EMPTY_ATTACHMENTS: AttachedFile[] = [];

export function Conversation() {
  const { id } = useParams();
  if (!id) return null;

  const location = useLocation();
  const state = location.state as
    | { pending?: string; projectId?: string }
    | null;
  const pending = state?.pending;
  const conversationProjectId = useConversationStore(
    (s) => s.items.find((item) => item.id === id)?.project_id ?? undefined,
  );
  const projectId = state?.projectId ?? conversationProjectId ?? undefined;

  const touch = useConversationStore((s) => s.touch);
  const refreshConvs = useConversationStore((s) => s.refresh);
  const refreshProjects = useProjectStore((s) => s.refresh);

  const attachments = useAttachmentsStore(
    (s) => s.pending[id] ?? EMPTY_ATTACHMENTS,
  );
  const addAttachments = useAttachmentsStore((s) => s.add);
  const clearAttachments = useAttachmentsStore((s) => s.clear);

  const onProjectBound = useCallback(() => {
    // Conversation just got bound to a project — refresh both stores so
    // the sidebar can immediately move this item from Ad-hoc to the new
    // project group, mid-stream.
    refreshConvs();
    refreshProjects();
  }, [refreshConvs, refreshProjects]);

  const {
    turns,
    loading,
    streaming,
    contextLimit,
    send,
    cancel,
    resume,
    markApprovalHandled,
    markQuestionAnswered,
  } = useChatStream(id, {
    onProjectBound,
    projectId,
  });

  const onSend = async (text: string) => {
    // Snapshot then clear so the chip strip disappears in the same paint
    // the user's message renders in the transcript. If the send throws we
    // don't restore — the attachments are already visible in the sent
    // message's marker text, so re-adding them would be confusing.
    const files = attachments;
    const markers = files.length > 0 ? serializeAttachments(files) : "";
    const finalText = markers
      ? text.trim()
        ? `${markers}\n${text}`
        : markers
      : text;

    touch(id, text.trim() || files[0]?.name || "附件", { projectId });
    if (files.length > 0) clearAttachments(id);
    await send(finalText);
    refreshConvs();
    refreshProjects();
  };

  const onPickFiles = useCallback(async () => {
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
      void saveImageFiles(id, files, electronAPI.savePastedImage, addAttachments);
    },
    [id, addAttachments],
  );

  // After the user answers an approval, refresh the sidebar immediately so
  // the "等待审批" pill reflects that the interrupt was handled. The resumed
  // stream will refresh again when it drains for the final idle / next-pending
  // state.
  const onApprovalDecision = useCallback(async (item: { callId: string }, decision: "approve" | "deny") => {
    markApprovalHandled(item.callId, decision);
    refreshConvs();
    refreshProjects();
  }, [markApprovalHandled, refreshConvs, refreshProjects]);

  // ask_user 场景：用户答完 / 取消后立即把对应 tool 卡从 pending 打成
  // running 或 cancelled，避免 UI 卡在 pending 状态直到 resume 事件回填。
  const onQuestionDecision = useCallback(async (callId: string, cancelled: boolean) => {
    markQuestionAnswered(callId, cancelled);
    refreshConvs();
    refreshProjects();
  }, [markQuestionAnswered, refreshConvs, refreshProjects]);

  const onApprovalResume = useCallback(async () => {
    await resume();
    refreshConvs();
    refreshProjects();
  }, [resume, refreshConvs, refreshProjects]);

  const pendingFiredRef = useRef(false);
  useEffect(() => {
    if (loading) return;
    if (!pending) return;
    if (pendingFiredRef.current) return;
    pendingFiredRef.current = true;
    window.history.replaceState({}, "");
    onSend(pending);
  }, [loading, pending]);

  if (loading) {
    return (
      <>
        <ConversationHeader conversationId={id} />
        <div className="flex-1 flex items-center justify-center">
          <p className="text-sm text-muted">加载会话…</p>
        </div>
      </>
    );
  }

  return (
    <>
      <ConversationHeader conversationId={id} />
      <div className="flex-1 min-h-0 flex">
        <div className="flex-1 min-w-0 flex flex-col">
          <Transcript
            turns={turns}
            streaming={streaming}
            contextLimit={contextLimit}
          />
          <div className="relative">
            <PromptInput
              streaming={streaming}
              onSend={onSend}
              onCancel={cancel}
              hasAttachments={attachments.length > 0}
              topSlot={<AttachmentChips conversationID={id} />}
              leftActions={<AttachButton onClick={onPickFiles} />}
              rightActions={<ApprovalModeDropdown conversationID={id} />}
              onImageFiles={onImageFiles}
            />
            <PendingInterruptDock
              conversationID={id}
              onApprovalDecision={onApprovalDecision}
              onQuestionDecision={onQuestionDecision}
              onResume={onApprovalResume}
            />
          </div>
        </div>
        <WorkspacePanel streaming={streaming} projectId={projectId} />
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
