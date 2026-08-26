import { useEffect, useRef, useState, type ReactElement } from "react";
import { cn } from "@/lib/utils";
import { useApprovalMode } from "@/features/chat/approval-mode-store";
import type { ApprovalMode } from "@/lib/api";

// The modes exposed to the user. Order here is the order in the menu.
//
// Deliberately a subset of backend approval.Mode: "auto" (rule fast path plus
// an LLM classifier) still exists server-side but is not offered here. Letting
// a model decide what needs a human is a bigger promise than this product is
// ready to make, and every call that misses the fast path costs an extra LLM
// round trip. The backend keeps it so the choice stays reversible.
//
// Colour discipline: we deliberately avoid tinting (blue/amber/etc.) so the
// dropdown reads as a neutral setting, not a status indicator. Escalation to
// full_access is signalled by inverting the chip (bg-ink text-paper), which
// stands out without introducing a new hue.
const OPTIONS: {
  value: ApprovalMode;
  label: string;
  hint: string;
  icon: (className?: string) => ReactElement;
}[] = [
  {
    value: "default",
    label: "默认权限",
    hint: "每次写/改文件都问",
    icon: (c) => <ShieldCheckIcon className={c} />,
  },
  {
    value: "full_access",
    label: "完全放行",
    hint: "跳过所有审批，仅本会话",
    icon: (c) => <ShieldOffIcon className={c} />,
  },
];

export function ApprovalModeDropdown({
  conversationID,
}: {
  conversationID: string;
}) {
  const { mode, change, pending } = useApprovalMode(conversationID);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Close on outside click / Esc — no shadcn Popover so we roll our own.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!rootRef.current) return;
      if (!rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const current = OPTIONS.find((o) => o.value === mode) ?? OPTIONS[0];

  const pick = async (next: ApprovalMode) => {
    setOpen(false);
    if (next === mode) return;
    await change(next);
  };

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        disabled={pending}
        aria-haspopup="menu"
        aria-expanded={open}
        className={cn(
          "inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-xs font-medium",
          "border transition-colors",
          // full_access = inverted chip so escalation is visible without colour
          mode === "full_access"
            ? "border-ink bg-ink text-paper hover:opacity-90"
            : "border-rule/60 bg-paper text-ink hover:bg-subtle",
          pending && "opacity-60 pointer-events-none",
        )}
      >
        {current.icon("size-3.5")}
        <span>{current.label}</span>
        <ChevronDownIcon
          className={cn("size-3 transition-transform", open && "rotate-180")}
        />
      </button>

      {open && (
        <div
          role="menu"
          className={cn(
            "absolute bottom-full left-0 mb-1.5 w-56 origin-bottom-left",
            "rounded-lg border border-rule bg-paper p-1 shadow-lg",
            "z-20",
          )}
        >
          <div className="px-2 pb-1 pt-1 text-[10px] font-mono uppercase tracking-wider text-muted">
            权限
          </div>
          {OPTIONS.map((opt) => {
            const active = opt.value === mode;
            return (
              <button
                key={opt.value}
                type="button"
                role="menuitem"
                onClick={() => pick(opt.value)}
                className={cn(
                  "w-full flex items-start gap-2 rounded-md px-2 py-1.5 text-left",
                  "transition-colors hover:bg-subtle",
                  active && "bg-subtle text-ink",
                )}
              >
                {opt.icon(cn("size-4 mt-0.5 shrink-0", active ? "text-ink" : "text-muted"))}
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-1.5 text-[13px] font-medium">
                    <span>{opt.label}</span>
                    {active && <CheckIcon className="size-3.5" />}
                  </div>
                  <div className="text-[11px] text-muted mt-0.5">{opt.hint}</div>
                </div>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ShieldCheckIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden>
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

function ShieldOffIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden>
      <path d="M19.7 14a7.7 7.7 0 0 0 .3-2V5l-8-3-3.2 1.2" />
      <path d="M4.7 4.7 4 5v7c0 6 8 10 8 10a20.3 20.3 0 0 0 5.6-4.4" />
      <path d="m2 2 20 20" />
    </svg>
  );
}

function ChevronDownIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden>
      <path d="m6 9 6 6 6-6" />
    </svg>
  );
}

function CheckIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden>
      <path d="M20 6 9 17l-5-5" />
    </svg>
  );
}
