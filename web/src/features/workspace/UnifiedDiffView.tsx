import { cn } from "@/lib/utils";

type DiffLine = {
  text: string;
  kind: "add" | "delete" | "context" | "hunk" | "meta";
  oldLine?: number;
  newLine?: number;
};

const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/;
const MAX_RENDERED_LINES = 2000;

export function parseUnifiedDiff(patch: string): DiffLine[] {
  let oldLine: number | undefined;
  let newLine: number | undefined;
  const lines: DiffLine[] = [];
  for (const text of patch.split("\n")) {
    if (
      text.startsWith("diff --git ") ||
      text.startsWith("index ") ||
      text.startsWith("--- ") ||
      text.startsWith("+++ ") ||
      text.startsWith("new file mode ") ||
      text.startsWith("deleted file mode ") ||
      text.startsWith("similarity index ") ||
      text.startsWith("rename from ") ||
      text.startsWith("rename to ")
    ) {
      continue;
    }
    const hunk = HUNK_RE.exec(text);
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      const context = hunk[3].trim();
      lines.push({
        text: `修改块 · 旧第 ${oldLine} 行 / 新第 ${newLine} 行${
          context ? ` · ${context}` : ""
        }`,
        kind: "hunk",
      });
      continue;
    }
    if (text.startsWith("+") && !text.startsWith("+++")) {
      const line = newLine;
      if (newLine !== undefined) newLine++;
      lines.push({ text: text.slice(1), kind: "add", newLine: line });
      continue;
    }
    if (text.startsWith("-") && !text.startsWith("---")) {
      const line = oldLine;
      if (oldLine !== undefined) oldLine++;
      lines.push({ text: text.slice(1), kind: "delete", oldLine: line });
      continue;
    }
    if (text.startsWith(" ")) {
      const before = oldLine;
      const after = newLine;
      if (oldLine !== undefined) oldLine++;
      if (newLine !== undefined) newLine++;
      lines.push({
        text: text.slice(1),
        kind: "context",
        oldLine: before,
        newLine: after,
      });
      continue;
    }
    if (text !== "") lines.push({ text, kind: "meta" });
  }
  return lines;
}

export function UnifiedDiffView({
  patch,
  truncated,
}: {
  patch: string;
  truncated?: boolean;
}) {
  const allLines = parseUnifiedDiff(patch);
  const lines = allLines.slice(0, MAX_RENDERED_LINES);
  const clipped = truncated || lines.length < allLines.length;

  return (
    <div className="min-h-0 min-w-0 overflow-auto font-mono text-[11px] leading-[1.55] scrollbar-subtle">
      <div className="w-max min-w-full py-1">
        <div className="sticky top-0 z-10 grid min-w-max grid-cols-[3rem_3rem_1.25rem_max-content] border-b border-rule bg-paper text-[9px] uppercase tracking-[0.12em] text-muted">
          <span className="border-r border-rule/50 px-2 py-1 text-right">
            旧
          </span>
          <span className="border-r border-rule/50 px-2 py-1 text-right">
            新
          </span>
          <span />
          <span className="py-1 pr-4">内容</span>
        </div>
        {lines.map((line, index) => (
          <div
            key={`${index}:${line.kind}:${line.text}`}
            className={cn(
              "grid min-w-max grid-cols-[3rem_3rem_1.25rem_max-content]",
              line.kind === "add" && "bg-emerald-500/10",
              line.kind === "delete" && "bg-red-500/10",
              line.kind === "hunk" && "bg-subtle text-accent",
              line.kind === "meta" && "text-muted",
            )}
          >
            <LineNumber value={line.oldLine} />
            <LineNumber value={line.newLine} />
            <span
              className={cn(
                "select-none py-0.5 text-center",
                line.kind === "add" && "text-emerald-700 dark:text-emerald-300",
                line.kind === "delete" && "text-red-700 dark:text-red-300",
              )}
            >
              {line.kind === "add" ? "+" : line.kind === "delete" ? "−" : ""}
            </span>
            <span
              className={cn(
                "whitespace-pre py-0.5 pr-4",
                line.kind === "add" && "text-emerald-800 dark:text-emerald-300",
                line.kind === "delete" && "text-red-800 dark:text-red-300",
              )}
            >
              {line.text || " "}
            </span>
          </div>
        ))}
      </div>
      {clipped && (
        <div className="sticky bottom-0 border-t border-rule bg-paper px-3 py-1.5 text-[10px] text-amber-700 dark:text-amber-400">
          Diff 内容过长，仅显示前 {MAX_RENDERED_LINES} 行
        </div>
      )}
    </div>
  );
}

function LineNumber({ value }: { value?: number }) {
  return (
    <span className="select-none border-r border-rule/50 px-2 py-0.5 text-right text-muted/60">
      {value ?? ""}
    </span>
  );
}
