import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import type { ToolCall } from "@/hooks/useChatStream";
import {
  activityDefaultOpen,
  type ToolActivity,
} from "./tool-activity";

export function ToolActivityGroup({
  activity,
  renderTool,
}: {
  activity: ToolActivity;
  renderTool: (tool: ToolCall, index: number) => ReactNode;
}) {
  const [open, setOpen] = useState(() => activityDefaultOpen(activity));
  const previousStatus = useRef(activity.status);

  useEffect(() => {
    const before = previousStatus.current;
    previousStatus.current = activity.status;
    if (activity.status === "running" || activity.status === "pending") {
      setOpen(true);
      return;
    }
    if (
      (activity.status === "error" || activity.status === "cancelled") &&
      before !== activity.status
    ) {
      setOpen(true);
      return;
    }
    if (before !== "ok" && activity.status === "ok") {
      setOpen(false);
    }
  }, [activity.kind, activity.status]);

  const status = activityStatus(activity.status);
  return (
    <aside className="overflow-hidden rounded-lg border border-rule/70 bg-subtle/25 px-3 py-2 font-mono text-[12px] leading-relaxed">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        className="flex w-full cursor-pointer items-baseline gap-2 text-left"
        aria-expanded={open}
      >
        <Chevron open={open} />
        <span className="min-w-0 truncate text-ink">{activity.label}</span>
        {activity.kind === "changes" &&
          (activity.additions > 0 || activity.deletions > 0) && (
            <span className="flex shrink-0 items-center gap-1.5 tabular-nums">
              <span className="text-emerald-600 dark:text-emerald-400">+{activity.additions}</span>
              <span className="text-red-600 dark:text-red-400">−{activity.deletions}</span>
            </span>
          )}
        <span
          className={cn(
            "ml-1 inline-flex shrink-0 items-center gap-1.5 text-[10px] uppercase tracking-[0.12em]",
            status.className,
          )}
        >
          {status.dot}
          <span>{status.label}</span>
        </span>
      </button>
      {open && (
        <div className="mt-2 space-y-2 border-t border-rule/60 pt-2">
          {activity.tools.map((tool, index) => renderTool(tool, index))}
        </div>
      )}
    </aside>
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

function activityStatus(status: ToolCall["status"]): {
  label: string;
  className: string;
  dot: ReactNode;
} {
  if (status === "running") {
    return {
      label: "running",
      className: "text-accent",
      dot: <span className="size-1.5 rounded-full bg-accent animate-pulse" />,
    };
  }
  if (status === "pending") {
    return {
      label: "pending",
      className: "text-amber-700 dark:text-amber-400",
      dot: <span className="size-1.5 rounded-full bg-amber-500" />,
    };
  }
  if (status === "error") {
    return {
      label: "failed",
      className: "text-red-700 dark:text-red-400",
      dot: <span className="text-red-600">×</span>,
    };
  }
  if (status === "cancelled") {
    return {
      label: "cancelled",
      className: "text-muted",
      dot: <span className="size-1.5 rounded-full bg-muted" />,
    };
  }
  return {
    label: "done",
    className: "text-emerald-700 dark:text-emerald-400",
    dot: <span className="text-emerald-600">✓</span>,
  };
}
