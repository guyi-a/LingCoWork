import { useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export function WorkSummary({
  durationMs,
  children,
}: {
  durationMs?: number;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <section className="my-4 overflow-hidden rounded-lg border border-rule/70 bg-subtle/20">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left font-mono text-[11px] text-ink/80 transition-colors hover:bg-subtle/60 hover:text-ink"
        aria-expanded={open}
      >
        <Chevron open={open} />
        <span className="text-ink">
          {durationMs === undefined
            ? "Worked"
            : `Worked for ${formatDuration(durationMs)}`}
        </span>
      </button>
      {open && (
        <div className="space-y-3 border-t border-rule/60 px-3 py-3">
          {children}
        </div>
      )}
    </section>
  );
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 10 10"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn(
        "shrink-0 text-muted transition-transform",
        open && "rotate-90",
      )}
      aria-hidden
    >
      <path d="m3.5 2 3 3-3 3" />
    </svg>
  );
}

function formatDuration(ms: number): string {
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(Math.round(seconds % 60)).padStart(2, "0")}s`;
}
