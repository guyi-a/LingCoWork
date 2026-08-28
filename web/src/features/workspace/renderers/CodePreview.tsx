import { useEffect, useRef, useState } from "react";
import { highlightCode, resolveLanguage } from "@/lib/shiki";

export function CodePreview({
  content,
  fileName,
  highlightLine,
}: {
  content: string;
  fileName: string;
  highlightLine?: number | null;
}) {
  const [html, setHtml] = useState<string | null>(null);
  const hostRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    setHtml(null);
    const lang = resolveLanguage(fileName);
    highlightCode(content, lang, { showLineNumbers: true })
      .then((h) => {
        if (!cancelled) setHtml(h);
      })
      .catch(() => {
        if (!cancelled) setHtml(null);
      });
    return () => {
      cancelled = true;
    };
  }, [content, fileName]);

  useEffect(() => {
    if (!html || !highlightLine || !hostRef.current) return;
    const frame = window.requestAnimationFrame(() => {
      const host = hostRef.current;
      if (!host) return;
      host
        .querySelectorAll(".shiki-line-target")
        .forEach((node) => node.classList.remove("shiki-line-target"));
      const line = host.querySelector<HTMLElement>(
        `.line[data-line="${highlightLine}"]`,
      );
      if (!line) return;
      line.classList.add("shiki-line-target");
      line.scrollIntoView({ block: "center", behavior: "smooth" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [highlightLine, html]);

  if (html) {
    return (
      <div
        ref={hostRef}
        className="shiki-host"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  }

  return (
    <pre className="p-3 font-mono text-[12px] leading-[1.6] text-ink whitespace-pre">
      {content}
    </pre>
  );
}
