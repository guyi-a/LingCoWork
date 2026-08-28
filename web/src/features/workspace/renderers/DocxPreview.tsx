// docx 预览 —— docx-preview 库客户端真渲染。
//
// 从 workspaceInlineURL 拿 ArrayBuffer，喂给 docx-preview 的 renderAsync
// 直接渲到 div 内。包含完整段落格式 / 标题 / 列表 / 表格 / 图片。
//
// Symbol/Wingdings PUA 字体修复：docx 列表项目符号 (•▪○) 在 Symbol 字体下是 PUA
// 字符 (0xF0xx 区段)，浏览器没装 Symbol 字体会渲成 □。修法：把 PUA 字符替换成
// 等价 Unicode，并把 Symbol/Wingdings font-family 改成 inherit。
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

const SYMBOL_PUA_MAP: Record<number, string> = {
  0xf0b7: "•",
  0xf0a7: "▪",
  0xf0a8: "■",
  0xf0fc: "✔",
  0xf0fb: "✔",
  0xf06f: "○",
  0xf0fe: "☑",
  0xf071: "●",
  0xf0a1: "●",
  0xf076: "❖",
  0xf0d8: "▲",
  0xf0e0: "✉",
  0xf0e8: "◆",
};

const SYMBOL_FONT_RE = /symbol|wingdings/i;

function escCssChar(ch: string): string {
  const code = ch.codePointAt(0);
  return code === undefined ? "" : "\\" + code.toString(16) + " ";
}

function fixSymbolChars(container: HTMLElement) {
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
  let node: Text | null;
  while ((node = walker.nextNode() as Text | null)) {
    const span = node.parentElement;
    if (!span || !SYMBOL_FONT_RE.test(span.style.fontFamily)) continue;

    let replaced = false;
    const text = node.nodeValue ?? "";
    const chars = Array.from(text);
    const mapped = chars.map((ch) => {
      const code = ch.codePointAt(0);
      if (code === undefined) return ch;
      const sub = SYMBOL_PUA_MAP[code];
      if (sub) {
        replaced = true;
        return sub;
      }
      if (code >= 0xf000 && code <= 0xf0ff) {
        replaced = true;
        return "•";
      }
      return ch;
    });
    if (replaced) {
      node.nodeValue = mapped.join("");
      span.style.fontFamily = "inherit";
    }
  }

  const styles = container.querySelectorAll("style");
  styles.forEach((style) => {
    let css = style.textContent ?? "";
    let changed = false;
    css = css.replace(/\\([Ff]0[0-9A-Fa-f]{2})/g, (_m, hex) => {
      const code = parseInt(hex, 16);
      const sub = SYMBOL_PUA_MAP[code];
      changed = true;
      return sub ? escCssChar(sub) : escCssChar("•");
    });
    css = css.replace(
      /font-family:\s*["']?(Symbol|Wingdings\d?)["']?/gi,
      () => {
        changed = true;
        return "font-family: inherit";
      },
    );
    if (changed) style.textContent = css;
  });
}

export function DocxPreview({
  conversationId,
  path,
  name,
  projectId,
  version,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    setLoading(true);
    setError(null);
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

        const { renderAsync } = await import("docx-preview");
        if (ac.signal.aborted || !containerRef.current) return;

        await renderAsync(buffer, containerRef.current, undefined, {
          inWrapper: false,
          ignoreWidth: true,
          ignoreHeight: true,
          breakPages: false,
          renderHeaders: false,
          renderFooters: false,
          renderFootnotes: true,
          renderEndnotes: true,
          useBase64URL: true,
        });
        if (ac.signal.aborted) return;
        fixSymbolChars(containerRef.current);
        setLoading(false);
      } catch (err) {
        if (ac.signal.aborted) return;
        setError(err instanceof Error ? err.message : "docx 加载失败");
        setLoading(false);
      }
    })();

    return () => ac.abort();
  }, [conversationId, path, projectId, version]);

  if (error) {
    return (
      <div className="p-6 flex flex-col items-center justify-center gap-3 text-center">
        <div className="text-[13px] text-red-600">docx 预览失败：{error}</div>
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
    <div className="relative flex h-full min-h-0 flex-col">
      {loading && (
        <div className="absolute inset-0 z-10 flex items-center justify-center bg-canvas/80">
          <div className="font-mono text-[11px] text-muted">Loading…</div>
        </div>
      )}
      <style>{`
        @font-face { font-family: Symbol;     src: local('Arial Unicode MS'), local('Arial'); }
        @font-face { font-family: Wingdings;  src: local('Arial Unicode MS'), local('Arial'); }
        @font-face { font-family: Wingdings2; src: local('Arial Unicode MS'), local('Arial'); }
        @font-face { font-family: Wingdings3; src: local('Arial Unicode MS'), local('Arial'); }
        .ia-docx-container p[class*="num"]::before {
          font-family: inherit !important;
        }
      `}</style>
      <div
        ref={containerRef}
        className="ia-docx-container scrollbar-subtle min-h-0 flex-1 overflow-auto px-5 py-4 text-[13px] leading-relaxed [&_table]:border-collapse [&_table]:w-full [&_td]:border [&_td]:border-rule [&_td]:px-2 [&_td]:py-1 [&_th]:border [&_th]:border-rule [&_th]:px-2 [&_th]:py-1 [&_img]:max-w-full [&_img]:h-auto"
      />
    </div>
  );
}
