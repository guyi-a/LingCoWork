import { useEffect, useMemo, useState } from "react";
import { NavLink, useNavigate } from "react-router";
import { useConversationStore } from "@/stores/conversations";
import { useProjectStore } from "@/stores/projects";
import { ConversationItem } from "./ConversationItem";
import { ProjectGroup } from "./ProjectGroup";

export function Sidebar() {
  const navigate = useNavigate();
  const [collapsed, setCollapsed] = useState(false);
  const conversations = useConversationStore((s) => s.items);
  const convLoading = useConversationStore((s) => s.loading);
  const refreshConv = useConversationStore((s) => s.refresh);
  const projects = useProjectStore((s) => s.items);
  const refreshProjects = useProjectStore((s) => s.refresh);

  useEffect(() => {
    refreshConv();
    refreshProjects();
  }, [refreshConv, refreshProjects]);

  const { adhocConversations, byProject } = useMemo(() => {
    const adhoc: typeof conversations = [];
    const byPid = new Map<string, typeof conversations>();
    for (const c of conversations) {
      if (c.project_id) {
        const list = byPid.get(c.project_id) ?? [];
        list.push(c);
        byPid.set(c.project_id, list);
      } else {
        adhoc.push(c);
      }
    }
    return { adhocConversations: adhoc, byProject: byPid };
  }, [conversations]);

  const onNew = () => navigate("/");

  return (
    <aside className="relative w-[280px] shrink-0 flex flex-col overflow-hidden rounded-[26px] bg-paper/92 shadow-[0_0_0_1px_var(--color-rule),0_18px_60px_oklch(0_0_0/0.08)] backdrop-blur transition-[width] duration-200 ease-out data-[collapsed=true]:w-16" data-collapsed={collapsed}>
      <header className="shrink-0 h-6 drag-region" aria-hidden />

      {collapsed ? (
        <div className="flex flex-col items-center gap-1 px-2 pt-0">
          <button
            type="button"
            onClick={() => setCollapsed(false)}
            aria-label="展开侧边栏"
            title="展开侧边栏"
            className="flex size-10 items-center justify-center rounded-xl text-muted transition-colors hover:bg-rule/70 hover:text-ink cursor-pointer"
          >
            <CollapseIcon collapsed />
          </button>
          <button
            type="button"
            onClick={onNew}
            aria-label="新建对话"
            title="新建对话"
            className="flex size-10 items-center justify-center rounded-xl text-muted transition-colors hover:bg-rule/70 hover:text-ink cursor-pointer"
          >
            <PlusIcon />
          </button>
        </div>
      ) : (
        <nav className="sidebar-scroll flex-1 overflow-y-auto pl-4 pr-1 pb-4 pt-0">
          <div className="pr-3">
            <div className="mb-1.5 flex h-10 w-full items-center rounded-xl transition-colors hover:bg-rule/70">
              <button
                type="button"
                onClick={onNew}
                className="flex h-full min-w-0 flex-1 items-center gap-3 px-3 text-left text-[15px] font-medium text-ink cursor-pointer"
              >
                <span className="flex size-4 shrink-0 items-center justify-center">
                  <PlusIcon />
                </span>
                <span className="truncate">新建对话</span>
              </button>
              <button
                type="button"
                onClick={() => setCollapsed(true)}
                aria-label="收缩侧边栏"
                title="收缩侧边栏"
                className="mr-1 flex size-8 shrink-0 items-center justify-center rounded-lg text-muted transition-colors hover:bg-paper/80 hover:text-ink cursor-pointer"
              >
                <CollapseIcon />
              </button>
            </div>

            <GroupLabel label="Projects" count={projects.length} />
            <div className="mb-3">
              {projects.length === 0 ? (
                <p className="px-2.5 py-1 text-[11px] text-muted/70">还没有项目</p>
              ) : (
                projects.map((p) => (
                  <ProjectGroup
                    key={p.id}
                    project={p}
                    conversations={byProject.get(p.id) ?? []}
                  />
                ))
              )}
            </div>

            {(adhocConversations.length > 0 ||
              (convLoading && conversations.length === 0)) && (
              <>
                <GroupLabel label="Ad-hoc" count={adhocConversations.length} />
                <ul>
                  {convLoading && conversations.length === 0 ? (
                    <li className="px-2.5 py-2 text-xs text-muted">加载中…</li>
                  ) : (
                    adhocConversations.map((c) => (
                      <ConversationItem key={c.id} item={c} />
                    ))
                  )}
                </ul>
              </>
            )}
          </div>
        </nav>
      )}

      <footer className="shrink-0 border-t border-rule/60 p-2">
        <NavLink
          to="/settings/connectors"
          title="连接器"
          className={({ isActive }) =>
            [
              "flex h-10 items-center rounded-xl text-[14px] transition-colors hover:bg-rule/70 hover:text-ink",
              collapsed ? "justify-center" : "gap-3 px-3",
              isActive ? "text-ink" : "text-muted",
            ].join(" ")
          }
        >
          <span className="flex size-4 shrink-0 items-center justify-center">
            <PlugIcon />
          </span>
          {!collapsed && <span className="truncate">连接器</span>}
        </NavLink>
      </footer>
    </aside>
  );
}

function PlugIcon() {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="shrink-0"
    >
      <path d="M9 2v6" />
      <path d="M15 2v6" />
      <path d="M6 8h12v3a6 6 0 0 1-12 0V8z" />
      <path d="M12 17v5" />
    </svg>
  );
}

function GroupLabel({ label, count }: { label: string; count: number }) {
  return (
    <div className="px-2.5 pt-1.5 pb-1 flex h-7 items-center gap-2">
      <span className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted/70">
        {label}
      </span>
      {count > 0 && (
        <span className="font-mono text-[10px] text-muted/60">{count}</span>
      )}
    </div>
  );
}

function PlusIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="shrink-0 text-muted"
    >
      <path d="M8 3v10M3 8h10" />
    </svg>
  );
}

function CollapseIcon({ collapsed = false }: { collapsed?: boolean }) {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="shrink-0"
    >
      <rect x="2.5" y="3" width="11" height="10" rx="1.8" />
      <path d="M6.5 3v10" />
      {collapsed ? <path d="M9 6l2 2-2 2" /> : <path d="M11 6L9 8l2 2" />}
    </svg>
  );
}
