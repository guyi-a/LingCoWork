import { useCallback, useEffect, useState } from "react";
import {
  getMemory,
  saveMemory,
  MemoryConflictError,
  NoWorkspaceError,
  type MemoryDoc,
} from "@/lib/api";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

/**
 * 两级记忆共用的编辑器。用户级从侧栏底部进，项目级从对话里进 —— 差别只是
 * conversationId 传不传，别的都一样，所以没有理由做成两个组件。
 */
export function MemoryEditor({
  open,
  onClose,
  conversationId,
  projectId,
}: {
  open: boolean;
  onClose: () => void;
  /** 省略即用户级。 */
  conversationId?: string;
  projectId?: string;
}) {
  const isProject = !!conversationId;
  const [doc, setDoc] = useState<MemoryDoc | null>(null);
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    setConflict(false);
    try {
      const next = await getMemory(conversationId, projectId);
      setDoc(next);
      setDraft(next.content);
    } catch (err) {
      setDoc(null);
      setError(
        err instanceof NoWorkspaceError
          ? "这个会话还没有工作区，项目记忆要先建工作区才有地方存。"
          : err instanceof Error
            ? err.message
            : String(err),
      );
    } finally {
      setLoading(false);
    }
  }, [conversationId, projectId]);

  useEffect(() => {
    if (open) void load();
  }, [open, load]);

  const bytes = new TextEncoder().encode(draft).length;
  const limit = doc?.limit ?? 0;
  // 超限在这里就拦住：服务端也会拒（413），但让用户点了保存才知道写不进去，
  // 等于把一次白跑的往返和一段可能丢掉的编辑推给他。
  const overflow = limit > 0 && bytes > limit;
  const dirty = !!doc && draft !== doc.content;

  const save = async () => {
    if (!doc || saving || overflow || !dirty) return;
    setSaving(true);
    setError("");
    try {
      const next = await saveMemory(draft, doc.hash, conversationId, projectId);
      setDoc(next);
      setDraft(next.content);
      setConflict(false);
      onClose();
    } catch (err) {
      if (err instanceof MemoryConflictError) {
        // 不覆盖用户的草稿：他手上的编辑是真人写的，冲突时该由他决定留哪份。
        setConflict(true);
        setError("记忆在你编辑期间被 Agent 改过了。重新加载会丢掉当前修改。");
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-w-2xl rounded-2xl p-0">
        <div className="border-b border-rule px-6 py-5">
          <DialogTitle className="mb-1">
            {isProject ? "项目记忆" : "用户记忆"}
          </DialogTitle>
          <DialogDescription>
            {isProject
              ? "这个工作区的约定：技术栈、构建命令、目录规范、不要动的文件。"
              : "跨项目稳定的个人偏好：怎么称呼你、用什么语言回答、代码风格、常用工具链。"}
          </DialogDescription>
          {doc && (
            <p className="mt-2 font-mono text-[11px] text-muted">{doc.path}</p>
          )}
        </div>

        <div className="space-y-3 px-6 py-5">
          {loading ? (
            <p className="py-8 text-center text-[13px] text-muted">加载中…</p>
          ) : doc ? (
            <>
              <textarea
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                rows={14}
                spellCheck={false}
                placeholder={"一行一条，直接写内容，日期分组保存时自动加\n\n回答用中文\n包管理器用 pnpm"}
                className={cn(
                  "block w-full resize-y rounded-lg border bg-paper px-4 py-3 font-mono text-[12px] leading-5 text-ink outline-none",
                  overflow
                    ? "border-red-400 focus:ring-2 focus:ring-red-100 dark:border-red-800 dark:focus:ring-red-900"
                    : "border-rule focus:border-accent focus:ring-2 focus:ring-accent/10",
                )}
              />
              <div className="flex items-center justify-between text-[12px]">
                <span className={cn("font-mono", overflow ? "text-red-700 dark:text-red-400" : "text-muted")}>
                  {bytes} / {limit} 字节
                </span>
                <span className={overflow ? "text-red-700 dark:text-red-400" : "text-muted"}>
                  {overflow
                    ? "超出上限，先合并或删掉几条再保存"
                    : "一行一条，按日期分组"}
                </span>
              </div>
            </>
          ) : null}

          {error && (
            <div className="space-y-2 rounded-lg bg-red-50 px-3 py-2 text-[12px] text-red-700 dark:bg-red-950/40 dark:text-red-400">
              <p className="font-mono">{error}</p>
              {conflict && (
                <button
                  type="button"
                  onClick={() => void load()}
                  className="inline-flex h-7 items-center rounded-md border border-red-300 bg-paper px-3 text-red-700 hover:bg-red-100 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-900/40"
                >
                  丢弃我的修改并重新加载
                </button>
              )}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 border-t border-rule px-6 py-4">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-8 items-center rounded-lg border border-rule bg-paper px-4 text-sm text-ink hover:bg-subtle"
          >
            取消
          </button>
          <button
            type="button"
            disabled={!doc || saving || overflow || !dirty}
            onClick={() => void save()}
            className={cn(
              "inline-flex h-8 items-center rounded-lg bg-ink px-4 text-sm font-medium text-paper transition-opacity",
              (!doc || saving || overflow || !dirty) &&
                "pointer-events-none opacity-40",
            )}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

export function MemoryIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4"
      aria-hidden="true"
    >
      <rect x="3.5" y="3.5" width="9" height="9" rx="1.5" />
      <path d="M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2" />
      <path d="M6.5 6.5h3v3h-3z" />
    </svg>
  );
}
