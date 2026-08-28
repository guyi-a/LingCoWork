import { workspaceInlineURL } from "@/lib/api";

export function PdfPreview({
  conversationId,
  path,
  projectId,
  version,
}: {
  conversationId: string;
  path: string;
  projectId?: string;
  version?: number;
}) {
  const url = workspaceInlineURL(conversationId, path, { projectId, version });
  return (
    <div className="flex h-full min-h-0 flex-col bg-subtle">
      <iframe
        src={url}
        title={path}
        className="h-full w-full border-0"
      />
    </div>
  );
}
