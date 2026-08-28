import { useEffect, useState } from "react";
import { fetchWorkspaceFile, type WorkspaceFile } from "@/lib/api";
import { useWorkspaceStore } from "./store";
import { cn } from "@/lib/utils";
import { MarkdownRenderer } from "./renderers/MarkdownRenderer";
import { CodePreview } from "./renderers/CodePreview";
import { TablePreview, isTablePath } from "./renderers/TablePreview";
import { ImageRenderer } from "./renderers/ImageRenderer";
import { PdfPreview } from "./renderers/PdfPreview";
import { DocxPreview } from "./renderers/DocxPreview";
import { PptxPreview } from "./renderers/PptxPreview";
import { MediaPreview } from "./renderers/MediaPreview";
import { UnsupportedRenderer } from "./renderers/UnsupportedRenderer";
import { FileSwitcherOverlay } from "./FileSwitcherOverlay";

type InlineKind = "pdf" | "docx" | "pptx" | "video" | "audio";

const VIDEO_EXTS = new Set(["mp4", "webm", "ogv", "mov", "mkv", "m4v"]);
const AUDIO_EXTS = new Set(["mp3", "wav", "ogg", "m4a", "flac", "aac"]);

function detectInlineKind(path: string): InlineKind | null {
  const lower = path.toLowerCase();
  const dot = lower.lastIndexOf(".");
  if (dot < 0) return null;
  const ext = lower.slice(dot + 1);
  if (ext === "pdf") return "pdf";
  if (ext === "docx") return "docx";
  if (ext === "pptx") return "pptx";
  if (VIDEO_EXTS.has(ext)) return "video";
  if (AUDIO_EXTS.has(ext)) return "audio";
  return null;
}

function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i >= 0 ? path.slice(i + 1) : path;
}

export function FilePreview({
  conversationId,
  path,
  projectId,
}: {
  conversationId: string;
  path: string;
  projectId?: string;
}) {
  const closePreview = useWorkspaceStore((s) => s.closePreview);
  const previewLine = useWorkspaceStore((s) => s.previewLine);
  const filesVersion = useWorkspaceStore((s) => s.filesVersion);
  const switcherOpen = useWorkspaceStore((s) => s.switcherOpen);
  const toggleSwitcher = useWorkspaceStore((s) => s.toggleSwitcher);

  const inlineKind = detectInlineKind(path);

  const [file, setFile] = useState<WorkspaceFile | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    // PDF / video / audio: skip metadata fetch — the browser will pull bytes
    // itself via the inline endpoint (see gpt review Sprint 7 phase X).
    if (inlineKind) {
      setFile(null);
      setError(null);
      setLoading(false);
      return;
    }
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    setFile(null);
    fetchWorkspaceFile(conversationId, path, { projectId }, ac.signal)
      .then((f) => setFile(f))
      .catch((e) => {
        if (e.name === "AbortError") return;
        if (String(e.message ?? e).includes("404")) {
          closePreview();
          return;
        }
        setError(String(e.message ?? e));
      })
      .finally(() => setLoading(false));
    return () => ac.abort();
  }, [
    closePreview,
    conversationId,
    filesVersion,
    inlineKind,
    path,
    projectId,
  ]);

  return (
    <div className="relative flex flex-col min-h-0 flex-1">
      <div className="px-3 py-2 border-b border-rule flex items-center gap-2 shrink-0">
        <button
          type="button"
          onClick={toggleSwitcher}
          className={cn(
            "min-w-0 flex-1 truncate text-left font-mono text-[12px] text-ink",
            "cursor-pointer rounded px-1.5 py-0.5 hover:bg-subtle transition-colors",
          )}
          title={path}
          aria-expanded={switcherOpen}
          aria-label="切换预览文件"
        >
          {path}
        </button>
        {file && (
          <span className="shrink-0 font-mono text-[10px] text-muted">
            {formatSize(file.size)}
          </span>
        )}
        <button
          type="button"
          onClick={closePreview}
          aria-label="关闭文件预览"
          className="shrink-0 text-muted hover:text-ink cursor-pointer transition-colors leading-none px-1"
        >
          <CloseIcon />
        </button>
      </div>

      {switcherOpen && (
        <FileSwitcherOverlay conversationId={conversationId} projectId={projectId} />
      )}

      <div className="flex-1 overflow-auto min-h-0 scrollbar-subtle">
        {inlineKind === "pdf" && (
          <PdfPreview
            conversationId={conversationId}
            path={path}
            projectId={projectId}
            version={filesVersion}
          />
        )}
        {inlineKind === "docx" && (
          <DocxPreview
            conversationId={conversationId}
            path={path}
            name={basename(path)}
            projectId={projectId}
            version={filesVersion}
          />
        )}
        {inlineKind === "pptx" && (
          <PptxPreview
            conversationId={conversationId}
            path={path}
            name={basename(path)}
            projectId={projectId}
            version={filesVersion}
          />
        )}
        {inlineKind === "video" && (
          <MediaPreview
            conversationId={conversationId}
            path={path}
            name={basename(path)}
            kind="video"
            projectId={projectId}
            version={filesVersion}
          />
        )}
        {inlineKind === "audio" && (
          <MediaPreview
            conversationId={conversationId}
            path={path}
            name={basename(path)}
            kind="audio"
            projectId={projectId}
            version={filesVersion}
          />
        )}

        {!inlineKind && loading && (
          <div className="p-4 font-mono text-[11px] text-muted">Loading…</div>
        )}
        {!inlineKind && error && (
          <div className="p-4 text-[13px] text-red-600">加载失败：{error}</div>
        )}
        {!inlineKind && !loading && !error && file && (
          <>
            {file.kind === "markdown" && file.content !== undefined && (
              <MarkdownRenderer content={file.content} />
            )}
            {file.kind === "text" &&
              file.content !== undefined &&
              (isTablePath(file.path) ? (
                <TablePreview content={file.content} path={file.path} />
              ) : (
                <CodePreview
                  content={file.content}
                  fileName={file.name}
                  highlightLine={previewLine}
                />
              ))}
            {file.kind === "image" && (
              <ImageRenderer
                conversationId={conversationId}
                path={file.path}
                name={file.name}
                projectId={projectId}
                version={filesVersion}
              />
            )}
            {(file.kind === "binary" || file.kind === "unsupported") && (
              <UnsupportedRenderer
                conversationId={conversationId}
                path={file.path}
                name={file.name}
                size={file.size}
                projectId={projectId}
                reason={
                  file.kind === "binary"
                    ? "非文本文件"
                    : "暂不支持此类型预览"
                }
              />
            )}
          </>
        )}
      </div>

      {file?.truncated && (
        <div
          className={cn(
            "shrink-0 border-t border-rule px-3 py-1.5",
            "font-mono text-[10px] text-muted",
          )}
        >
          truncated at 512 KB · 完整文件 {formatSize(file.size)}
        </div>
      )}
    </div>
  );
}

function CloseIcon() {
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
    >
      <path d="M4 4 L12 12 M12 4 L4 12" />
    </svg>
  );
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}
