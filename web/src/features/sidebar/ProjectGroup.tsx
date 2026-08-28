import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ConversationItem } from "./ConversationItem";
import { ProjectMenu } from "./ProjectMenu";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { cn } from "@/lib/utils";
import type {
  ConversationItem as ConvItem,
  ProjectItem,
} from "@/lib/api";

export function ProjectGroup({
  project,
  conversations,
}: {
  project: ProjectItem;
  conversations: ConvItem[];
}) {
  const [open, setOpen] = useState(true);
  const [menuOpen, setMenuOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [closeOpen, setCloseOpen] = useState(false);
  const navigate = useNavigate();
  const { id: activeId } = useParams();
  const refreshConvs = useConversationStore((s) => s.refresh);
  const removeProject = useProjectStore((s) => s.remove);

  const onNewConversation = () => {
    const id = crypto.randomUUID();
    navigate(`/c/${id}`, { state: { projectId: project.id } });
  };

  const onConfirmClose = async () => {
    setCloseOpen(false);
    const warning = await removeProject(project.id).catch((e) => {
      console.error(e);
      return undefined;
    });
    if (warning) {
      console.warn("project closed with warning:", warning);
    }
    await refreshConvs();
    // If the active conversation was inside this project, route home.
    const wasInside = conversations.some((c) => c.id === activeId);
    if (wasInside) navigate("/");
  };

  return (
    <div className="group/project relative">
      <div
        className={cn(
          "h-7 px-2.5 flex items-center gap-1.5 rounded-lg",
          "hover:bg-rule/70 transition-colors",
        )}
      >
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex items-center gap-2 flex-1 min-w-0 text-left text-ink cursor-pointer"
        >
          <ChevronIcon open={open} />
          <FolderIcon />
          <span className="text-[13px] leading-none truncate">
            {project.name || project.id}
          </span>
        </button>
        <button
          type="button"
          onClick={onNewConversation}
          title={`在 ${project.name || project.id} 中新建对话`}
          aria-label={`在 ${project.name || project.id} 中新建对话`}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted transition-colors hover:bg-paper hover:text-ink"
        >
          <PlusIcon />
        </button>
        <div
          className="flex size-6 shrink-0 items-center justify-center"
        >
          <ProjectMenu
            project={project}
            open={menuOpen}
            onOpenChange={setMenuOpen}
            onRename={() => setRenameOpen(true)}
            onCloseProject={() => setCloseOpen(true)}
          />
        </div>
      </div>

      {open && (
        <ul className="py-0.5">
          {conversations.length === 0 ? (
            <li className="pl-8 pr-2 py-0.5">
              <button
                type="button"
                onClick={onNewConversation}
                className="text-[11px] text-muted/70 transition-colors hover:text-ink"
              >
                + 开始对话
              </button>
            </li>
          ) : (
            conversations.map((c) => (
              <ConversationItem key={c.id} item={c} indent />
            ))
          )}
        </ul>
      )}

      <RenameDialog
        open={renameOpen}
        onOpenChange={setRenameOpen}
        project={project}
      />
      <CloseProjectDialog
        open={closeOpen}
        onOpenChange={setCloseOpen}
        project={project}
        conversationCount={conversations.length}
        onConfirm={onConfirmClose}
      />
    </div>
  );
}

// --- Icons ---

function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={cn(
        "shrink-0 text-muted transition-transform duration-150",
        open && "rotate-90",
      )}
    >
      <path d="M4 2 L8 6 L4 10" />
    </svg>
  );
}

function FolderIcon() {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="shrink-0 text-muted/90"
    >
      <path d="M2 4.5A1.5 1.5 0 0 1 3.5 3H6l1.5 1.5H12.5A1.5 1.5 0 0 1 14 6v6.5A1.5 1.5 0 0 1 12.5 14h-9A1.5 1.5 0 0 1 2 12.5V4.5Z" />
    </svg>
  );
}

function PlusIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      aria-hidden
    >
      <path d="M8 3v10M3 8h10" />
    </svg>
  );
}

// --- Rename dialog ---

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

function RenameDialog({
  open,
  onOpenChange,
  project,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: ProjectItem;
}) {
  const rename = useProjectStore((s) => s.rename);
  const [value, setValue] = useState(project.name);
  const [pending, setPending] = useState(false);

  // Reset input when dialog opens with a different project.
  if (open && !pending && value !== project.name && !value) {
    setValue(project.name);
  }

  const submit = async () => {
    const v = value.trim();
    if (!v || v === project.name) {
      onOpenChange(false);
      return;
    }
    setPending(true);
    try {
      await rename(project.id, v);
      onOpenChange(false);
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => { onOpenChange(v); if (v) setValue(project.name); }}>
      <DialogContent aria-describedby="rename-desc">
        <DialogTitle>Rename project</DialogTitle>
        <DialogDescription id="rename-desc">
          仅修改项目展示名，项目 ID（{project.id}）和工作区路径不变。
        </DialogDescription>
        <input
          type="text"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
            if (e.key === "Escape") onOpenChange(false);
          }}
          autoFocus
          className="mt-4 w-full px-3 py-2 border border-rule focus:border-ink focus:outline-none bg-paper text-[14px]"
          placeholder="项目名"
        />
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="px-3 py-1.5 text-[13px] border border-rule hover:border-ink cursor-pointer"
          >
            取消
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={pending || !value.trim()}
            className="px-3 py-1.5 text-[13px] bg-accent text-paper hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            {pending ? "保存中…" : "保存"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// --- Close project confirm dialog ---

function CloseProjectDialog({
  open,
  onOpenChange,
  project,
  conversationCount,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: ProjectItem;
  conversationCount: number;
  onConfirm: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);

  const submit = async () => {
    setPending(true);
    try {
      await onConfirm();
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby="close-project-desc">
        <DialogTitle>关闭项目</DialogTitle>
        <DialogDescription id="close-project-desc">
          将从 LingCoWork 中移除项目「<span className="text-ink">{project.name || project.id}</span>」
          及其下 {conversationCount} 段会话与全部消息。磁盘上的工作区目录
          <span className="font-mono text-[11px] text-ink"> {project.workspace}</span>
          {" "}不会被删除；之后仍可重新打开该文件夹。会话移除后不可恢复。
        </DialogDescription>
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="px-3 py-1.5 text-[13px] border border-rule hover:border-ink cursor-pointer"
          >
            取消
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={pending}
            className="px-3 py-1.5 text-[13px] bg-red-600 text-paper hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            {pending ? "关闭中…" : "确认关闭"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
