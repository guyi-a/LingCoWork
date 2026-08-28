import { useEffect, useMemo, useRef, useState } from "react";
import { fetchWorkspaceTree, type WorkspaceTreeEntry } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useWorkspaceStore } from "./store";
import { buildWorkspaceTree, WorkspaceTreeList } from "./WorkspaceTree";

export function FileSwitcherOverlay({
  conversationId,
  projectId,
}: {
  conversationId: string;
  projectId?: string;
}) {
  const closeSwitcher = useWorkspaceStore((s) => s.closeSwitcher);
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const [entries, setEntries] = useState<WorkspaceTreeEntry[] | null>(null);
  const [rootName, setRootName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const panelRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    fetchWorkspaceTree(conversationId, { projectId }, ac.signal)
      .then((tree) => {
        setEntries(tree.entries);
        setRootName(tree.workspace.root_name);
      })
      .catch((e) => {
        if (e.name === "AbortError") return;
        setError(String(e.message ?? e));
      })
      .finally(() => setLoading(false));
    return () => ac.abort();
  }, [conversationId, filesVersion, projectId]);

  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      if (!panelRef.current?.contains(event.target as Node)) closeSwitcher();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeSwitcher();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [closeSwitcher]);

  const tree = useMemo(
    () => (entries ? buildWorkspaceTree(entries) : []),
    [entries],
  );

  return (
    <div
      ref={panelRef}
      className={cn(
        "absolute right-3 top-11 z-30 w-[280px] overflow-hidden",
        "rounded-xl border border-rule/80 bg-paper shadow-[0_18px_50px_rgba(15,23,42,0.16)]",
      )}
    >
      <div className="border-b border-rule/70 px-3 py-2">
        <span
          className="block truncate font-mono text-[10px] uppercase tracking-[0.14em] text-muted/80"
          title={rootName}
        >
          {rootName || "Files"}
        </span>
      </div>
      <div className="max-h-[58vh] overflow-auto p-1.5 scrollbar-subtle">
        {loading && !entries && (
          <div className="p-2 font-mono text-[11px] text-muted">Loading…</div>
        )}
        {error && (
          <div className="p-2 text-[12px] text-red-600">加载失败：{error}</div>
        )}
        {!loading && !error && entries?.length === 0 && (
          <div className="p-2 text-[12px] text-muted">空工作区</div>
        )}
        {tree.length > 0 && (
          <WorkspaceTreeList
            nodes={tree}
            treeKey={projectId ?? conversationId}
            compact
          />
        )}
      </div>
    </div>
  );
}
