import { useEffect, useMemo, useState, type Ref } from "react";
import {
  fetchWorkspaceTree,
  type WorkspaceTreeEntry,
} from "@/lib/api";
import {
  Command,
  CommandEmpty,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { filterContextEntries } from "./context-mention";

export function ContextPicker({
  conversationId,
  projectId,
  query,
  onSelect,
  commandRef,
}: {
  conversationId: string;
  projectId: string;
  query: string;
  onSelect: (entry: WorkspaceTreeEntry) => void;
  commandRef?: Ref<HTMLDivElement>;
}) {
  const [entries, setEntries] = useState<WorkspaceTreeEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [truncated, setTruncated] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setLoading(true);
    setError("");
    fetchWorkspaceTree(
      conversationId,
      { projectId },
      controller.signal,
    )
      .then((tree) => {
        if (!active) return;
        setEntries(tree.entries);
        setTruncated(!!tree.truncated);
      })
      .catch((err: unknown) => {
        if (!active || (err instanceof DOMException && err.name === "AbortError")) {
          return;
        }
        setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [conversationId, projectId]);

  const filtered = useMemo(
    () => filterContextEntries(entries, query),
    [entries, query],
  );

  return (
    <div className="absolute bottom-full left-0 z-30 mb-2 w-full max-w-lg overflow-hidden rounded-xl border border-rule bg-paper shadow-[0_12px_36px_oklch(0_0_0/0.14)]">
      <Command
        id="context-picker"
        loop
        ref={commandRef}
        shouldFilter={false}
        aria-label="选择工作区上下文"
      >
        <div className="flex h-9 items-center justify-between gap-3 border-b border-rule px-3">
          <span className="truncate font-mono text-[10px] uppercase tracking-[0.14em] text-muted">
            添加上下文 · @{query}
          </span>
          {truncated && (
            <span className="shrink-0 text-[10px] text-amber-600 dark:text-amber-400">
              结果已截断
            </span>
          )}
        </div>
        <CommandList>
          {loading && (
            <div className="px-3 py-8 text-center text-sm text-muted">
              正在加载工作区…
            </div>
          )}
          {!loading && error && (
            <div className="px-3 py-6 text-center text-sm text-red-600 dark:text-red-400">
              加载失败：{error}
            </div>
          )}
          {!loading && !error && (
            <>
              <CommandEmpty>没有匹配的文件或文件夹</CommandEmpty>
              {filtered.map((entry) => (
                <CommandItem
                  key={`${entry.is_dir ? "folder" : "file"}:${entry.path}`}
                  value={entry.path}
                  onSelect={() => onSelect(entry)}
                >
                  {entry.is_dir ? <FolderIcon /> : <FileIcon />}
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-medium text-ink">
                      {entry.name}
                    </div>
                    <div className="truncate text-[11px] text-muted">
                      {entry.path}
                    </div>
                  </div>
                </CommandItem>
              ))}
            </>
          )}
        </CommandList>
      </Command>
    </div>
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
      className="size-4 shrink-0 text-accent"
      aria-hidden
    >
      <path d="M3 6.5A2.5 2.5 0 0 1 5.5 4H10l2 2h8v10.5A2.5 2.5 0 0 1 17.5 19h-12A2.5 2.5 0 0 1 3 16.5Z" />
    </svg>
  );
}

function FileIcon() {
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
      <path d="M6 3h9l3 3v15H6z" />
      <path d="M14 3v4h4" />
    </svg>
  );
}
