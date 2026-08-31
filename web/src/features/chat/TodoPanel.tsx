import { useEffect, useState } from "react";
import type { WorkItem, WorkPlan } from "@/lib/api";
import { cn } from "@/lib/utils";

export function TodoPanel({ plan }: { plan: WorkPlan }) {
  const terminal = plan.status === "completed" || plan.status === "cancelled";
  const [open, setOpen] = useState(!terminal);
  const completed = plan.items.filter(
    (item) => item.status === "completed" || item.status === "cancelled",
  ).length;

  useEffect(() => {
    if (terminal) setOpen(false);
  }, [terminal]);

  return (
    <section className="mx-auto mb-2 w-full max-w-3xl px-8">
      <div className="overflow-hidden rounded-xl border border-rule bg-paper shadow-sm">
        <button
          type="button"
          onClick={() => setOpen((value) => !value)}
          className="flex w-full items-center gap-2 px-3 py-2.5 text-left hover:bg-subtle/50"
          aria-expanded={open}
        >
          <span className={cn("text-muted transition-transform", open && "rotate-90")}>
            ›
          </span>
          <span className="text-xs font-medium text-ink">
            {plan.status === "completed"
              ? "Plan complete"
              : plan.status === "cancelled"
                ? "Plan cancelled"
                : "Implementation plan"}
          </span>
          <span className="ml-auto font-mono text-[10px] tabular-nums text-muted">
            {completed}/{plan.items.length}
          </span>
        </button>
        {open && (
          <div className="space-y-1 border-t border-rule px-3 py-2">
            {plan.overview && (
              <p className="mb-2 text-xs leading-5 text-muted">{plan.overview}</p>
            )}
            {plan.items.map((item) => (
              <TodoRow key={item.id} item={item} />
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function TodoRow({ item }: { item: WorkItem }) {
  const done = item.status === "completed";
  const cancelled = item.status === "cancelled";
  return (
    <div className="flex items-start gap-2 rounded-md px-1 py-1.5">
      <span
        className={cn(
          "mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border text-[10px]",
          done && "border-emerald-500 bg-emerald-500 text-white",
          cancelled && "border-rule bg-subtle text-muted",
          item.status === "in_progress" &&
            "border-accent bg-accent/10 text-accent",
          item.status === "pending" && "border-rule text-transparent",
        )}
      >
        {done ? "✓" : cancelled ? "×" : item.status === "in_progress" ? "•" : ""}
      </span>
      <div className="min-w-0">
        <p
          className={cn(
            "text-xs leading-5 text-ink",
            (done || cancelled) && "text-muted line-through",
          )}
        >
          {item.content}
        </p>
        <span className="font-mono text-[9px] uppercase tracking-wide text-muted">
          {item.status.replace("_", " ")}
        </span>
      </div>
    </div>
  );
}
