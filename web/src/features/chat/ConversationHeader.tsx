import { useEffect, useMemo, useState } from "react";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { useWorkspaceStore } from "@/features/workspace/store";
import { MemoryEditor, MemoryIcon } from "@/features/memory/MemoryEditor";
import { cn } from "@/lib/utils";
import {
  fetchWorkspaceStatus,
  type WorkspaceRepositoryStatus,
} from "@/lib/api";
import { useProblemsStore } from "@/features/workspace/problems-store";

export function ConversationHeader({
  conversationId,
  projectId,
}: {
  conversationId: string;
  projectId?: string;
}) {
  const conversations = useConversationStore((s) => s.items);
  const projects = useProjectStore((s) => s.items);
  const panelOpen = useWorkspaceStore((s) => s.panelOpen);
  const togglePanel = useWorkspaceStore((s) => s.togglePanel);
  const setPanelOpen = useWorkspaceStore((s) => s.setPanelOpen);
  const setActiveTab = useWorkspaceStore((s) => s.setActiveTab);
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const [memoryOpen, setMemoryOpen] = useState(false);
  const [repoStatus, setRepoStatus] =
    useState<WorkspaceRepositoryStatus | null>(null);
  const problemCount = useProblemsStore(
    (s) => s.data[conversationId]?.error_count ?? 0,
  );

  const { title, projectName, workspace, hasProject } = useMemo(() => {
    const conv = conversations.find((c) => c.id === conversationId);
    const effectiveProjectId = projectId ?? conv?.project_id ?? undefined;
    const project = effectiveProjectId
      ? projects.find((p) => p.id === effectiveProjectId)
      : null;
    return {
      title: conv?.title || "新建会话",
      projectName: project?.name ?? "",
      workspace: project?.workspace ?? "",
      hasProject: !!effectiveProjectId,
    };
  }, [conversations, projects, conversationId, projectId]);

  useEffect(() => {
    if (!hasProject) {
      setRepoStatus(null);
      return;
    }
    const controller = new AbortController();
    void fetchWorkspaceStatus(
      conversationId,
      { projectId },
      controller.signal,
    )
      .then(setRepoStatus)
      .catch((err) => {
        if ((err as { name?: string }).name !== "AbortError") {
          setRepoStatus(null);
        }
      });
    return () => controller.abort();
  }, [conversationId, filesVersion, hasProject, projectId]);

  return (
    <header className="drag-region shrink-0 min-h-[50px] flex items-start gap-3 px-4 py-3 border-b border-rule bg-paper">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-baseline gap-2.5">
          {projectName && (
            <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted shrink-0">
              {projectName}
            </span>
          )}
          <h2
            className="min-w-0 flex-1 text-[15px] leading-5 text-ink truncate"
            title={title}
          >
            {title}
          </h2>
        </div>
        {workspace && (
          <div className="flex min-w-0 items-center gap-2 font-mono text-[10px] leading-4 text-muted/70">
            <span className="min-w-0 truncate" title={workspace}>
              {workspace}
            </span>
            {repoStatus?.git_repository && repoStatus.branch && (
              <span
                className="shrink-0 rounded bg-subtle px-1.5 text-muted"
                title={repoStatus.detached ? "Detached HEAD" : "当前 Git 分支"}
              >
                {repoStatus.detached ? `@${repoStatus.branch}` : repoStatus.branch}
              </span>
            )}
            {repoStatus?.git_repository && repoStatus.dirty && (
              <span
                className="shrink-0 text-amber-600 dark:text-amber-400"
                title={`暂存 ${repoStatus.staged} · 未暂存 ${repoStatus.unstaged} · 未跟踪 ${repoStatus.untracked}`}
              >
                ● {repoStatus.changed_files}
              </span>
            )}
            {!!repoStatus?.ahead && (
              <span className="shrink-0" title="领先上游">
                ↑{repoStatus.ahead}
              </span>
            )}
            {!!repoStatus?.behind && (
              <span className="shrink-0" title="落后上游">
                ↓{repoStatus.behind}
              </span>
            )}
          </div>
        )}
      </div>

      {/* 只有绑了工作区才有项目记忆可编辑。 */}
      {problemCount > 0 && (
        <button
          type="button"
          onClick={() => {
            setPanelOpen(true);
            setActiveTab("problems");
          }}
          className="no-drag shrink-0 rounded px-2 py-1 font-mono text-[10px] text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-950/40"
          title="查看 Problems"
        >
          {problemCount} errors
        </button>
      )}

      {hasProject && (
        <button
          type="button"
          onClick={() => setMemoryOpen(true)}
          title="编辑项目记忆"
          className={cn(
            "no-drag shrink-0 inline-flex items-center gap-1.5 px-2.5 py-1 rounded",
            "text-[11px] font-mono uppercase tracking-[0.14em]",
            "cursor-pointer transition-colors",
            "text-muted hover:text-ink hover:bg-subtle",
          )}
        >
          <MemoryIcon />
          <span>Memory</span>
        </button>
      )}

      <button
        type="button"
        onClick={togglePanel}
        aria-pressed={panelOpen}
        title={panelOpen ? "关闭工作区" : "打开工作区"}
        className={cn(
          "no-drag shrink-0 inline-flex items-center gap-1.5 px-2.5 py-1 rounded",
          "text-[11px] font-mono uppercase tracking-[0.14em]",
          "cursor-pointer transition-colors",
          panelOpen
            ? "bg-subtle text-accent"
            : "text-muted hover:text-ink hover:bg-subtle",
        )}
      >
        <PanelIcon open={panelOpen} />
        <span>Workspace</span>
      </button>

      <MemoryEditor
        open={memoryOpen}
        onClose={() => setMemoryOpen(false)}
        conversationId={conversationId}
      />
    </header>
  );
}

function PanelIcon({ open }: { open: boolean }) {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="2" y="3" width="12" height="10" rx="1.5" />
      <path d="M10 3v10" />
      {open && <path d="M12 6l-1.5 2 1.5 2" />}
    </svg>
  );
}
