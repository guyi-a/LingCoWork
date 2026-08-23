import { useMemo, useState } from "react";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { useWorkspaceStore } from "@/features/workspace/store";
import { MemoryEditor, MemoryIcon } from "@/features/memory/MemoryEditor";
import { cn } from "@/lib/utils";

export function ConversationHeader({
  conversationId,
}: {
  conversationId: string;
}) {
  const conversations = useConversationStore((s) => s.items);
  const projects = useProjectStore((s) => s.items);
  const panelOpen = useWorkspaceStore((s) => s.panelOpen);
  const togglePanel = useWorkspaceStore((s) => s.togglePanel);
  const [memoryOpen, setMemoryOpen] = useState(false);

  const { title, projectName, hasProject } = useMemo(() => {
    const conv = conversations.find((c) => c.id === conversationId);
    if (!conv) return { title: "", projectName: "", hasProject: false };
    const project = conv.project_id
      ? projects.find((p) => p.id === conv.project_id)
      : null;
    return {
      title: conv.title || "新建会话",
      projectName: project?.name ?? "",
      hasProject: !!conv.project_id,
    };
  }, [conversations, projects, conversationId]);

  return (
    <header className="drag-region shrink-0 min-h-[50px] flex items-start gap-3 px-4 py-3 border-b border-rule bg-paper">
      <div className="min-w-0 flex-1 flex items-baseline gap-2.5">
        {projectName && (
          <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted shrink-0">
            {projectName}
          </span>
        )}
        <h2
          className="min-w-0 flex-1 text-[15px] leading-6 text-ink truncate"
          title={title}
        >
          {title}
        </h2>
      </div>

      {/* 只有绑了工作区才有项目记忆可编辑。临时对话下这个按钮不出现 ——
          让它出现再报"没有工作区"是把一个已知答案做成一次点击。 */}
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
        <span>Files</span>
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
