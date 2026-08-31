import type { AgentMode } from "@/lib/api";
import { cn } from "@/lib/utils";

export function AgentModeDropdown({
  mode,
  disabled,
  onChange,
}: {
  mode: AgentMode;
  disabled?: boolean;
  onChange: (mode: AgentMode) => void;
}) {
  return (
    <div className="flex h-7 items-center rounded-md border border-rule bg-paper p-0.5">
      {(["agent", "plan"] as const).map((value) => (
        <button
          key={value}
          type="button"
          disabled={disabled}
          onClick={() => onChange(value)}
          className={cn(
            "h-6 rounded px-2 font-mono text-[10px] uppercase tracking-wide transition-colors",
            value === mode
              ? "bg-ink text-paper"
              : "text-muted hover:bg-subtle hover:text-ink",
            disabled && "cursor-not-allowed opacity-50",
          )}
          title={
            value === "plan"
              ? "只读调研并生成可编辑计划"
              : "直接搜索、修改并验证"
          }
        >
          {value}
        </button>
      ))}
    </div>
  );
}
