// pptx 预览 —— @aiden0z/pptx-renderer 客户端真渲染。
//
// 从 workspaceInlineURL 拿 ArrayBuffer，喂给 PptxViewer 渲到 div。
// windowed list mode —— 大量 slide 时按需渲染，不一次性渲全。
// 参考 PentaLoom / krow-app 的实现。

import { useEffect, useRef, useState } from "react";
import { workspaceDownloadURL, workspaceInlineURL } from "@/lib/api";

interface Props {
  conversationId: string;
  path: string;
  name: string;
  projectId?: string;
  version?: number;
}

export function PptxPreview({
  conversationId,
  path,
  name,
  projectId,
  version,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  // PptxViewer 实例 —— 切换文件时要 destroy() 释放，否则上一个 viewer 的 DOM 还挂着
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const viewerRef = useRef<any>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    setLoading(true);
    setError(null);

    if (viewerRef.current) {
      viewerRef.current.destroy();
      viewerRef.current = null;
    }
    if (containerRef.current) containerRef.current.innerHTML = "";

    (async () => {
      try {
        const url = workspaceInlineURL(conversationId, path, {
          projectId,
          version,
        });
        const res = await fetch(url, { signal: ac.signal });
        if (!res.ok) {
          throw new Error(`${res.status} ${res.statusText}`);
        }
        const buffer = await res.arrayBuffer();
        if (ac.signal.aborted) return;

        // 动态 import —— 重库 (~500KB)，只在用户真预览 pptx 时拉
        const { PptxViewer, RECOMMENDED_ZIP_LIMITS } = await import(
          "@aiden0z/pptx-renderer"
        );
        if (ac.signal.aborted || !containerRef.current) return;

        const viewer = new PptxViewer(containerRef.current, {
          fitMode: "contain",
          scrollContainer: containerRef.current.parentElement ?? undefined,
          zipLimits: RECOMMENDED_ZIP_LIMITS,
        });
        viewerRef.current = viewer;

        await viewer.open(buffer, {
          renderMode: "list",
          listOptions: { windowed: true, batchSize: 4 },
          signal: ac.signal,
        });

        if (!ac.signal.aborted) setLoading(false);
      } catch (err) {
        if (ac.signal.aborted) return;
        setError(err instanceof Error ? err.message : "pptx 加载失败");
        setLoading(false);
      }
    })();

    return () => {
      ac.abort();
      if (viewerRef.current) {
        viewerRef.current.destroy();
        viewerRef.current = null;
      }
    };
  }, [conversationId, path, projectId, version]);

  if (error) {
    return (
      <div className="p-6 flex flex-col items-center justify-center gap-3 text-center">
        <div className="text-[13px] text-red-600">pptx 预览失败：{error}</div>
        <a
          href={workspaceDownloadURL(conversationId, path, { projectId })}
          download={name}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-rule rounded text-[12px] text-ink hover:border-accent hover:text-accent transition-colors"
        >
          下载查看
        </a>
      </div>
    );
  }

  return (
    <div className="relative h-full min-h-0 overflow-auto bg-subtle scrollbar-subtle">
      {loading && (
        <div className="absolute inset-0 z-10 flex items-center justify-center bg-canvas/80">
          <div className="font-mono text-[11px] text-muted">Loading…</div>
        </div>
      )}
      <div ref={containerRef} className="ia-pptx-container h-full w-full" />
    </div>
  );
}
