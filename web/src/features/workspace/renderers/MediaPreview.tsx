import { workspaceDownloadURL, workspaceInlineURL } from "@/lib/api";

export function MediaPreview({
  conversationId,
  path,
  name,
  kind,
  projectId,
  version,
}: {
  conversationId: string;
  path: string;
  name: string;
  kind: "video" | "audio";
  projectId?: string;
  version?: number;
}) {
  const src = workspaceInlineURL(conversationId, path, { projectId, version });
  const downloadHref = workspaceDownloadURL(conversationId, path, { projectId });

  return (
    <div className="flex h-full min-h-0 flex-col items-center justify-center gap-4 bg-subtle p-6">
      {kind === "video" ? (
        <video
          src={src}
          controls
          preload="metadata"
          className="max-h-full max-w-full rounded"
        >
          <track kind="captions" />
        </video>
      ) : (
        <audio
          src={src}
          controls
          preload="metadata"
          className="w-full max-w-md"
        />
      )}
      <a
        href={downloadHref}
        download={name}
        className="text-[11px] font-mono uppercase tracking-[0.12em] text-muted hover:text-ink transition-colors"
      >
        浏览器无法播放？下载文件
      </a>
    </div>
  );
}
