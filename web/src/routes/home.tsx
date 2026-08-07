import { useCallback, useMemo } from "react";
import { useNavigate } from "react-router";
import { PromptInput } from "@/features/chat/PromptInput";
import { AttachmentChips } from "@/features/chat/AttachmentChips";
import {
  useAttachmentsStore,
  saveImageFiles,
  type AttachedFile,
} from "@/features/chat/attachments-store";
import { electronAPI } from "@/lib/electron-api";
import { cn } from "@/lib/utils";
import type { Instruction } from "@/lib/api";

// Stable empty array — see attachments-store notes on why the selector
// must not return a fresh literal each render.
const EMPTY_ATTACHMENTS: AttachedFile[] = [];

export function Home() {
  const navigate = useNavigate();

  // Mint the conversation id up front so the attach store can key off it
  // BEFORE we navigate. When Conversation mounts under this same id it
  // finds the attachments already in place and prepends their markers to
  // the pending first message. useMemo (not useRef) so React Strict Mode
  // double-mount in dev still yields a stable id across the double render.
  const draftId = useMemo(() => crypto.randomUUID(), []);

  const attachments = useAttachmentsStore(
    (s) => s.pending[draftId] ?? EMPTY_ATTACHMENTS,
  );
  const addAttachments = useAttachmentsStore((s) => s.add);

  const onSend = (text: string, instruction?: Instruction) => {
    navigate(`/c/${draftId}`, {
      state: { pending: text, pendingInstruction: instruction },
    });
  };

  const onPickFiles = useCallback(async () => {
    try {
      const picked = await electronAPI.pickFiles();
      if (picked.length > 0) addAttachments(draftId, picked);
    } catch (err) {
      console.error("[attach] pickFiles failed:", err);
    }
  }, [draftId, addAttachments]);

  // Same paste/drop pipeline as Conversation. The draftId is what the
  // subsequent Conversation route will use as its store key, so files
  // dropped here appear as chips on both pages.
  const onImageFiles = useCallback(
    (files: File[]) => {
      void saveImageFiles(draftId, files, electronAPI.savePastedImage, addAttachments);
    },
    [draftId, addAttachments],
  );

  return (
    <>
      <header className="shrink-0 h-6 drag-region" aria-hidden />
      <div className="flex-1 flex items-center justify-center">
        <div className="max-w-md text-center px-8">
          <div className="font-mono text-[10px] tracking-[0.2em] uppercase text-muted mb-4">
            LingCoWork · Co-work · with AI
          </div>
          <h2 className="text-2xl mb-3">跟 LingCoWork 一起开工</h2>
          <p className="text-sm text-muted leading-relaxed">
            在下方输入以开始，或从左侧打开已有会话。
            模型的推理过程会作为边注呈现。
          </p>
        </div>
      </div>
      <PromptInput
        streaming={false}
        onSend={onSend}
        onCancel={() => {}}
        hasAttachments={attachments.length > 0}
        topSlot={<AttachmentChips conversationID={draftId} />}
        leftActions={<AttachButton onClick={onPickFiles} />}
        onImageFiles={onImageFiles}
      />
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
