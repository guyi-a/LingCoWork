import type { ToolCall } from "@/hooks/useChatStream";

export type ToolActivityKind = "explore" | "changes" | "commands" | "rag";

export type ToolActivity = {
  kind: ToolActivityKind;
  tools: ToolCall[];
  status: ToolCall["status"];
  label: string;
  files: string[];
  searches: number;
  additions: number;
  deletions: number;
};

export type ToolActivityRow =
  | { kind: "single"; tool: ToolCall; index: number }
  | { kind: "activity"; activity: ToolActivity; index: number };

const TOOL_ACTIVITY_KIND: Record<string, ToolActivityKind | undefined> = {
  glob: "explore",
  grep: "explore",
  read_file: "explore",
  list_files: "explore",
  file_info: "explore",
  apply_patch: "changes",
  write_file: "changes",
  write_file_chunked: "changes",
  mkdir: "changes",
  rm: "changes",
  mv: "changes",
  cp: "changes",
  run_command: "commands",
  rag_search: "rag",
};

export function groupToolActivities(tools: ToolCall[]): ToolActivityRow[] {
  const rows: ToolActivityRow[] = [];
  let index = 0;
  while (index < tools.length) {
    const tool = tools[index];
    const activityKind = TOOL_ACTIVITY_KIND[tool.name];
    if (!activityKind) {
      rows.push({ kind: "single", tool, index });
      index++;
      continue;
    }
    let end = index + 1;
    while (
      end < tools.length &&
      TOOL_ACTIVITY_KIND[tools[end].name] === activityKind
    ) {
      end++;
    }
    rows.push({
      kind: "activity",
      activity: projectActivity(activityKind, tools.slice(index, end)),
      index,
    });
    index = end;
  }
  return rows;
}

export function projectActivity(
  kind: ToolActivityKind,
  tools: ToolCall[],
): ToolActivity {
  const filePaths = new Set<string>();
  let searches = 0;
  let additions = 0;
  let deletions = 0;

  for (const tool of tools) {
    const args = parseObject(tool.argsJson);
    const result = parseObject(tool.content);
    if (kind === "explore") {
      if (tool.name === "glob" || tool.name === "grep" || tool.name === "list_files") {
        searches++;
      }
      if (tool.name === "read_file" || tool.name === "file_info") {
        addString(filePaths, args?.path);
      }
    } else if (kind === "changes") {
      const argumentPath =
        stringValue(args?.path) || stringValue(args?.dst);
      addString(filePaths, argumentPath || result?.path);
      additions += numberValue(result?.additions) ?? 0;
      deletions += numberValue(result?.deletions) ?? 0;
    }
  }

  const files = [...filePaths];
  return {
    kind,
    tools,
    status: aggregateActivityStatus(tools),
    label: activityLabel(kind, tools.length, files.length, searches),
    files,
    searches,
    additions,
    deletions,
  };
}

export function aggregateActivityStatus(
  tools: ToolCall[],
): ToolCall["status"] {
  if (tools.some((tool) => tool.status === "error")) return "error";
  if (tools.some((tool) => tool.status === "pending")) return "pending";
  if (tools.some((tool) => tool.status === "running")) return "running";
  if (tools.some((tool) => tool.status === "cancelled")) return "cancelled";
  return "ok";
}

export function activityDefaultOpen(activity: ToolActivity): boolean {
  return activity.status === "running" || activity.status === "pending";
}

export function allToolsSettled(tools: ToolCall[]): boolean {
  return tools.length > 0 &&
    tools.every(
      (tool) => tool.status !== "running" && tool.status !== "pending",
    );
}

function activityLabel(
  kind: ToolActivityKind,
  toolCount: number,
  fileCount: number,
  searches: number,
): string {
  switch (kind) {
    case "explore": {
      const parts: string[] = [];
      if (fileCount > 0) parts.push(`${fileCount} ${plural(fileCount, "file", "files")}`);
      if (searches > 0) parts.push(`${searches} ${plural(searches, "search", "searches")}`);
      return parts.length > 0 ? `Explored ${parts.join(", ")}` : "Explored workspace";
    }
    case "changes": {
      const count = fileCount || toolCount;
      return `Changed ${count} ${plural(count, "file", "files")}`;
    }
    case "commands":
      return `Ran ${toolCount} ${plural(toolCount, "command", "commands")}`;
    case "rag":
      return `Searched knowledge base ${toolCount} ${plural(toolCount, "time", "times")}`;
  }
}

function parseObject(value: string | undefined): Record<string, unknown> | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function addString(target: Set<string>, value: unknown) {
  if (typeof value === "string" && value.trim()) {
    target.add(value.trim());
  }
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function plural(count: number, singular: string, pluralForm: string): string {
  return count === 1 ? singular : pluralForm;
}
