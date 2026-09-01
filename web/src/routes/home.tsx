import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { PromptInput } from "@/features/chat/PromptInput";
import { AgentModeDropdown } from "@/features/chat/AgentModeDropdown";
import { AttachmentChips } from "@/features/chat/AttachmentChips";
import {
  useAttachmentsStore,
  saveImageFiles,
  type AttachedFile,
} from "@/features/chat/attachments-store";
import {
  chooseWorkspaceDirectory,
  electronAPI,
} from "@/lib/electron-api";
import { cn } from "@/lib/utils";
import type { AgentMode, Instruction, ProjectItem } from "@/lib/api";
import { useProjectStore } from "@/stores/projects";

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
  const openFolder = useProjectStore((s) => s.openFolder);
  const [project, setProject] = useState<ProjectItem | null>(null);
  const [workspacePending, setWorkspacePending] = useState(false);
  const [workspaceError, setWorkspaceError] = useState("");
  const [mode, setMode] = useState<AgentMode>("agent");

  const onSend = (text: string, instruction?: Instruction) => {
    if (!project) return;
    navigate(`/c/${draftId}`, {
      state: {
        pending: text,
        pendingInstruction: instruction,
        projectId: project.id,
        mode,
      },
    });
  };

  const onPickWorkspace = useCallback(async () => {
    setWorkspaceError("");
    setWorkspacePending(true);
    try {
      const path = await chooseWorkspaceDirectory();
      if (!path) return;
      const selected = await openFolder(path);
      setProject(selected);
    } catch (err) {
      setWorkspaceError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorkspacePending(false);
    }
  }, [openFolder]);

  const onPickFiles = useCallback(async () => {
    if (!electronAPI) return;
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
      if (!electronAPI) return;
      void saveImageFiles(draftId, files, electronAPI.savePastedImage, addAttachments);
    },
    [draftId, addAttachments],
  );

  return (
    <>
      <header className="shrink-0 h-6 drag-region" aria-hidden />
      <div className="flex min-h-0 flex-1 items-center overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl py-10">
          <div className="px-8 text-center">
            <div className="mb-4 font-mono text-[10px] uppercase tracking-[0.2em] text-muted">
              LingCoWork · Co-work · with AI
            </div>
            <h2 className="mb-3 text-2xl">跟 LingCoWork 一起开工</h2>
            <p className="text-sm leading-relaxed text-muted">
              选择一个工作区，然后告诉我你想完成什么。
            </p>
          </div>

          <div className="mt-7">
            <PromptInput
              streaming={false}
              blocked={!project || workspacePending}
              context={
                project
                  ? {
                      conversationId: draftId,
                      projectId: project.id,
                      workspace: project.workspace,
                    }
                  : undefined
              }
              placeholder={
                project
                  ? "描述任务 · 输入 @ 添加工作区上下文"
                  : "请先选择工作区"
              }
              blockedHint="请先选择工作区"
              onSend={onSend}
              onCancel={() => {}}
              hasAttachments={attachments.length > 0}
              topSlot={
                <>
                  <AttachmentChips conversationID={draftId} />
                  {workspaceError && (
                    <p className="px-5 pt-3 text-xs text-red-600">
                      {workspaceError}
                    </p>
                  )}
                </>
              }
              leftActions={
                <div className="flex min-w-0 items-center gap-2">
                  <WorkspaceChip
                    project={project}
                    pending={workspacePending}
                    onClick={onPickWorkspace}
                  />
                  {project && electronAPI && (
                    <AttachButton onClick={onPickFiles} />
                  )}
                </div>
              }
              rightActions={
                <AgentModeDropdown mode={mode} onChange={setMode} />
              }
              onImageFiles={project && electronAPI ? onImageFiles : undefined}
            />
          </div>

        </div>
      </div>
    </>
  );
}

function WorkspaceChip({
  project,
  pending,
  onClick,
}: {
  project: ProjectItem | null;
  pending: boolean;
  onClick: () => void;
}) {
  const label = pending
    ? "正在打开…"
    : project?.name ?? "选择工作区";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={pending}
      title={project?.workspace ?? "选择已有文件夹作为工作区"}
      className={cn(
        "inline-flex h-7 min-w-0 max-w-52 items-center gap-1.5 rounded-md border px-2 text-[12px] transition-colors",
        project
          ? "border-accent/40 bg-subtle text-ink"
          : "border-rule bg-paper text-accent hover:bg-subtle",
        "disabled:cursor-not-allowed disabled:opacity-60",
      )}
    >
      <FolderIcon />
      <span className="truncate">{label}</span>
      <ChevronDownIcon />
    </button>
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

function FolderIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0 text-muted"
      aria-hidden
    >
      <path d="M3 6.5A2.5 2.5 0 0 1 5.5 4H10l2 2h6.5A2.5 2.5 0 0 1 21 8.5v8A2.5 2.5 0 0 1 18.5 19h-13A2.5 2.5 0 0 1 3 16.5v-10Z" />
    </svg>
  );
}

function ChevronDownIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-3 shrink-0 text-muted"
      aria-hidden
    >
      <path d="m4 6 4 4 4-4" />
    </svg>
  );
}
