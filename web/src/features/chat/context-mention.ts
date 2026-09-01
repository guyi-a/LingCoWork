import type { WorkspaceTreeEntry } from "@/lib/api";
import type { PickedLocalFile } from "@/lib/electron-api";

export type ContextMention = {
  query: string;
  start: number;
  end: number;
};

// Only the token immediately before the caret is a context mention. Requiring
// whitespace (or start-of-input) before @ keeps email addresses and prose
// elsewhere in the draft from unexpectedly opening the picker.
export function findContextMention(
  text: string,
  caret: number,
): ContextMention | null {
  const beforeCaret = text.slice(0, caret);
  const match = /(^|\s)@([^\s@]*)$/.exec(beforeCaret);
  if (!match) return null;
  const start = caret - match[2].length - 1;
  return { query: match[2], start, end: caret };
}

export function replaceContextMention(
  text: string,
  mention: ContextMention,
): { text: string; caret: number } {
  const next = text.slice(0, mention.start) + text.slice(mention.end);
  return { text: next, caret: mention.start };
}

export function filterContextEntries(
  entries: WorkspaceTreeEntry[],
  query: string,
): WorkspaceTreeEntry[] {
  const needle = query.trim().toLocaleLowerCase();
  const sorted = [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.path.localeCompare(b.path);
  });
  if (!needle) return sorted;
  return sorted.filter((entry) =>
    `${entry.name}\n${entry.path}`.toLocaleLowerCase().includes(needle),
  );
}

// Workspace tree paths are API-relative. Keep their conversion at this
// boundary so attachment serialization only ever sees absolute paths.
export function workspaceEntryToAttachment(
  workspace: string,
  entry: WorkspaceTreeEntry,
): PickedLocalFile {
  const relative = entry.path.replace(/^[/\\]+/, "");
  const separator = workspace.includes("\\") && !workspace.includes("/") ? "\\" : "/";
  const root = workspace.replace(/[/\\]+$/, "");
  return {
    path: relative ? `${root}${separator}${relative.replace(/[/\\]/g, separator)}` : root,
    name: entry.name,
    isDirectory: entry.is_dir,
  };
}
