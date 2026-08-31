import { useEffect } from "react";
import type { ValidationDiagnostic } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useWorkspaceStore } from "./store";
import { useProblemsStore } from "./problems-store";

export function WorkspaceProblems({
  conversationId,
}: {
  conversationId: string;
}) {
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const openFile = useWorkspaceStore((s) => s.openFile);
  const data = useProblemsStore((s) => s.data[conversationId]);
  const scope = useProblemsStore(
    (s) => s.scope[conversationId] ?? "current",
  );
  const loading = useProblemsStore((s) => s.loading[conversationId] === true);
  const error = useProblemsStore((s) => s.error[conversationId]);
  const load = useProblemsStore((s) => s.load);
  const setScope = useProblemsStore((s) => s.setScope);

  useEffect(() => {
    const controller = new AbortController();
    void load(conversationId, controller.signal);
    return () => controller.abort();
  }, [conversationId, filesVersion, load, scope]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-10 shrink-0 items-center border-b border-rule px-3">
        <ScopeButton
          active={scope === "current"}
          onClick={() => setScope(conversationId, "current")}
        >
          本轮
        </ScopeButton>
        <ScopeButton
          active={scope === "conversation"}
          onClick={() => setScope(conversationId, "conversation")}
        >
          全部
        </ScopeButton>
        {data && (
          <span className="ml-auto font-mono text-[10px] text-muted">
            {data.error_count} errors · {data.warning_count} warnings
          </span>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-2">
        {loading && !data ? (
          <Empty>Loading problems…</Empty>
        ) : error ? (
          <Empty tone="error">{error}</Empty>
        ) : !data || data.runs.length === 0 ? (
          <Empty>尚未运行结构化验证</Empty>
        ) : data.error_count === 0 && data.warning_count === 0 ? (
          <Empty tone="success">最新验证全部通过</Empty>
        ) : (
          <div className="space-y-2">
            {data.runs.map((run) => (
              <section
                key={run.tool_call_id}
                className="overflow-hidden rounded-lg border border-rule/70"
              >
                <div className="bg-subtle/35 px-3 py-2">
                  <div className="flex items-center gap-2">
                    <span
                      className={cn(
                        "size-1.5 rounded-full",
                        run.validation.passed
                          ? "bg-emerald-500"
                          : "bg-red-500",
                      )}
                    />
                    <span className="font-mono text-[10px] uppercase text-ink">
                      {run.validation.kind}
                    </span>
                    <span className="ml-auto font-mono text-[9px] text-muted">
                      {formatDuration(run.duration_ms)}
                    </span>
                  </div>
                  <p
                    className="mt-1 truncate font-mono text-[10px] text-muted"
                    title={run.command}
                  >
                    {run.command}
                  </p>
                </div>
                <div className="divide-y divide-rule/60">
                  {(run.validation.diagnostics ?? []).map((diagnostic) => (
                    <ProblemRow
                      key={diagnostic.id}
                      diagnostic={diagnostic}
                      onOpen={(item) =>
                        item.path && openFile(item.path, item.line)
                      }
                    />
                  ))}
                </div>
                {!run.validation.parse_ok && (
                  <p className="px-3 py-2 text-[10px] text-amber-700">
                    未能结构化解析完整输出，请展开命令卡查看原始日志。
                  </p>
                )}
              </section>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function ProblemRow({
  diagnostic,
  onOpen,
}: {
  diagnostic: ValidationDiagnostic;
  onOpen: (diagnostic: ValidationDiagnostic) => void;
}) {
  return (
    <button
      type="button"
      disabled={!diagnostic.path}
      onClick={() => onOpen(diagnostic)}
      className={cn(
        "block w-full px-3 py-2 text-left",
        diagnostic.path ? "hover:bg-subtle/50" : "cursor-default",
      )}
    >
      <div className="flex items-start gap-2">
        <span
          className={cn(
            "mt-1 size-1.5 shrink-0 rounded-full",
            diagnostic.severity === "warning"
              ? "bg-amber-500"
              : diagnostic.severity === "info"
                ? "bg-accent"
                : "bg-red-500",
          )}
        />
        <span className="min-w-0 flex-1 text-[11px] leading-4 text-ink">
          {diagnostic.message}
        </span>
      </div>
      {(diagnostic.path || diagnostic.code) && (
        <p className="mt-1 truncate pl-3.5 font-mono text-[9px] text-muted">
          {diagnostic.path}
          {diagnostic.line ? `:${diagnostic.line}` : ""}
          {diagnostic.column ? `:${diagnostic.column}` : ""}
          {diagnostic.code ? ` · ${diagnostic.code}` : ""}
        </p>
      )}
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
  children: string;
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

function Empty({
  children,
  tone,
}: {
  children: string;
  tone?: "error" | "success";
}) {
  return (
    <div
      className={cn(
        "flex h-32 items-center justify-center text-xs text-muted",
        tone === "error" && "text-red-600",
        tone === "success" && "text-emerald-600",
      )}
    >
      {children}
    </div>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
