import { useEffect, useMemo, useRef, useState } from "react";
import { fetchWorkspaceTree, type WorkspaceTreeEntry } from "@/lib/api";
import { useWorkspaceStore } from "./store";
import { cn } from "@/lib/utils";

type TreeNode = {
  entry: WorkspaceTreeEntry;
  children: TreeNode[];
};

export function buildWorkspaceTree(entries: WorkspaceTreeEntry[]): TreeNode[] {
  const byPath = new Map<string, TreeNode>();
  const roots: TreeNode[] = [];
  const sorted = [...entries].sort((a, b) => a.path.localeCompare(b.path));
  for (const e of sorted) {
    const node: TreeNode = { entry: e, children: [] };
    byPath.set(e.path, node);
    const slash = e.path.lastIndexOf("/");
    if (slash === -1) {
      roots.push(node);
    } else {
      const parentPath = e.path.slice(0, slash);
      const parent = byPath.get(parentPath);
      if (parent) parent.children.push(node);
      else roots.push(node);
    }
  }
  const sortNodes = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => {
      if (a.entry.is_dir !== b.entry.is_dir) return a.entry.is_dir ? -1 : 1;
      return a.entry.name.localeCompare(b.entry.name);
    });
    for (const n of nodes) sortNodes(n.children);
  };
  sortNodes(roots);
  return roots;
}

export function WorkspaceTree({
  conversationId,
  projectId,
}: {
  conversationId: string;
  projectId?: string;
}) {
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const [entries, setEntries] = useState<WorkspaceTreeEntry[] | null>(null);
  const [rootName, setRootName] = useState<string>("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const targetRef = useRef<string>("");

  useEffect(() => {
    const ac = new AbortController();
    const target = `${conversationId}:${projectId ?? ""}`;
    const targetChanged = targetRef.current !== target;
    targetRef.current = target;
    if (targetChanged) {
      setEntries(null);
      setRootName("");
      setError(null);
    }
    setLoading(true);
    fetchWorkspaceTree(conversationId, { projectId }, ac.signal)
      .then((tree) => {
        setEntries(tree.entries);
        setRootName(tree.workspace.root_name);
        setError(null);
      })
      .catch((e) => {
        if (e.name === "AbortError") return;
        setError(String(e.message ?? e));
      })
      .finally(() => setLoading(false));
    return () => ac.abort();
  }, [conversationId, filesVersion, projectId]);

  const tree = useMemo(
    () => (entries ? buildWorkspaceTree(entries) : []),
    [entries],
  );

  if (loading && !entries) {
    return (
      <div className="p-4 font-mono text-[11px] text-muted">Loading…</div>
    );
  }
  if (error) {
    if (error.includes("404")) {
      return (
        <div className="p-4 text-[13px] text-muted">
          这个会话还没有 workspace。等 agent 首次落盘就会出现。
        </div>
      );
    }
    return (
      <div className="p-4 text-[13px] text-red-600">加载失败：{error}</div>
    );
  }
  return (
    <div className="flex flex-col min-h-0 flex-1">
      <div className="px-3 py-2 border-b border-rule shrink-0">
        <span
          className="font-mono text-[10px] tracking-[0.14em] uppercase text-muted/80 truncate block"
          title={rootName}
        >
          {rootName}
        </span>
      </div>
      {entries && entries.length > 0 && (
        <WorkspaceTreeList
          nodes={tree}
          treeKey={projectId ?? conversationId}
          className="flex-1 overflow-auto py-1 min-h-0"
        />
      )}
    </div>
  );
}

export function WorkspaceTreeList({
  nodes,
  treeKey,
  className,
  compact = false,
}: {
  nodes: TreeNode[];
  treeKey: string;
  className?: string;
  compact?: boolean;
}) {
  return (
    <ul className={cn("scrollbar-subtle", className)}>
      {nodes.map((node) => (
        <TreeItem
          key={node.entry.path}
          node={node}
          treeKey={treeKey}
          depth={0}
          compact={compact}
        />
      ))}
    </ul>
  );
}

function TreeItem({
  node,
  treeKey,
  depth,
  compact,
}: {
  node: TreeNode;
  treeKey: string;
  depth: number;
  compact: boolean;
}) {
  const directoryKey = `${treeKey}:${node.entry.path}`;
  const open = useWorkspaceStore(
    (s) => s.expandedDirectories[directoryKey] === true,
  );
  const toggleDirectory = useWorkspaceStore((s) => s.toggleDirectory);
  const previewPath = useWorkspaceStore((s) => s.previewPath);
  const openFile = useWorkspaceStore((s) => s.openFile);
  const isActive = previewPath === node.entry.path;
  const isDir = node.entry.is_dir;

  return (
    <li>
      <button
        type="button"
        onClick={() =>
          isDir ? toggleDirectory(directoryKey) : openFile(node.entry.path)
        }
        className={cn(
          "flex items-center gap-1.5 w-full text-left px-2 rounded",
          "hover:bg-subtle/70 transition-colors cursor-pointer",
          compact ? "py-0.5 text-[12px]" : "py-1 text-[13px]",
          isActive && "bg-subtle text-ink font-medium",
        )}
      >
        {isDir ? (
          <ChevronIcon open={open} />
        ) : (
          <span className="inline-block w-[10px]" />
        )}
        {isDir ? <FolderIcon /> : <FileIcon />}
        <span className="truncate">{node.entry.name}</span>
      </button>
      {isDir && open && node.children.length > 0 && (
        <ul className="ml-3 pl-1.5 border-l border-rule/70">
          {node.children.map((child) => (
            <TreeItem
              key={child.entry.path}
              node={child}
              treeKey={treeKey}
              depth={depth + 1}
              compact={compact}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

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
      className={cn("transition-transform shrink-0 text-muted", open && "rotate-90")}
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
      className="shrink-0 text-muted"
    >
      <path d="M2 4.5A1.5 1.5 0 0 1 3.5 3H6l1.5 1.5H12.5A1.5 1.5 0 0 1 14 6v6.5A1.5 1.5 0 0 1 12.5 14h-9A1.5 1.5 0 0 1 2 12.5V4.5Z" />
    </svg>
  );
}

function FileIcon() {
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
      className="shrink-0 text-muted"
    >
      <path d="M4 2h5.5L13 5.5V14a0 0 0 0 1 0 0H4a0 0 0 0 1 0 0V2Z" />
      <path d="M9.5 2v3.5H13" />
    </svg>
  );
}
