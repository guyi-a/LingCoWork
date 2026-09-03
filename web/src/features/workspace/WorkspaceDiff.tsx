import { useEffect, useMemo, useState } from "react";
import {
  fetchWorkspaceChanges,
  fetchWorkspaceDiff,
  type WorkspaceChangedFile,
  type WorkspaceChanges,
  type WorkspaceDiff,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import { useWorkspaceStore } from "./store";
import { UnifiedDiffView } from "./UnifiedDiffView";

export function WorkspaceDiffPanel({
  conversationId,
  projectId,
}: {
  conversationId: string;
  projectId?: string;
}) {
  const scope = useWorkspaceStore((s) => s.diffScope);
  const setScope = useWorkspaceStore((s) => s.setDiffScope);
  const selectedPath = useWorkspaceStore((s) => s.selectedDiffPath);
  const selectPath = useWorkspaceStore((s) => s.selectDiffPath);
  const openFile = useWorkspaceStore((s) => s.openFile);
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const [changes, setChanges] = useState<WorkspaceChanges | null>(null);
  const [diff, setDiff] = useState<WorkspaceDiff | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    const timer = window.setTimeout(() => {
      setLoading(true);
      fetchWorkspaceChanges(
        conversationId,
        scope,
        { projectId },
        ac.signal,
      )
        .then((result) => {
          setChanges(result);
          setError(null);
          const nextPath =
            selectedPath &&
            result.files.some(
              (file) => file.path === selectedPath || file.old_path === selectedPath,
            )
              ? selectedPath
              : (result.files[0]?.path ?? null);
          if (nextPath !== selectedPath) selectPath(nextPath);
        })
        .catch((err) => {
          if (err.name !== "AbortError") setError(String(err.message ?? err));
        })
        .finally(() => setLoading(false));
    }, 120);
    return () => {
      window.clearTimeout(timer);
      ac.abort();
    };
  }, [
    conversationId,
    filesVersion,
    projectId,
    scope,
    selectPath,
    selectedPath,
  ]);

  useEffect(() => {
    if (!selectedPath) {
      setDiff(null);
      return;
    }
    const ac = new AbortController();
    fetchWorkspaceDiff(
      conversationId,
      selectedPath,
      scope,
      { projectId },
      ac.signal,
    )
      .then((result) => setDiff(result))
      .catch((err) => {
        if (err.name === "AbortError") return;
        setDiff(null);
        setError(String(err.message ?? err));
      });
    return () => ac.abort();
  }, [conversationId, filesVersion, projectId, scope, selectedPath]);

  const totals = useMemo(
    () =>
      (changes?.files ?? []).reduce(
        (sum, file) => ({
          additions: sum.additions + file.additions,
          deletions: sum.deletions + file.deletions,
        }),
        { additions: 0, deletions: 0 },
      ),
    [changes],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-rule px-2 py-1.5">
        <ScopeButton
          active={scope === "agent"}
          onClick={() => setScope("agent")}
        >
          本轮 Agent
        </ScopeButton>
        <ScopeButton active={scope === "all"} onClick={() => setScope("all")}>
          全部 Git
        </ScopeButton>
        <span className="ml-auto flex items-center gap-2 font-mono text-[10px]">
          <span className="text-emerald-600 dark:text-emerald-400">+{totals.additions}</span>
          <span className="text-red-600 dark:text-red-400">−{totals.deletions}</span>
        </span>
      </div>

      {error && (
        <div className="shrink-0 border-b border-rule px-3 py-2 text-[11px] text-red-700 dark:text-red-400">
          {error}
        </div>
      )}
      {scope === "all" && changes && !changes.git_repository ? (
        <EmptyState text="当前 Workspace 不是 Git 仓库根目录，无法展示全部 Git Diff。" />
      ) : changes && changes.files.length === 0 && !loading ? (
        <EmptyState
          text={
            scope === "agent"
              ? "本轮 Agent 尚未产生可比较的文件变更。"
              : "Git 工作区没有未提交变更。"
          }
        />
      ) : (
        <>
          <div className="max-h-48 shrink-0 overflow-auto border-b border-rule py-1 scrollbar-subtle">
            {changes?.files.map((file) => (
              <ChangeRow
                key={`${file.old_path ?? ""}:${file.path}`}
                file={file}
                active={
                  selectedPath === file.path || selectedPath === file.old_path
                }
                onSelect={() => selectPath(file.path)}
              />
            ))}
            {loading && !changes && (
              <div className="px-3 py-2 font-mono text-[10px] text-muted">
                Loading changes…
              </div>
            )}
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            {diff ? (
              <>
                <div className="flex shrink-0 items-center gap-2 border-b border-rule px-3 py-1.5">
                  <button
                    type="button"
                    onClick={() => openFile(diff.path)}
                    className="min-w-0 flex-1 truncate text-left font-mono text-[11px] text-accent hover:underline"
                    title={diff.path}
                  >
                    {diff.path}
                  </button>
                  <StatusBadge status={diff.status} />
                </div>
                {diff.sensitive ? (
                  <EmptyState text="敏感文件只展示变更状态，不返回正文 Diff。" />
                ) : diff.binary ? (
                  <EmptyState text="二进制文件不提供行级 Diff。" />
                ) : diff.too_large ? (
                  <EmptyState text="文件过大，已跳过行级 Diff。" />
                ) : diff.patch ? (
                  <UnifiedDiffView patch={diff.patch} truncated={diff.truncated} />
                ) : (
                  <EmptyState text="该文件没有可展示的文本差异。" />
                )}
              </>
            ) : (
              <EmptyState text="选择一个文件查看 Diff。" />
            )}
          </div>
        </>
      )}
      {changes?.truncated && (
        <div className="shrink-0 border-t border-rule px-3 py-1.5 text-[10px] text-amber-700 dark:text-amber-400">
          变更文件过多，列表已截断
        </div>
      )}
    </div>
  );
}

function ChangeRow({
  file,
  active,
  onSelect,
}: {
  file: WorkspaceChangedFile;
  active: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-1.5 text-left transition-colors",
        active ? "bg-subtle" : "hover:bg-subtle/60",
      )}
    >
      <StatusBadge status={file.status} compact />
      <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink">
        {file.path}
      </span>
      <span className="flex shrink-0 gap-1.5 font-mono text-[10px]">
        {file.additions > 0 && (
          <span className="text-emerald-600 dark:text-emerald-400">+{file.additions}</span>
        )}
        {file.deletions > 0 && (
          <span className="text-red-600 dark:text-red-400">−{file.deletions}</span>
        )}
      </span>
    </button>
  );
}

function ScopeButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded px-2 py-1 text-[10px] transition-colors",
        active ? "bg-subtle text-ink" : "text-muted hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

function StatusBadge({
  status,
  compact = false,
}: {
  status: WorkspaceChangedFile["status"];
  compact?: boolean;
}) {
  const label =
    status === "added"
      ? "A"
      : status === "deleted"
        ? "D"
        : status === "renamed"
          ? "R"
          : "M";
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded font-mono font-semibold",
        compact ? "size-4 text-[9px]" : "h-5 min-w-5 px-1 text-[9px]",
        status === "added" && "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
        status === "deleted" && "bg-red-500/10 text-red-700 dark:text-red-300",
        status === "renamed" && "bg-blue-500/10 text-blue-700 dark:text-blue-300",
        status === "modified" && "bg-amber-500/10 text-amber-700 dark:text-amber-300",
      )}
    >
      {label}
    </span>
  );
}

function EmptyState({ text }: { text: string }) {
  return (
    <div className="flex min-h-32 flex-1 items-center justify-center px-6 text-center text-[12px] text-muted">
      {text}
    </div>
  );
}
