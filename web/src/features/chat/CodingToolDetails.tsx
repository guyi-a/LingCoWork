import type { ToolCall } from "@/hooks/useChatStream";
import type { ValidationDiagnostic, ValidationSummary } from "@/lib/api";
import { useWorkspaceStore } from "@/features/workspace/store";
import { cn } from "@/lib/utils";

const CODING_TOOL_NAMES = new Set([
  "glob",
  "grep",
  "apply_patch",
  "run_command",
]);

type JsonObject = Record<string, unknown>;

type GlobResult = {
  pattern?: string;
  path?: string;
  matches?: string[];
  match_count?: number;
  files_scanned?: number;
  truncated?: boolean;
  reason?: string;
  duration_ms?: number;
};

type GrepMatch = {
  path?: string;
  line?: number;
  column?: number;
  text?: string;
  before?: string[];
  after?: string[];
};

type GrepResult = {
  pattern?: string;
  path?: string;
  glob?: string;
  matches?: GrepMatch[];
  match_count?: number;
  files_scanned?: number;
  files_skipped?: number;
  truncated?: boolean;
  reason?: string;
  duration_ms?: number;
};

type PatchResult = {
  path?: string;
  hunks?: number;
  hunk_details?: PatchHunkDetail[];
  additions?: number;
  deletions?: number;
  bytes_before?: number;
  bytes_after?: number;
};

type PatchHunkDetail = {
  index?: number;
  line?: number;
  additions?: number;
  deletions?: number;
};

type CommandResult = {
  exit_code?: number;
  duration_ms?: number;
  stdout?: string;
  stderr?: string;
  validation?: ValidationSummary;
};

type AnnotatedPatchLine = {
  text: string;
  kind: "add" | "delete" | "context" | "hunk" | "meta";
  line?: number;
};

export function isCodingTool(name: string): boolean {
  return CODING_TOOL_NAMES.has(name);
}

export function codingToolLabel(tool: ToolCall): string {
  const args = parseObject(tool.argsJson);
  const result = parseObject(tool.content);
  switch (tool.name) {
    case "glob": {
      const pattern = stringValue(args?.pattern);
      const count = numberValue(result?.match_count);
      return count === undefined ? pattern : `${pattern} · ${count} files`;
    }
    case "grep": {
      const pattern = stringValue(args?.pattern);
      const count = numberValue(result?.match_count);
      return count === undefined ? pattern : `${pattern} · ${count} hits`;
    }
    case "apply_patch": {
      const path = stringValue(result?.path) || stringValue(args?.path);
      const additions = numberValue(result?.additions);
      const deletions = numberValue(result?.deletions);
      return additions === undefined || deletions === undefined
        ? basename(path)
        : `${basename(path)} · +${additions} −${deletions}`;
    }
    case "run_command": {
      const command = stringValue(args?.command);
      const validation = validationSummary(result?.validation);
      if (!validation) return command;
      const label = validation.passed ? "passed" : "failed";
      return `${validation.kind} ${label} · ${validation.error_count} errors`;
    }
    default:
      return "";
  }
}

export function CodingToolLabel({ tool }: { tool: ToolCall }) {
  if (tool.name === "apply_patch") {
    const args = parseObject(tool.argsJson);
    const result = parseObject(tool.content);
    const path = stringValue(result?.path) || stringValue(args?.path);
    const additions = numberValue(result?.additions);
    const deletions = numberValue(result?.deletions);
    if (additions !== undefined && deletions !== undefined) {
      return (
        <>
          <span className="text-ink/70">{basename(path)}</span>
          <span>·</span>
          <span className="text-emerald-600 dark:text-emerald-400">+{additions}</span>
          <span className="text-red-600 dark:text-red-400">−{deletions}</span>
        </>
      );
    }
  }
  return <span className="text-ink/70">{codingToolLabel(tool)}</span>;
}

export function CodingToolDetails({ tool }: { tool: ToolCall }) {
  const args = parseObject(tool.argsJson);
  const result = parseObject(tool.content);

  if (tool.name === "glob") {
    return (
      <GlobDetails
        args={args}
        result={result as GlobResult | null}
      />
    );
  }
  if (tool.name === "grep") {
    return (
      <GrepDetails
        args={args}
        result={result as GrepResult | null}
      />
    );
  }
  if (tool.name === "apply_patch") {
    return (
      <PatchDetails
        args={args}
        result={result as PatchResult | null}
      />
    );
  }
  if (tool.name === "run_command") {
    return (
      <CommandDetails
        args={args}
        result={result as CommandResult | null}
      />
    );
  }
  return null;
}

function CommandDetails({
  args,
  result,
}: {
  args: JsonObject | null;
  result: CommandResult | null;
}) {
  const openFile = useWorkspaceStore((s) => s.openFile);
  const setActiveTab = useWorkspaceStore((s) => s.setActiveTab);
  const setPanelOpen = useWorkspaceStore((s) => s.setPanelOpen);
  const validation = validationSummary(result?.validation);
  const diagnostics = validation?.diagnostics ?? [];
  const output = stringValue(result?.stderr) || stringValue(result?.stdout);

  return (
    <div className="space-y-2">
      <QueryLine label="Command" value={stringValue(args?.command)} />
      {validation && (
        <>
          <div className="flex items-center gap-2 text-[11px]">
            <span
              className={cn(
                "font-medium",
                validation.passed
                  ? "text-emerald-600 dark:text-emerald-400"
                  : "text-red-600 dark:text-red-400",
              )}
            >
              {validation.kind} {validation.passed ? "passed" : "failed"}
            </span>
            <span className="text-muted">
              {validation.error_count} errors · {validation.warning_count} warnings
            </span>
            <button
              type="button"
              onClick={() => {
                setPanelOpen(true);
                setActiveTab("problems");
              }}
              className="ml-auto text-accent hover:underline"
            >
              View Problems
            </button>
          </div>
          {diagnostics.length > 0 && (
            <div className="divide-y divide-rule/60 rounded-md border border-rule/70">
              {diagnostics.slice(0, 5).map((diagnostic) => (
                <button
                  key={diagnostic.id}
                  type="button"
                  disabled={!diagnostic.path}
                  onClick={() =>
                    diagnostic.path &&
                    openFile(diagnostic.path, diagnostic.line)
                  }
                  className={cn(
                    "block w-full px-2 py-1.5 text-left text-[10px]",
                    diagnostic.path && "hover:bg-subtle",
                  )}
                >
                  <span className="block truncate text-ink">
                    {diagnostic.message}
                  </span>
                  {diagnostic.path && (
                    <span className="block truncate font-mono text-muted">
                      {diagnostic.path}
                      {diagnostic.line ? `:${diagnostic.line}` : ""}
                      {diagnostic.column ? `:${diagnostic.column}` : ""}
                    </span>
                  )}
                </button>
              ))}
            </div>
          )}
          {!validation.parse_ok && (
            <p className="text-[10px] text-amber-700 dark:text-amber-400">
              未能结构化解析完整输出，以下为原始日志。
            </p>
          )}
        </>
      )}
      {(!validation || !validation.parse_ok) && output && (
        <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md border border-rule/70 bg-subtle/35 p-2 text-[10px] leading-4 text-ink/80">
          {output.slice(0, 4000)}
        </pre>
      )}
      <ResultMeta durationMs={numberValue(result?.duration_ms)} />
    </div>
  );
}

function GlobDetails({
  args,
  result,
}: {
  args: JsonObject | null;
  result: GlobResult | null;
}) {
  const openFile = useWorkspaceStore((s) => s.openFile);
  const pattern = stringValue(args?.pattern) || stringValue(result?.pattern);
  const base = stringValue(args?.path) || stringValue(result?.path) || ".";
  const matches = stringArray(result?.matches);

  return (
    <div className="space-y-2">
      <QueryLine label="Pattern" value={pattern} secondary={`in ${base}`} />
      {matches.length > 0 && (
        <div className="max-h-56 space-y-0.5 overflow-auto rounded-md border border-rule/70 bg-subtle/35 p-1">
          {matches.map((path) => (
            <PathButton key={path} path={path} onClick={() => openFile(path)} />
          ))}
        </div>
      )}
      {result && matches.length === 0 && (
        <p className="text-[11px] text-muted">No matching files</p>
      )}
      <ResultMeta
        scanned={numberValue(result?.files_scanned)}
        durationMs={numberValue(result?.duration_ms)}
        truncated={result?.truncated === true}
        reason={stringValue(result?.reason)}
      />
    </div>
  );
}

function GrepDetails({
  args,
  result,
}: {
  args: JsonObject | null;
  result: GrepResult | null;
}) {
  const openFile = useWorkspaceStore((s) => s.openFile);
  const pattern = stringValue(args?.pattern) || stringValue(result?.pattern);
  const base = stringValue(args?.path) || stringValue(result?.path) || ".";
  const glob = stringValue(args?.glob) || stringValue(result?.glob);
  const matches = grepMatches(result?.matches);

  return (
    <div className="space-y-2">
      <QueryLine
        label="Pattern"
        value={pattern}
        secondary={`in ${base}${glob ? ` · ${glob}` : ""}`}
      />
      {matches.length > 0 && (
        <div className="max-h-64 space-y-1 overflow-auto rounded-md border border-rule/70 bg-subtle/35 p-1">
          {matches.map((match, index) => {
            const path = stringValue(match.path);
            const line = numberValue(match.line);
            const column = numberValue(match.column);
            return (
              <button
                key={`${path}:${line ?? 0}:${index}`}
                type="button"
                disabled={!path}
                onClick={() => path && openFile(path, line)}
                className={cn(
                  "block w-full rounded px-2 py-1.5 text-left transition-colors",
                  path ? "hover:bg-paper" : "cursor-default",
                )}
              >
                <span className="block truncate text-[10px] text-accent">
                  {path || "(unknown)"}
                  {line !== undefined ? `:${line}` : ""}
                  {column !== undefined ? `:${column}` : ""}
                </span>
                {match.text && (
                  <span className="block truncate text-[11px] text-ink/80">
                    {match.text}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
      {result && matches.length === 0 && (
        <p className="text-[11px] text-muted">No matches</p>
      )}
      <ResultMeta
        scanned={numberValue(result?.files_scanned)}
        skipped={numberValue(result?.files_skipped)}
        durationMs={numberValue(result?.duration_ms)}
        truncated={result?.truncated === true}
        reason={stringValue(result?.reason)}
      />
    </div>
  );
}

function PatchDetails({
  args,
  result,
}: {
  args: JsonObject | null;
  result: PatchResult | null;
}) {
  const selectDiffPath = useWorkspaceStore((s) => s.selectDiffPath);
  const path = stringValue(result?.path) || stringValue(args?.path);
  const patch = stringValue(args?.patch);
  const patchLines = annotatePatchLines(patch, patchHunkDetails(result?.hunk_details));
  const visiblePatchLines = patchLines.slice(0, 120);
  const patchTruncated = patchLines.length > visiblePatchLines.length;

  return (
    <div className="space-y-2">
      {patch && (
        <div className="overflow-hidden rounded-md border border-rule/70 bg-subtle/35">
          {path && (
            <button
              type="button"
              onClick={() => selectDiffPath(path)}
              title={path}
              className="block w-full truncate border-b border-rule/60 px-2 py-1 text-left text-[10px] text-accent transition-colors hover:bg-paper"
            >
              {path}
            </button>
          )}
          <pre className="max-h-64 overflow-auto py-1 text-[11px] leading-[1.55]">
            <code>
              {visiblePatchLines.map((line, index) => (
                <span
                  key={`${index}:${line.text}`}
                  className={cn(
                    "flex min-w-max",
                    line.kind === "add" && "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
                    line.kind === "delete" && "bg-red-500/10 text-red-700 dark:text-red-300",
                    line.kind === "hunk" && "text-accent",
                    (line.kind === "context" || line.kind === "meta") &&
                      "text-ink/70",
                  )}
                >
                  <span className="w-10 shrink-0 select-none pr-2 text-right text-muted/60">
                    {line.line ?? ""}
                  </span>
                  <span className="whitespace-pre pr-2">{line.text || " "}</span>
                </span>
              ))}
            </code>
          </pre>
          {patchTruncated && (
            <div className="border-t border-rule/60 px-2 py-1 text-[10px] text-amber-700 dark:text-amber-400">
              Patch preview truncated at 120 lines
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function patchHunkDetails(value: unknown): PatchHunkDetail[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is PatchHunkDetail =>
          item !== null && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

function annotatePatchLines(
  patch: string,
  details: PatchHunkDetail[],
): AnnotatedPatchLine[] {
  let hunkIndex = 0;
  let oldLine: number | undefined;
  let newLine: number | undefined;

  return patch.split("\n").map((text) => {
    if (text.startsWith("@@")) {
      hunkIndex++;
      const detail =
        details.find((item) => numberValue(item.index) === hunkIndex) ??
        details[hunkIndex - 1];
      const startLine = numberValue(detail?.line);
      oldLine = startLine;
      newLine = startLine;
      return {
        text: `修改块 ${hunkIndex}${startLine ? ` · 第 ${startLine} 行` : ""}`,
        kind: "hunk",
      };
    }
    if (text.startsWith("-")) {
      const line = oldLine;
      if (oldLine !== undefined) oldLine++;
      return { text, kind: "delete", line };
    }
    if (text.startsWith("+")) {
      const line = newLine;
      if (newLine !== undefined) newLine++;
      return { text, kind: "add", line };
    }
    if (text.startsWith(" ")) {
      const line = newLine;
      if (oldLine !== undefined) oldLine++;
      if (newLine !== undefined) newLine++;
      return { text, kind: "context", line };
    }
    return { text, kind: "meta" };
  });
}

function QueryLine({
  label,
  value,
  secondary,
}: {
  label: string;
  value: string;
  secondary?: string;
}) {
  return (
    <div className="min-w-0">
      <span className="mr-2 text-[9px] uppercase tracking-[0.18em] text-muted">
        {label}
      </span>
      <span className="break-all text-[11px] text-ink">{value}</span>
      {secondary && (
        <span className="ml-2 text-[10px] text-muted">{secondary}</span>
      )}
    </div>
  );
}

function PathButton({
  path,
  onClick,
  className,
}: {
  path: string;
  onClick: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={path}
      className={cn(
        "block w-full truncate rounded px-1.5 py-1 text-left text-[11px] text-accent transition-colors hover:bg-paper",
        className,
      )}
    >
      {path}
    </button>
  );
}

function ResultMeta({
  scanned,
  skipped,
  durationMs,
  truncated,
  reason,
}: {
  scanned?: number;
  skipped?: number;
  durationMs?: number;
  truncated?: boolean;
  reason?: string;
}) {
  const parts: string[] = [];
  if (scanned !== undefined) parts.push(`${scanned} files scanned`);
  if (skipped) parts.push(`${skipped} skipped`);
  if (durationMs !== undefined) parts.push(`${durationMs} ms`);
  if (truncated) parts.push(reason || "truncated");
  if (parts.length === 0) return null;
  return (
    <p className={cn("text-[10px]", truncated ? "text-amber-700 dark:text-amber-400" : "text-muted")}>
      {parts.join(" · ")}
    </p>
  );
}

function parseObject(value: string | undefined): JsonObject | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as JsonObject)
      : null;
  } catch {
    return null;
  }
}

function validationSummary(value: unknown): ValidationSummary | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  if (
    typeof raw.kind !== "string" ||
    typeof raw.passed !== "boolean" ||
    typeof raw.parse_ok !== "boolean"
  ) {
    return null;
  }
  const diagnostics = Array.isArray(raw.diagnostics)
    ? raw.diagnostics.filter(
        (item): item is ValidationDiagnostic =>
          !!item &&
          typeof item === "object" &&
          !Array.isArray(item) &&
          typeof (item as { id?: unknown }).id === "string" &&
          typeof (item as { message?: unknown }).message === "string",
      )
    : [];
  return {
    kind: raw.kind as ValidationSummary["kind"],
    passed: raw.passed,
    parser: stringValue(raw.parser),
    parse_ok: raw.parse_ok,
    diagnostics,
    error_count: numberValue(raw.error_count) ?? 0,
    warning_count: numberValue(raw.warning_count) ?? 0,
    truncated: raw.truncated === true,
  };
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function grepMatches(value: unknown): GrepMatch[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is GrepMatch =>
          item !== null && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

function basename(path: string): string {
  const normalized = path.replace(/\\/g, "/").replace(/\/+$/, "");
  const index = normalized.lastIndexOf("/");
  return index >= 0 ? normalized.slice(index + 1) || path : path;
}

